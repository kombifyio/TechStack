package jobs

import (
	"context"
	"fmt"
	"strings"

	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
)

// BackupAgentResolver resolves the enrolled agent that currently owns a
// stack's runtime. It is resolved at dispatch rather than carried in the job,
// because a stack can be re-enrolled between the scan and the run and a stale
// agent id would send the backup to a worker that no longer owns the node.
type BackupAgentResolver func(ctx context.Context, tenantID, stackID string) (string, error)

// BackupAdmissionRequest is the immutable identity envelope resolved before a
// backup may be enqueued.
type BackupAdmissionRequest struct {
	TenantID string
	StackID  string
	// UserID is the subject the entitlement chain resolves. It is required:
	// an unattributed backup cannot be gated, and a gate that cannot identify
	// the subject must deny rather than guess.
	UserID string
	// IncludeContent requests the content data classes on top of config.
	// Media classes ride the same flag today; splitting them is the per-class
	// retention work in kombify-StackKits-dwu2.
	IncludeContent bool
}

// BackupAdmissionDecision is the result of the pre-enqueue gate. A denied
// decision carries the structured envelope the surface renders; it is never a
// bare error, because the customer has to be told which tier they are on and
// what, if anything, they can do about it.
type BackupAdmissionDecision struct {
	Denied bool
	// QuotaBytes is the granted storage budget for the resolved tier.
	QuotaBytes int64
	// UsedBytes is the measured object-store total. It is the control-plane
	// measurement, never the node-reported repository size.
	UsedBytes int64
	// Details is the structured denial envelope, populated only when denied.
	Details map[string]any
}

// BackupAdmission resolves entitlement and storage quota for one stack.
// Implementations must be read-only and must fail closed: a missing checker, a
// check error, an unmeasured stack or an exceeded budget all deny.
//
// This runs BEFORE the job is enqueued, never inside the handler. Enqueuing
// first and checking later would already have committed the platform to the
// cost-bearing action the gate exists to refuse.
type BackupAdmission func(context.Context, BackupAdmissionRequest) (BackupAdmissionDecision, error)

// ErrBackupAdmissionUnavailable reports that no admission authority is wired.
// It is deliberately an error rather than an allow: an unconfigured gate must
// never read as an approved one.
var ErrBackupAdmissionUnavailable = fmt.Errorf("backup admission authority is not configured")

// AdmitBackup resolves the gate for one request. A nil admission denies.
func AdmitBackup(ctx context.Context, admission BackupAdmission, req BackupAdmissionRequest) (BackupAdmissionDecision, error) {
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.StackID = strings.TrimSpace(req.StackID)
	req.UserID = strings.TrimSpace(req.UserID)
	if req.TenantID == "" || req.StackID == "" || req.UserID == "" {
		return BackupAdmissionDecision{Denied: true}, fmt.Errorf("backup admission requires exact tenant, stack and subject identity")
	}
	if admission == nil {
		return BackupAdmissionDecision{Denied: true}, ErrBackupAdmissionUnavailable
	}
	decision, err := admission(ctx, req)
	if err != nil {
		// A failing authority denies. The alternative - treating an
		// unreachable gate as permission - is how a fail-closed contract
		// silently becomes fail-open under load.
		return BackupAdmissionDecision{Denied: true}, err
	}
	return decision, nil
}

// BackupPayload is the durable job payload. It carries identity only:
// repository credentials are resolved from encrypted custody at dispatch time
// and never persist in a job row, a log line or a result.
type BackupPayload struct {
	TenantID       string `json:"tenant_id"`
	StackID        string `json:"stack_id"`
	StackName      string `json:"stack_name,omitempty"`
	OwnerID        string `json:"owner_id,omitempty"`
	IncludeContent bool   `json:"include_content"`
	// QuotaBytes and AdmittedUsedBytes record what the gate saw when it
	// admitted this job, so a receipt can show the basis of the decision
	// rather than a figure re-read later under different conditions.
	QuotaBytes        int64 `json:"quota_bytes"`
	AdmittedUsedBytes int64 `json:"admitted_used_bytes"`
}

// BackupHandler runs one admitted backup against the enrolled node.
//
// It does not re-run the entitlement gate. That is not an omission: the gate
// resolved before the job existed, and re-resolving here would either duplicate
// the decision or, worse, disagree with it after the fact while the job is
// already durable.
//
// Dispatch prefers the typed agent command set. That is the transport the node
// actually has: the Guard polls outbound and holds a per-agent token, so there
// is no inbound path to it. The HTTP runner remains only for a co-located
// self-hosted StackKits action server.
func BackupHandler(cfg *ProvisionConfig) JobHandler {
	return func(ctx context.Context, job *Job, q *Queue) error {
		if cfg == nil {
			return fmt.Errorf("backup handler is not configured")
		}
		payload, err := backupPayloadFromJob(job)
		if err != nil {
			return err
		}
		if cfg.StackKitCommander != nil {
			return dispatchBackupCommand(ctx, cfg, job, payload)
		}
		if cfg.RuntimeActions.BackupRunner == nil {
			return fmt.Errorf("no backup dispatcher is configured: neither a typed agent commander nor a StackKits action runner")
		}
		return cfg.RuntimeActions.BackupRunner.Run(ctx, RuntimeActionRequest{
			StackID:   payload.StackID,
			StackName: payload.StackName,
			TenantID:  payload.TenantID,
			OwnerID:   payload.OwnerID,
		})
	}
}

func dispatchBackupCommand(ctx context.Context, cfg *ProvisionConfig, job *Job, payload BackupPayload) error {
	if cfg.BackupAgentResolver == nil {
		return fmt.Errorf("backup dispatch requires an agent resolver for stack %s", payload.StackID)
	}
	agentID, err := cfg.BackupAgentResolver(ctx, payload.TenantID, payload.StackID)
	if err != nil {
		return fmt.Errorf("resolve backup agent for stack %s: %w", payload.StackID, err)
	}
	if strings.TrimSpace(agentID) == "" {
		return fmt.Errorf("stack %s has no enrolled agent to run a backup", payload.StackID)
	}
	// The job id is the idempotency key. The Guard poll is at-least-once, so a
	// redelivered command must not cost the customer a second snapshot.
	command := &agentpb.StackKitCommand{
		CommandId:      strings.TrimSpace(job.ID),
		Operation:      agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN,
		TimeoutSeconds: backupCommandTimeoutSeconds,
	}
	result, err := sendStackKitCommandBoundedForTenant(ctx, cfg.StackKitCommander, payload.TenantID, agentID, command)
	if err != nil {
		return fmt.Errorf("backup run for stack %s: %w", payload.StackID, err)
	}
	if result == nil || !result.Success {
		exit := int32(-1)
		if result != nil {
			exit = result.ExitCode
		}
		return fmt.Errorf("backup run for stack %s failed with exit code %d", payload.StackID, exit)
	}
	return nil
}

// backupCommandTimeoutSeconds bounds one snapshot on the node. A first seed of
// a large library is slow, so this is generous; the queue, not the command,
// owns retry.
const backupCommandTimeoutSeconds = 3600

func backupPayloadFromJob(job *Job) (BackupPayload, error) {
	if job == nil {
		return BackupPayload{}, fmt.Errorf("backup job is required")
	}
	raw := job.Payload
	payload := BackupPayload{
		TenantID:  payloadString(raw, "tenant_id"),
		StackID:   payloadString(raw, "stack_id"),
		StackName: payloadString(raw, "stack_name"),
		OwnerID:   payloadString(raw, "owner_id"),
	}
	if payload.StackID == "" {
		payload.StackID = strings.TrimSpace(job.TargetID)
	}
	if payload.StackName == "" {
		payload.StackName = strings.TrimSpace(job.TargetName)
	}
	if payload.TenantID == "" || payload.StackID == "" {
		return BackupPayload{}, fmt.Errorf("backup job requires exact tenant and stack identity")
	}
	return payload, nil
}
