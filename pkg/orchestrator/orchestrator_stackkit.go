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

var ErrStackKitLifecycleUnavailable = errors.New("typed StackKits lifecycle is unavailable")

// ErrStackKitBackupCustodyManaged refuses an operator backup operation for a
// kit whose backup repository is kombify-managed custody. Those backups are
// cost-bearing and run only through the entitlement-gated scheduled backup
// path. Owner-approved managed restore drills additionally require fresh
// admission and an operation-scoped signed renewal.
var ErrStackKitBackupCustodyManaged = errors.New("backup operations for this StackKit run through the managed backup schedule")

// ConfigureStackKitCommander installs the sole lifecycle Adapter used by
// operator requests. Startup binds this seam to the authenticated transport the
// deployment's enrolled runtimes actually poll (HTTPS Guard in SaaS, mTLS gRPC
// when available in self-hosted mode).
func (o *Orchestrator) ConfigureStackKitCommander(sender jobs.StackKitCommandSender) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stackKitCommander = sender
	o.cfg.StackKitCommander = sender
	jobs.RegisterStackKitLifecycleHandler(o.queue, jobs.StackKitLifecycleConfig{Sender: sender, AdvancedIssuer: o.cfg.AdvancedIssuer, ManagedStackKitInventory: o.managedStackKitInventory, ManagedAddressAuthority: o.managedChangeSetAddressAuthority})
	jobs.RegisterDefaultHandlers(o.queue, o.provisionConfig(o.cfg.RuntimeActions))
}

// StackKitCommander returns the lifecycle Adapter installed by
// ConfigureStackKitCommander, or nil before it is configured.
func (o *Orchestrator) StackKitCommander() jobs.StackKitCommandSender {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.stackKitCommander
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
	normalized = jobs.ApplyStackKitRolloutDefaults(normalized, stackKitRolloutBinding(stack), stackKitCatalogRef(stack))
	// A caller cannot use a Basement spelling to bypass managed backup custody.
	if (jobs.IsStackKitBackupOperation(normalized.Operation) || normalized.Operation == jobs.StackKitLifecycleRestoreDrill) && stackKitRolloutBinding(stack).StackKit == "cloud-kit" && normalized.StackKit != "cloud-kit" {
		return "", ErrStackKitBackupCustodyManaged
	}
	// Direct managed backup operations remain schedule-owned.
	if jobs.IsStackKitBackupOperation(normalized.Operation) &&
		normalized.StackKit != jobs.DefaultBasementKitRef {
		return "", ErrStackKitBackupCustodyManaged
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

	var admission map[string]interface{}
	if normalized.Operation == jobs.StackKitLifecycleRestoreDrill && normalized.StackKit != jobs.DefaultBasementKitRef {
		rollout := stackKitRolloutBinding(stack)
		if normalized.StackKit != "cloud-kit" || rollout.StackKit != "cloud-kit" || normalized.SpecPath != rollout.SpecPath || normalized.StackKitInstanceID == "" {
			return "", ErrStackKitLifecycleUnavailable
		}
		o.mu.RLock()
		gate := o.cfg.ManagedRestoreAdmission
		o.mu.RUnlock()
		decision, gateErr := jobs.AdmitBackup(ctx, gate, jobs.BackupAdmissionRequest{TenantID: stack.tenantID, StackID: stack.id, UserID: stack.ownerID})
		if gateErr != nil {
			return "", fmt.Errorf("%w: managed backup admission unavailable", ErrStackKitLifecycleUnavailable)
		}
		if decision.Denied {
			return "", &ManagedBackupDeniedError{Details: decision.Details}
		}
		if decision.QuotaBytes <= 0 || decision.UsedBytes < 0 || decision.UsedBytes >= decision.QuotaBytes || decision.MeasuredAt.IsZero() || time.Since(decision.MeasuredAt) > 5*time.Minute || decision.MeasuredAt.After(time.Now().Add(time.Minute)) {
			return "", ErrStackKitLifecycleUnavailable
		}
		admission = map[string]interface{}{"tenant_id": stack.tenantID, "stack_id": stack.id, "owner_id": stack.ownerID, "agent_id": binding.AgentID, "include_content": decision.IncludeContent, "quota_bytes": decision.QuotaBytes, "used_bytes": decision.UsedBytes, "measured_at": decision.MeasuredAt.UTC().Format(time.RFC3339Nano)}
	}
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
	if admission != nil {
		job.Payload[jobs.ManagedBackupAdmissionField] = admission
		job.Result[jobs.ManagedBackupAdmissionField] = admission
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

// StackKitLifecycleJobQueued reports whether this process's queue still holds
// the job. A durable pending job it does not hold was lost to a restart.
func (o *Orchestrator) StackKitLifecycleJobQueued(jobID string) bool {
	if o == nil || o.queue == nil {
		return false
	}
	_, queued := o.queue.Get(jobID)
	return queued
}

func bindStackKitLifecycleInstance(request jobs.StackKitLifecycleRequest, stack *orchestratorStack) (jobs.StackKitLifecycleRequest, error) {
	if request.Operation == jobs.StackKitLifecycleAdvancedTrustImport || jobs.IsAdvancedStackKitLifecycleOperation(request.Operation) {
		// The trust binding records the StackKit stack id when the stack has one.
		if stack != nil {
			request.StackKitInstanceID = strings.TrimSpace(stack.stackKitInstanceID)
		}
		return request, nil
	}
	if !jobs.IsStackKitServiceLifecycleOperation(request.Operation) && request.Operation != jobs.StackKitLifecycleRemove &&
		!jobs.IsStackKitBackupOperation(request.Operation) {
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
	WorkerID string
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
			WorkerID: worker.ID,
			NodeRef:  strings.TrimSpace(worker.Hostname), ServerID: serverID,
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

// stackKitRolloutBinding returns what the stack's last managed rollout applied.
// The stack config copy is durable; the runtime summary copy is the deploy
// result itself and is replaced by later job results.
func stackKitRolloutBinding(stack *orchestratorStack) jobs.StackKitRolloutBinding {
	if stack == nil {
		return jobs.StackKitRolloutBinding{}
	}
	for _, values := range []map[string]any{stack.config, stack.runtimeSummary} {
		if binding, ok := jobs.DecodeStackKitRolloutBinding(values[jobs.StackKitRolloutBindingResultField]); ok {
			return binding
		}
	}
	return jobs.StackKitRolloutBinding{}
}

// stackKitCatalogRef is the kit the stack record was created for, used only
// when no rollout has recorded the kit it applied.
func stackKitCatalogRef(stack *orchestratorStack) string {
	if stack == nil {
		return ""
	}
	for _, values := range []map[string]any{stack.runtimeSummary, stack.config} {
		for _, key := range []string{runtimeFieldStackKitRef, "stackkit"} {
			if value := strings.TrimSpace(stringFromAny(values[key])); value != "" {
				return value
			}
		}
	}
	return ""
}

// persistStackKitRolloutBinding copies a completed rollout's binding into the
// stack config. The runtime summary is replaced by every later job result, so
// it cannot be the authority later operator operations read their StackSpec
// path and kit from.
func (o *Orchestrator) persistStackKitRolloutBinding(tenantID string, job jobs.JobSnapshot) {
	if job.Type != jobs.JobTypeDeploy || job.State != jobs.JobStateCompleted {
		return
	}
	binding, ok := jobs.DecodeStackKitRolloutBinding(job.Result[jobs.StackKitRolloutBindingResultField])
	store := o.effectiveStackStore()
	if !ok || store == nil {
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		stack, err := store.GetStack(o.ctx, tenantID, job.TargetID)
		if err != nil {
			o.log.Warn("stackkit_rollout_binding_not_persisted", "stack_id", job.TargetID, "error", err)
			return
		}
		if current, exists := jobs.DecodeStackKitRolloutBinding(stack.Config[jobs.StackKitRolloutBindingResultField]); exists && current == binding &&
			stringFromAny(stack.Config[jobs.StackKitModeField]) == jobs.StackKitModeAdvanced {
			return
		}
		config := make(map[string]any, len(stack.Config)+1)
		for key, value := range stack.Config {
			config[key] = value
		}
		config[jobs.StackKitRolloutBindingResultField] = binding.Map()
		// Every Techstack-managed deployment runs Advanced Mode.
		config[jobs.StackKitModeField] = jobs.StackKitModeAdvanced
		_, err = store.CompareAndSwapStackConfig(o.ctx, controlplane.StackConfigCAS{
			TenantID: tenantID, StackID: job.TargetID, ExpectedUpdatedAt: stack.UpdatedAt, Config: config,
		})
		if err == nil {
			return
		}
		if !errors.Is(err, controlplane.ErrConflict) {
			o.log.Warn("stackkit_rollout_binding_not_persisted", "stack_id", job.TargetID, "error", err)
			return
		}
	}
	o.log.Warn("stackkit_rollout_binding_not_persisted", "stack_id", job.TargetID, "error", "stack config kept changing")
}

// ConfigureManagedRestoreAdmission shares the schedule's positive backend gate.
func (o *Orchestrator) ConfigureManagedRestoreAdmission(admission jobs.BackupAdmission) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cfg.ManagedRestoreAdmission = admission
}

type ManagedBackupDeniedError struct{ Details map[string]any }

func (e *ManagedBackupDeniedError) Error() string { return "managed backup admission denied" }
