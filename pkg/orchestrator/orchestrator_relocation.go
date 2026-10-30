package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// WorkloadRelocationInput names existing deployments only. Paths, node
// identities and source reachability are resolved from current authorities.
type WorkloadRelocationInput struct {
	SourceStackID      string   `json:"source_stack_id"`
	SourceAgentID      string   `json:"source_agent_id"`
	DestinationStackID string   `json:"destination_stack_id"`
	DestinationAgentID string   `json:"destination_agent_id"`
	Projects           []string `json:"projects,omitempty"`
	ResourceUUIDs      []string `json:"resource_uuids,omitempty"`
	WalletItemID       string   `json:"wallet_item_id"`
	DomainSwitches     []string `json:"domain_switches,omitempty"`
	OwnerApproved      bool     `json:"owner_approved"`
}

func (o *Orchestrator) relocationNode(ctx context.Context, tenant, owner, stackID, agentID string, source bool) (jobs.WorkloadRelocationNode, string, error) {
	var node jobs.WorkloadRelocationNode
	stack, err := o.findStackForJob(ctx, stackID, ProvisionStackOptions{RequestContext: ctx, TenantID: tenant, OwnerID: owner})
	if err != nil {
		return node, "", err
	}
	if stack.tenantID != tenant || stack.ownerID != owner {
		return node, "", controlplane.ErrNotFound
	}
	if stackStringValue(stack, jobs.StackKitModeField) != jobs.StackKitModeAdvanced || stack.stackKitInstanceID == "" {
		return node, "", fmt.Errorf("%w: relocation requires an enrolled Advanced deployment", ErrStackKitLifecycleUnavailable)
	}
	if o.cfg.AdvancedIssuer == nil {
		return node, "", ErrStackKitLifecycleUnavailable
	}
	trust, err := o.cfg.AdvancedIssuer.TrustBindingFor(ctx, tenant, stackID)
	if err != nil || trust.StackID != stack.stackKitInstanceID || trust.OwnerRef == "" {
		return node, "", ErrStackKitLifecycleUnavailable
	}
	binding, err := o.admitStackKitLifecycleAgent(ctx, stack, agentID)
	if err != nil {
		return node, "", err
	}
	rollout := stackKitRolloutBinding(stack)
	if rollout.StackKit == "" || rollout.SpecPath == "" || binding.ServerID == "" {
		return node, "", ErrStackKitLifecycleUnavailable
	}
	store := o.canonicalServerRuntimeStore()
	if store == nil {
		return node, "", ErrStackKitLifecycleUnavailable
	}
	runtime, err := store.GetServerRuntime(ctx, tenant, binding.ServerID)
	if err != nil || runtime == nil || runtime.OwnerSubjectID != owner || runtime.StackID != stackID || runtime.WorkerID != binding.WorkerID || !serverRuntimeAllowsStackDeploy(stack, *runtime, time.Now().UTC()) {
		return node, "", ErrStackKitLifecycleUnavailable
	}
	if err := o.authorizeApplicationOperation(ctx, tenant, owner, runtime.ID); err != nil {
		return node, "", err
	}
	normalized := jobs.ApplyStackKitRolloutDefaults(jobs.StackKitLifecycleRequest{StackID: stackID, TenantID: tenant, OwnerID: owner, AgentID: binding.AgentID}, rollout, stackKitCatalogRef(stack))
	node = jobs.WorkloadRelocationNode{OwnerRef: trust.OwnerRef, StackID: stackID, StackKitInstanceID: stack.stackKitInstanceID, AgentID: binding.AgentID, NodeID: binding.NodeID, NodeRef: binding.NodeRef, StackKit: normalized.StackKit, StackName: normalized.StackName, WorkingDirectory: normalized.WorkingDirectory, SpecPath: normalized.SpecPath}
	if err := jobs.RequireStackKitNode(node.StackKit, node.NodeRef); err != nil {
		return node, "", err
	}
	if !source {
		return node, "", nil
	}
	host, _ := runtime.Metadata["host"].(map[string]any)
	ip, ipErr := netip.ParseAddr(stringFromAny(host["public_ip"]))
	if ipErr != nil || !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return node, "", fmt.Errorf("%w: current Guard source address is unavailable", ErrStackKitLifecycleUnavailable)
	}
	if runtime.LeaseID != "" {
		lease, err := o.exactManagedRuntimeLeaseForStack(ctx, stack, runtime.LeaseID)
		if err != nil || lease == nil {
			return node, "", ErrManagedRuntimeLeaseNotNativeActive
		}
		providerIP, err := netip.ParseAddr(lease.Metadata["runtime_public_ip"])
		if err != nil || providerIP != ip {
			return node, "", fmt.Errorf("source address differs from current provider lease")
		}
	}
	return node, ip.String(), nil
}

func (o *Orchestrator) EnqueueWorkloadRelocation(ctx context.Context, tenant, owner, key string, input WorkloadRelocationInput) (string, error) {
	if o == nil || o.jobStore == nil || o.StackKitCommander() == nil {
		return "", ErrStackKitLifecycleUnavailable
	}
	if len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n\x00") {
		return "", fmt.Errorf("one bounded Idempotency-Key is required")
	}
	intent, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	intentDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(intent))
	id := fmt.Sprintf("relocation-%x", sha256.Sum256([]byte(tenant+"\x00"+owner+"\x00"+key)))
	if existing, err := o.jobStore.GetJob(ctx, tenant, id); err == nil {
		if existing.Type != string(jobs.JobTypeWorkloadRelocation) || existing.StackID != input.SourceStackID || existing.Result["relocation_request_sha256"] != intentDigest || existing.Payload["owner_id"] != owner {
			return "", controlplane.ErrConflict
		}
		return id, nil // Never redispatch an uncertain or completed semantic move.
	} else if !errors.Is(err, controlplane.ErrNotFound) {
		return "", err
	}
	source, address, err := o.relocationNode(ctx, tenant, owner, input.SourceStackID, input.SourceAgentID, true)
	if err != nil {
		return "", err
	}
	destination, _, err := o.relocationNode(ctx, tenant, owner, input.DestinationStackID, input.DestinationAgentID, false)
	if err != nil {
		return "", err
	}
	req, err := jobs.NormalizeWorkloadRelocationRequest(jobs.WorkloadRelocationRequest{TenantID: tenant, OwnerID: owner, Source: source, Destination: destination, Projects: input.Projects, ResourceUUIDs: input.ResourceUUIDs, SourceAddress: address, WalletItemID: input.WalletItemID, DomainSwitches: input.DomainSwitches, OwnerApproved: input.OwnerApproved}, nil)
	if err != nil {
		return "", err
	}
	payload, err := jobs.WorkloadRelocationPayload(req)
	if err != nil {
		return "", err
	}
	payload["tenant_id"], payload["owner_id"], payload["agent_id"] = tenant, owner, source.AgentID
	payload["execution_targets"] = []controlplane.ExecutionTarget{{StackID: source.StackID, AgentID: source.AgentID}, {StackID: destination.StackID, AgentID: destination.AgentID}}
	job := &jobs.Job{ID: id, Type: jobs.JobTypeWorkloadRelocation, TargetType: targetTypeStack, TargetID: source.StackID, TargetName: source.StackName, Payload: payload, Result: map[string]any{"relocation_request_sha256": intentDigest}, MaxAttempts: 1}
	jobs.CopyEdgeFlagsFromContext(ctx, job.Payload)
	jobs.CaptureRequestAuthority(ctx, job, tenant, owner)
	jobs.CaptureRequestOperateAuthority(ctx, job, tenant, owner)
	_, err = o.jobStore.CreateJob(ctx, controlplane.UpsertJobRequest{ID: id, TenantID: tenant, StackID: source.StackID, Type: string(job.Type), State: persistentStatePending, Payload: job.Payload, Result: job.Result, ScheduledFor: time.Now().UTC()})
	if err != nil {
		if existing, readErr := o.jobStore.GetJob(ctx, tenant, id); readErr == nil && existing.Type == string(jobs.JobTypeWorkloadRelocation) && existing.StackID == input.SourceStackID && existing.Result["relocation_request_sha256"] == intentDigest && existing.Payload["owner_id"] == owner {
			return id, nil
		}
		return "", err
	}
	if err := o.enqueueWithSync(job, tenant); err != nil {
		return "", err
	}
	return id, nil
}

func (o *Orchestrator) InspectWorkloadRelocation(ctx context.Context, tenant, owner, stackID, id string, resolve bool) (map[string]any, error) {
	return o.recoverWorkloadRelocation(ctx, tenant, owner, stackID, id, resolve, false)
}
func (o *Orchestrator) AbortWorkloadRelocation(ctx context.Context, tenant, owner, stackID, id string) (map[string]any, error) {
	return o.recoverWorkloadRelocation(ctx, tenant, owner, stackID, id, true, true)
}
func (o *Orchestrator) recoverWorkloadRelocation(ctx context.Context, tenant, owner, stackID, id string, resolve, abort bool) (map[string]any, error) {
	if o == nil || o.jobStore == nil {
		return nil, ErrStackKitLifecycleUnavailable
	}
	job, err := o.jobStore.GetJob(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if job.Type != string(jobs.JobTypeWorkloadRelocation) || job.StackID != stackID || job.Payload["owner_id"] != owner {
		return nil, controlplane.ErrNotFound
	}
	var request jobs.WorkloadRelocationRequest
	raw, err := json.Marshal(job.Payload["workload_relocation"])
	if err != nil || json.Unmarshal(raw, &request) != nil {
		return nil, controlplane.ErrConflict
	}
	if request.TenantID != tenant || request.OwnerID != owner {
		return nil, controlplane.ErrNotFound
	}
	for _, side := range []struct {
		node   jobs.WorkloadRelocationNode
		source bool
	}{{request.Source, true}, {request.Destination, false}} {
		current, _, err := o.relocationNode(ctx, tenant, owner, side.node.StackID, side.node.AgentID, side.source)
		if err != nil {
			return nil, err
		}
		if current.NodeID != side.node.NodeID || current.NodeRef != side.node.NodeRef || current.StackKitInstanceID != side.node.StackKitInstanceID || current.OwnerRef != side.node.OwnerRef || current.WorkingDirectory != side.node.WorkingDirectory || current.SpecPath != side.node.SpecPath {
			return nil, controlplane.ErrConflict
		}
	}
	outcome, ok := job.Result["workload_relocation"].(map[string]any)
	if !ok {
		return nil, controlplane.ErrConflict
	}
	plan, ok := outcome["plan_json"].(string)
	if !ok || plan == "" {
		return nil, fmt.Errorf("move has no admitted plan checkpoint")
	}
	if resolve && (job.State == "running" || (outcome["state"] != "transferring" && outcome["state"] != "destination_verification_required" && outcome["state"] != "transfer_receipt_incomplete" && outcome["state"] != "moved" && outcome["state"] != "aborted")) {
		return nil, controlplane.ErrConflict
	}
	if abort && outcome["state"] == "moved" {
		return nil, controlplane.ErrConflict
	}
	var recoveryStore controlplane.WorkloadRelocationResolutionStore
	if resolve {
		var ok bool
		recoveryStore, ok = o.jobStore.(controlplane.WorkloadRelocationResolutionStore)
		if !ok {
			return nil, ErrStackKitLifecycleUnavailable
		}
		if err := recoveryStore.ClaimWorkloadRelocationRecovery(ctx, controlplane.WorkloadRelocationResolution{TenantID: tenant, OwnerID: owner, JobID: id, PlanSHA256: stringFromAny(outcome["plan_sha256"])}); err != nil {
			return nil, err
		}
		recoveryCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ctx = recoveryCtx
		done := make(chan struct{})
		defer close(done)
		go func() {
			ticker := time.NewTicker(25 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-recoveryCtx.Done():
					return
				case <-ticker.C:
					if err := recoveryStore.RenewWorkloadRelocationRecovery(recoveryCtx, tenant, id); err != nil {
						cancel()
						return
					}
				}
			}
		}()
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_ = recoveryStore.FailWorkloadRelocationRecovery(cleanup, tenant, id, "Recovery did not reach a durable verified resolution; operation fence retained")
		}()
	}
	var proof map[string]any
	resolvedOutcome := "moved"
	if abort {
		if outcome["state"] == "moved" {
			return nil, controlplane.ErrConflict
		}
		proof, err = jobs.AbortWorkloadRelocation(ctx, o.StackKitCommander(), request, id, []byte(plan), stringFromAny(outcome["release_version"]), stringFromAny(outcome["release_archive_sha256"]))
		resolvedOutcome = "aborted"
	} else if outcome["state"] == "aborted" {
		proof, err = jobs.ReadbackAbortedWorkloadRelocation(ctx, o.StackKitCommander(), request, id, []byte(plan), stringFromAny(outcome["release_version"]), stringFromAny(outcome["release_archive_sha256"]))
		resolvedOutcome = "aborted"
	} else {
		proof, err = jobs.ReadbackWorkloadRelocation(ctx, o.StackKitCommander(), request, id, []byte(plan), stringFromAny(outcome["release_version"]), stringFromAny(outcome["release_archive_sha256"]))
	}
	if err != nil {
		return nil, err
	}
	if resolve {
		job, err = recoveryStore.ResolveWorkloadRelocation(ctx, controlplane.WorkloadRelocationResolution{TenantID: tenant, OwnerID: owner, JobID: id, PlanSHA256: stringFromAny(outcome["plan_sha256"]), Proof: proof, Outcome: resolvedOutcome, At: time.Now().UTC()})
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"job_id": id, "state": job.State, "node_verification": proof, "source_retained": true, "resolved": resolve}, nil
}
