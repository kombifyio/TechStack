package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

const (
	durableDecommissionResumeBatch = 50
	durableDecommissionTenantBatch = 32
	durableDecommissionResumePoll  = time.Second
	// Queue progress heartbeats land every 500ms. Six missed heartbeats keep
	// recovery prompt without mistaking an actively reporting execution for a
	// crashed one. Both the store CAS and SQL tenant directory use this exact
	// duration as their stale-running fence.
	durableDecommissionStaleRunningGrace = 3 * time.Second
)

// ResumeDueProviderDecommissionJobs rehydrates only exact, already-due native
// provider-decommission waits for one tenant. It intentionally rebuilds the
// queue job from canonical control-plane state instead of treating result JSON
// as a payload: persisted wait metadata is a scheduling fence, not authority.
//
// Multiple replicas may safely call this for the same tenant. They can each
// offer the same durable job ID to their local queue, but StartJob is the
// durable compare-and-set immediately before the handler/provider dispatch and
// fences all but one executor.
func (o *Orchestrator) ResumeDueProviderDecommissionJobs(ctx context.Context, tenantID string) error {
	if o == nil || o.jobStore == nil {
		return nil
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("durable provider-decommission recovery requires a tenant id")
	}
	if ctx == nil {
		ctx = o.ctx
	}

	candidates, err := o.jobStore.ListManagedDestroyRecoveryCandidates(
		ctx,
		tenantID,
		managedDecommissionRecoveryMarkerKey,
		managedDecommissionRecoveryMarkerSchema,
		durableDecommissionResumeBatch,
	)
	if err != nil {
		return fmt.Errorf("list due managed provider-decommission recovery candidates: %w", err)
	}
	pending, reclaimErrors := o.reclaimStaleManagedProviderDecommissionJobs(ctx, tenantID, candidates)
	var recoveryErrors []error
	recoveryErrors = append(recoveryErrors, reclaimErrors...)
	for _, persisted := range pending {
		if strings.TrimSpace(persisted.TenantID) != tenantID {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("due provider-decommission list returned job %s outside tenant scope", persisted.ID))
			continue
		}
		if !isDueProviderDecommissionRecovery(persisted) {
			continue
		}
		if err := o.rehydrateDueProviderDecommissionJob(ctx, tenantID, persisted); err != nil {
			recoveryErrors = append(recoveryErrors, err)
		}
	}
	return errors.Join(recoveryErrors...)
}

// reclaimStaleManagedProviderDecommissionJobs scans only a tenant returned by
// the bounded secret-free directory, then asks the job store to atomically
// move an exact marker-bound stale running execution back to pending. The scan
// is merely candidate discovery; the store compare-and-set is the authority.
func (o *Orchestrator) reclaimStaleManagedProviderDecommissionJobs(
	ctx context.Context,
	tenantID string,
	candidates []controlplane.Job,
) ([]controlplane.Job, []error) {
	now := o.durableRecoveryNow()
	staleBefore := now.Add(-durableDecommissionStaleRunningGrace)
	var recoveryErrors []error
	pending := make([]controlplane.Job, 0, len(candidates))
	for _, candidate := range candidates {
		if !isManagedProviderDecommissionRecovery(candidate) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(candidate.State), persistentStatePending) {
			pending = append(pending, candidate)
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(candidate.State), persistentStateRunning) {
			continue
		}
		reclaimed, reclaimErr := o.jobStore.ReclaimStaleManagedDestroyRecovery(ctx, controlplane.ReclaimStaleManagedDestroyRecoveryRequest{
			TenantID: tenantID, JobID: candidate.ID, StackID: candidate.StackID,
			RecoveryMarkerKey:    managedDecommissionRecoveryMarkerKey,
			RecoveryMarkerSchema: managedDecommissionRecoveryMarkerSchema,
			StaleBefore:          staleBefore, ReclaimedAt: now,
		})
		if reclaimErr == nil && reclaimed != nil {
			// Reclaim returns the exact pending row written by the store CAS.
			// Rehydrate it in this sweep without another broad tenant query.
			// The result marker remains untouched by the transition.
			pending = append(pending, *reclaimed)
			continue
		}
		if reclaimErr == nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("reclaim stale provider-decommission job %s: store returned no job", candidate.ID))
			continue
		}
		if errors.Is(reclaimErr, controlplane.ErrConflict) {
			continue
		}
		recoveryErrors = append(recoveryErrors, fmt.Errorf("reclaim stale provider-decommission job %s: %w", candidate.ID, reclaimErr))
	}
	return pending, recoveryErrors
}

// RunDueProviderDecommissionRecovery continuously recovers due durable
// decommission waits through the provider-control runtime's secret-free
// tenant directory. It must be registered as a provider-control auxiliary so
// the existing mutation activation gate remains the lifecycle authority.
func (o *Orchestrator) RunDueProviderDecommissionRecovery(
	ctx context.Context,
	source providercontrol.DueProviderDecommissionTenantSource,
) {
	if o == nil || source == nil {
		return
	}
	if ctx == nil {
		ctx = o.ctx
	}
	cursor := ""
	run := func() {
		next, err := o.resumeDueProviderDecommissionTenantPage(ctx, source, cursor)
		// Keep keyset progress even when one tenant's durable record is
		// malformed or temporarily unavailable. A later full sweep will retry
		// it; pinning the cursor here would starve unrelated tenants forever.
		cursor = next
		if err != nil {
			o.log.Warn("provider_decommission_wait_recovery_failed", "error", err)
		}
	}
	run()
	ticker := time.NewTicker(durableDecommissionResumePoll)
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

func (o *Orchestrator) resumeDueProviderDecommissionTenantPage(
	ctx context.Context,
	source providercontrol.DueProviderDecommissionTenantSource,
	cursor string,
) (string, error) {
	if o == nil || source == nil {
		return "", nil
	}
	if ctx == nil {
		ctx = o.ctx
	}
	page, err := source.ListDueProviderDecommissionTenants(ctx, cursor, durableDecommissionTenantBatch)
	if err != nil {
		return cursor, fmt.Errorf("list due provider-decommission tenants: %w", err)
	}
	seen := make(map[string]struct{}, len(page.TenantIDs))
	var recoveryErrors []error
	for _, tenantID := range page.TenantIDs {
		tenantID = strings.TrimSpace(tenantID)
		if tenantID == "" {
			recoveryErrors = append(recoveryErrors, errors.New("provider decommission wait directory returned an empty tenant id"))
			continue
		}
		if _, duplicate := seen[tenantID]; duplicate {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("provider decommission wait directory returned duplicate tenant %q", tenantID))
			continue
		}
		seen[tenantID] = struct{}{}
		if err := o.ResumeDueProviderDecommissionJobs(ctx, tenantID); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("resume tenant %s: %w", tenantID, err))
		}
	}
	return strings.TrimSpace(page.NextCursor), errors.Join(recoveryErrors...)
}

func (o *Orchestrator) rehydrateDueProviderDecommissionJob(
	ctx context.Context,
	tenantID string,
	persisted controlplane.Job,
) error {
	stack, err := o.findArchivedControlPlaneStackForRecovery(ctx, persisted.StackID, ProvisionStackOptions{
		TenantID: tenantID,
	})
	if err != nil {
		return fmt.Errorf("rehydrate provider-decommission job %s: load canonical stack: %w", persisted.ID, err)
	}
	if strings.TrimSpace(stack.tenantID) != tenantID || strings.TrimSpace(stack.ownerID) == "" {
		return fmt.Errorf("rehydrate provider-decommission job %s: canonical stack custody is incomplete", persisted.ID)
	}
	required, err := o.managedRuntimeDecommissionRequired(ctx, stack)
	if err != nil {
		return fmt.Errorf("rehydrate provider-decommission job %s: classify canonical managed runtime: %w", persisted.ID, err)
	}
	if !required {
		return fmt.Errorf("rehydrate provider-decommission job %s: canonical stack no longer requires provider decommission", persisted.ID)
	}
	if !isDueProviderDecommissionRecovery(persisted) {
		return fmt.Errorf("rehydrate provider-decommission job %s: durable recovery marker is missing or invalid", persisted.ID)
	}

	// A directory entry remains due until StartJob flips the durable row to
	// running. Serialize repeated polls on this replica and retain its first
	// local offer rather than enqueueing the same ID into the local channel a
	// second time. Cross-replica concurrency remains fenced by StartJob.
	o.durableResumeMu.Lock()
	defer o.durableResumeMu.Unlock()
	if local, exists := o.queue.Get(persisted.ID); exists {
		snapshot := local.Snapshot()
		switch snapshot.State {
		case jobs.JobStatePending, jobs.JobStateWaiting, jobs.JobStateRunning:
			// A fencing loser has been explicitly detached from durable state.
			// It must not suppress the next restart-safe recovery cycle; Enqueue
			// will replace only this local pointer while the detached one cannot
			// write or execute again.
			if !snapshot.PersistenceSuppressed {
				return nil
			}
		}
	}

	// Do not copy arbitrary persisted Result/Payload. The old process may have
	// recorded diagnostics there, but it cannot grant tenant, owner, or
	// decommission authority after restart. The one additional retained value is
	// the server-generated, digest-validated port teardown batch captured before
	// the original queue admission.
	result := map[string]any{
		managedDecommissionRecoveryMarkerKey: managedProviderDecommissionRecoveryMarker(tenantID, stack.id),
	}
	if rawSnapshot, exists := persisted.Result[jobs.PortTeardownSnapshotResultField]; exists {
		portSnapshot, decodeErr := portinventory.DecodeTeardownSnapshot(rawSnapshot)
		if decodeErr != nil || portSnapshot.TenantID != tenantID ||
			portSnapshot.OwnerSubjectID != stack.ownerID || portSnapshot.TechstackID != stack.id {
			return fmt.Errorf("rehydrate provider-decommission job %s: invalid port teardown snapshot", persisted.ID)
		}
		result[jobs.PortTeardownSnapshotResultField] = portSnapshot
	}
	rehydrated := &jobs.Job{
		ID:          persisted.ID,
		Type:        jobs.JobTypeDestroy,
		TargetType:  targetTypeStack,
		TargetID:    stack.id,
		TargetName:  stack.name,
		Priority:    persisted.Priority,
		MaxAttempts: 3,
		Payload: map[string]any{
			stackOwnerIDField:  stack.ownerID,
			stackTenantIDField: tenantID,
			jobs.ManagedRuntimeDecommissionRequiredField: true,
		},
		Result: result,
	}
	if err := rehydrated.BindTenantID(tenantID); err != nil {
		return fmt.Errorf("rehydrate provider-decommission job %s: bind tenant: %w", persisted.ID, err)
	}
	if err := o.ensureDurablePendingJob(rehydrated, tenantID); err != nil {
		return fmt.Errorf("rehydrate provider-decommission job %s: validate durable fence: %w", persisted.ID, err)
	}
	if err := o.queue.Enqueue(rehydrated); err != nil {
		return fmt.Errorf("rehydrate provider-decommission job %s: enqueue: %w", persisted.ID, err)
	}
	o.startRehydratedJobProgressSync(rehydrated.Snapshot().ID, tenantID)
	return nil
}

// startRehydratedJobProgressSync leaves the durable waiting receipt intact
// until a local worker has won StartJob. A generic initial pending snapshot
// would erase job_wait before the durable execution fence, recreating the
// stranded-job failure if the process died in that window.
func (o *Orchestrator) startRehydratedJobProgressSync(jobID, tenantID string) {
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-o.ctx.Done():
				return
			case <-ticker.C:
			}
			job, ok := o.queue.Get(jobID)
			if !ok {
				return
			}
			snapshot := job.Snapshot()
			if snapshot.PersistenceSuppressed {
				return
			}
			if snapshot.State == jobs.JobStatePending && snapshot.StartedAt == nil {
				continue
			}
			o.syncJobProgress(jobID, tenantID)
			return
		}
	}()
}
