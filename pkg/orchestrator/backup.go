package orchestrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/releasechannel"
)

// EnqueueBackup admits one already-admitted backup into the queue. It is the
// narrow seam the due-stack scanner drives; the scanner owns the schedule and
// the gate, the orchestrator owns queue mechanics, and neither reaches into
// the other.
//
// The entitlement and quota gate resolved before this is called. Re-resolving
// here would either duplicate the decision or disagree with it while the job
// is already on its way to becoming durable.
func (o *Orchestrator) EnqueueBackup(_ context.Context, payload jobs.BackupPayload) error {
	if o == nil || o.queue == nil {
		return fmt.Errorf("orchestrator is not configured for backup jobs")
	}
	job, err := backupJob(payload, jobs.NewID())
	if err != nil {
		return err
	}
	unlockStack := o.stackLocks.lock(job.TargetID)
	defer unlockStack()
	return o.enqueueWithSync(job, payload.TenantID)
}

func backupJob(payload jobs.BackupPayload, id string) (*jobs.Job, error) {
	tenantID, stackID := strings.TrimSpace(payload.TenantID), strings.TrimSpace(payload.StackID)
	if tenantID == "" || stackID == "" || strings.TrimSpace(payload.OwnerID) == "" {
		return nil, fmt.Errorf("backup job requires exact tenant, stack and owner identity")
	}
	return &jobs.Job{
		ID:         id,
		Type:       jobs.JobTypeBackup,
		TargetType: targetTypeStack,
		TargetID:   stackID,
		TargetName: strings.TrimSpace(payload.StackName),
		Payload: map[string]interface{}{
			stackTenantIDField:    tenantID,
			stackOwnerIDField:     strings.TrimSpace(payload.OwnerID),
			"stack_id":            stackID,
			"stack_name":          strings.TrimSpace(payload.StackName),
			"include_content":     payload.IncludeContent,
			"quota_bytes":         payload.QuotaBytes,
			"admitted_used_bytes": payload.AdmittedUsedBytes,
		},
	}, nil
}

// ReserveBackupTx writes to the existing jobs authority in the scheduler's
// transaction. A slot has one deterministic job identity, and a lost response
// cannot commit cadence without its job or replay a second backup for the slot.
func (o *Orchestrator) ReserveBackupTx(ctx context.Context, tx *sql.Tx, payload jobs.BackupPayload, slot time.Time) error {
	writer, ok := o.jobStore.(interface {
		CreateJobTx(context.Context, *sql.Tx, controlplane.UpsertJobRequest) (*controlplane.Job, error)
	})
	if !ok || tx == nil || slot.IsZero() || !o.ownsJobType(jobs.JobTypeBackup) {
		return fmt.Errorf("durable backup reservation is unavailable")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("backup.schedule/v1:%d:%s%d:%s:%s", len(payload.TenantID), payload.TenantID, len(payload.StackID), payload.StackID, slot.UTC().Format(time.RFC3339Nano))))
	job, err := backupJob(payload, fmt.Sprintf("backup-%x", digest))
	if err != nil {
		return err
	}
	_, err = writer.CreateJobTx(ctx, tx, controlplane.UpsertJobRequest{
		ID: job.ID, TenantID: payload.TenantID, StackID: payload.StackID,
		Type: string(jobs.JobTypeBackup), State: persistentStatePending,
		Payload: redactedJobPayloadForRecovery(job.Payload), ScheduledFor: time.Now().UTC(),
	})
	return err
}

func (o *Orchestrator) ownsJobType(kind jobs.JobType) bool {
	return o != nil && (o.cfg.JobTypeFilter == nil || o.cfg.JobTypeFilter(kind))
}

// RunPendingJobWake reuses the durable pending directory continuously, rather
// than treating startup recovery as an inter-process queue. StartJob and the
// existing execution-owner lease fence every attempt after local admission.
type backupWakeCursor struct {
	afterTenant, tenant, afterID string
	after                        time.Time
}
type pendingBackupPager interface {
	ListPendingBackupJobPage(context.Context, string, time.Time, string, int) ([]controlplane.Job, error)
}

func (o *Orchestrator) RunPendingJobWake(ctx context.Context) {
	pager, ok := o.jobStore.(pendingBackupPager)
	if !ok {
		o.log.Error("durable_job_wake_unavailable", "reason", "bounded backup page required")
		return
	}
	cursor := backupWakeCursor{}
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		if err := o.wakePendingBackupPage(ctx, pager, &cursor); err != nil && ctx.Err() == nil {
			o.log.Error("durable_job_wake_failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

// Cursor state schedules inspection only; the durable job row, current release
// owner and StartJob lease still decide execution. Every pass bounds inspected
// rows and tenants, including rows skipped for redaction, age or supersession.
func (o *Orchestrator) wakePendingBackupPage(ctx context.Context, pager pendingBackupPager, cursor *backupWakeCursor) error {
	examined, visited := 0, 0
	for examined < maxPendingJobRequeuesPerBoot && visited < pendingJobTenantPage {
		if cursor.tenant == "" {
			tenants, err := o.jobStore.ListPendingJobTenants(ctx, cursor.afterTenant, 1)
			if err != nil {
				return err
			}
			if len(tenants) == 0 {
				*cursor = backupWakeCursor{}
				return nil
			}
			cursor.afterTenant = tenants[0]
			visited++
			if !releasechannel.OwnsTenant(tenants[0]) {
				continue
			}
			cursor.tenant = tenants[0]
			cursor.after, cursor.afterID = time.Time{}, ""
		}
		limit := min(pendingJobPage, maxPendingJobRequeuesPerBoot-examined)
		rows, err := pager.ListPendingBackupJobPage(ctx, cursor.tenant, cursor.after, cursor.afterID, limit)
		if err != nil {
			return err
		}
		examined += len(rows)
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			cursor.after, cursor.afterID = last.ScheduledFor, last.ID
			// The fixed SQL page already applies the same one-hour recovery window
			// against the database clock. Do not apply a second host-clock cutoff.
			o.requeuePendingJobRows(ctx, cursor.tenant, time.Now().UTC(), rows, false)
		}
		if len(rows) < limit {
			if err := o.jobStore.CompactPendingJobTenant(ctx, cursor.tenant); err != nil {
				return err
			}
			cursor.tenant = ""
		}
	}
	return nil
}
