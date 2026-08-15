package vmleases

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

var ErrOperationJournalUnavailable = errors.New("vmleases: operation journal unavailable")

const (
	OperationEventEnrollment    = "enrollment"
	OperationEventRetry         = "retry"
	OperationEventRuntimeAction = "runtime_action"
	OperationEventDecommission  = "decommission"

	OperationStatusPending          = "pending"
	OperationStatusRetrying         = "retrying"
	OperationStatusEnrolled         = "enrolled"
	OperationStatusFailed           = "failed"
	OperationStatusStarted          = "started"
	OperationStatusStopped          = "stopped"
	OperationStatusSSHEnabled       = "ssh_enabled"
	OperationStatusSSHDisabled      = "ssh_disabled"
	OperationStatusSSHInfoRequested = "ssh_info_requested"
	OperationStatusStatusRequested  = "status_requested"
	OperationStatusDecommissioned   = "decommissioned"
	OperationStatusCustodyResolved  = "custody_resolved"

	OperationActorSystem = "system"
)

type OperationEvent struct {
	TenantID                 string          `json:"tenant_id"`
	LeaseID                  vmlease.LeaseID `json:"lease_id"`
	EventType                string          `json:"event_type"`
	Status                   string          `json:"status"`
	Actor                    string          `json:"actor,omitempty"`
	Error                    string          `json:"error,omitempty"`
	ResourceGenerationDigest string          `json:"resource_generation_digest,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
}

type OperationJournalStore interface {
	AppendOperation(ctx context.Context, event OperationEvent) error
}

type OperationJournalReader interface {
	ListOperations(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, limit int) ([]OperationEvent, error)
}

// ConfirmedDecommissionReader performs an exact, tenant-scoped lookup for the
// durable proof that a concrete resource generation was decommissioned. A
// destructive retry must use this capability instead of scanning a truncated
// operation list.
type ConfirmedDecommissionReader interface {
	HasConfirmedDecommission(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, resourceGenerationDigest string) (bool, error)
}

func (s *Service) RecordOperation(ctx context.Context, event OperationEvent) error {
	return s.recordOperation(ctx, event)
}

// RecordOperationStrict persists an event or returns an explicit capability
// error. RecordOperation remains best-effort for compatibility with stores
// used by non-destructive runtime actions; destructive flows must use this
// method before changing authoritative lease state.
func (s *Service) RecordOperationStrict(ctx context.Context, event OperationEvent) error {
	if s == nil || s.store == nil {
		return ErrOperationJournalUnavailable
	}
	if _, ok := s.store.(OperationJournalStore); !ok {
		return ErrOperationJournalUnavailable
	}
	return s.recordOperation(ctx, event)
}

func (s *Service) ListOperations(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, limit int) ([]OperationEvent, error) {
	if s == nil || s.store == nil {
		return []OperationEvent{}, nil
	}
	reader, ok := s.store.(OperationJournalReader)
	if !ok {
		return []OperationEvent{}, nil
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	if leaseID == "" {
		return nil, ErrNotFound
	}
	return reader.ListOperations(ctx, tenantID, leaseID, limit)
}

// HasConfirmedDecommission reports whether the exact resource generation has
// a durable successful decommission proof. Unlike ListOperations, absence of
// the store capability is explicit so destructive replay can fail closed.
func (s *Service) HasConfirmedDecommission(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, resourceGenerationDigest string) (bool, error) {
	if s == nil || s.store == nil {
		return false, ErrOperationJournalUnavailable
	}
	reader, ok := s.store.(ConfirmedDecommissionReader)
	if !ok {
		return false, ErrOperationJournalUnavailable
	}
	tenantID = strings.TrimSpace(tenantID)
	resourceGenerationDigest = strings.TrimSpace(resourceGenerationDigest)
	if tenantID == "" {
		return false, ErrTenantRequired
	}
	if leaseID == "" {
		return false, ErrNotFound
	}
	if !validResourceGenerationDigest(resourceGenerationDigest) {
		return false, ErrResourceGenerationDigest
	}
	return reader.HasConfirmedDecommission(ctx, tenantID, leaseID, resourceGenerationDigest)
}

func (s *Service) recordOperation(ctx context.Context, event OperationEvent) error {
	if s == nil || s.store == nil {
		return nil
	}
	journal, ok := s.store.(OperationJournalStore)
	if !ok {
		return nil
	}
	event.TenantID = strings.TrimSpace(event.TenantID)
	event.EventType = strings.TrimSpace(event.EventType)
	event.Status = strings.TrimSpace(event.Status)
	event.Actor = strings.TrimSpace(event.Actor)
	event.Error = strings.TrimSpace(event.Error)
	event.ResourceGenerationDigest = strings.TrimSpace(event.ResourceGenerationDigest)
	if event.TenantID == "" {
		return ErrTenantRequired
	}
	if event.LeaseID == "" {
		return ErrNotFound
	}
	if event.EventType == OperationEventDecommission && event.Status == OperationStatusDecommissioned && !validResourceGenerationDigest(event.ResourceGenerationDigest) {
		return ErrResourceGenerationDigest
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.now().UTC()
	} else {
		event.CreatedAt = event.CreatedAt.UTC()
	}
	if event.Actor == "" {
		event.Actor = OperationActorSystem
	}
	return journal.AppendOperation(ctx, event)
}
