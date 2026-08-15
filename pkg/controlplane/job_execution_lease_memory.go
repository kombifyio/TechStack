package controlplane

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

var _ JobExecutionReclaimStore = (*MemoryStore)(nil)

// memoryJobExecutionLease mirrors the two durable lease columns. It is kept
// beside the job map rather than on Job so the shared row projection stays
// identical to what the Postgres scan returns.
type memoryJobExecutionLease struct {
	OwnerID   string
	ExpiresAt time.Time
}

func (s *MemoryStore) issueJobExecutionLeaseLocked(jobID string, at time.Time) {
	if s.jobLeases == nil {
		s.jobLeases = make(map[string]memoryJobExecutionLease)
	}
	s.jobLeases[jobID] = memoryJobExecutionLease{
		OwnerID:   processExecutionOwnerID,
		ExpiresAt: at.UTC().Add(JobExecutionLeaseTTL),
	}
}

func (s *MemoryStore) renewJobExecutionLeaseLocked(jobID string, at time.Time) {
	if s.jobLeases == nil {
		return
	}
	lease, ok := s.jobLeases[jobID]
	if !ok {
		return
	}
	lease.ExpiresAt = at.UTC().Add(JobExecutionLeaseTTL)
	s.jobLeases[jobID] = lease
}

func (s *MemoryStore) releaseJobExecutionLeaseLocked(jobID string) {
	delete(s.jobLeases, jobID)
}

// ExpireJobExecutionLease backdates one lease so a test can reproduce a process
// death without waiting out the real TTL. It is the memory-store equivalent of
// an orphan whose owner never came back.
func (s *MemoryStore) ExpireJobExecutionLease(jobID string, expiredAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobLeases == nil {
		return
	}
	lease, ok := s.jobLeases[jobID]
	if !ok {
		return
	}
	lease.ExpiresAt = expiredAt.UTC()
	s.jobLeases[jobID] = lease
}

// SetJobExecutionLeaseOwner rewrites one lease's owner so a test can express
// "this execution belongs to a process that is gone".
func (s *MemoryStore) SetJobExecutionLeaseOwner(jobID, ownerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobLeases == nil {
		return
	}
	lease, ok := s.jobLeases[jobID]
	if !ok {
		return
	}
	lease.OwnerID = strings.TrimSpace(ownerID)
	s.jobLeases[jobID] = lease
}

// ListJobExecutionReclaimTenants reports the tenants that currently hold a
// leased running execution due for inspection.
func (s *MemoryStore) ListJobExecutionReclaimTenants(
	_ context.Context,
	afterTenantID string,
	limit int,
	leaseCutoff time.Time,
) ([]string, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("controlplane: job execution reclaim tenant limit from 1 to 100 is required")
	}
	afterTenantID = strings.TrimSpace(afterTenantID)
	s.mu.RLock()
	defer s.mu.RUnlock()

	earliest := make(map[string]time.Time)
	for jobID, lease := range s.jobLeases {
		job, ok := s.jobs[jobID]
		if !ok || job.State != jobStateRunning {
			continue
		}
		if current, seen := earliest[job.TenantID]; seen && !lease.ExpiresAt.Before(current) {
			continue
		}
		earliest[job.TenantID] = lease.ExpiresAt
	}
	tenants := make([]string, 0, len(earliest))
	for tenantID, expiresAt := range earliest {
		if tenantID <= afterTenantID || expiresAt.After(leaseCutoff.UTC()) {
			continue
		}
		tenants = append(tenants, tenantID)
	}
	sort.Strings(tenants)
	if len(tenants) > limit {
		tenants = tenants[:limit]
	}
	return tenants, nil
}

// CompactJobExecutionReclaimTenant has no directory to maintain in memory.
func (s *MemoryStore) CompactJobExecutionReclaimTenant(_ context.Context, _ string) error {
	return nil
}

// ListExpiredJobExecutionLeases returns the tenant's running jobs whose lease
// has lapsed, oldest lease first.
func (s *MemoryStore) ListExpiredJobExecutionLeases(
	_ context.Context,
	tenantID string,
	expiredBefore time.Time,
	limit int,
) ([]JobExecutionLease, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	if limit <= 0 {
		limit = 50
	}
	cutoff := expiredBefore.UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]JobExecutionLease, 0)
	for jobID, lease := range s.jobLeases {
		job, ok := s.jobs[jobID]
		if !ok || job.TenantID != tenantID || job.State != jobStateRunning {
			continue
		}
		if lease.ExpiresAt.After(cutoff) {
			continue
		}
		out = append(out, JobExecutionLease{
			JobID:          job.ID,
			TenantID:       job.TenantID,
			StackID:        job.StackID,
			Type:           job.Type,
			Step:           job.Step,
			OwnerID:        lease.OwnerID,
			StartedAt:      cloneTime(job.StartedAt),
			UpdatedAt:      job.UpdatedAt,
			LeaseExpiresAt: lease.ExpiresAt,
			Result:         cloneMap(job.Result),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LeaseExpiresAt.Equal(out[j].LeaseExpiresAt) {
			return out[i].JobID < out[j].JobID
		}
		return out[i].LeaseExpiresAt.Before(out[j].LeaseExpiresAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ReclaimExpiredJobExecution terminalizes one exact expired execution. Every
// fact the scan observed is rechecked here, so a renewed lease is refused.
func (s *MemoryStore) ReclaimExpiredJobExecution(
	_ context.Context,
	req ReclaimExpiredJobExecutionRequest,
) (*Job, error) {
	req = normalizeReclaimExpiredJobExecutionRequest(req)
	if !validReclaimExpiredJobExecutionRequest(req) {
		return nil, fmt.Errorf("controlplane: exact expired job execution identity required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[req.JobID]
	if !ok || job.TenantID != req.TenantID || job.StackID != req.StackID || job.State != jobStateRunning {
		return nil, ErrConflict
	}
	lease, leased := s.jobLeases[req.JobID]
	if !leased || lease.OwnerID != req.ExpectedOwnerID || lease.ExpiresAt.After(req.LeaseExpiredBefore.UTC()) {
		return nil, ErrConflict
	}

	reclaimedAt := req.ReclaimedAt.UTC()
	if reclaimedAt.IsZero() {
		reclaimedAt = s.now()
	}
	job.State = jobStateFailed
	job.Error = req.Error
	job.ErrorDetails = req.ErrorDetails
	job.CompletedAt = &reclaimedAt
	job.UpdatedAt = reclaimedAt
	if len(req.ResultPatch) > 0 {
		merged := cloneMap(job.Result)
		if merged == nil {
			merged = map[string]any{}
		}
		for key, value := range req.ResultPatch {
			merged[key] = value
		}
		job.Result = merged
	}
	s.jobs[job.ID] = job
	s.releaseJobExecutionLeaseLocked(job.ID)
	return cloneJob(job), nil
}

func (s *MemoryStore) ResumeExpiredJobExecution(
	_ context.Context,
	req ResumeExpiredJobExecutionRequest,
) (*Job, error) {
	req = normalizeResumeExpiredJobExecutionRequest(req)
	if !validResumeExpiredJobExecutionRequest(req) {
		return nil, fmt.Errorf("controlplane: exact resumable job execution identity required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[req.JobID]
	if !ok || job.TenantID != req.TenantID || job.StackID != req.StackID || job.State != jobStateRunning {
		return nil, ErrConflict
	}
	lease, leased := s.jobLeases[req.JobID]
	if !leased || lease.OwnerID != req.ExpectedOwnerID || lease.ExpiresAt.After(req.LeaseExpiredBefore.UTC()) {
		return nil, ErrConflict
	}
	resumedAt := req.ResumedAt.UTC()
	if resumedAt.IsZero() {
		resumedAt = s.now()
	}
	job.State = jobStatePending
	job.ScheduledFor = resumedAt
	job.CompletedAt = nil
	job.Error = ""
	job.ErrorDetails = ""
	job.UpdatedAt = resumedAt
	s.jobs[job.ID] = job
	s.releaseJobExecutionLeaseLocked(job.ID)
	return cloneJob(job), nil
}
