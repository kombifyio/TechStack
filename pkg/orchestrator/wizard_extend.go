package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// PrepareWizardExtend resolves changed workload placements onto the existing
// canonical servers and reads their pinned PLAN binding before desired state is
// saved. It neither provisions resources nor dispatches mutating commands.
func (o *Orchestrator) PrepareWizardExtend(ctx context.Context, tenantID, ownerID, stackID, runID, requestDigest string, before, candidate map[string]any) ([]jobs.StackKitLifecycleRequest, error) {
	return o.prepareWizardUseCaseChange(ctx, tenantID, ownerID, stackID, runID, requestDigest, before, candidate, nil)
}

// Removal uses the same canonical server admission, PLAN binding and durable
// Advanced group. Only its exact catalog-bound retired members may disappear.
func (o *Orchestrator) PrepareWizardUseCaseRemoval(ctx context.Context, tenantID, ownerID, stackID, runID, requestDigest string, before, candidate map[string]any, removal stackkitcommand.UseCaseRemoval) ([]jobs.StackKitLifecycleRequest, error) {
	if err := removal.Validate(); err != nil {
		return nil, err
	}
	return o.prepareWizardUseCaseChange(ctx, tenantID, ownerID, stackID, runID, requestDigest, before, candidate, removal.WorkloadRefs())
}

func (o *Orchestrator) prepareWizardUseCaseChange(ctx context.Context, tenantID, ownerID, stackID, runID, requestDigest string, before, candidate map[string]any, retired []string) ([]jobs.StackKitLifecycleRequest, error) {
	if o == nil || o.jobStore == nil || o.StackKitCommander() == nil {
		return nil, ErrStackKitLifecycleUnavailable
	}
	stack, err := o.findStackForJob(ctx, stackID, ProvisionStackOptions{RequestContext: ctx, TenantID: tenantID, OwnerID: ownerID})
	if err != nil {
		return nil, err
	}
	if stack.tenantID != tenantID || stack.ownerID != ownerID || stack.stackKitInstanceID == "" {
		return nil, ErrStackKitLifecycleUnavailable
	}
	targets, err := changedWizardNodesForRetirement(before, candidate, retired)
	if err != nil || len(targets) == 0 {
		return nil, err
	}
	store := o.canonicalServerRuntimeStore()
	if store == nil {
		return nil, ErrStackKitLifecycleUnavailable
	}
	servers, err := store.ListServerRuntimesByTenant(ctx, tenantID, stackID)
	if err != nil {
		return nil, err
	}
	bindings, err := o.approvedStackKitLifecycleAgentBindings(ctx, stack)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return nil, err
	}
	o.mu.RLock()
	issuer := o.cfg.AdvancedIssuer
	o.mu.RUnlock()
	requests := make([]jobs.StackKitLifecycleRequest, 0, len(targets))
	for _, nodeRef := range targets {
		var selected *stackKitLifecycleAgentBinding
		serverID := ""
		for _, binding := range bindings {
			for _, server := range servers {
				if server.OwnerSubjectID != ownerID || server.WorkerID != binding.WorkerID || server.ID != binding.ServerID ||
					server.DecommissionedAt != nil || (server.ConnectionState != "connected" && server.ConnectionState != "degraded") {
					continue
				}
				ref, _ := server.Metadata["spec_node_id"].(string)
				if ref == "" {
					ref, _ = jobs.StackKitNodeRefForAgent(firstNonEmptyString(stackKitRolloutBinding(stack).StackKit, stackKitCatalogRef(stack)), binding.NodeRef)
				}
				if ref != nodeRef {
					continue
				}
				if selected != nil {
					return nil, fmt.Errorf("%w: node %s has multiple approved runtime bindings", ErrStackKitLifecycleUnavailable, nodeRef)
				}
				copy := binding
				selected, serverID = &copy, server.ID
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("%w: node %s has no connected canonical approved server", ErrStackKitLifecycleUnavailable, nodeRef)
		}
		if err := o.authorizeApplicationOperation(ctx, tenantID, ownerID, serverID); err != nil {
			return nil, err
		}
		request := jobs.ApplyStackKitRolloutDefaults(jobs.StackKitLifecycleRequest{
			TenantID: tenantID, OwnerID: ownerID, StackID: stackID, StackKitInstanceID: stack.stackKitInstanceID,
			AgentID: selected.AgentID, NodeID: selected.NodeID, NodeRef: nodeRef,
			Operation: jobs.StackKitLifecycleAdvancedChangeSet, OwnerApproved: true, CandidateSpecJSON: raw,
			WizardExtend: &jobs.WizardExtendBinding{RunID: runID, RequestDigest: requestDigest},
		}, stackKitRolloutBinding(stack), stackKitCatalogRef(stack))
		request, err = jobs.PrepareWizardExtend(ctx, o.StackKitCommander(), issuer, request)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	jobIDs := make([]string, 0, len(requests))
	for _, request := range requests {
		jobIDs = append(jobIDs, request.DurableJobID)
	}
	for index := range requests {
		requests[index].WizardExtend.JobIDs = append([]string(nil), jobIDs...)
	}
	return requests, nil
}

// A plan tuple never moves the action to another server. The approved Guard
// still has to own this exact canonical node in the receiving stack.
func (o *Orchestrator) admitWizardExecutionNode(ctx context.Context, stack *orchestratorStack, request jobs.StackKitLifecycleRequest, binding stackKitLifecycleAgentBinding) error {
	if request.WizardExtend == nil || request.WizardExtend.ExecutionBinding == nil {
		return nil
	}
	if binding.ServerID == "" || request.WizardExtend.ExecutionBinding.NodeRef != request.NodeRef {
		return ErrStackKitLifecycleUnavailable
	}
	store := o.canonicalServerRuntimeStore()
	if store == nil {
		return ErrStackKitLifecycleUnavailable
	}
	server, err := store.GetServerRuntime(ctx, request.TenantID, binding.ServerID)
	if err != nil || server == nil || server.StackID != stack.id || server.TenantID != request.TenantID || server.OwnerSubjectID != request.OwnerID ||
		server.WorkerID != binding.WorkerID || server.DecommissionedAt != nil || server.DesiredState == "decommissioned" {
		return ErrStackKitLifecycleUnavailable
	}
	nodeRef, _ := server.Metadata["spec_node_id"].(string)
	if nodeRef == "" {
		nodeRef, _ = jobs.StackKitNodeRefForAgent(request.StackKit, binding.NodeRef)
	}
	if nodeRef != request.NodeRef {
		return ErrStackKitLifecycleUnavailable
	}
	return nil
}

// replayWizardExtend returns the original accepted job before reading mutable
// worker bindings. A lost pending queue entry reuses its original payload;
// running and terminal jobs are never redispatched by an HTTP retry.
func (o *Orchestrator) replayWizardExtend(ctx context.Context, request jobs.StackKitLifecycleRequest) (string, bool, error) {
	if request.WizardExtend == nil {
		return "", false, nil
	}
	if _, err := jobs.NormalizeStackKitLifecycleRequest(request); err != nil {
		return "", false, err
	}
	if o == nil || o.jobStore == nil {
		return "", false, ErrStackKitLifecycleUnavailable
	}
	stack, err := o.findStackForJob(ctx, request.StackID, ProvisionStackOptions{RequestContext: ctx, TenantID: request.TenantID, OwnerID: request.OwnerID})
	if err != nil {
		return "", false, err
	}
	if stack.ownerID != request.OwnerID || stack.tenantID != request.TenantID {
		return "", false, controlplane.ErrNotFound
	}
	existing, err := o.jobStore.GetJob(ctx, request.TenantID, request.DurableJobID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if existing.StackID != request.StackID || existing.Type != string(jobs.JobTypeStackKitLifecycle) || !jobs.MatchesWizardExtendReceipt(existing.Result, request) {
		return "", false, fmt.Errorf("%w: wizard extension belongs to another accepted request", controlplane.ErrConflict)
	}
	if strings.EqualFold(existing.State, persistentStatePending) {
		if _, queued := o.queue.Get(existing.ID); !queued {
			if len(existing.Payload) == 0 {
				return "", false, ErrStackKitLifecycleUnavailable
			}
			job := &jobs.Job{ID: existing.ID, Type: jobs.JobTypeStackKitLifecycle, TargetType: targetTypeStack,
				TargetID: existing.StackID, TargetName: stack.name, Payload: existing.Payload, Result: existing.Result, MaxAttempts: 1}
			jobs.CaptureRequestAuthority(ctx, job, request.TenantID, request.OwnerID)
			if err := o.enqueueWithSync(job, request.TenantID); err != nil {
				return "", false, err
			}
		}
	}
	return existing.ID, true, nil
}

func changedWizardNodes(before, candidate map[string]any) ([]string, error) {
	return changedWizardNodesForRetirement(before, candidate, nil)
}

func changedWizardNodesForRetirement(before, candidate map[string]any, retired []string) ([]string, error) {
	old, _ := before["workloads"].(map[string]any)
	next, _ := candidate["workloads"].(map[string]any)
	nodes, _ := candidate["nodes"].([]any)
	defaultNode := ""
	if len(nodes) == 1 {
		node, _ := nodes[0].(map[string]any)
		defaultNode, _ = node["id"].(string)
	}

	changed := map[string]bool{}
	allowedRetired := map[string]bool{}
	for _, ref := range retired {
		if allowedRetired[ref] {
			return nil, fmt.Errorf("retired workload selection is duplicated")
		}
		if _, exists := old[ref]; !exists {
			return nil, fmt.Errorf("retired workload %s is not installed", ref)
		}
		if _, exists := next[ref]; exists {
			return nil, fmt.Errorf("retired workload %s remains selected", ref)
		}
		allowedRetired[ref] = true
	}
	markPlacement := func(workload string, value any) error {
		entry, _ := value.(map[string]any)
		placement, _ := entry["placement"].(map[string]any)
		refs, _ := placement["nodeRefs"].([]any)
		if len(refs) == 0 && defaultNode != "" {
			changed[defaultNode] = true
			return nil
		}
		if len(refs) == 0 {
			return fmt.Errorf("workload %s requires an accepted node placement", workload)
		}
		for _, raw := range refs {
			ref, _ := raw.(string)
			if strings.TrimSpace(ref) == "" {
				return fmt.Errorf("workload %s has invalid node placement", workload)
			}
			changed[ref] = true
		}
		return nil
	}
	for workload, value := range next {
		oldJSON, _ := json.Marshal(old[workload])
		nextJSON, _ := json.Marshal(value)
		if string(oldJSON) != string(nextJSON) {
			if err := markPlacement(workload, value); err != nil {
				return nil, err
			}
		}
	}
	for workload, value := range old {
		if _, kept := next[workload]; kept {
			continue
		}
		if !allowedRetired[workload] {
			return nil, fmt.Errorf("adding use cases cannot remove existing workload %s", workload)
		}
		if err := markPlacement(workload, value); err != nil {
			return nil, err
		}
	}
	// Foundation, platform, route and capability changes may affect hosts
	// outside the newly placed workload. Reconcile all declared nodes for a
	// non-workload delta; the pinned CLI retains ownership of layer planning.
	foundationChanged := false
	for key, value := range candidate {
		if key == "workloads" {
			continue
		}
		oldJSON, _ := json.Marshal(before[key])
		nextJSON, _ := json.Marshal(value)
		if string(oldJSON) != string(nextJSON) {
			foundationChanged = true
			break
		}
	}
	// An already desired workload can still be absent on the host after a
	// failed operation. Reconcile it rather than report a saved spec as an
	// installed application; StackKits decides which unchanged units to skip.
	if len(changed) == 0 || foundationChanged {
		for _, raw := range nodes {
			node, _ := raw.(map[string]any)
			if enabled, set := node["enabled"].(bool); set && !enabled {
				continue
			}
			if ref, _ := node["id"].(string); ref != "" {
				changed[ref] = true
			}
		}
	}
	out := make([]string, 0, len(changed))
	for node := range changed {
		out = append(out, node)
	}
	sort.Strings(out)
	return out, nil
}
