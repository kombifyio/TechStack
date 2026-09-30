package signals

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actions"
)

const maxDeliveryAttempts = 8

type Record struct {
	SequenceID int64
	Envelope   Envelope
	Attempts   int
	CreatedAt  time.Time
}

type Claim struct {
	Record
	Generation int64
	Owner      string
	Token      string `json:"-"`
	ExpiresAt  time.Time
}

type PostgresOutbox struct {
	db          *sql.DB
	remediation RemediationPolicy
}

// NewPostgresOutbox uses Techstack's DefaultRemediationPolicy.
func NewPostgresOutbox(db *sql.DB) *PostgresOutbox {
	return NewPostgresOutboxWithPolicy(db, DefaultRemediationPolicy())
}

// NewPostgresOutboxWithPolicy binds an explicit remediation policy.
func NewPostgresOutboxWithPolicy(db *sql.DB, policy RemediationPolicy) *PostgresOutbox {
	return &PostgresOutbox{db: db, remediation: policy}
}

// ResolveServerOwner returns the canonical user subject for a tenant-scoped
// server. Producers use it when their runtime observation has tenant/server
// identity but no user label; the Gateway publisher can then mint an exact OBO
// service call without persisting or guessing another identity.
func (o *PostgresOutbox) ResolveServerOwner(ctx context.Context, tenantID, serverID string) (string, error) {
	if o == nil || o.db == nil {
		return "", fmt.Errorf("ril signals: database not configured")
	}
	tenantID, serverID = strings.TrimSpace(tenantID), strings.TrimSpace(serverID)
	if !validTenantID(tenantID) || !stableID(serverID) {
		return "", ErrInvalidSignal
	}
	tx, err := o.beginTenant(ctx, tenantID)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var owner string
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(owner_subject_id, '')
		FROM servers
		WHERE tenant_id=$1 AND id=$2
	`, tenantID, serverID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || strings.TrimSpace(owner) == "" {
		return "", ErrServerUnauthorized
	}
	if err != nil {
		return "", err
	}
	owner = strings.TrimSpace(owner)
	if !userIDPattern.MatchString(owner) {
		return "", ErrInvalidSignal
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return owner, nil
}

// Emit commits one canonical envelope only when the exact server exists in
// the supplied tenant's RLS scope. Reusing a dedupe key returns the original
// envelope without creating another delivery.
//
// Only a first insert consults the remediation policy. When the
// observation's alert rule maps to a remediation template, the governed
// action card ActionCardID is created in the same transaction as the outbox
// row and the stored envelope is marked actionable. The card belongs to the
// signal's tenant and owner; a signal without an owner stays
// notification-only. A dedupe replay never creates another card, and a
// failed policy lookup or card insert keeps the signal notification-only
// instead of losing it.
func (o *PostgresOutbox) Emit(ctx context.Context, observation Observation) (Record, bool, error) {
	if o == nil || o.db == nil {
		return Record{}, false, fmt.Errorf("ril signals: database not configured")
	}
	input, envelope, err := normalizeObservation(observation)
	if err != nil {
		return Record{}, false, err
	}
	tx, err := o.beginTenant(ctx, input.TenantID)
	if err != nil {
		return Record{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	payload, err := json.Marshal(envelope)
	if err != nil {
		return Record{}, false, err
	}

	var record Record
	var storedPayload []byte
	var inserted bool
	err = tx.QueryRowContext(ctx, `
		WITH authorized_server AS (
			SELECT id FROM servers
			WHERE tenant_id=$1 AND id=$2 AND ($12='' OR owner_subject_id=$12)
		), inserted AS (
			INSERT INTO ril_signal_outbox (
				tenant_id, signal_id, dedupe_key, server_id, source, severity,
				priority, trace_id, audit_id, envelope_json, action_card_id
			)
			SELECT $1,$3,$4,$2,$5,$6,$7,$8,$9,$10::jsonb,$11
			FROM authorized_server
			ON CONFLICT (tenant_id, dedupe_key) DO NOTHING
			RETURNING sequence_id, envelope_json, attempts, created_at, true AS inserted
		)
		SELECT sequence_id, envelope_json, attempts, created_at, inserted FROM inserted
		UNION ALL
		SELECT sequence_id, envelope_json, attempts, created_at, false
		FROM ril_signal_outbox
		WHERE tenant_id=$1 AND dedupe_key=$4 AND EXISTS (SELECT 1 FROM authorized_server)
		  AND NOT EXISTS (SELECT 1 FROM inserted)
		LIMIT 1
	`, input.TenantID, input.ServerID, input.SignalID, input.DedupeKey,
		input.Source, input.Severity, envelope.Priority, input.TraceID, input.AuditID,
		payload, envelope.ActionCardID, input.UserID).
		Scan(&record.SequenceID, &storedPayload, &record.Attempts, &record.CreatedAt, &inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, ErrServerUnauthorized
	}
	if err != nil {
		return Record{}, false, err
	}
	if err := json.Unmarshal(storedPayload, &record.Envelope); err != nil {
		return Record{}, false, err
	}
	if !inserted {
		// Actionability is decided once, at the first insert; a replay
		// compares only the producer-owned envelope.
		envelope.Actionable = record.Envelope.Actionable
		if !reflect.DeepEqual(record.Envelope, envelope) {
			return Record{}, false, ErrDedupeConflict
		}
	} else {
		actionable, err := o.attachGovernedCard(ctx, tx, input, envelope, record.SequenceID)
		if err != nil {
			return Record{}, false, err
		}
		record.Envelope.Actionable = actionable
	}
	if err := tx.Commit(); err != nil {
		return Record{}, false, err
	}
	return record, inserted, nil
}

// attachGovernedCard runs after the outbox insert passed the tenant, server,
// owner and dedupe fences. Inside one savepoint it resolves the remediation
// template, creates the governed card and marks the stored envelope
// actionable. Any failure there rolls back to the savepoint, so the signal is
// still committed as notification-only. Only savepoint errors are returned.
func (o *PostgresOutbox) attachGovernedCard(ctx context.Context, tx *sql.Tx, input Observation, envelope Envelope, sequenceID int64) (bool, error) {
	resolve := o.remediation[input.AlertRule]
	if input.AlertRule == "" || input.UserID == "" || resolve == nil {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT ril_governed_card`); err != nil {
		return false, err
	}
	if o.createGovernedCard(ctx, tx, resolve, input, envelope, sequenceID) == nil {
		_, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT ril_governed_card`)
		return err == nil, err
	}
	_, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT ril_governed_card`)
	return false, err
}

var errNotRemediable = errors.New("ril signals: signal is not remediable")

func (o *PostgresOutbox) createGovernedCard(ctx context.Context, tx *sql.Tx, resolve RemediationTemplate, input Observation, envelope Envelope, sequenceID int64) error {
	template, ok, err := resolve(ctx, tx, RemediationSubject{
		TenantID: input.TenantID, OwnerSubjectID: input.UserID, ServerID: input.ServerID,
		ServiceID: input.ServiceID, AlertRule: input.AlertRule,
		Source: input.Source, Severity: input.Severity,
	})
	if err != nil {
		return err
	}
	if !ok {
		return errNotRemediable
	}
	if _, err := actions.CreateInTx(ctx, tx, actions.CreateGovernedCard{
		ID: envelope.ActionCardID, TenantID: input.TenantID, OwnerSubjectID: input.UserID,
		ServerID: input.ServerID, Title: cardTitle(input), Severity: string(input.Severity),
		Template: template,
	}); err != nil {
		return err
	}
	envelope.Actionable = true
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE ril_signal_outbox SET envelope_json=$3::jsonb WHERE tenant_id=$1 AND sequence_id=$2`,
		input.TenantID, sequenceID, payload)
	return err
}

func cardTitle(input Observation) string {
	if input.Title != "" {
		return input.Title
	}
	return input.AlertRule
}

func (o *PostgresOutbox) Claim(ctx context.Context, tenantID, owner string, lease time.Duration) (Claim, error) {
	if o == nil || o.db == nil {
		return Claim{}, fmt.Errorf("ril signals: database not configured")
	}
	tenantID, owner = strings.TrimSpace(tenantID), strings.TrimSpace(owner)
	if !validTenantID(tenantID) || !validClaimOwner(owner) || lease < time.Second || lease > 10*time.Minute {
		return Claim{}, ErrInvalidSignal
	}
	token, err := claimToken()
	if err != nil {
		return Claim{}, err
	}
	tx, err := o.beginTenant(ctx, tenantID)
	if err != nil {
		return Claim{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var claim Claim
	var payload []byte
	err = tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT sequence_id FROM ril_signal_outbox
			WHERE tenant_id=$1 AND delivered_at IS NULL AND failed_at IS NULL
			  AND available_at <= clock_timestamp()
			  AND (claim_expires_at IS NULL OR claim_expires_at <= clock_timestamp())
			ORDER BY CASE priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3 WHEN 'normal' THEN 2 ELSE 1 END DESC,
				available_at, sequence_id
			LIMIT 1 FOR UPDATE SKIP LOCKED
		), claimed AS (
			UPDATE ril_signal_outbox AS work
			SET attempts=work.attempts+1, claim_generation=work.claim_generation+1,
				claim_owner=$2, claim_token_digest=$3,
				claim_expires_at=clock_timestamp()+($4*interval '1 second')
			FROM candidate WHERE work.sequence_id=candidate.sequence_id AND work.tenant_id=$1
			RETURNING work.sequence_id, work.envelope_json, work.attempts, work.created_at,
				work.claim_generation, work.claim_owner, work.claim_expires_at
		)
		SELECT * FROM claimed
	`, tenantID, owner, claimDigest(token), int64(lease/time.Second)).Scan(
		&claim.SequenceID, &payload, &claim.Attempts, &claim.CreatedAt,
		&claim.Generation, &claim.Owner, &claim.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, ErrOutboxEmpty
	}
	if err != nil {
		return Claim{}, err
	}
	if err := json.Unmarshal(payload, &claim.Envelope); err != nil {
		return Claim{}, err
	}
	claim.Token = token
	if err := tx.Commit(); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

// PendingTenants returns only opaque tenant identifiers from the delivery
// wake-up table. Signal payloads remain protected by tenant RLS and are claimed
// only after Claim establishes the matching app.tenant_id scope.
func (o *PostgresOutbox) PendingTenants(ctx context.Context, limit int) ([]string, error) {
	if o == nil || o.db == nil {
		return nil, fmt.Errorf("ril signals: database not configured")
	}
	if limit <= 0 || limit > 256 {
		limit = 64
	}
	rows, err := o.db.QueryContext(ctx, `
		SELECT tenant_id
		FROM ril_signal_delivery_tenants
		ORDER BY pending_at, tenant_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	tenants := make([]string, 0, limit)
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		if validTenantID(tenantID) {
			tenants = append(tenants, tenantID)
		}
	}
	return tenants, rows.Err()
}

// RetireTenantIfIdle removes a wake-up row only when the tenant-scoped queue
// has no due or delayed work left. A concurrent insert either remains visible
// to this transaction or recreates the wake-up row through the outbox trigger.
func (o *PostgresOutbox) RetireTenantIfIdle(ctx context.Context, tenantID string) error {
	if o == nil || o.db == nil || !validTenantID(strings.TrimSpace(tenantID)) {
		return ErrInvalidSignal
	}
	tx, err := o.beginTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		DELETE FROM ril_signal_delivery_tenants AS wake
		WHERE wake.tenant_id=$1
		  AND NOT EXISTS (
			SELECT 1 FROM ril_signal_outbox AS work
			WHERE work.tenant_id=$1
			  AND work.delivered_at IS NULL AND work.failed_at IS NULL
		  )
	`, tenantID)
	if err != nil {
		return err
	}
	if _, err := result.RowsAffected(); err != nil {
		return err
	}
	return tx.Commit()
}

func (o *PostgresOutbox) Complete(ctx context.Context, claim Claim) error {
	return o.mutateClaim(ctx, claim, func(tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, `
			UPDATE ril_signal_outbox
			SET delivered_at=clock_timestamp(), claim_owner=NULL,
				claim_token_digest=NULL, claim_expires_at=NULL, last_error=NULL
			WHERE tenant_id=$1 AND sequence_id=$2 AND signal_id=$3
			  AND claim_generation=$4 AND claim_owner=$5 AND claim_token_digest=$6
			  AND claim_expires_at > clock_timestamp()
			  AND delivered_at IS NULL AND failed_at IS NULL
		`, claim.Envelope.TenantID, claim.SequenceID, claim.Envelope.SignalID,
			claim.Generation, claim.Owner, claimDigest(claim.Token))
	})
}

func (o *PostgresOutbox) Retry(ctx context.Context, claim Claim, cause error) error {
	message := "delivery failed"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	terminal := claim.Attempts >= maxDeliveryAttempts
	delay := RetryDelay(claim.Attempts)
	return o.mutateClaim(ctx, claim, func(tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, `
			UPDATE ril_signal_outbox
			SET available_at=CASE WHEN $7 THEN available_at ELSE clock_timestamp()+($8*interval '1 second') END,
				failed_at=CASE WHEN $7 THEN clock_timestamp() ELSE NULL END,
				claim_owner=NULL, claim_token_digest=NULL, claim_expires_at=NULL,
				last_error=$9
			WHERE tenant_id=$1 AND sequence_id=$2 AND signal_id=$3
			  AND claim_generation=$4 AND claim_owner=$5 AND claim_token_digest=$6
			  AND claim_expires_at > clock_timestamp()
			  AND delivered_at IS NULL AND failed_at IS NULL
		`, claim.Envelope.TenantID, claim.SequenceID, claim.Envelope.SignalID,
			claim.Generation, claim.Owner, claimDigest(claim.Token), terminal,
			int64(delay/time.Second), message)
	})
}

func (o *PostgresOutbox) mutateClaim(ctx context.Context, claim Claim, mutation func(*sql.Tx) (sql.Result, error)) error {
	if o == nil || o.db == nil || claim.SequenceID <= 0 || claim.Generation <= 0 ||
		!validTenantID(claim.Envelope.TenantID) || !stableID(claim.Envelope.SignalID) ||
		!validClaimOwner(claim.Owner) || strings.TrimSpace(claim.Token) == "" {
		return ErrClaimLost
	}
	tx, err := o.beginTenant(ctx, claim.Envelope.TenantID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := mutation(tx)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return ErrClaimLost
	}
	return tx.Commit()
}

func (o *PostgresOutbox) beginTenant(ctx context.Context, tenantID string) (*sql.Tx, error) {
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func claimToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func claimDigest(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}

// Publisher is the only cross-repository seam. A productive adapter sends the
// envelope through Gateway; it does not alter classification or authority.
type Publisher interface {
	Publish(context.Context, Envelope) error
}

type Worker struct {
	Outbox    *PostgresOutbox
	Publisher Publisher
	Owner     string
	Lease     time.Duration
	Poll      time.Duration
	OnError   func(error)
}

func (w Worker) ProcessOne(ctx context.Context, tenantID string) error {
	if w.Outbox == nil || w.Publisher == nil {
		return fmt.Errorf("ril signals: worker not configured")
	}
	lease := w.Lease
	if lease == 0 {
		lease = 30 * time.Second
	}
	claim, err := w.Outbox.Claim(ctx, tenantID, w.Owner, lease)
	if err != nil {
		return err
	}
	if err := w.Publisher.Publish(ctx, claim.Envelope); err != nil {
		if retryErr := w.Outbox.Retry(ctx, claim, err); retryErr != nil {
			return errors.Join(err, retryErr)
		}
		return err
	}
	return w.Outbox.Complete(ctx, claim)
}

// ProcessNext fairly checks the stable tenant wake-up set and publishes at
// most one envelope. It never reads cross-tenant payload data.
func (w Worker) ProcessNext(ctx context.Context) error {
	if w.Outbox == nil || w.Publisher == nil {
		return fmt.Errorf("ril signals: worker not configured")
	}
	tenants, err := w.Outbox.PendingTenants(ctx, 64)
	if err != nil {
		return err
	}
	for _, tenantID := range tenants {
		err := w.ProcessOne(ctx, tenantID)
		if errors.Is(err, ErrOutboxEmpty) {
			if retireErr := w.Outbox.RetireTenantIfIdle(ctx, tenantID); retireErr != nil {
				return retireErr
			}
			continue
		}
		return err
	}
	return ErrOutboxEmpty
}

// Run drains immediately, then sleeps only while the queue is empty. Failures
// are already fenced and backoff-scheduled by ProcessOne, so one bad signal
// cannot stop the process-wide publisher.
func (w Worker) Run(ctx context.Context) {
	poll := w.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, ErrOutboxEmpty) && w.OnError != nil {
			w.OnError(err)
		}
		if err == nil {
			timer.Reset(0)
		} else {
			timer.Reset(poll)
		}
	}
}
