package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

var ErrStackKitLifecycleUnavailable = errors.New("typed StackKits lifecycle is unavailable")

// ConfigureStackKitCommander installs the sole lifecycle Adapter used by
// operator requests. Startup binds this seam to the authenticated transport the
// deployment's enrolled runtimes actually poll (HTTPS Guard in SaaS, mTLS gRPC
// when available in self-hosted mode).
func (o *Orchestrator) ConfigureStackKitCommander(sender jobs.StackKitCommandSender) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stackKitCommander = sender
	o.cfg.StackKitCommander = sender
	jobs.RegisterStackKitLifecycleHandler(o.queue, jobs.StackKitLifecycleConfig{Sender: sender})
	jobs.RegisterDefaultHandlers(o.queue, o.provisionConfig(o.cfg.RuntimeActions))
}

// ConfigureManagedStackKitInventory installs the control-plane authority that
// projects fresh managed backup custody into cloud-kit Inventory.
func (o *Orchestrator) ConfigureManagedStackKitInventory(builder jobs.ManagedStackKitInventoryBuilder) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.managedStackKitInventory = builder
	o.cfg.ManagedStackKitInventory = builder
	jobs.RegisterDefaultHandlers(o.queue, o.provisionConfig(o.cfg.RuntimeActions))
}

func (o *Orchestrator) EnqueueStackKitLifecycle(ctx context.Context, request jobs.StackKitLifecycleRequest) (string, error) {
	if o == nil {
		return "", ErrStackKitLifecycleUnavailable
	}
	normalized, err := jobs.NormalizeStackKitLifecycleRequest(request)
	if err != nil {
		return "", err
	}
	stack, err := o.findStackForJob(ctx, normalized.StackID, ProvisionStackOptions{
		RequestContext: ctx,
		TenantID:       normalized.TenantID,
		OwnerID:        normalized.OwnerID,
	})
	if err != nil {
		return "", err
	}
	if stack.tenantID != normalized.TenantID || stack.ownerID != normalized.OwnerID {
		return "", controlplane.ErrNotFound
	}
	normalized, err = bindStackKitLifecycleInstance(normalized, stack)
	if err != nil {
		return "", err
	}
	binding, err := o.admitStackKitLifecycleAgent(ctx, stack, normalized.AgentID)
	if err != nil {
		return "", err
	}
	normalized.AgentID = binding.AgentID
	normalized.NodeID = binding.NodeID

	jobID, replay, err := o.reserveStackKitLifecycleJob(ctx, stack, normalized)
	if err != nil {
		return "", err
	}
	if replay {
		return jobID, nil
	}
	job := &jobs.Job{
		ID:          jobID,
		Type:        jobs.JobTypeStackKitLifecycle,
		TargetType:  targetTypeStack,
		TargetID:    stack.id,
		TargetName:  stack.name,
		Payload:     jobs.StackKitLifecyclePayload(normalized),
		Result:      map[string]interface{}{"service_action_receipt": jobs.StackKitServiceActionReceipt(normalized)},
		MaxAttempts: 1,
	}
	jobs.CopyEdgeFlagsFromContext(ctx, job.Payload)
	jobs.CaptureRequestAuthority(ctx, job, stack.tenantID, stack.ownerID)
	if err := o.enqueueWithSync(job, stack.tenantID); err != nil {
		return "", err
	}
	o.log.Info(
		"stackkit_lifecycle_job_enqueued",
		"job_id", jobID,
		"stack_id", stack.id,
		"agent_id", binding.AgentID,
		"operation", normalized.Operation,
	)
	return jobID, nil
}

func bindStackKitLifecycleInstance(request jobs.StackKitLifecycleRequest, stack *orchestratorStack) (jobs.StackKitLifecycleRequest, error) {
	if !jobs.IsStackKitServiceLifecycleOperation(request.Operation) && request.Operation != jobs.StackKitLifecycleRemove {
		return request, nil
	}
	if stack == nil || strings.TrimSpace(stack.stackKitInstanceID) == "" {
		return request, fmt.Errorf("%w: stack has no StackKits instance identity", ErrStackKitLifecycleUnavailable)
	}
	request.StackKitInstanceID = strings.TrimSpace(stack.stackKitInstanceID)
	return request, nil
}

func (o *Orchestrator) reserveStackKitLifecycleJob(
	ctx context.Context,
	stack *orchestratorStack,
	request jobs.StackKitLifecycleRequest,
) (string, bool, error) {
	if request.DurableJobID == "" {
		jobID, err := o.createJobRecordForStack(
			ctx, stack, string(jobs.JobTypeStackKitLifecycle), "Queued typed StackKits "+request.Operation,
		)
		return jobID, false, err
	}
	if o.jobStore == nil || strings.TrimSpace(stack.tenantID) == "" {
		return "", false, fmt.Errorf("%w: durable job store is required for service action idempotency", ErrStackKitLifecycleUnavailable)
	}
	receipt := jobs.StackKitServiceActionReceipt(request)
	created, err := o.jobStore.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: request.DurableJobID, TenantID: stack.tenantID, StackID: stack.id,
		Type: string(jobs.JobTypeStackKitLifecycle), State: persistentStatePending,
		Step:    "Queued typed StackKits " + request.Operation,
		Message: "Queued typed StackKits " + request.Operation,
		Result:  map[string]any{"service_action_receipt": receipt},
	})
	if err == nil {
		return created.ID, false, nil
	}
	if !errors.Is(err, controlplane.ErrConflict) {
		return "", false, fmt.Errorf("reserve durable StackKits service action: %w", err)
	}
	existing, loadErr := o.jobStore.GetJob(ctx, stack.tenantID, request.DurableJobID)
	if loadErr != nil {
		return "", false, fmt.Errorf("load durable StackKits service action: %w", loadErr)
	}
	if existing.StackID != stack.id || !strings.EqualFold(existing.Type, string(jobs.JobTypeStackKitLifecycle)) ||
		!jobs.MatchesStackKitServiceActionReceipt(existing.Result, request) {
		return "", false, fmt.Errorf("%w: service action idempotency key belongs to another request", controlplane.ErrConflict)
	}
	if strings.EqualFold(existing.State, persistentStatePending) {
		if _, queued := o.queue.Get(existing.ID); !queued {
			// The durable receipt may have committed immediately before a process
			// crash. Rebuild the same in-memory job; StartJob's durable execution
			// claim prevents a second process from executing it concurrently.
			return existing.ID, false, nil
		}
	}
	return existing.ID, true, nil
}

func (o *Orchestrator) admitStackKitLifecycleAgent(ctx context.Context, stack *orchestratorStack, requestedAgentID string) (stackKitLifecycleAgentBinding, error) {
	return o.admitStackKitLifecycleAgentOnServer(ctx, stack, requestedAgentID, "")
}

func (o *Orchestrator) admitStackKitLifecycleAgentOnServer(ctx context.Context, stack *orchestratorStack, requestedAgentID, requestedServerID string) (stackKitLifecycleAgentBinding, error) {
	bindings, err := o.approvedStackKitLifecycleAgentBindings(ctx, stack)
	if err != nil {
		return stackKitLifecycleAgentBinding{}, err
	}
	requestedAgentID = strings.TrimSpace(requestedAgentID)
	requestedServerID = strings.TrimSpace(requestedServerID)
	for _, binding := range bindings {
		if binding.AgentID == requestedAgentID && (requestedServerID == "" || binding.ServerID == requestedServerID) {
			return binding, nil
		}
	}
	return stackKitLifecycleAgentBinding{}, fmt.Errorf("%w: requested agent and server are not an approved worker binding of this stack", ErrStackKitLifecycleUnavailable)
}

type stackKitLifecycleAgentBinding struct {
	NodeRef  string
	NodeID   string
	ServerID string
	AgentID  string
}

func (o *Orchestrator) approvedStackKitLifecycleAgentBindings(ctx context.Context, stack *orchestratorStack) ([]stackKitLifecycleAgentBinding, error) {
	if o.workerStore == nil {
		return nil, fmt.Errorf("%w: worker store is not configured", ErrStackKitLifecycleUnavailable)
	}
	workers, err := o.workerStore.ListWorkersByTenant(ctx, stack.tenantID)
	if err != nil {
		return nil, fmt.Errorf("%w: list stack workers: %v", ErrStackKitLifecycleUnavailable, err)
	}
	bindings := make([]stackKitLifecycleAgentBinding, 0, len(workers))
	for _, worker := range workers {
		if worker.StackID != stack.id || worker.OwnerSubjectID != stack.ownerID || !worker.Approved {
			continue
		}
		serverID := strings.TrimSpace(stringFromAny(worker.Capabilities["server_id"]))
		bindings = append(bindings, stackKitLifecycleAgentBinding{
			NodeRef: strings.TrimSpace(worker.Hostname), ServerID: serverID,
			NodeID:  firstNonEmptyString(stringFromAny(worker.Capabilities["node_id"]), serverID, worker.Hostname),
			AgentID: firstNonEmptyString(stringFromAny(worker.Capabilities["runtime_agent_id"]), worker.ID),
		})
	}
	return bindings, nil
}

func (o *Orchestrator) localStackKitTeardownNodeBindings(ctx context.Context, stack *orchestratorStack) ([]jobs.LocalStackKitTeardownNodeBinding, error) {
	approved, err := o.approvedStackKitLifecycleAgentBindings(ctx, stack)
	if err != nil {
		return nil, err
	}
	bindings := make([]jobs.LocalStackKitTeardownNodeBinding, 0, len(approved))
	for _, binding := range approved {
		// Old approved workers can predate exact server custody. They are not
		// teardown authority; a required node without a complete binding fails
		// closed when the Apply placements are partitioned below.
		if binding.NodeRef == "" || binding.ServerID == "" || binding.AgentID == "" {
			continue
		}
		bindings = append(bindings, jobs.LocalStackKitTeardownNodeBinding{
			NodeRef: binding.NodeRef, ServerID: binding.ServerID, AgentID: binding.AgentID,
		})
	}
	return bindings, nil
}
