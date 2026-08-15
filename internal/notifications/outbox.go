package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	outboxPollInterval = 2 * time.Second
	outboxClaimLease   = 30 * time.Second
	outboxMaxAttempts  = 8
)

var ErrRecipientRequired = errors.New("notifications: auth0 recipient required")

const deliveryChannelPayloadKey = "_delivery_channel"

type ProductEvent struct {
	Topic          string
	Channel        string
	Auth0UserID    string
	OrganizationID string
	Payload        map[string]any
	IdempotencyKey string
}

// ProductEventEnqueuer is the durable product-notification boundary used by
// route packages that need to record an event without owning delivery.
type ProductEventEnqueuer interface {
	Enqueue(context.Context, ProductEvent) error
}

type dispatchClient interface {
	DispatchProduct(context.Context, ProductEvent) (*DispatchResult, error)
}

type DispatchResult struct {
	DispatchID string `json:"dispatch_id"`
	FeedItemID string `json:"feed_item_id"`
}

type DispatchError struct {
	StatusCode int
	Code       string
	Retryable  bool
}

func (e *DispatchError) Error() string {
	return fmt.Sprintf("notifications: engine returned %d (%s)", e.StatusCode, e.Code)
}

type Outbox struct {
	db     *sql.DB
	client dispatchClient
	logger *slog.Logger
	now    func() time.Time
}

func NewOutbox(db *sql.DB, client dispatchClient, logger *slog.Logger) *Outbox {
	if logger == nil {
		logger = slog.Default()
	}
	return &Outbox{db: db, client: client, logger: logger, now: time.Now}
}

func (o *Outbox) Enqueue(ctx context.Context, event ProductEvent) error {
	if o == nil || o.db == nil {
		return fmt.Errorf("notifications: outbox database not configured")
	}
	event.Auth0UserID = strings.TrimSpace(event.Auth0UserID)
	event.OrganizationID = strings.TrimSpace(event.OrganizationID)
	event.IdempotencyKey = strings.TrimSpace(event.IdempotencyKey)
	if event.Auth0UserID == "" {
		return ErrRecipientRequired
	}
	if event.Topic == "" || event.IdempotencyKey == "" {
		return fmt.Errorf("notifications: topic and idempotency key required")
	}
	persistedPayload := make(map[string]any, len(event.Payload)+1)
	for key, value := range event.Payload {
		persistedPayload[key] = value
	}
	if channel := strings.TrimSpace(event.Channel); channel != "" && channel != "in_app" {
		persistedPayload[deliveryChannelPayloadKey] = channel
	}
	payload, err := json.Marshal(persistedPayload)
	if err != nil {
		return fmt.Errorf("notifications: marshal outbox payload: %w", err)
	}
	_, err = o.db.ExecContext(ctx, `
		INSERT INTO techstack_notification_outbox (
			idempotency_key, tenant_id, auth0_user_id, topic_slug, payload_json
		) VALUES ($1, NULLIF($2, ''), $3, $4, $5::jsonb)
		ON CONFLICT (idempotency_key) DO NOTHING
	`, event.IdempotencyKey, event.OrganizationID, event.Auth0UserID, string(event.Topic), payload)
	return err
}

// ResolveWorkerRecipient maps a tenant-bound monitoring agent to its owning
// Auth0 subject without bypassing the workers RLS policy.
func (o *Outbox) ResolveWorkerRecipient(ctx context.Context, tenantID, agentID string) (string, error) {
	if o == nil || o.db == nil {
		return "", fmt.Errorf("notifications: outbox database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	agentID = strings.TrimSpace(agentID)
	if tenantID == "" || agentID == "" {
		return "", ErrRecipientRequired
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		return "", err
	}
	var recipient string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(owner_subject_id, '')
		FROM workers WHERE tenant_id=$1 AND id=$2
	`, tenantID, agentID).Scan(&recipient); err != nil {
		return "", err
	}
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return "", ErrRecipientRequired
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return recipient, nil
}

// ResolveManagedLeaseRecipient maps a provider-control lease back to the
// Auth0 subject that owns its durable capacity reservation. Provider failures
// arrive after HTTP admission, so the terminal provider receipt no longer
// carries request-scoped user identity; the reservation is the canonical
// persisted owner binding for that exact lease.
func (o *Outbox) ResolveManagedLeaseRecipient(ctx context.Context, tenantID, leaseID string) (string, error) {
	if o == nil || o.db == nil {
		return "", fmt.Errorf("notifications: outbox database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	leaseID = strings.TrimSpace(leaseID)
	if tenantID == "" || leaseID == "" {
		return "", ErrRecipientRequired
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		return "", err
	}
	var recipient string
	if err := tx.QueryRowContext(ctx, `
		SELECT owner_subject_id
		FROM managed_runtime_capacity_reservations
		WHERE tenant_id=$1 AND lease_id=$2
		ORDER BY reserved_at DESC
		LIMIT 1
	`, tenantID, leaseID).Scan(&recipient); err != nil {
		return "", err
	}
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return "", ErrRecipientRequired
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return recipient, nil
}

type outboxItem struct {
	ProductEvent
	Attempts int
}

func (o *Outbox) Run(ctx context.Context) {
	if o == nil || o.db == nil || o.client == nil {
		return
	}
	ticker := time.NewTicker(outboxPollInterval)
	defer ticker.Stop()
	for {
		if err := o.processOne(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, context.Canceled) {
			o.logger.Warn("notification_outbox_process_failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (o *Outbox) processOne(ctx context.Context) error {
	now := o.now()
	item, err := o.claim(ctx, now)
	if err != nil {
		return err
	}
	result, dispatchErr := o.client.DispatchProduct(ctx, item.ProductEvent)
	if dispatchErr == nil {
		_, err = o.db.ExecContext(ctx, `
			UPDATE techstack_notification_outbox
			SET status='delivered', dispatch_id=$2, feed_item_id=$3,
				delivered_at=$4, last_error=NULL, updated_at=$4
			WHERE idempotency_key=$1
		`, item.IdempotencyKey, result.DispatchID, result.FeedItemID, now)
		return err
	}

	item.Attempts++
	terminal := item.Attempts >= outboxMaxAttempts || isTerminalDispatchError(dispatchErr)
	status := "retrying"
	next := now.Add(retryDelay(item.Attempts))
	if terminal {
		status = "failed"
		next = now
	}
	_, err = o.db.ExecContext(ctx, `
		UPDATE techstack_notification_outbox
		SET status=$2, attempts=$3, next_attempt_at=$4, last_error=$5, updated_at=$6
		WHERE idempotency_key=$1
	`, item.IdempotencyKey, status, item.Attempts, next, truncateError(dispatchErr), now)
	return err
}

func (o *Outbox) claim(ctx context.Context, now time.Time) (*outboxItem, error) {
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var item outboxItem
	var payload []byte
	err = tx.QueryRowContext(ctx, `
		SELECT idempotency_key, COALESCE(tenant_id,''), auth0_user_id, topic_slug,
			payload_json::text, attempts
		FROM techstack_notification_outbox
		WHERE status IN ('pending','retrying') AND next_attempt_at <= $1
		ORDER BY next_attempt_at, created_at
		LIMIT 1 FOR UPDATE SKIP LOCKED
	`, now).Scan(&item.IdempotencyKey, &item.OrganizationID, &item.Auth0UserID, &item.Topic, &payload, &item.Attempts)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &item.Payload); err != nil {
		return nil, err
	}
	if channel, ok := item.Payload[deliveryChannelPayloadKey].(string); ok {
		item.Channel = strings.TrimSpace(channel)
		delete(item.Payload, deliveryChannelPayloadKey)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE techstack_notification_outbox SET next_attempt_at=$2, updated_at=$1
		WHERE idempotency_key=$3
	`, now, now.Add(outboxClaimLease), item.IdempotencyKey); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

func isTerminalDispatchError(err error) bool {
	var dispatchErr *DispatchError
	return errors.As(err, &dispatchErr) && !dispatchErr.Retryable
}

func retryDelay(attempt int) time.Duration {
	delay := time.Second * time.Duration(1<<min(attempt, 8))
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func truncateError(err error) string {
	message := err.Error()
	if len(message) > 1000 {
		return message[:1000]
	}
	return message
}
