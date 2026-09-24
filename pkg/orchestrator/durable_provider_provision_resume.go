package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

const (
	durableProviderProvisionResumeBatch   = 50
	durableProviderProvisionRecoveryBatch = 32
	durableProviderProvisionRecoveryPoll  = time.Second
)

func init() {
	isManagedProviderProvisionRecovery = isHostedManagedProviderProvisionRecovery
	rehydrateProviderProvisionWaitHook = func(
		ctx context.Context,
		o *Orchestrator,
		tenantID string,
		persisted controlplane.Job,
	) error {
		return o.rehydrateHostedProviderProvisionWait(ctx, tenantID, persisted)
	}
}

// RehydrateProviderProvisionWait offers a durable provider-provision wait back
// to this replica after provider-control has parked or resolved its native
// operation. The provider operation identity is the correlation boundary; no
// provider handle or create request is reconstructed here. ProvisionHandler
// replays the existing operation through native admission, which is what keeps
// this recovery provider-neutral and at-most-once.
//
// Multiple replicas may receive the same parked signal. The durable StartJob
// claim fences all but one local offer, while the local queue mutex prevents a
// healthy existing offer from being replaced on every provider-control poll.
func (o *Orchestrator) RehydrateProviderProvisionWait(
	ctx context.Context,
	operation providercontrol.OperationRef,
) error {
	if o == nil || o.jobStore == nil || o.queue == nil {
		return nil
	}
	tenantID := strings.TrimSpace(operation.TenantID)
	operationID := strings.TrimSpace(operation.OperationID)
	if tenantID == "" || operationID == "" {
		return fmt.Errorf("provider-provision recovery requires tenant and operation identity")
	}
	if ctx == nil {
		ctx = o.ctx
	}
	candidates, err := o.jobStore.ListProviderProvisionRecoveryCandidates(
		ctx, tenantID, operationID, durableProviderProvisionResumeBatch,
	)
	if err != nil {
		return fmt.Errorf("list provider-provision recovery candidates: %w", err)
	}
	var recoveryErrors []error
	for _, persisted := range candidates {
		if !isProviderProvisionWaitCandidate(persisted, tenantID, operationID) {
			continue
		}
		if err := o.rehydrateHostedProviderProvisionWait(ctx, tenantID, persisted); err != nil {
			recoveryErrors = append(recoveryErrors, err)
		}
	}
	return errors.Join(recoveryErrors...)
}

// RunProviderProvisionRecovery continuously sweeps the provider-neutral
// operation-reference projection. The first pass runs immediately at startup;
// later passes are bounded keyset pages. Each reference is revalidated through
// the exact tenant-scoped job query before a local queue offer is made.
func (o *Orchestrator) RunProviderProvisionRecovery(
	ctx context.Context,
	source providercontrol.ProviderProvisionWaitSource,
) {
	if o == nil || source == nil {
		return
	}
	if ctx == nil {
		ctx = o.ctx
	}
	cursor := ""
	run := func() {
		next, err := o.resumeProviderProvisionWaitPage(ctx, source, cursor)
		cursor = next
		if err != nil {
			o.log.Warn("provider_provision_wait_recovery_failed", "error", err)
		}
	}
	run()
	ticker := time.NewTicker(durableProviderProvisionRecoveryPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (o *Orchestrator) resumeProviderProvisionWaitPage(
	ctx context.Context,
	source providercontrol.ProviderProvisionWaitSource,
	cursor string,
) (string, error) {
	if o == nil || source == nil {
		return "", nil
	}
	if ctx == nil {
		ctx = o.ctx
	}
	page, err := source.ListProviderProvisionWaits(ctx, cursor, durableProviderProvisionRecoveryBatch)
	if err != nil {
		return cursor, fmt.Errorf("list provider-provision wait references: %w", err)
	}
	seen := make(map[string]struct{}, len(page.Operations))
	var recoveryErrors []error
	for _, operation := range page.Operations {
		operation.TenantID = strings.TrimSpace(operation.TenantID)
		operation.OperationID = strings.TrimSpace(operation.OperationID)
		if operation.TenantID == "" || operation.OperationID == "" {
			recoveryErrors = append(recoveryErrors, errors.New("provider provision wait recovery returned an empty operation reference"))
			continue
		}
		key := operation.TenantID + "\x00" + operation.OperationID
		if _, duplicate := seen[key]; duplicate {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("provider provision wait recovery returned duplicate operation %s/%s", operation.TenantID, operation.OperationID))
			continue
		}
		seen[key] = struct{}{}
		if err := o.RehydrateProviderProvisionWait(ctx, operation); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("rehydrate provider-provision wait %s/%s: %w", operation.TenantID, operation.OperationID, err))
		}
	}
	return strings.TrimSpace(page.NextCursor), errors.Join(recoveryErrors...)
}

func isProviderProvisionWaitCandidate(job controlplane.Job, tenantID, operationID string) bool {
	if strings.TrimSpace(job.TenantID) != strings.TrimSpace(tenantID) ||
		strings.TrimSpace(job.Type) != persistentJobTypeProvision ||
		strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.StackID) == "" ||
		strings.TrimSpace(job.State) != persistentStatePending ||
		strings.TrimSpace(stringFromAny(job.Result["operation_id"])) != strings.TrimSpace(operationID) {
		return false
	}
	wait, ok := job.Result["job_wait"].(map[string]any)
	return ok && strings.TrimSpace(stringFromAny(wait["state"])) == string(jobs.JobStateWaiting) &&
		strings.TrimSpace(stringFromAny(wait["reason"])) == jobs.WaitReasonManagedRuntimeProvider
}

func isHostedManagedProviderProvisionRecovery(job controlplane.Job) bool {
	if strings.TrimSpace(job.TenantID) == "" || strings.TrimSpace(job.ID) == "" ||
		strings.TrimSpace(job.StackID) == "" ||
		!strings.EqualFold(strings.TrimSpace(job.Type), persistentJobTypeProvision) ||
		strings.TrimSpace(stringFromAny(job.Result["operation_id"])) == "" {
		return false
	}
	wait, ok := job.Result["job_wait"].(map[string]any)
	if !ok || strings.TrimSpace(stringFromAny(wait["state"])) != string(jobs.JobStateWaiting) {
		return false
	}
	reason := strings.TrimSpace(stringFromAny(wait["reason"]))
	return reason == jobs.WaitReasonManagedRuntimeProvider || reason == jobs.WaitReasonCanonicalGuardEvidence
}

func (o *Orchestrator) rehydrateHostedProviderProvisionWait(
	ctx context.Context,
	tenantID string,
	persisted controlplane.Job,
) error {
	stack, err := o.findControlPlaneStackForJob(ctx, persisted.StackID, ProvisionStackOptions{
		TenantID: tenantID,
	})
	if err != nil {
		return fmt.Errorf("rehydrate provider-provision job %s: load canonical stack: %w", persisted.ID, err)
	}
	if strings.TrimSpace(stack.tenantID) != tenantID || strings.TrimSpace(stack.ownerID) == "" {
		return fmt.Errorf("rehydrate provider-provision job %s: canonical stack custody is incomplete", persisted.ID)
	}
	spec, rawIntent, err := providerProvisionRecoverySpec(stack)
	if err != nil {
		return fmt.Errorf("rehydrate provider-provision job %s: load canonical stack spec: %w", persisted.ID, err)
	}
	canonicalSpec, err := canonicalizeProvisionProviderIdentity(spec, stack.config)
	if err != nil {
		return fmt.Errorf("rehydrate provider-provision job %s: canonical provider identity: %w", persisted.ID, err)
	}
	spec = canonicalSpec

	// A provider wait is persisted as a pending control-plane row. Keep the
	// original auto-deploy intent only when the prior handler durably recorded
	// it; credentials, provider handles, and old diagnostics are deliberately
	// not copied into the new process-local job.
	payload := map[string]any{
		"spec":             cloneProvisionSpec(spec),
		stackOwnerIDField:  stack.ownerID,
		stackTenantIDField: tenantID,
	}
	if rawIntent != "" {
		payload["intent_raw"] = rawIntent
	}
	if boolResult(persisted.Result["auto_deploy"]) {
		payload["auto_deploy"] = true
	}
	if prepared := persisted.Result[jobs.PreparedManagedLeaseRequestResultKey]; prepared != nil {
		// Provider-wait recovery runs in a fresh process. The original job
		// Payload was process-local, while this Result checkpoint is part of the
		// durable control-plane projection. Restore it before ProvisionHandler
		// crosses native admission so the replay remains byte-equivalent.
		payload[jobs.PreparedManagedLeaseRequestPayloadKey] = prepared
	}
	if guest := persisted.Result[jobs.CustomerGuestResultKey]; guest != nil {
		payload[jobs.CustomerGuestResultKey] = guest
	}
	rehydrated := &jobs.Job{
		ID:          persisted.ID,
		Type:        jobs.JobTypeProvision,
		TargetType:  targetTypeStack,
		TargetID:    stack.id,
		TargetName:  stack.name,
		Priority:    persisted.Priority,
		MaxAttempts: 3,
		Payload:     payload,
	}
	if err := rehydrated.BindTenantID(tenantID); err != nil {
		return fmt.Errorf("rehydrate provider-provision job %s: bind tenant: %w", persisted.ID, err)
	}

	o.durableResumeMu.Lock()
	defer o.durableResumeMu.Unlock()
	if local, exists := o.queue.Get(persisted.ID); exists {
		snapshot := local.Snapshot()
		switch snapshot.State {
		case jobs.JobStatePending, jobs.JobStateWaiting, jobs.JobStateRunning:
			if !snapshot.PersistenceSuppressed {
				return nil
			}
		}
	}
	if err := o.ensureDurablePendingJob(rehydrated, tenantID); err != nil {
		return fmt.Errorf("rehydrate provider-provision job %s: validate durable fence: %w", persisted.ID, err)
	}
	if err := o.queue.Enqueue(rehydrated); err != nil {
		return fmt.Errorf("rehydrate provider-provision job %s: enqueue: %w", persisted.ID, err)
	}
	o.startRehydratedJobProgressSync(rehydrated.Snapshot().ID, tenantID)
	return nil
}

func providerProvisionRecoverySpec(stack *orchestratorStack) (map[string]interface{}, string, error) {
	if stack == nil {
		return nil, "", fmt.Errorf("canonical stack is required")
	}
	if spec, ok := providerIdentityMap(stack.config["user_config"]); ok && len(spec) > 0 {
		return recoveredProvisionSpec(stack, spec), "", nil
	}
	if raw, ok := stack.config["user_config_raw"].(string); ok && strings.TrimSpace(raw) != "" {
		parsed, parsedOK := parseProvisionProviderRaw(raw)
		if parsedOK && len(parsed) > 0 {
			return recoveredProvisionSpec(stack, parsed), raw, nil
		}
	}
	return nil, "", fmt.Errorf("canonical stack has no recoverable user_config")
}

func recoveredProvisionSpec(stack *orchestratorStack, spec map[string]interface{}) map[string]interface{} {
	recovered := cloneProvisionSpec(spec)
	if canonical, ok := providerIdentityMap(stack.config["stack_spec_v2"]); ok && len(canonical) > 0 {
		recovered["stack_spec_v2"] = cloneProvisionSpec(canonical)
	}
	return recovered
}
