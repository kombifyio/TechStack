package orchestrator

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// Reload rather than accepting an IP, rollout path, or lease from the candidate.
func (o *Orchestrator) managedChangeSetAddressAuthority(ctx context.Context, req jobs.StackKitLifecycleRequest) (jobs.ManagedAddressRuntime, error) {
	var result jobs.ManagedAddressRuntime
	stack, err := o.findStackForJob(ctx, req.StackID, ProvisionStackOptions{RequestContext: ctx, TenantID: req.TenantID, OwnerID: req.OwnerID})
	if err != nil {
		return result, err
	}
	if stack.tenantID != req.TenantID || stack.ownerID != req.OwnerID || stack.stackKitInstanceID != req.StackKitInstanceID {
		return result, ErrStackKitLifecycleUnavailable
	}
	result.Rollout = stackKitRolloutBinding(stack)
	if result.Rollout.SpecPath != req.SpecPath || result.Rollout.StackKit != req.StackKit {
		return result, ErrStackKitLifecycleUnavailable
	}
	binding, err := o.admitStackKitLifecycleAgent(ctx, stack, req.AgentID)
	if err != nil {
		return result, err
	}
	store := o.canonicalServerRuntimeStore()
	if store == nil || binding.ServerID == "" || binding.NodeID != req.NodeID {
		return result, ErrStackKitLifecycleUnavailable
	}
	runtime, err := store.GetServerRuntime(ctx, req.TenantID, binding.ServerID)
	if err != nil {
		return result, err
	}
	if runtime == nil || runtime.ID != binding.ServerID || runtime.WorkerID != binding.WorkerID || !serverRuntimeAllowsStackDeploy(stack, *runtime, time.Now().UTC()) {
		return result, ErrStackKitLifecycleUnavailable
	}
	// No lease means no provider-address authority. The jobs layer only permits
	// the existing Advanced path after proving no new hosted routes are needed.
	result.ServerID, result.Generation = runtime.ID, runtime.Generation
	if runtime.LeaseID != "" {
		lease, err := o.exactManagedRuntimeLeaseForStack(ctx, stack, runtime.LeaseID)
		if err != nil {
			return result, err
		}
		if lease == nil {
			return result, ErrManagedRuntimeLeaseNotNativeActive
		}
		if _, ok := exactManagedRuntimeDeployEligibleRuntime(stack, runtime.LeaseID, runtime.ID, []controlplane.ServerRuntime{*runtime}, time.Now().UTC()); !ok {
			return result, ErrManagedRuntimeLeaseNotNativeActive
		}
		providerIP, err := netip.ParseAddr(lease.Metadata["runtime_public_ip"])
		host, _ := runtime.Metadata["host"].(map[string]any)
		observedIP, observedErr := netip.ParseAddr(stringFromAny(host["public_ip"]))
		if err != nil || observedErr != nil || providerIP != observedIP {
			return result, fmt.Errorf("managed route target differs from current provider lease address")
		}
		result.PublicIP, result.LeaseID = providerIP.String(), runtime.LeaseID
	}
	return result, nil
}
