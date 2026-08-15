package controlplane

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// JobExecutionLeaseTTL is how long a running job's execution lease stays
	// valid without a renewal. The orchestrator's progress heartbeat renews it
	// every 500ms, so this window absorbs 180 consecutive missed heartbeats
	// before a live execution could ever look orphaned.
	JobExecutionLeaseTTL = 90 * time.Second
	// jobExecutionLeaseTTLInterval is the same window expressed for SQL, where
	// clock_timestamp() rather than any caller clock stamps the deadline.
	jobExecutionLeaseTTLInterval = "90 seconds"
)

// processExecutionOwnerID identifies this control-plane process for the whole
// life of the process. It is deliberately NOT pkg/instance's identity: that ID
// is persisted under the data directory and survives restarts, so it cannot
// distinguish a live execution from one abandoned by a previous boot. Every
// store built in this process shares this value, which is what lets the
// reclaimer tell "another live replica owns it" from "nobody owns it".
var processExecutionOwnerID = uuid.NewString()

// ProcessExecutionOwnerID exposes the boot-scoped owner identity so callers can
// log it and so the reclaimer can recognise its own executions.
func ProcessExecutionOwnerID() string { return processExecutionOwnerID }

// JobExecutionLease is the secret-free view of one leased running execution.
// It is deliberately narrower than Job: the reclaim scan needs identity, the
// lease fence and the durable receipt that classifies recoverability, and
// nothing else.
type JobExecutionLease struct {
	JobID          string
	TenantID       string
	StackID        string
	Type           string
	Step           string
	OwnerID        string
	StartedAt      *time.Time
	UpdatedAt      time.Time
	LeaseExpiresAt time.Time
	Result         map[string]any
}

// ReclaimExpiredJobExecutionRequest terminalizes exactly one running job whose
// execution lease has expired.
//
// Every field participates in the same conditional UPDATE, so the reclaim can
// never touch a live execution: if the owner re-stamped its lease between the
// scan and this write, ExpectedOwnerID or LeaseExpiredBefore no longer match
// and the store reports ErrConflict instead of stealing the row.
type ReclaimExpiredJobExecutionRequest struct {
	TenantID string
	JobID    string
	StackID  string
	// ExpectedOwnerID is the owner observed by the scan. An empty value means
	// the scan observed no owner and requires the row to still have none.
	ExpectedOwnerID string
	// LeaseExpiredBefore is the instant the lease must not have outlived.
	LeaseExpiredBefore time.Time
	Error              string
	ErrorDetails       string
	// ResultPatch is merged into result_json so the ledger carries the durable,
	// operator-readable reason rather than a bare state change.
	ResultPatch map[string]any
	ReclaimedAt time.Time
}

// ResumeExpiredJobExecutionRequest returns one exact resumable wait to pending
// without discarding its durable checkpoint.
type ResumeExpiredJobExecutionRequest struct {
	TenantID           string
	JobID              string
	StackID            string
	ExpectedOwnerID    string
	LeaseExpiredBefore time.Time
	ResumedAt          time.Time
}

// JobExecutionReclaimStore is the narrow surface the reclaimer drives. It is
// kept out of JobStore on purpose: reclaim is a maintenance concern with its
// own cross-tenant discovery, exactly like serverregistry.SweepStore.
type JobExecutionReclaimStore interface {
	// ListJobExecutionReclaimTenants pages tenant IDs from the secret-free
	// wake-up directory whose earliest leased execution is due for inspection.
	ListJobExecutionReclaimTenants(ctx context.Context, afterTenantID string, limit int, leaseCutoff time.Time) ([]string, error)
	// ListExpiredJobExecutionLeases returns the tenant's running jobs whose
	// lease already lapsed, oldest first.
	ListExpiredJobExecutionLeases(ctx context.Context, tenantID string, expiredBefore time.Time, limit int) ([]JobExecutionLease, error)
	// ReclaimExpiredJobExecution terminalizes one exact expired execution and
	// releases the per-stack execution claim it was holding.
	ReclaimExpiredJobExecution(ctx context.Context, req ReclaimExpiredJobExecutionRequest) (*Job, error)
	// ResumeExpiredJobExecution releases an expired execution claim and returns
	// an already-idempotent provider wait to the pending queue.
	ResumeExpiredJobExecution(ctx context.Context, req ResumeExpiredJobExecutionRequest) (*Job, error)
	// CompactJobExecutionReclaimTenant refreshes or retires the tenant's
	// wake-up entry after a pass. Implementations without a directory no-op.
	CompactJobExecutionReclaimTenant(ctx context.Context, tenantID string) error
}

func normalizeResumeExpiredJobExecutionRequest(req ResumeExpiredJobExecutionRequest) ResumeExpiredJobExecutionRequest {
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.JobID = strings.TrimSpace(req.JobID)
	req.StackID = strings.TrimSpace(req.StackID)
	req.ExpectedOwnerID = strings.TrimSpace(req.ExpectedOwnerID)
	return req
}

func validResumeExpiredJobExecutionRequest(req ResumeExpiredJobExecutionRequest) bool {
	return req.TenantID != "" && req.JobID != "" && req.StackID != "" && !req.LeaseExpiredBefore.IsZero()
}

func normalizeReclaimExpiredJobExecutionRequest(req ReclaimExpiredJobExecutionRequest) ReclaimExpiredJobExecutionRequest {
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.JobID = strings.TrimSpace(req.JobID)
	req.StackID = strings.TrimSpace(req.StackID)
	req.ExpectedOwnerID = strings.TrimSpace(req.ExpectedOwnerID)
	req.Error = strings.TrimSpace(req.Error)
	req.ErrorDetails = strings.TrimSpace(req.ErrorDetails)
	return req
}

func validReclaimExpiredJobExecutionRequest(req ReclaimExpiredJobExecutionRequest) bool {
	return req.TenantID != "" && req.JobID != "" && req.StackID != "" &&
		req.Error != "" && !req.LeaseExpiredBefore.IsZero()
}
