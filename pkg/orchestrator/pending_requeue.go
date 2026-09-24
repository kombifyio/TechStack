package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

const (
	// jobRecoveryRedactionMarkerKey records which payload keys were stripped
	// before the durable write, so a later boot pass knows the job is not
	// safely resumable and leaves it to the user-facing retry path.
	jobRecoveryRedactionMarkerKey = "_recovery_redacted_keys"

	// pendingJobTenantPage bounds one directory page.
	pendingJobTenantPage = 100

	// pendingJobPage bounds the per-tenant pending read.
	pendingJobPage = 50

	// maxPendingJobRequeuesPerBoot bounds one boot pass so a pathological
	// directory cannot flood the queue.
	maxPendingJobRequeuesPerBoot = 200

	// pendingJobRecoveryWindow bounds how old a stranded pending job may be to
	// be re-enqueued. A crash is followed by a restart within seconds; a job
	// that has been pending longer was almost certainly superseded by a later
	// dispatch and must not be resurrected behind the user's back.
	pendingJobRecoveryWindow = time.Hour
)

// jobRecoveryRedactedPayloadKeys are the handler-input keys that never reach
// the durable jobs row:
//   - owner_spec_bootstrap carries a one-time owner-spec capability token,
//   - prepared_managed_lease is sealed at-most-once provider authority,
//   - intent_raw is imported user configuration that may embed secrets.
//
// A job admitted with any of them is not auto-resumed after a restart; the
// user-facing retry path re-issues the credential or sealed request instead.
var jobRecoveryRedactedPayloadKeys = []string{
	"owner_spec_bootstrap",
	"prepared_managed_lease",
	"intent_raw",
}

// redactedJobPayloadForRecovery projects a handler payload onto the durable
// recovery shape. Every call must pass through here before a payload reaches
// the jobs row.
func redactedJobPayloadForRecovery(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	out := make(map[string]any, len(payload))
	redacted := make([]string, 0, len(jobRecoveryRedactedPayloadKeys))
	for key, value := range payload {
		if isJobRecoveryRedactedKey(key) {
			redacted = append(redacted, key)
			continue
		}
		out[key] = value
	}
	if len(redacted) > 0 {
		sort.Strings(redacted)
		out[jobRecoveryRedactionMarkerKey] = redacted
	}
	return out
}

func isJobRecoveryRedactedKey(key string) bool {
	for _, candidate := range jobRecoveryRedactedPayloadKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

func jobRecoveryRedactedKeys(payload map[string]any) []string {
	raw, ok := payload[jobRecoveryRedactionMarkerKey]
	if !ok {
		return nil
	}
	switch typed := raw.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok {
				out = append(out, value)
			}
		}
		return out
	default:
		return []string{"unknown"}
	}
}

// pendingJobMaxAttempts mirrors the dispatch defaults so a recovered job keeps
// the retry budget its origin gave it.
func pendingJobMaxAttempts(jobType jobs.JobType) int {
	if jobType == jobs.JobTypeProvision {
		return 3
	}
	return 1
}

// RequeuePendingJobs re-submits durable pending jobs that were admitted before
// a restart but never entered this process's queue. A job is only re-enqueued
// when all of the following hold:
//
//   - its payload survived redaction (no one-time capability or sealed
//     provider authority was stripped),
//   - a handler is registered for its type, so the queue cannot terminalize
//     it as unknown,
//   - it is the stack's newest job and is inside the recovery window, so a
//     later dispatch supersedes it instead of racing it.
//
// The per-stack execution claim remains the single-flight authority: a job
// another replica already picked up is deferred by the queue, not duplicated.
func (o *Orchestrator) RequeuePendingJobs(ctx context.Context) (int, error) {
	if o == nil || o.jobStore == nil || o.queue == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = o.ctx
	}
	now := time.Now().UTC()
	requeued := 0
	skipped := 0
	afterTenant := ""
	for {
		tenantIDs, err := o.jobStore.ListPendingJobTenants(ctx, afterTenant, pendingJobTenantPage)
		if err != nil {
			return requeued, fmt.Errorf("list pending job tenants: %w", err)
		}
		if len(tenantIDs) == 0 {
			break
		}
		for _, tenantID := range tenantIDs {
			afterTenant = tenantID
			count, skipCount := o.requeueTenantPendingJobs(ctx, tenantID, now)
			requeued += count
			skipped += skipCount
			if requeued >= maxPendingJobRequeuesPerBoot {
				o.log.Info("pending_jobs_requeue_bounded", "requeued", requeued, "skipped", skipped)
				return requeued, nil
			}
			if err := o.jobStore.CompactPendingJobTenant(ctx, tenantID); err != nil {
				o.log.Warn("pending_job_directory_compact_failed", "tenant_id", tenantID, "error", err)
			}
		}
		if len(tenantIDs) < pendingJobTenantPage {
			break
		}
	}
	o.log.Info("pending_jobs_requeued", "requeued", requeued, "skipped", skipped)
	return requeued, nil
}

func (o *Orchestrator) requeueTenantPendingJobs(ctx context.Context, tenantID string, now time.Time) (int, int) {
	rows, err := o.jobStore.ListPendingJobs(ctx, tenantID, pendingJobPage)
	if err != nil {
		o.log.Warn("pending_jobs_list_failed", "tenant_id", tenantID, "error", err)
		return 0, 0
	}
	requeued := 0
	skipped := 0
	for index := range rows {
		row := rows[index]
		if redacted := jobRecoveryRedactedKeys(row.Payload); len(redacted) > 0 {
			skipped++
			o.log.Warn("pending_job_requeue_skipped_redacted",
				"job_id", row.ID, "stack_id", row.StackID, "type", row.Type,
				"redacted_keys", strings.Join(redacted, ","))
			continue
		}
		if len(row.Payload) == 0 {
			skipped++
			o.log.Warn("pending_job_requeue_skipped_no_payload",
				"job_id", row.ID, "stack_id", row.StackID, "type", row.Type)
			continue
		}
		if row.CreatedAt.Before(now.Add(-pendingJobRecoveryWindow)) {
			skipped++
			o.log.Warn("pending_job_requeue_skipped_stale",
				"job_id", row.ID, "stack_id", row.StackID, "type", row.Type,
				"created_at", row.CreatedAt.UTC().Format(time.RFC3339))
			continue
		}
		jobType := jobs.JobType(strings.TrimSpace(row.Type))
		if !o.queue.HasHandler(jobType) {
			skipped++
			o.log.Warn("pending_job_requeue_skipped_unknown_type",
				"job_id", row.ID, "stack_id", row.StackID, "type", row.Type)
			continue
		}
		superseded, supersedeErr := o.pendingJobIsSuperseded(ctx, tenantID, row)
		if supersedeErr != nil {
			o.log.Warn("pending_job_supersede_check_failed",
				"job_id", row.ID, "stack_id", row.StackID, "error", supersedeErr)
			continue
		}
		if superseded {
			skipped++
			continue
		}
		payload := make(map[string]any, len(row.Payload))
		for key, value := range row.Payload {
			payload[key] = value
		}
		job := &jobs.Job{
			ID:          row.ID,
			Type:        jobType,
			TargetType:  targetTypeStack,
			TargetID:    row.StackID,
			Payload:     payload,
			Result:      row.Result,
			MaxAttempts: pendingJobMaxAttempts(jobType),
		}
		if err := o.enqueueWithSync(job, tenantID); err != nil {
			o.log.Warn("pending_job_requeue_failed", "job_id", row.ID, "stack_id", row.StackID, "error", err)
			continue
		}
		requeued++
		o.log.Info("pending_job_requeued", "job_id", row.ID, "stack_id", row.StackID, "type", row.Type)
	}
	return requeued, skipped
}

// pendingJobIsSuperseded reports whether a newer dispatch exists for the same
// stack. The check is order-independent: the list contract's ordering must not
// decide recovery, and a same-instant sibling is left to the per-stack
// execution claim instead of being judged here.
func (o *Orchestrator) pendingJobIsSuperseded(ctx context.Context, tenantID string, row controlplane.Job) (bool, error) {
	if strings.TrimSpace(row.StackID) == "" {
		return false, nil
	}
	recent, err := o.jobStore.ListJobsByStack(ctx, tenantID, row.StackID, pendingJobPage)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	if len(recent) == 0 {
		return true, nil
	}
	for index := range recent {
		candidate := recent[index]
		if candidate.ID == row.ID {
			continue
		}
		if candidate.CreatedAt.After(row.CreatedAt) {
			return true, nil
		}
	}
	return false, nil
}
