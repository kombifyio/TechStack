package guardposture

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/db/tenantcontext"
	"github.com/google/uuid"
)

// findingsRetention is how long a resolved finding stays as history.
const findingsRetention = 90 * 24 * time.Hour

// maxRepairHistory bounds what admission reads.
const maxRepairHistory = 200

// PostgresStore is the Store over the control-plane database.
type PostgresStore struct {
	db   *sql.DB
	sink AlertSink
}

// NewPostgresStore wraps a control-plane database handle. sink may be nil, in
// which case findings are recorded without notifications.
func NewPostgresStore(db *sql.DB, sink AlertSink) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("guardposture: database handle is required")
	}
	return &PostgresStore{db: db, sink: sink}, nil
}

func (s *PostgresStore) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := tenantcontext.ApplyTenant(ctx, tx, tenantID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func scope(tenantID, workerID string) (string, string, error) {
	tenantID, workerID = strings.TrimSpace(tenantID), strings.TrimSpace(workerID)
	if tenantID == "" || workerID == "" {
		return "", "", ErrNotFound
	}
	return tenantID, workerID, nil
}

const postureColumns = `tenant_id, worker_id, server_id, owner_subject_id, display_name, source_epoch, source_sequence,
	reported_at, guard_version, guard_signed, security_status, security_reason_code, security_reported_at, evidence,
	backup_status, backup_reason_code, backup_ready, backup_snapshot_state, backup_last_snapshot_at,
	backup_restore_state, backup_last_restore_at, backup_reported_at,
	guard_mode, guard_update_state, guard_update_version, guard_deny_mutations, guard_observed_at,
	verify_requested_at, verify_request_id, verify_unechoed, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	utc := value.Time.UTC()
	return &utc
}

func timeVal(value sql.NullTime) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func scanPosture(row scanner) (*Posture, error) {
	var p Posture
	var reported, securityReported, snapshotAt, restoreAt, backupReported, guardObserved, verifyRequested sql.NullTime
	var evidence []byte
	if err := row.Scan(
		&p.TenantID, &p.WorkerID, &p.ServerID, &p.OwnerSubjectID, &p.DisplayName, &p.SourceEpoch, &p.SourceSequence,
		&reported, &p.GuardVersion, &p.GuardSigned, &p.Security.Status, &p.Security.ReasonCode, &securityReported, &evidence,
		&p.Backup.Status, &p.Backup.ReasonCode, &p.Backup.Ready, &p.Backup.SnapshotState, &snapshotAt,
		&p.Backup.RestoreState, &restoreAt, &backupReported,
		&p.GuardMode, &p.GuardUpdateState, &p.GuardUpdateVersion, &p.GuardDenyMutations, &guardObserved,
		&verifyRequested, &p.VerifyRequestID, &p.VerifyUnechoed, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	p.ReportedAt, p.Security.ReportedAt = timeVal(reported), timeVal(securityReported)
	p.Backup.LastSnapshotAt, p.Backup.LastRestoreAt, p.Backup.ReportedAt = timePtr(snapshotAt), timePtr(restoreAt), timeVal(backupReported)
	p.GuardObservedAt, p.VerifyRequestedAt = timeVal(guardObserved), timePtr(verifyRequested)
	if len(evidence) > 0 {
		var decoded Evidence
		if err := json.Unmarshal(evidence, &decoded); err == nil {
			p.Security.Evidence = &decoded
		}
	}
	return &p, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, workerID string) (*Posture, error) {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return nil, err
	}
	var out *Posture
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		posture, queryErr := scanPosture(tx.QueryRowContext(ctx,
			`SELECT `+postureColumns+` FROM guard_posture_states WHERE tenant_id = $1 AND worker_id = $2`, tenantID, workerID))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		out = posture
		return queryErr
	})
	return out, err
}

func (s *PostgresStore) ListByTenant(ctx context.Context, tenantID string) ([]Posture, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, nil
	}
	var out []Posture
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `SELECT `+postureColumns+` FROM guard_posture_states WHERE tenant_id = $1 ORDER BY worker_id`, tenantID)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			posture, scanErr := scanPosture(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *posture)
		}
		return rows.Err()
	})
	return out, err
}

// lockPosture makes sure the row exists and locks it for the transaction.
func lockPosture(ctx context.Context, tx *sql.Tx, identity Identity, name string, now time.Time) (*Posture, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO guard_posture_states (tenant_id, worker_id, server_id, owner_subject_id, display_name, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, worker_id) DO UPDATE SET
			server_id = EXCLUDED.server_id, owner_subject_id = EXCLUDED.owner_subject_id,
			display_name = CASE WHEN EXCLUDED.display_name <> '' THEN EXCLUDED.display_name ELSE guard_posture_states.display_name END
	`, identity.TenantID, identity.WorkerID, identity.ServerID, identity.OwnerSubjectID, name, now); err != nil {
		return nil, err
	}
	return scanPosture(tx.QueryRowContext(ctx,
		`SELECT `+postureColumns+` FROM guard_posture_states WHERE tenant_id = $1 AND worker_id = $2 FOR UPDATE`,
		identity.TenantID, identity.WorkerID))
}

func (s *PostgresStore) ApplyReport(ctx context.Context, write ReportWrite) (bool, error) {
	identity := write.Identity
	if _, _, err := scope(identity.TenantID, identity.WorkerID); err != nil {
		return false, err
	}
	applied := false
	err := s.withTenant(ctx, identity.TenantID, func(tx *sql.Tx) error {
		current, err := lockPosture(ctx, tx, identity, write.DisplayName, write.Now)
		if err != nil {
			return err
		}
		if current.SourceEpoch == write.Report.SourceEpoch && current.SourceSequence != 0 && write.Report.Sequence <= current.SourceSequence {
			return nil
		}
		applied = true
		security := current.Security
		if write.Security != nil {
			security = MergeSecurity(current.Security, *write.Security)
		}
		backup := current.Backup
		if write.Backup != nil {
			backup = MergeBackup(current.Backup, *write.Backup)
		}
		var evidence any
		if security.Evidence != nil {
			raw, marshalErr := json.Marshal(security.Evidence)
			if marshalErr != nil {
				return marshalErr
			}
			evidence = string(raw)
		}
		securityReported, backupReported := nullTime(security.ReportedAt), nullTime(backup.ReportedAt)
		if write.Security != nil {
			AnswerVerifyRequest(current, write.Report.VerifyRequestID)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE guard_posture_states SET
				source_epoch = $3, source_sequence = $4, reported_at = $5, guard_version = $6, guard_signed = $7,
				security_status = $8, security_reason_code = $9, security_reported_at = $10, evidence = $11::jsonb,
				backup_status = $12, backup_reason_code = $13, backup_ready = $14, backup_snapshot_state = $15,
				backup_last_snapshot_at = $16, backup_restore_state = $17, backup_last_restore_at = $18, backup_reported_at = $19,
				verify_request_id = $20, verify_unechoed = $21
			WHERE tenant_id = $1 AND worker_id = $2
		`, identity.TenantID, identity.WorkerID, write.Report.SourceEpoch, write.Report.Sequence, write.Now,
			write.Report.GuardVersion, write.Report.GuardSigned,
			security.Status, security.ReasonCode, securityReported, evidence,
			backup.Status, backup.ReasonCode, backup.Ready, backup.SnapshotState, backup.LastSnapshotAt,
			backup.RestoreState, backup.LastRestoreAt, backupReported, current.VerifyRequestID, current.VerifyUnechoed); err != nil {
			return err
		}
		name := firstNonEmpty(write.DisplayName, current.DisplayName)
		for _, reconciliation := range write.Reconciliations {
			if err := s.reconcileTx(ctx, tx, reconciliation, identity, name, write.Now); err != nil {
				return err
			}
		}
		return nil
	})
	return applied, err
}

func nullTime(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at
}

func (s *PostgresStore) ApplyGuard(ctx context.Context, write GuardWrite) error {
	identity := write.Identity
	if _, _, err := scope(identity.TenantID, identity.WorkerID); err != nil {
		return err
	}
	return s.withTenant(ctx, identity.TenantID, func(tx *sql.Tx) error {
		current, err := lockPosture(ctx, tx, identity, write.DisplayName, write.Now)
		if err != nil {
			return err
		}
		obs := write.Observation
		if _, err := tx.ExecContext(ctx, `
			UPDATE guard_posture_states SET guard_mode = $3, guard_update_state = $4, guard_update_version = $5,
				guard_deny_mutations = $6, guard_observed_at = $7
			WHERE tenant_id = $1 AND worker_id = $2
		`, identity.TenantID, identity.WorkerID, obs.Mode, obs.UpdateState, obs.UpdateVersion, obs.DenyMutations, write.Now); err != nil {
			return err
		}
		return s.reconcileTx(ctx, tx, write.Reconciliation, identity, firstNonEmpty(write.DisplayName, current.DisplayName), write.Now)
	})
}

func (s *PostgresStore) RequestVerify(ctx context.Context, identity Identity, name, requestID string, at time.Time) error {
	if _, _, err := scope(identity.TenantID, identity.WorkerID); err != nil {
		return err
	}
	return s.withTenant(ctx, identity.TenantID, func(tx *sql.Tx) error {
		if _, err := lockPosture(ctx, tx, identity, name, at); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE guard_posture_states SET verify_requested_at = $3, verify_request_id = $4, verify_unechoed = 0 WHERE tenant_id = $1 AND worker_id = $2`,
			identity.TenantID, identity.WorkerID, at, requestID)
		return err
	})
}

const findingColumns = `id, tenant_id, worker_id, server_id, owner_subject_id, display_name, finding_key, scope, severity, state,
	expected, observed, reason_code, capability, opened_at, last_seen_at, alert_due_at, alerted_at, resolved_at`

func scanFinding(row scanner) (FindingRecord, error) {
	var f FindingRecord
	var alerted, resolved sql.NullTime
	if err := row.Scan(&f.ID, &f.TenantID, &f.WorkerID, &f.ServerID, &f.OwnerSubjectID, &f.DisplayName, &f.Key, &f.Scope,
		&f.Severity, &f.State, &f.Expected, &f.Observed, &f.ReasonCode, &f.Capability,
		&f.OpenedAt, &f.LastSeenAt, &f.AlertDueAt, &alerted, &resolved); err != nil {
		return f, err
	}
	f.OpenedAt, f.LastSeenAt, f.AlertDueAt = f.OpenedAt.UTC(), f.LastSeenAt.UTC(), f.AlertDueAt.UTC()
	f.AlertedAt, f.ResolvedAt = timePtr(alerted), timePtr(resolved)
	return f, nil
}

// reconcileTx applies one scope's observation to the ledger and emits its
// alerts inside the transaction.
func (s *PostgresStore) reconcileTx(ctx context.Context, tx *sql.Tx, rec Reconciliation, identity Identity, name string, now time.Time) error {
	if rec.Empty() {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+findingColumns+` FROM guard_posture_findings
		WHERE tenant_id = $1 AND worker_id = $2 AND scope = $3 AND resolved_at IS NULL FOR UPDATE`,
		identity.TenantID, identity.WorkerID, rec.Scope)
	if err != nil {
		return err
	}
	var open []FindingRecord
	for rows.Next() {
		record, scanErr := scanFinding(rows)
		if scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		open = append(open, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	change := planReconciliation(rec, open, identity, name, now, uuid.NewString)
	for _, record := range change.open {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO guard_posture_findings (`+findingColumns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		`, record.ID, record.TenantID, record.WorkerID, record.ServerID, record.OwnerSubjectID, record.DisplayName, record.Key, record.Scope,
			record.Severity, record.State, record.Expected, record.Observed, record.ReasonCode, record.Capability,
			record.OpenedAt, record.LastSeenAt, record.AlertDueAt, record.AlertedAt, record.ResolvedAt); err != nil {
			return err
		}
	}
	for _, record := range append(append([]FindingRecord{}, change.update...), change.close...) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE guard_posture_findings SET server_id = $3, owner_subject_id = $4, display_name = $5, severity = $6, state = $7,
				expected = $8, observed = $9, reason_code = $10, capability = $11, last_seen_at = $12, alerted_at = $13, resolved_at = $14
			WHERE tenant_id = $1 AND id = $2
		`, record.TenantID, record.ID, record.ServerID, record.OwnerSubjectID, record.DisplayName, record.Severity, record.State,
			record.Expected, record.Observed, record.ReasonCode, record.Capability, record.LastSeenAt, record.AlertedAt, record.ResolvedAt); err != nil {
			return err
		}
	}
	if len(change.close) > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM guard_posture_findings WHERE tenant_id = $1 AND worker_id = $2 AND resolved_at < $3`,
			identity.TenantID, identity.WorkerID, now.Add(-findingsRetention)); err != nil {
			return err
		}
	}
	if s.sink != nil {
		for _, event := range change.alerts {
			if err := s.sink.EnqueueAlert(ctx, tx, event); err != nil {
				return fmt.Errorf("enqueue %s: %w", event.EventKey, err)
			}
		}
	}
	return nil
}

func (s *PostgresStore) OpenFindings(ctx context.Context, tenantID, workerID string) ([]FindingRecord, error) {
	return s.findings(ctx, tenantID, workerID, time.Time{}, 0, true)
}

func (s *PostgresStore) Findings(ctx context.Context, tenantID, workerID string, since time.Time, limit int) ([]FindingRecord, error) {
	return s.findings(ctx, tenantID, workerID, since, limit, false)
}

func (s *PostgresStore) findings(ctx context.Context, tenantID, workerID string, since time.Time, limit int, openOnly bool) ([]FindingRecord, error) {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var out []FindingRecord
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		query := `SELECT ` + findingColumns + ` FROM guard_posture_findings WHERE tenant_id = $1 AND worker_id = $2 AND opened_at >= $3`
		if openOnly {
			query += ` AND resolved_at IS NULL`
		}
		rows, queryErr := tx.QueryContext(ctx, query+` ORDER BY opened_at DESC LIMIT $4`, tenantID, workerID, since, limit)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			record, scanErr := scanFinding(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, record)
		}
		return rows.Err()
	})
	return out, err
}

const repairColumns = `id, tenant_id, worker_id, server_id, mode, requested_by, controls, status, reason_code, command_id, steps,
	requested_at, expires_at, dispatched_at, finished_at`

// repairSelectColumns adds the local-policy binding (migration 159) to what a
// read returns; an online repair leaves it NULL.
const repairSelectColumns = repairColumns + `, operation_id, policy_id, policy_version, worker_generation, evidence_revision, evidence_digest`

func scanRepair(row scanner) (Repair, error) {
	var r Repair
	var controls, steps []byte
	var dispatched, finished sql.NullTime
	var operationID, policyID, digest sql.NullString
	var policyVersion, generation, revision sql.NullInt64
	if err := row.Scan(&r.ID, &r.TenantID, &r.WorkerID, &r.ServerID, &r.Mode, &r.RequestedBy, &controls, &r.Status, &r.ReasonCode,
		&r.CommandID, &steps, &r.RequestedAt, &r.ExpiresAt, &dispatched, &finished,
		&operationID, &policyID, &policyVersion, &generation, &revision, &digest); err != nil {
		return r, err
	}
	if operationID.Valid {
		r.Local = &LocalBinding{OperationID: operationID.String, PolicyID: policyID.String, PolicyVersion: policyVersion.Int64,
			Generation: generation.Int64, Revision: revision.Int64, Digest: digest.String}
	}
	if err := json.Unmarshal(controls, &r.Controls); err != nil {
		return r, err
	}
	if len(steps) > 0 {
		_ = json.Unmarshal(steps, &r.Steps)
	}
	r.RequestedAt, r.ExpiresAt = r.RequestedAt.UTC(), r.ExpiresAt.UTC()
	r.DispatchedAt, r.FinishedAt = timePtr(dispatched), timePtr(finished)
	return r, nil
}

func repairsTx(ctx context.Context, tx *sql.Tx, tenantID, workerID string, limit int, forUpdate bool) ([]Repair, error) {
	query := `SELECT ` + repairSelectColumns + ` FROM guard_posture_repairs WHERE tenant_id = $1 AND worker_id = $2 ORDER BY requested_at DESC LIMIT $3`
	rows, err := tx.QueryContext(ctx, query, tenantID, workerID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Repair
	for rows.Next() {
		repair, scanErr := scanRepair(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, repair)
	}
	return out, rows.Err()
}

func (s *PostgresStore) AdmitRepair(ctx context.Context, tenantID, workerID string, now time.Time, admit func([]Repair, *Posture) (*Repair, error)) (*Repair, error) {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return nil, err
	}
	var out *Repair
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		// One admission per server at a time: the bounds are a check then an
		// insert, and two requests must not both pass the same check.
		if err := lockServerRepairs(ctx, tx, tenantID, workerID); err != nil {
			return err
		}
		history, err := repairsTx(ctx, tx, tenantID, workerID, maxRepairHistory, false)
		if err != nil {
			return err
		}
		var posture *Posture
		if stored, getErr := scanPosture(tx.QueryRowContext(ctx,
			`SELECT `+postureColumns+` FROM guard_posture_states WHERE tenant_id = $1 AND worker_id = $2`, tenantID, workerID)); getErr == nil {
			posture = stored
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		repair, err := admit(history, posture)
		if err != nil {
			return err
		}
		repair.TenantID, repair.WorkerID = tenantID, workerID
		controls, _ := json.Marshal(repair.Controls)
		steps, _ := json.Marshal(nonNilSteps(repair.Steps))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO guard_posture_repairs (`+repairColumns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11::jsonb,$12,$13,$14,$15)
		`, repair.ID, tenantID, workerID, repair.ServerID, repair.Mode, repair.RequestedBy, string(controls), repair.Status,
			repair.ReasonCode, repair.CommandID, string(steps), repair.RequestedAt, repair.ExpiresAt, repair.DispatchedAt, repair.FinishedAt); err != nil {
			return err
		}
		copied := *repair
		out = &copied
		return nil
	})
	return out, err
}

func nonNilSteps(steps []RepairStep) []RepairStep {
	if steps == nil {
		return []RepairStep{}
	}
	return steps
}

func (s *PostgresStore) UpdateRepair(ctx context.Context, tenantID, repairID string, fn func(*Repair)) (*Repair, error) {
	tenantID = strings.TrimSpace(tenantID)
	var out *Repair
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		repair, err := scanRepair(tx.QueryRowContext(ctx,
			`SELECT `+repairSelectColumns+` FROM guard_posture_repairs WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, repairID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		fn(&repair)
		steps, _ := json.Marshal(nonNilSteps(repair.Steps))
		if _, err := tx.ExecContext(ctx, `
			UPDATE guard_posture_repairs SET status = $3, reason_code = $4, steps = $5::jsonb, dispatched_at = $6, finished_at = $7
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, repairID, repair.Status, repair.ReasonCode, string(steps), repair.DispatchedAt, repair.FinishedAt); err != nil {
			return err
		}
		out = &repair
		return nil
	})
	return out, err
}

func (s *PostgresStore) Repairs(ctx context.Context, tenantID, workerID string, limit int) ([]Repair, error) {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxRepairHistory {
		limit = maxRepairHistory
	}
	var out []Repair
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var queryErr error
		out, queryErr = repairsTx(ctx, tx, tenantID, workerID, limit, false)
		return queryErr
	})
	return out, err
}

func (s *PostgresStore) GetAutoRepair(ctx context.Context, tenantID, workerID string) (*AutoRepairPolicy, error) {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return nil, err
	}
	var out *AutoRepairPolicy
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var policy AutoRepairPolicy
		var controls []byte
		var expires, approved sql.NullTime
		queryErr := tx.QueryRowContext(ctx, `SELECT policy_id, version, enabled, controls, expires_at, approved_by, approved_at, updated_at
			FROM guard_repair_policies WHERE tenant_id = $1 AND worker_id = $2`, tenantID, workerID).
			Scan(&policy.PolicyID, &policy.Version, &policy.Enabled, &controls, &expires, &policy.ApprovedBy, &approved, &policy.UpdatedAt)
		if errors.Is(queryErr, sql.ErrNoRows) {
			return nil
		}
		if queryErr != nil {
			return queryErr
		}
		_ = json.Unmarshal(controls, &policy.Controls)
		policy.TenantID, policy.WorkerID, policy.ExpiresAt, policy.ApprovedAt = tenantID, workerID, timePtr(expires), timePtr(approved)
		out = &policy
		return nil
	})
	return out, err
}

// lockServerRepairs serializes every repair admission, policy registration and
// local evidence acceptance of one server.
func lockServerRepairs(ctx context.Context, tx *sql.Tx, tenantID, workerID string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "guard-repair:"+tenantID+":"+workerID)
	return err
}

func (s *PostgresStore) SetAutoRepair(ctx context.Context, policy AutoRepairPolicy) (*AutoRepairPolicy, error) {
	tenantID, workerID, err := scope(policy.TenantID, policy.WorkerID)
	if err != nil {
		return nil, err
	}
	controls, _ := json.Marshal(nonNilControls(policy.Controls))
	var out *AutoRepairPolicy
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if err := lockServerRepairs(ctx, tx, tenantID, workerID); err != nil {
			return err
		}
		var policyID string
		var version int64
		queryErr := tx.QueryRowContext(ctx, `SELECT policy_id, version FROM guard_repair_policies WHERE tenant_id = $1 AND worker_id = $2`,
			tenantID, workerID).Scan(&policyID, &version)
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}
		if policyID == "" {
			policyID, version = uuid.NewString(), 0
		}
		version++
		if _, err := tx.ExecContext(ctx, `
			UPDATE guard_repair_policy_versions SET superseded_at = now()
			WHERE tenant_id = $1 AND worker_id = $2 AND superseded_at IS NULL
		`, tenantID, workerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO guard_repair_policy_versions (tenant_id, worker_id, policy_id, version, enabled, controls, expires_at, approved_by, approved_at)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)
		`, tenantID, workerID, policyID, version, policy.Enabled, string(controls), policy.ExpiresAt, policy.ApprovedBy, policy.ApprovedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO guard_repair_policies (tenant_id, worker_id, enabled, controls, expires_at, approved_by, approved_at, updated_at, policy_id, version)
			VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, now(), $8, $9)
			ON CONFLICT (tenant_id, worker_id) DO UPDATE SET enabled = EXCLUDED.enabled, controls = EXCLUDED.controls,
				expires_at = EXCLUDED.expires_at, approved_by = EXCLUDED.approved_by, approved_at = EXCLUDED.approved_at, updated_at = now(),
				policy_id = EXCLUDED.policy_id, version = EXCLUDED.version
		`, tenantID, workerID, policy.Enabled, string(controls), policy.ExpiresAt, policy.ApprovedBy, policy.ApprovedAt, policyID, version); err != nil {
			return err
		}
		stored := policy
		stored.TenantID, stored.WorkerID, stored.PolicyID, stored.Version = tenantID, workerID, policyID, version
		out = &stored
		return nil
	})
	return out, err
}

func nonNilControls(controls []string) []string {
	if controls == nil {
		return []string{}
	}
	return controls
}

func (s *PostgresStore) AcceptLocalOperation(ctx context.Context, tenantID, workerID string, ref LocalRef, decide func(LocalState) (*Repair, error)) error {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return err
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if err := lockServerRepairs(ctx, tx, tenantID, workerID); err != nil {
			return err
		}
		var state LocalState
		existing, err := scanRepair(tx.QueryRowContext(ctx, `SELECT `+repairSelectColumns+` FROM guard_posture_repairs
			WHERE tenant_id = $1 AND worker_id = $2 AND operation_id = $3`, tenantID, workerID, ref.OperationID))
		switch {
		case err == nil:
			state.Existing = &existing
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		var version PolicyVersion
		var controls []byte
		var expires, approved, superseded sql.NullTime
		err = tx.QueryRowContext(ctx, `
			SELECT policy_id, version, enabled, controls, expires_at, approved_at, superseded_at
			FROM guard_repair_policy_versions WHERE tenant_id = $1 AND worker_id = $2 AND policy_id = $3 AND version = $4
		`, tenantID, workerID, ref.PolicyID, ref.PolicyVersion).Scan(&version.PolicyID, &version.Version, &version.Enabled, &controls, &expires, &approved, &superseded)
		switch {
		case err == nil:
			_ = json.Unmarshal(controls, &version.Controls)
			version.ExpiresAt, version.ApprovedAt, version.SupersededAt = timePtr(expires), timePtr(approved), timePtr(superseded)
			state.Version = &version
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if state.History, err = repairsTx(ctx, tx, tenantID, workerID, maxRepairHistory, false); err != nil {
			return err
		}
		record, err := decide(state)
		if err != nil || record == nil {
			return err
		}
		b := record.Local
		if b == nil {
			return errors.New("guardposture: local evidence without its binding")
		}
		controlsJSON, _ := json.Marshal(record.Controls)
		steps, _ := json.Marshal(nonNilSteps(record.Steps))
		if state.Existing != nil {
			_, err = tx.ExecContext(ctx, `
				UPDATE guard_posture_repairs SET status = $3, reason_code = $4, steps = $5::jsonb, finished_at = $6,
					evidence_revision = $7, evidence_digest = $8
				WHERE tenant_id = $1 AND id = $2
			`, tenantID, record.ID, record.Status, record.ReasonCode, string(steps), record.FinishedAt, b.Revision, b.Digest)
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO guard_posture_repairs (`+repairColumns+`, operation_id, policy_id, policy_version, worker_generation, evidence_revision, evidence_digest)
			VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11::jsonb,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		`, record.ID, tenantID, workerID, record.ServerID, record.Mode, record.RequestedBy, string(controlsJSON), record.Status,
			record.ReasonCode, record.CommandID, string(steps), record.RequestedAt, record.ExpiresAt, record.DispatchedAt, record.FinishedAt,
			b.OperationID, b.PolicyID, b.PolicyVersion, b.Generation, b.Revision, b.Digest)
		return err
	})
}
