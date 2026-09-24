package providercontrol

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// AsyncRequest is the provider-neutral correlation for an asynchronously
// accepted provider mutation. It carries no credentials or response body.
type AsyncRequest struct {
	TenantID, OperationID, ProviderID     string
	RequestID, StatusRef, TargetNativeRef string
	State, FailureCode, FailureMessage    string
	CreatedAt, UpdatedAt                  time.Time
}

// AsyncRequestStore persists the request identity before an adapter returns
// control to the durable provider-operation worker.
type AsyncRequestStore struct{ database *sql.DB }

func NewAsyncRequestStore(database *sql.DB) (*AsyncRequestStore, error) {
	if database == nil {
		return nil, fmt.Errorf("providercontrol: async request database is required")
	}
	return &AsyncRequestStore{database: database}, nil
}

func (s *AsyncRequestStore) Record(ctx context.Context, request AsyncRequest) error {
	request = normalizeAsyncRequest(request)
	if incompleteAsyncRequestIdentity(request) {
		return fmt.Errorf("providercontrol: async request identity is incomplete")
	}
	return withTenantDatabase(ctx, s.database, request.TenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
		INSERT INTO provider_async_requests (
			tenant_id, operation_id, provider_id, request_id, status_ref,
			target_native_ref, state, failure_code, failure_message, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,NULL,NULL,$8,$8)
		ON CONFLICT (tenant_id, operation_id) DO NOTHING`,
			request.TenantID, request.OperationID, request.ProviderID, request.RequestID,
			request.StatusRef, request.TargetNativeRef, request.State, request.CreatedAt.UTC())
		if err != nil {
			return fmt.Errorf("providercontrol: record async request: %w", err)
		}
		stored, ok, err := loadAsyncRequest(ctx, tx, request.TenantID, request.OperationID)
		if err != nil {
			return err
		}
		if !ok || !asyncRequestReplayMatches(stored, request) {
			return fmt.Errorf("providercontrol: async request replay identity mismatch")
		}
		return nil
	})
}

func incompleteAsyncRequestIdentity(request AsyncRequest) bool {
	return request.TenantID == "" || request.OperationID == "" || request.ProviderID == "" || request.RequestID == "" ||
		request.StatusRef == "" || request.TargetNativeRef == "" || request.State == "" || request.CreatedAt.IsZero()
}

func asyncRequestReplayMatches(stored, request AsyncRequest) bool {
	return stored.ProviderID == request.ProviderID && stored.RequestID == request.RequestID &&
		stored.StatusRef == request.StatusRef && stored.TargetNativeRef == request.TargetNativeRef
}

func (s *AsyncRequestStore) Load(ctx context.Context, tenantID, operationID string) (AsyncRequest, bool, error) {
	var request AsyncRequest
	var ok bool
	err := withTenantDatabase(ctx, s.database, tenantID, func(tx *sql.Tx) error {
		var err error
		request, ok, err = loadAsyncRequest(ctx, tx, tenantID, operationID)
		return err
	})
	return request, ok, err
}

func loadAsyncRequest(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, tenantID, operationID string) (AsyncRequest, bool, error) {
	var request AsyncRequest
	err := queryer.QueryRowContext(ctx, `
		SELECT tenant_id, operation_id, provider_id, request_id, status_ref,
		       target_native_ref, state, COALESCE(failure_code,''),
		       COALESCE(failure_message,''), created_at, updated_at
		FROM provider_async_requests WHERE tenant_id=$1 AND operation_id=$2`,
		strings.TrimSpace(tenantID), strings.TrimSpace(operationID)).Scan(
		&request.TenantID, &request.OperationID, &request.ProviderID, &request.RequestID,
		&request.StatusRef, &request.TargetNativeRef, &request.State, &request.FailureCode,
		&request.FailureMessage, &request.CreatedAt, &request.UpdatedAt)
	if err == sql.ErrNoRows {
		return AsyncRequest{}, false, nil
	}
	if err != nil {
		return AsyncRequest{}, false, fmt.Errorf("providercontrol: load async request: %w", err)
	}
	return request, true, nil
}

func (s *AsyncRequestStore) Observe(ctx context.Context, tenantID, operationID, state, code, message string, now time.Time) error {
	return withTenantDatabase(ctx, s.database, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
		UPDATE provider_async_requests
		SET state=$3, failure_code=NULLIF($4,''), failure_message=NULLIF($5,''), updated_at=$6
		WHERE tenant_id=$1 AND operation_id=$2`, strings.TrimSpace(tenantID), strings.TrimSpace(operationID),
			strings.TrimSpace(state), strings.TrimSpace(code), strings.TrimSpace(message), now.UTC())
		if err != nil {
			return fmt.Errorf("providercontrol: update async request: %w", err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			return fmt.Errorf("providercontrol: async request observation target is missing")
		}
		return nil
	})
}

func normalizeAsyncRequest(request AsyncRequest) AsyncRequest {
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.OperationID = strings.TrimSpace(request.OperationID)
	request.ProviderID = strings.TrimSpace(request.ProviderID)
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.StatusRef = strings.TrimSpace(request.StatusRef)
	request.TargetNativeRef = strings.TrimSpace(request.TargetNativeRef)
	request.State = strings.TrimSpace(request.State)
	return request
}
