package orchestrator

import (
	"context"
	"fmt"
	"sort"

	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// AuthorUseCaseConfiguration resolves an installed workload to its already
// approved physical Guard. The compiler cannot select or provision a target.
func (o *Orchestrator) useCaseAuthoringRequest(ctx context.Context, tenantID, ownerID, stackID, workloadRef string) (jobs.StackKitLifecycleRequest, error) {
	var empty jobs.StackKitLifecycleRequest
	if o == nil || o.StackKitCommander() == nil {
		return empty, ErrStackKitLifecycleUnavailable
	}
	stack, err := o.findStackForJob(ctx, stackID, ProvisionStackOptions{RequestContext: ctx, TenantID: tenantID, OwnerID: ownerID})
	if err != nil {
		return empty, err
	}
	if stack.tenantID != tenantID || stack.ownerID != ownerID || stack.stackKitInstanceID == "" {
		return empty, ErrStackKitLifecycleUnavailable
	}
	// The persisted canonical desired spec supplies placement; the receiving
	// CLI supplies the current canonical baseline and its native spec hash.
	spec, ok := stack.config["stack_spec_v2"].(map[string]any)
	if !ok {
		return empty, ErrStackKitLifecycleUnavailable
	}
	workloads, _ := spec["workloads"].(map[string]any)
	entry, ok := workloads[workloadRef].(map[string]any)
	if !ok {
		return empty, fmt.Errorf("selected workload is not installed")
	}
	placement, _ := entry["placement"].(map[string]any)
	refs, _ := placement["nodeRefs"].([]any)
	nodes, _ := spec["nodes"].([]any)
	var targetRefs []string
	for _, raw := range refs {
		if ref, ok := raw.(string); ok && ref != "" {
			targetRefs = append(targetRefs, ref)
		}
	}
	if len(targetRefs) == 0 && len(nodes) == 1 {
		node, _ := nodes[0].(map[string]any)
		if ref, ok := node["id"].(string); ok {
			targetRefs = append(targetRefs, ref)
		}
	}
	if len(targetRefs) == 0 {
		return empty, fmt.Errorf("selected workload has no exact installed node")
	}
	sort.Strings(targetRefs)
	bindings, err := o.approvedStackKitLifecycleAgentBindings(ctx, stack)
	if err != nil {
		return empty, err
	}
	store := o.canonicalServerRuntimeStore()
	if store == nil {
		return empty, ErrStackKitLifecycleUnavailable
	}
	servers, err := store.ListServerRuntimesByTenant(ctx, tenantID, stackID)
	if err != nil {
		return empty, err
	}
	var selected *stackKitLifecycleAgentBinding
	serverID := ""
	for _, binding := range bindings {
		for _, server := range servers {
			if server.OwnerSubjectID != ownerID || server.WorkerID != binding.WorkerID || server.ID != binding.ServerID || server.DecommissionedAt != nil || (server.ConnectionState != "connected" && server.ConnectionState != "degraded") {
				continue
			}
			ref, _ := server.Metadata["spec_node_id"].(string)
			if ref == "" {
				ref, _ = jobs.StackKitNodeRefForAgent(firstNonEmptyString(stackKitRolloutBinding(stack).StackKit, stackKitCatalogRef(stack)), binding.NodeRef)
			}
			if ref != targetRefs[0] {
				continue
			}
			if selected != nil {
				return empty, fmt.Errorf("selected workload node has multiple approved runtime bindings")
			}
			copy := binding
			selected = &copy
			serverID = server.ID
		}
	}
	if selected == nil {
		return empty, ErrStackKitLifecycleUnavailable
	}
	if err := o.authorizeApplicationOperation(ctx, tenantID, ownerID, serverID); err != nil {
		return empty, err
	}
	request := jobs.ApplyStackKitRolloutDefaults(jobs.StackKitLifecycleRequest{TenantID: tenantID, OwnerID: ownerID, StackID: stackID, StackKitInstanceID: stack.stackKitInstanceID, AgentID: selected.AgentID, NodeID: selected.NodeID, NodeRef: targetRefs[0]}, stackKitRolloutBinding(stack), stackKitCatalogRef(stack))
	return request, nil
}

func (o *Orchestrator) AuthorUseCaseConfiguration(ctx context.Context, tenantID, ownerID, stackID string, configuration stackkitcommand.UseCaseConfiguration) (stackkitcommand.UseCaseConfigurationEvidence, error) {
	request, err := o.useCaseAuthoringRequest(ctx, tenantID, ownerID, stackID, configuration.WorkloadSlug)
	if err != nil {
		return stackkitcommand.UseCaseConfigurationEvidence{}, err
	}
	return jobs.AuthorUseCaseConfiguration(ctx, o.StackKitCommander(), request, configuration)
}

func (o *Orchestrator) AuthorUseCaseRemoval(ctx context.Context, tenantID, ownerID, stackID string, removal stackkitcommand.UseCaseRemoval) (stackkitcommand.UseCaseRemovalEvidence, error) {
	if err := removal.Validate(); err != nil {
		return stackkitcommand.UseCaseRemovalEvidence{}, err
	}
	request, err := o.useCaseAuthoringRequest(ctx, tenantID, ownerID, stackID, removal.WorkloadRefs()[0])
	if err != nil {
		return stackkitcommand.UseCaseRemovalEvidence{}, err
	}
	return jobs.AuthorUseCaseRemoval(ctx, o.StackKitCommander(), request, removal)
}
