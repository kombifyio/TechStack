package guardreach

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/db/tenantcontext"
)

// ErrNotFound means no reach row exists for the worker.
var ErrNotFound = errors.New("guardreach: no reach state for this worker")

// Key names one reach row.
type Key struct{ TenantID, WorkerID string }

// Enrollment is what the control plane knows when a Guard server is paired or
// its key is confirmed. It creates the row (desired active); an existing row
// only refreshes display fields and, when supplied, the owner roles.
type Enrollment struct {
	TenantID       string
	WorkerID       string
	ServerID       string
	StackID        string
	OwnerSubjectID string
	DisplayName    string
	Placement      string
	OwnerRoles     []string
	// ConnectTenantID is the tenant Gateway derived for the owner's own
	// session (its org id, else usr:<subject>), supplied only where the owner's
	// Gateway-fronted request is present. It is authoritative; without it the
	// tenant is mapped from the Techstack tenant by ConnectTenant.
	ConnectTenantID string
}

// Store persists reach state. Implementations are tenant scoped.
type Store interface {
	Enroll(ctx context.Context, e Enrollment) error
	Get(ctx context.Context, tenantID, workerID string) (*Row, error)
	// ListByTenant returns every reach row of one tenant (the server read model).
	ListByTenant(ctx context.Context, tenantID string) ([]Row, error)
	// Update applies fn to the row under a row lock and persists the result
	// when fn reports a change. fn must not perform I/O.
	Update(ctx context.Context, tenantID, workerID string, fn func(*Row) bool) (*Row, error)
	ListDue(ctx context.Context, now time.Time, afterTenant, afterWorker string, limit int) ([]Key, error)
	// Claim leases one due row for the reconciler; false means another worker
	// holds it or it is no longer due.
	Claim(ctx context.Context, tenantID, workerID string, now time.Time, lease time.Duration) (*Row, bool, error)
	FindByConnect(ctx context.Context, connectTenantID, serverID string) ([]Key, error)
	Cursor(ctx context.Context, name string) (int64, error)
	AdvanceCursor(ctx context.Context, name string, position int64) error
}

// PostgresStore is the Store over the control-plane database.
type PostgresStore struct{ db *sql.DB }

// NewPostgresStore wraps a control-plane database handle.
func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("guardreach: database handle is required")
	}
	return &PostgresStore{db: db}, nil
}

const rowColumns = `tenant_id, worker_id, server_id, stack_id, owner_subject_id, display_name, placement,
	connect_tenant_id, owner_roles, desired_state, key_thumbprint, credential_epoch, binding_generation,
	me_state, me_generation, me_address, me_reason, me_updated_at,
	connect_state, connect_generation, connect_node_id, connect_reason, connect_updated_at, connect_retry_at,
	relay_state, relay_reason, relay_generation, relay_reported_at,
	revoke_reason, attempts, next_attempt_at, last_error, created_at, updated_at,
	public_enabled, public_service, public_scheme, public_port, public_enabled_by, public_enabled_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanRow(scanner rowScanner) (*Row, error) {
	var r Row
	var desired, meState, connectState string
	var roles string
	var meAt, connectAt, connectRetry, relayAt, publicAt sql.NullTime
	if err := scanner.Scan(
		&r.TenantID, &r.WorkerID, &r.ServerID, &r.StackID, &r.OwnerSubjectID, &r.DisplayName, &r.Placement,
		&r.ConnectTenantID, &roles, &desired, &r.KeyThumbprint, &r.CredentialEpoch, &r.Generation,
		&meState, &r.Me.Generation, &r.Me.Ref, &r.Me.ReasonCode, &meAt,
		&connectState, &r.Connect.Generation, &r.Connect.Ref, &r.Connect.ReasonCode, &connectAt, &connectRetry,
		&r.Relay.State, &r.Relay.ReasonCode, &r.Relay.Generation, &relayAt,
		&r.RevokeReason, &r.Attempts, &r.NextAttemptAt, &r.LastError, &r.CreatedAt, &r.UpdatedAt,
		&r.Public.Enabled, &r.Public.Service, &r.Public.Scheme, &r.Public.Port, &r.Public.EnabledBy, &publicAt,
	); err != nil {
		return nil, err
	}
	if roles != "" {
		r.OwnerRoles = strings.Split(roles, ",")
	}
	r.Desired, r.Me.State, r.Connect.State = Desired(desired), TargetState(meState), TargetState(connectState)
	if meAt.Valid {
		r.Me.UpdatedAt = &meAt.Time
	}
	if connectAt.Valid {
		r.Connect.UpdatedAt = &connectAt.Time
	}
	if connectRetry.Valid {
		r.Connect.RetryAt = &connectRetry.Time
	}
	if relayAt.Valid {
		r.Relay.ReportedAt = &relayAt.Time
	}
	if publicAt.Valid {
		r.Public.EnabledAt = &publicAt.Time
	}
	return &r, nil
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

// Enroll creates the row or refreshes its descriptive fields. It never touches
// desired state, key binding or registration state of an existing row, so a
// repeated enrollment call cannot resurrect a revoked Guard.
func (s *PostgresStore) Enroll(ctx context.Context, e Enrollment) error {
	tenantID, workerID, err := scope(e.TenantID, e.WorkerID)
	if err != nil {
		return errors.New("guardreach: tenant and worker are required")
	}
	serverID, owner := strings.TrimSpace(e.ServerID), strings.TrimSpace(e.OwnerSubjectID)
	if serverID == "" || owner == "" {
		return errors.New("guardreach: server and owner are required")
	}
	connectTenant, observed := strings.TrimSpace(e.ConnectTenantID), true
	if connectTenant == "" || len(connectTenant) > 255 {
		var mapErr error
		connectTenant, observed = "", false
		if connectTenant, mapErr = ConnectTenant(tenantID, owner); mapErr != nil {
			connectTenant = ""
		}
	}
	roles := StaffRoles(e.OwnerRoles)
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			INSERT INTO guard_reach_states (
				tenant_id, worker_id, server_id, stack_id, owner_subject_id, display_name,
				placement, connect_tenant_id, owner_roles, connect_tenant_observed
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (tenant_id, worker_id) DO UPDATE SET
				stack_id = CASE WHEN EXCLUDED.stack_id <> '' THEN EXCLUDED.stack_id ELSE guard_reach_states.stack_id END,
				display_name = CASE WHEN EXCLUDED.display_name <> '' THEN EXCLUDED.display_name ELSE guard_reach_states.display_name END,
				placement = EXCLUDED.placement,
				connect_tenant_id = CASE
					WHEN EXCLUDED.connect_tenant_observed AND NOT guard_reach_states.connect_tenant_observed THEN EXCLUDED.connect_tenant_id
					ELSE guard_reach_states.connect_tenant_id END,
				connect_tenant_observed = guard_reach_states.connect_tenant_observed OR EXCLUDED.connect_tenant_observed,
				owner_roles = CASE WHEN EXCLUDED.owner_roles <> '' THEN EXCLUDED.owner_roles ELSE guard_reach_states.owner_roles END
			WHERE guard_reach_states.server_id = EXCLUDED.server_id
			  AND guard_reach_states.owner_subject_id = EXCLUDED.owner_subject_id
		`, tenantID, workerID, serverID, strings.TrimSpace(e.StackID), owner, strings.TrimSpace(e.DisplayName),
			normalizePlacement(e.Placement), connectTenant, strings.Join(roles, ","), observed)
		return execErr
	})
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, workerID string) (*Row, error) {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return nil, err
	}
	var out *Row
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, queryErr := scanRow(tx.QueryRowContext(ctx,
			`SELECT `+rowColumns+` FROM guard_reach_states WHERE tenant_id = $1 AND worker_id = $2`, tenantID, workerID))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		out = row
		return queryErr
	})
	return out, err
}

func (s *PostgresStore) ListByTenant(ctx context.Context, tenantID string) ([]Row, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrNotFound
	}
	var out []Row
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `SELECT `+rowColumns+` FROM guard_reach_states WHERE tenant_id = $1 ORDER BY worker_id`, tenantID)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			row, scanErr := scanRow(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *row)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PostgresStore) Update(ctx context.Context, tenantID, workerID string, fn func(*Row) bool) (*Row, error) {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return nil, err
	}
	var out *Row
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, queryErr := scanRow(tx.QueryRowContext(ctx,
			`SELECT `+rowColumns+` FROM guard_reach_states WHERE tenant_id = $1 AND worker_id = $2 FOR UPDATE`, tenantID, workerID))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if queryErr != nil {
			return queryErr
		}
		out = row
		if !fn(row) {
			return nil
		}
		_, execErr := tx.ExecContext(ctx, `
			UPDATE guard_reach_states SET
				desired_state = $3, key_thumbprint = $4, credential_epoch = $5, binding_generation = $6,
				connect_tenant_id = $7, owner_roles = $25,
				me_state = $8, me_generation = $9, me_address = $10, me_reason = $11, me_updated_at = $12,
				connect_state = $13, connect_generation = $14, connect_node_id = $15, connect_reason = $16, connect_updated_at = $17,
				connect_retry_at = $26,
				relay_state = $18, relay_reason = $19, relay_generation = $20, relay_reported_at = $21,
				attempts = $22, next_attempt_at = $23, last_error = $24, revoke_reason = $27,
				public_enabled = $28, public_service = $29, public_scheme = $30, public_port = $31,
				public_enabled_by = $32, public_enabled_at = $33
			WHERE tenant_id = $1 AND worker_id = $2
		`, tenantID, workerID, string(row.Desired), row.KeyThumbprint, row.CredentialEpoch, row.Generation,
			row.ConnectTenantID,
			string(row.Me.State), row.Me.Generation, row.Me.Ref, clampReason(row.Me.ReasonCode), row.Me.UpdatedAt,
			string(row.Connect.State), row.Connect.Generation, row.Connect.Ref, clampReason(row.Connect.ReasonCode), row.Connect.UpdatedAt,
			row.Relay.State, clampReason(row.Relay.ReasonCode), row.Relay.Generation, row.Relay.ReportedAt,
			row.Attempts, row.NextAttemptAt.UTC(), clampReason(row.LastError), strings.Join(row.OwnerRoles, ","),
			row.Connect.RetryAt, row.RevokeReason,
			row.Public.Enabled, row.Public.Service, row.Public.Scheme, row.Public.Port, row.Public.EnabledBy, row.Public.EnabledAt)
		return execErr
	})
	return out, err
}

func (s *PostgresStore) ListDue(ctx context.Context, now time.Time, afterTenant, afterWorker string, limit int) ([]Key, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tenant_id, worker_id FROM techstack_list_due_guard_reach($1, $2, $3, $4)`,
		now.UTC(), strings.TrimSpace(afterTenant), strings.TrimSpace(afterWorker), limit)
	if err != nil {
		return nil, fmt.Errorf("list due guard reach: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var keys []Key
	for rows.Next() {
		var key Key
		if err := rows.Scan(&key.TenantID, &key.WorkerID); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *PostgresStore) Claim(ctx context.Context, tenantID, workerID string, now time.Time, lease time.Duration) (*Row, bool, error) {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return nil, false, err
	}
	var out *Row
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, queryErr := scanRow(tx.QueryRowContext(ctx, `
			UPDATE guard_reach_states
			SET next_attempt_at = $3
			WHERE tenant_id = $1 AND worker_id = $2 AND next_attempt_at <= $4
			RETURNING `+rowColumns,
			tenantID, workerID, now.UTC().Add(lease), now.UTC()))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return nil
		}
		out = row
		return queryErr
	})
	return out, out != nil, err
}

func (s *PostgresStore) FindByConnect(ctx context.Context, connectTenantID, serverID string) ([]Key, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id, worker_id FROM techstack_find_guard_reach_by_connect($1, $2)`,
		strings.TrimSpace(connectTenantID), strings.TrimSpace(serverID))
	if err != nil {
		return nil, fmt.Errorf("find guard reach by connect node: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var keys []Key
	for rows.Next() {
		var key Key
		if err := rows.Scan(&key.TenantID, &key.WorkerID); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *PostgresStore) Cursor(ctx context.Context, name string) (int64, error) {
	var position int64
	err := s.db.QueryRowContext(ctx, `SELECT position FROM guard_reach_cursors WHERE name = $1`, name).Scan(&position)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return position, err
}

// AdvanceCursor moves the position forward only; replicas that consume the
// same page concurrently cannot move it back.
func (s *PostgresStore) AdvanceCursor(ctx context.Context, name string, position int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO guard_reach_cursors (name, position) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET position = GREATEST(guard_reach_cursors.position, EXCLUDED.position), updated_at = now()
	`, name, position)
	return err
}

var _ Store = (*PostgresStore)(nil)
