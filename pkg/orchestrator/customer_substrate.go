package orchestrator

import (
	"context"
	"fmt"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

// ProvisionCustomerGuest performs the existing pinned StackKit rollout after
// guest preparation. The canonical Guard admission waits for this exact lease;
// the Proxmox host itself can never satisfy the guest execution boundary.
func (o *Orchestrator) ProvisionCustomerGuest(stackID string, binding jobs.CustomerGuestBinding, opts ProvisionStackOptions) (string, error) {
	if binding.LeaseID == "" || binding.OperationID == "" || binding.ServerID != runtimeidentity.LeaseServerID(binding.LeaseID) {
		return "", fmt.Errorf("exact customer guest receipt required")
	}
	stack, err := o.findStackForJob(opts.RequestContext, stackID, opts)
	if err != nil {
		return "", err
	}
	spec, _, err := providerProvisionRecoverySpec(stack)
	if err != nil {
		return "", err
	}
	opts.CustomerGuest = &binding
	opts.AutoDeploy = true
	return o.ProvisionStackWithOptions(stackID, spec, opts)
}

func customerUbuntuLease(lease vmlease.Lease) bool {
	return lease.CustodyClass == vmlease.CustodyCustomerSubstrate && lease.Resource.ProviderID == "proxmox" &&
		lease.BillingMode == vmlease.BillingModeLocal && lease.RecreatePolicy == vmlease.RecreatePolicyNever &&
		lease.Metadata["runtime_offering_id"] == "ubuntu-24.04" && lease.Metadata["node_id"] != ""
}

func (o *Orchestrator) requireCustomerGuestLease(ctx context.Context, stack *orchestratorStack, binding jobs.CustomerGuestBinding) error {
	if binding.OperationID == "" || binding.ServerID != runtimeidentity.LeaseServerID(binding.LeaseID) {
		return fmt.Errorf("exact customer guest receipt required")
	}
	lease, err := o.exactManagedRuntimeLeaseForStack(ctx, stack, binding.LeaseID)
	if err != nil {
		return err
	}
	if lease == nil || !customerUbuntuLease(*lease) {
		return fmt.Errorf("customer rollout requires its native Ubuntu guest")
	}
	verifier, ok := o.cfg.RuntimeActions.LeaseManager.(interface {
		VerifyCustomerGuest(context.Context, string, string, string, string, string, string) error
	})
	if !ok {
		return fmt.Errorf("native customer guest receipt verifier unavailable")
	}
	return verifier.VerifyCustomerGuest(ctx, stack.tenantID, stack.ownerID, stack.id, binding.LeaseID, binding.ServerID, binding.OperationID)
}

func (o *Orchestrator) requireNoActiveCustomerGuestRollout(ctx context.Context, stack *orchestratorStack) error {
	if o.jobStore == nil {
		return fmt.Errorf("durable job store required")
	}
	active, err := o.jobStore.ListJobsByStack(ctx, stack.tenantID, stack.id, 100)
	if err != nil {
		return err
	}
	for _, job := range active {
		if job.Type != "provision" && job.Type != "deploy" && job.Type != "destroy" {
			continue
		}
		switch job.State {
		case "pending", "running", "waiting":
			return fmt.Errorf("another stack lifecycle operation is active")
		}
	}
	return nil
}
