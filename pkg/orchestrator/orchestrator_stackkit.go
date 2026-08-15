package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pocketbase/pocketbase/core"

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
		RequestContext:      ctx,
		TenantID:            normalized.TenantID,
		OwnerID:             normalized.OwnerID,
		requireControlPlane: o.effectiveStackStore() != nil,
	})
	if err != nil {
		return "", err
	}
	if stack.tenantID != normalized.TenantID || stack.ownerID != normalized.OwnerID {
		return "", controlplane.ErrNotFound
	}
	agentID, err := o.admitStackKitLifecycleAgent(ctx, stack, normalized.AgentID)
	if err != nil {
		return "", err
	}
	normalized.AgentID = agentID

	jobID, record, replay, err := o.reserveStackKitLifecycleJob(ctx, stack, normalized)
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
	if err := o.enqueueWithSync(job, record, stack.tenantID); err != nil {
		return "", err
	}
	o.log.Info(
		"stackkit_lifecycle_job_enqueued",
		"job_id", jobID,
		"stack_id", stack.id,
		"agent_id", agentID,
		"operation", normalized.Operation,
	)
	return jobID, nil
}

func (o *Orchestrator) reserveStackKitLifecycleJob(
	ctx context.Context,
	stack *orchestratorStack,
	request jobs.StackKitLifecycleRequest,
) (string, *core.Record, bool, error) {
	if request.DurableJobID == "" {
		jobID, record, err := o.createJobRecordForStack(
			ctx, stack, string(jobs.JobTypeStackKitLifecycle), "Queued typed StackKits "+request.Operation,
		)
		return jobID, record, false, err
	}
	if o.jobStore == nil || strings.TrimSpace(stack.tenantID) == "" {
		return "", nil, false, fmt.Errorf("%w: durable job store is required for service action idempotency", ErrStackKitLifecycleUnavailable)
	}
	o.ensureControlPlaneStackForRecord(ctx, stack)
	receipt := jobs.StackKitServiceActionReceipt(request)
	created, err := o.jobStore.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: request.DurableJobID, TenantID: stack.tenantID, StackID: stack.id,
		Type: string(jobs.JobTypeStackKitLifecycle), State: persistentStatePending,
		Step:    "Queued typed StackKits " + request.Operation,
		Message: "Queued typed StackKits " + request.Operation,
		Result:  map[string]any{"service_action_receipt": receipt},
	})
	if err == nil {
		return created.ID, nil, false, nil
	}
	if !errors.Is(err, controlplane.ErrConflict) {
		return "", nil, false, fmt.Errorf("reserve durable StackKits service action: %w", err)
	}
	existing, loadErr := o.jobStore.GetJob(ctx, stack.tenantID, request.DurableJobID)
	if loadErr != nil {
		return "", nil, false, fmt.Errorf("load durable StackKits service action: %w", loadErr)
	}
	if existing.StackID != stack.id || !strings.EqualFold(existing.Type, string(jobs.JobTypeStackKitLifecycle)) ||
		!jobs.MatchesStackKitServiceActionReceipt(existing.Result, request) {
		return "", nil, false, fmt.Errorf("%w: service action idempotency key belongs to another request", controlplane.ErrConflict)
	}
	if strings.EqualFold(existing.State, persistentStatePending) {
		if _, queued := o.queue.Get(existing.ID); !queued {
			// The durable receipt may have committed immediately before a process
			// crash. Rebuild the same in-memory job; StartJob's durable execution
			// claim prevents a second process from executing it concurrently.
			return existing.ID, nil, false, nil
		}
	}
	return existing.ID, nil, true, nil
}

func (o *Orchestrator) admitStackKitLifecycleAgent(ctx context.Context, stack *orchestratorStack, requestedAgentID string) (string, error) {
	if o.workerStore == nil {
		return "", fmt.Errorf("%w: worker store is not configured", ErrStackKitLifecycleUnavailable)
	}
	workers, err := o.workerStore.ListWorkersByTenant(ctx, stack.tenantID)
	if err != nil {
		return "", fmt.Errorf("%w: list stack workers: %v", ErrStackKitLifecycleUnavailable, err)
	}
	requestedAgentID = strings.TrimSpace(requestedAgentID)
	for _, worker := range workers {
		if worker.StackID != stack.id || worker.OwnerSubjectID != stack.ownerID || !worker.Approved {
			continue
		}
		agentID := firstNonEmptyString(
			stringFromAny(worker.Capabilities["runtime_agent_id"]),
			worker.ID,
		)
		if agentID == requestedAgentID {
			return agentID, nil
		}
	}
	return "", fmt.Errorf("%w: requested agent is not an approved worker of this stack", ErrStackKitLifecycleUnavailable)
}
