package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/jobs"
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
	tenantID := strings.TrimSpace(payload.TenantID)
	stackID := strings.TrimSpace(payload.StackID)
	if tenantID == "" || stackID == "" {
		return fmt.Errorf("backup job requires exact tenant and stack identity")
	}
	job := &jobs.Job{
		ID:         jobs.NewID(),
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
	}
	unlockStack := o.stackLocks.lock(stackID)
	defer unlockStack()
	return o.enqueueWithSync(job, tenantID)
}
