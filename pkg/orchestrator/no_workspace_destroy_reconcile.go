package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

var (
	// ErrNoWorkspaceDestroyOwnershipMismatch prevents a durable destroy receipt
	// from retiring another owner's stack projection.
	ErrNoWorkspaceDestroyOwnershipMismatch = errors.New("no-workspace destroy ownership does not match the stored stack")
	// ErrNoWorkspaceDestroyLeasePresent preserves the projection whenever a
	// current lease is still bound to it. This branch never calls a provider;
	// it only refuses to hide potentially billable infrastructure.
	ErrNoWorkspaceDestroyLeasePresent = errors.New("no-workspace destroy still has a bound lease")
)

const noWorkspaceDestroyReconcileRetryAfter = 2 * time.Second

// reconcileNoWorkspaceDestroy archives only the exact control-plane stack
// requested by a destroy job after the handler proved there is no local
// workspace. It reads the current tenant-scoped lease inventory immediately
// before archive so a delayed provider allocation cannot be hidden by an old
// no-lease classification. It deliberately never invokes provider control nor
// reads or writes PocketBase's legacy projection.
func (o *Orchestrator) reconcileNoWorkspaceDestroy(ctx context.Context, request jobs.NoWorkspaceDestroyReconcileRequest) error {
	stackID := strings.TrimSpace(request.StackID)
	tenantID := strings.TrimSpace(request.TenantID)
	ownerID := strings.TrimSpace(request.OwnerID)
	if stackID == "" || tenantID == "" || ownerID == "" {
		return fmt.Errorf("exact stack, tenant, and owner are required for no-workspace destroy reconciliation")
	}
	if o == nil || o.stackStore == nil {
		return waitForNoWorkspaceDestroyReconciliation(errors.New("control-plane stack authority is unavailable"))
	}

	stack, err := o.stackStore.GetStack(ctx, tenantID, stackID)
	if errors.Is(err, controlplane.ErrNotFound) {
		// The target was already archived by the same prior destroy. Treat that
		// as a completed, idempotent reconciliation rather than recreating it.
		return nil
	}
	if err != nil {
		return waitForNoWorkspaceDestroyReconciliation(fmt.Errorf("load exact stack projection: %w", err))
	}
	if stack == nil || strings.TrimSpace(stack.OwnerSubjectID) != ownerID {
		return ErrNoWorkspaceDestroyOwnershipMismatch
	}

	if stackHasManagedProviderIntent(stack) {
		if o.leaseLister == nil {
			return waitForNoWorkspaceDestroyReconciliation(errors.New("managed stack lease authority is unavailable"))
		}
		records, err := o.leaseLister.ListInventoryByTenant(ctx, tenantID)
		if err != nil {
			return waitForNoWorkspaceDestroyReconciliation(fmt.Errorf("read current lease inventory: %w", err))
		}
		for _, record := range records {
			lease := record.Lease
			if strings.TrimSpace(lease.Metadata["stack_id"]) != stackID || lease.CancelledAt != nil {
				continue
			}
			return fmt.Errorf("%w: %s", ErrNoWorkspaceDestroyLeasePresent, lease.ID)
		}
	}

	if err := o.stackStore.SoftDeleteStack(ctx, tenantID, stackID); err != nil && !errors.Is(err, controlplane.ErrNotFound) {
		return waitForNoWorkspaceDestroyReconciliation(fmt.Errorf("archive exact stack projection: %w", err))
	}
	return nil
}

// stackHasManagedProviderIntent identifies only the explicit managed
// provisioning shapes. A local or BYO stack may use any provider name in its
// own configuration; that alone must never force it onto a Kombify managed
// lease-authority wait path.
func stackHasManagedProviderIntent(stack *controlplane.Stack) bool {
	if stack == nil {
		return false
	}
	return managedProviderIntent(stack.Config)
}

func managedProviderIntent(fields map[string]any) bool {
	if fields == nil {
		return false
	}
	for _, key := range []string{runtimeFieldLane, "server_mode"} {
		switch strings.ToLower(strings.TrimSpace(stringFromAny(fields[key]))) {
		case runtimeLaneMonthly, "managed-cloud":
			return true
		}
	}
	if strings.EqualFold(strings.TrimSpace(stringFromAny(fields[runtimeFieldConnectionMode])), runtimeConnectionManaged) ||
		strings.EqualFold(strings.TrimSpace(stringFromAny(fields[runtimeFieldProvisionMode])), runtimeProvisionKombify) {
		return true
	}
	for _, key := range []string{runtimeFieldLeaseProvider, "provider_id", runtimeFieldSimProviderID} {
		switch strings.ToLower(strings.TrimSpace(stringFromAny(fields[key]))) {
		case "centron-managed", "ionos-managed", "managed-cloud":
			return true
		}
	}
	return false
}

func waitForNoWorkspaceDestroyReconciliation(cause error) error {
	return &jobs.JobWaitError{
		Reason:      "waiting_no_workspace_destroy_reconciliation",
		Message:     "Waiting for durable no-workspace stack reconciliation",
		ResumeAfter: noWorkspaceDestroyReconcileRetryAfter,
		Cause:       cause,
	}
}
