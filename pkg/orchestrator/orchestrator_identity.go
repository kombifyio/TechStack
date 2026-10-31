package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const stackKitIdentityWorkspace = "/opt/stackkit"

// StackKitIdentityCommandRequest is the closed synchronous identity operation
// admitted for one exact Owner-owned StackKit runtime.
type StackKitIdentityCommandRequest struct {
	TenantID             string
	OwnerID              string
	DeploymentID         string
	Operation            agentpb.StackKitOperation
	OwnerApproved        bool
	HouseholdUsername    string
	HouseholdEmail       string
	HouseholdDisplayName string
	// HouseholdApps are node app ids; HouseholdAppsSet false means all apps.
	HouseholdApps               []string
	HouseholdAppsSet            bool
	HouseholdProjectionKey      string
	HouseholdProjectionRevision uint64
	HouseholdProjectionAction   string
	// Required for managed household projections. These values come from the
	// durable owner/member binding, never from an arbitrary client selector.
	ExpectedAgentID            string
	ExpectedStackKitInstanceID string
}

type StackKitIdentityTarget struct {
	AgentID, StackKitInstanceID string
}

// ResolveStackKitIdentityTarget reads the exact approved runtime before the
// household service commits its target. It performs no node effect.
func (o *Orchestrator) ResolveStackKitIdentityTarget(ctx context.Context, tenantID, ownerID, deploymentID string) (StackKitIdentityTarget, error) {
	if o == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(deploymentID) == "" {
		return StackKitIdentityTarget{}, ErrStackKitLifecycleUnavailable
	}
	stack, err := o.findStackForJob(ctx, deploymentID, ProvisionStackOptions{RequestContext: ctx, TenantID: tenantID, OwnerID: ownerID})
	if err != nil {
		return StackKitIdentityTarget{}, err
	}
	if stack.tenantID != tenantID || stack.ownerID != ownerID {
		return StackKitIdentityTarget{}, controlplane.ErrNotFound
	}
	binding, err := o.identityRuntimeAgentBinding(ctx, stack)
	if err != nil {
		return StackKitIdentityTarget{}, err
	}
	if binding.AgentID == "" || stack.stackKitInstanceID == "" {
		return StackKitIdentityTarget{}, ErrStackKitLifecycleUnavailable
	}
	return StackKitIdentityTarget{AgentID: binding.AgentID, StackKitInstanceID: stack.stackKitInstanceID}, nil
}

type tenantIdentityCommandSender interface {
	SendStackKitCommandForTenant(context.Context, string, string, *agentpb.StackKitCommand) (*agentpb.StackKitResult, error)
}

// RunStackKitIdentityCommand dispatches directly to the enrolled runtime. It
// deliberately creates no job or durable result because approved mutations
// may return a one-time activation credential.
func (o *Orchestrator) RunStackKitIdentityCommand(ctx context.Context, request StackKitIdentityCommandRequest) (*agentpb.StackKitResult, error) {
	if o == nil {
		return nil, ErrStackKitLifecycleUnavailable
	}
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.OwnerID = strings.TrimSpace(request.OwnerID)
	request.DeploymentID = strings.TrimSpace(request.DeploymentID)
	if request.TenantID == "" || request.OwnerID == "" || request.DeploymentID == "" {
		return nil, fmt.Errorf("%w: exact identity authority is required", ErrStackKitLifecycleUnavailable)
	}
	stack, err := o.findStackForJob(ctx, request.DeploymentID, ProvisionStackOptions{
		RequestContext: ctx, TenantID: request.TenantID, OwnerID: request.OwnerID,
	})
	if err != nil {
		return nil, err
	}
	if stack.tenantID != request.TenantID || stack.ownerID != request.OwnerID {
		return nil, controlplane.ErrNotFound
	}
	binding, err := o.identityRuntimeAgentBinding(ctx, stack)
	if err != nil {
		return nil, err
	}
	// A caller that names the runtime (the household worker's legacy list and
	// remove included) is held to it exactly like a projection.
	expected := request.ExpectedAgentID != "" || request.ExpectedStackKitInstanceID != ""
	if (request.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_PROJECT || expected) &&
		(request.ExpectedAgentID == "" || request.ExpectedStackKitInstanceID == "" ||
			request.ExpectedAgentID != binding.AgentID || request.ExpectedStackKitInstanceID != stack.stackKitInstanceID) {
		// Retain the original target even if its current inventory changed.
		// A pending/unknown first effect never authorizes another node.
		return nil, fmt.Errorf("%w: household identity runtime binding changed or missing", ErrStackKitLifecycleUnavailable)
	}
	release, err := jobs.TargetStackKitReleaseForStack(ctx, request.TenantID, request.DeploymentID)
	if err != nil || release == nil {
		return nil, fmt.Errorf("%w: pinned StackKits identity runtime is unavailable", ErrStackKitLifecycleUnavailable)
	}
	receipt := release.Receipt()
	command := &agentpb.StackKitCommand{
		CommandId: uuid.NewString(), Operation: request.Operation,
		WorkingDirectory: stackKitIdentityWorkspace, TimeoutSeconds: 30,
		Release: &agentpb.StackKitReleasePin{
			Version: receipt.Version, PlatformOs: receipt.Platform.OS, PlatformArch: receipt.Platform.Arch,
			ArchiveSha256: receipt.ArchiveSHA256, ReleaseIndexSha256: receipt.IndexSHA256,
		},
		// The identity commands read the StackSpec the rollout applied.
		SpecPath:      jobs.ApplyStackKitRolloutDefaults(jobs.StackKitLifecycleRequest{}, stackKitRolloutBinding(stack), "").SpecPath,
		OwnerApproved: request.OwnerApproved, StackkitInstanceId: stack.stackKitInstanceID,
		HouseholdUsername: strings.TrimSpace(request.HouseholdUsername), HouseholdEmail: strings.TrimSpace(request.HouseholdEmail),
		HouseholdDisplayName: strings.TrimSpace(request.HouseholdDisplayName),
		HouseholdApps:        request.HouseholdApps, HouseholdAppsSet: request.HouseholdAppsSet,
		HouseholdProjectionKey:      request.HouseholdProjectionKey,
		HouseholdProjectionRevision: request.HouseholdProjectionRevision,
		HouseholdProjectionAction:   request.HouseholdProjectionAction,
	}
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return nil, err
	}
	o.mu.RLock()
	sender := o.stackKitCommander
	o.mu.RUnlock()
	if sender == nil {
		return nil, ErrStackKitLifecycleUnavailable
	}
	var result *agentpb.StackKitResult
	if scoped, ok := sender.(tenantIdentityCommandSender); ok {
		result, err = scoped.SendStackKitCommandForTenant(ctx, request.TenantID, binding.AgentID, command)
	} else {
		result, err = sender.SendStackKitCommand(ctx, binding.AgentID, command)
	}
	if err != nil {
		return nil, err
	}
	if err := stackkitcommand.ValidateResult(result, command); err != nil {
		return nil, err
	}
	if !result.Success {
		// A valid failed/denied envelope describes a failed effect, not a
		// successful transport operation. Never acknowledge a removal from it.
		return nil, fmt.Errorf("StackKits identity operation failed (exit code %d)", result.ExitCode)
	}
	return result, nil
}

func (o *Orchestrator) identityRuntimeAgentBinding(ctx context.Context, stack *orchestratorStack) (stackKitLifecycleAgentBinding, error) {
	bindings, err := o.approvedStackKitLifecycleAgentBindings(ctx, stack)
	if err != nil {
		return stackKitLifecycleAgentBinding{}, err
	}
	if len(bindings) == 1 {
		return bindings[0], nil
	}
	var selected *stackKitLifecycleAgentBinding
	for index := range bindings {
		if !strings.EqualFold(bindings[index].NodeRef, "main") && !strings.EqualFold(bindings[index].NodeID, "main") {
			continue
		}
		if selected != nil {
			return stackKitLifecycleAgentBinding{}, fmt.Errorf("%w: identity controller binding is ambiguous", ErrStackKitLifecycleUnavailable)
		}
		candidate := bindings[index]
		selected = &candidate
	}
	if selected == nil {
		return stackKitLifecycleAgentBinding{}, fmt.Errorf("%w: identity controller binding is unavailable", ErrStackKitLifecycleUnavailable)
	}
	return *selected, nil
}
