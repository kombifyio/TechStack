package orchestrator

import (
	"context"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// The public runtime keeps only the provider-neutral recovery seam. Hosted
// provider recovery installs the optional hooks from the excluded private
// extension; local self-hosted jobs are never re-dispatched to a provider.
const (
	managedDecommissionRecoveryMarkerKey    = "managed_provider_decommission_recovery"
	managedDecommissionRecoveryMarkerSchema = "techstack.managed-provider-decommission-recovery/v1"
)

func (o *Orchestrator) durableRecoveryNow() time.Time {
	if o == nil || o.now == nil {
		return time.Now().UTC()
	}
	return o.now().UTC()
}

func managedProviderDecommissionRecoveryMarker(tenantID, stackID string) map[string]any {
	return map[string]any{
		"schema":    managedDecommissionRecoveryMarkerSchema,
		"tenant_id": strings.TrimSpace(tenantID),
		"stack_id":  strings.TrimSpace(stackID),
	}
}

func hasManagedProviderDecommissionRecoveryMarker(result map[string]any, tenantID, stackID string) bool {
	marker, ok := result[managedDecommissionRecoveryMarkerKey].(map[string]any)
	if !ok {
		return false
	}
	return strings.TrimSpace(stringFromAny(marker["schema"])) == managedDecommissionRecoveryMarkerSchema &&
		strings.TrimSpace(stringFromAny(marker["tenant_id"])) == strings.TrimSpace(tenantID) &&
		strings.TrimSpace(stringFromAny(marker["stack_id"])) == strings.TrimSpace(stackID)
}

func isManagedProviderDecommissionRecovery(job controlplane.Job) bool {
	if strings.TrimSpace(job.TenantID) == "" || strings.TrimSpace(job.ID) == "" ||
		strings.TrimSpace(job.StackID) == "" ||
		!strings.EqualFold(strings.TrimSpace(job.Type), persistentJobTypeDestroy) {
		return false
	}
	return hasManagedProviderDecommissionRecoveryMarker(job.Result, job.TenantID, job.StackID)
}

func isDueProviderDecommissionRecovery(job controlplane.Job) bool {
	return isManagedProviderDecommissionRecovery(job) &&
		strings.EqualFold(strings.TrimSpace(job.State), persistentStatePending) &&
		!job.ScheduledFor.IsZero()
}

var isManagedProviderProvisionRecovery = func(controlplane.Job) bool { return false }

var rehydrateProviderProvisionWaitHook = func(context.Context, *Orchestrator, string, controlplane.Job) error {
	return nil
}

func (o *Orchestrator) rehydrateProviderProvisionWait(
	ctx context.Context,
	tenantID string,
	persisted controlplane.Job,
) error {
	return rehydrateProviderProvisionWaitHook(ctx, o, tenantID, persisted)
}
