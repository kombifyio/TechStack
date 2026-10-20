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
	config := jobs.StackKitLifecycleConfig{Sender: sender, AdvancedIssuer: o.cfg.AdvancedIssuer, OriginCertificates: o.cfg.OriginCertificates, ManagedStackKitInventory: o.managedStackKitInventory, ManagedAddressAuthority: o.managedChangeSetAddressAuthority, ApplicationAuthorize: o.authorizeApplicationOperation, ServiceRuntimeAuthorize: o.authorizeDeclaredServiceRuntime, ConnectorAuthorize: o.authorizeApplicationConnector}
	config.WalletHostDelivery = walletHostDelivery(o.cfg.KombifyWallet, o.cfg.WalletHostAuthority)
	config.Services, _ = o.cfg.StackStore.(interface {
		controlplane.ServiceRuntimeStore
		controlplane.ServiceEventWriter
		controlplane.ServiceEventReader
	})
	jobs.RegisterStackKitLifecycleHandler(o.queue, config)
	jobs.RegisterWorkloadRelocationHandler(o.queue, config)
	jobs.RegisterDefaultHandlers(o.queue, o.provisionConfig(o.cfg.RuntimeActions))
}

// Reuse the approved lifecycle binding at execution and intent projection; a
// queued card action cannot follow a service moved onto another worker.
func (o *Orchestrator) authorizeDeclaredServiceRuntime(ctx context.Context, req jobs.StackKitLifecycleRequest, serverID string) error {
	stack, err := o.findStackForJob(ctx, req.StackID, ProvisionStackOptions{RequestContext: ctx, TenantID: req.TenantID, OwnerID: req.OwnerID})
	if err != nil {
		return err
	}
	if stack.tenantID != req.TenantID || stack.ownerID != req.OwnerID || stack.stackKitInstanceID != req.StackKitInstanceID {
		return ErrStackKitLifecycleUnavailable
	}
	binding, err := o.admitStackKitLifecycleAgentOnServer(ctx, stack, req.AgentID, serverID)
	if err != nil {
		return err
	}
	store := o.canonicalServerRuntimeStore()
	if store == nil {
		return ErrStackKitLifecycleUnavailable
	}
	runtime, err := store.GetServerRuntime(ctx, req.TenantID, serverID)
	if err != nil || runtime == nil || runtime.ID != serverID || runtime.TenantID != req.TenantID || runtime.StackID != req.StackID || runtime.OwnerSubjectID != req.OwnerID || runtime.WorkerID != binding.WorkerID {
		return ErrStackKitLifecycleUnavailable
	}
	return nil
}

func (o *Orchestrator) ConfigureApplicationOperationAuthorization(authorize func(context.Context, string, string, string) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.applicationAuthorize = authorize
}

// ConfigureApplicationConnectorRefreshAuthorization installs the current
// backend read policy used for ordinary and owner-requested reconciliation.
func (o *Orchestrator) ConfigureApplicationConnectorRefreshAuthorization(authorize func(context.Context, string, string, string) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.connectorReadAuthorize = authorize
}

func (o *Orchestrator) authorizeApplicationOperation(ctx context.Context, tenantID, ownerID, serverID string) error {
	o.mu.RLock()
	authorize := o.applicationAuthorize
	o.mu.RUnlock()
	if authorize == nil {
		return fmt.Errorf("application operate policy is unavailable")
	}
	return authorize(ctx, tenantID, ownerID, serverID)
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

// ConfigureFederationFabric installs the external fabric authority that issues
// the inter-site link binding of a managed Modern deployment.
func (o *Orchestrator) ConfigureFederationFabric(fabric jobs.FederationFabric) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cfg.FederationFabric = fabric
	jobs.RegisterDefaultHandlers(o.queue, o.provisionConfig(o.cfg.RuntimeActions))
}

func (o *Orchestrator) EnqueueStackKitLifecycle(ctx context.Context, request jobs.StackKitLifecycleRequest) (string, error) {
	if o == nil {
		return "", ErrStackKitLifecycleUnavailable
	}
	if strings.ToLower(strings.TrimSpace(request.Operation)) == jobs.StackKitLifecycleBackupRestoreActivate && request.RestoreIdempotencyKeySHA256 == "" {
		return "", fmt.Errorf("restore activation requires a scoped Idempotency-Key")
	}
	if id, replayed, err := o.ReplayRestoreActivationRequest(ctx, request); err != nil || replayed {
		return id, err
	}
	normalized, err := jobs.NormalizeStackKitLifecycleRequest(request)
	if err != nil {
		return "", err
	}
	if jobs.IsAIProviderStep(normalized.Operation) {
		// The delivery steps run only inside ai_provider, which supplies the
		// Wallet's sealed envelope and its delivering key.
		return "", fmt.Errorf("%w: %s runs only as a step of %s", ErrStackKitLifecycleUnavailable, normalized.Operation, jobs.StackKitLifecycleAIProvider)
	}
	if jobs.IsSSHTrustStep(normalized.Operation) {
		return "", fmt.Errorf("%w: %s runs only as a step of %s", ErrStackKitLifecycleUnavailable, normalized.Operation, jobs.StackKitLifecycleSSHTrustApply)
	}
	// These fields belong to canonical runtime admission, never the caller.
	normalized.ManagedRuntimeLeaseID, normalized.ManagedRuntimeServerID = "", ""
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
	if normalized.Operation == jobs.StackKitLifecycleBackupRestoreActivate || normalized.Operation == jobs.StackKitLifecycleBackupRestoreRecover {
		actualKit := stackKitRolloutBinding(stack).StackKit
		if actualKit == "" {
			actualKit = stackKitCatalogRef(stack)
		}
		if jobs.NormalizeStackKitRef(actualKit) != jobs.DefaultBasementKitRef {
			return "", ErrStackKitBackupCustodyManaged
		}
	}
	// A caller cannot use a Basement spelling to bypass managed backup custody.
	if (jobs.IsStackKitBackupOperation(normalized.Operation) || normalized.Operation == jobs.StackKitLifecycleRestoreDrill) && stackKitRolloutBinding(stack).StackKit == "cloud-kit" && normalized.StackKit != "cloud-kit" {
		return "", ErrStackKitBackupCustodyManaged
	}
	// Direct managed backup operations remain schedule-owned.
	if jobs.IsStackKitBackupOperation(normalized.Operation) &&
		normalized.StackKit != jobs.DefaultBasementKitRef {
		// Typed activation is native node custody, not admission to bypass the
		// managed schedule or its released Advanced restore contract.
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
	// The node the admitted agent owns picks the Site and channel of a
	// multi-node kit (Modern Homelab); an agent that owns none of its nodes is
	// refused here, before a job exists.
	normalized.NodeRef = binding.NodeRef
	if err := jobs.RequireStackKitNode(normalized.StackKit, normalized.NodeRef); err != nil {
		return "", fmt.Errorf("%w: %v", ErrStackKitLifecycleUnavailable, err)
	}
	var restoreAdmission map[string]interface{}
	if normalized.Operation == jobs.StackKitLifecycleBackupRestoreRecover {
		normalized, restoreAdmission, err = o.originalRestoreActivationAdmission(ctx, normalized)
		if err != nil {
			return "", err
		}
	}
	if normalized.Operation == jobs.StackKitLifecycleGenerate && normalized.StackKit == "cloud-kit" {
		// Classification comes from the approved server's canonical lease, not
		// a kit spelling or transient stack summary. Unleased workers retain
		// their existing standalone generation path.
		normalized.ManagedRuntimeLeaseID = ""
		normalized.ManagedRuntimeServerID = ""
		store := o.canonicalServerRuntimeStore()
		if store != nil && binding.ServerID != "" {
			runtime, runtimeErr := store.GetServerRuntime(ctx, stack.tenantID, binding.ServerID)
			if runtimeErr != nil || runtime == nil || runtime.ID != binding.ServerID || runtime.TenantID != stack.tenantID || runtime.StackID != stack.id || runtime.OwnerSubjectID != stack.ownerID || runtime.WorkerID != binding.WorkerID {
				return "", ErrStackKitLifecycleUnavailable
			}
			normalized.ManagedRuntimeLeaseID = strings.TrimSpace(runtime.LeaseID)
			normalized.ManagedRuntimeServerID = runtime.ID
		}
		if recorded := stackStringValue(stack, runtimeFieldLeaseID); recorded != "" && recorded != normalized.ManagedRuntimeLeaseID {
			return "", ErrManagedRuntimeLeaseNotNativeActive
		}
		if normalized.ManagedRuntimeLeaseID != "" {
			normalized, err = bindStackKitLifecycleInstance(normalized, stack)
			if err != nil {
				return "", err
			}
		}
	}

	if jobs.IsApplicationConnectorLifecycleOperation(normalized.Operation) {
		originalNode := normalized.Connector.Request.Connector.Binding.Node
		normalized, err = jobs.BindApplicationConnectorNode(normalized)
		if err != nil {
			return "", err
		}
		if normalized.Connector.OriginalOperationID != "" && normalized.Connector.Request.Connector.Binding.Node != originalNode {
			return "", ErrStackKitLifecycleUnavailable
		}
		if o.workerStore == nil {
			return "", ErrStackKitLifecycleUnavailable
		}
		worker, readErr := o.workerStore.GetWorker(ctx, normalized.TenantID, binding.WorkerID)
		if readErr != nil || worker == nil {
			return "", ErrStackKitLifecycleUnavailable
		}
		credentials, credentialErr := controlplane.WorkerCredentialStateFromWorker(*worker)
		if credentialErr != nil || credentials.Generation <= 0 {
			return "", ErrStackKitLifecycleUnavailable
		}
		if normalized.Connector.OriginalOperationID != "" && normalized.Connector.WorkerGeneration != credentials.Generation {
			return "", ErrStackKitLifecycleUnavailable
		}
		normalized.Connector.WorkerGeneration = credentials.Generation
		if err := o.authorizeApplicationConnector(ctx, normalized); err != nil {
			return "", err
		}
	}

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
	normalized, err = jobs.FinalizeRILStackKitOperationBinding(normalized)
	if err != nil {
		return "", err
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
	if receipt := jobs.RILStackKitOperationReceipt(normalized); receipt != nil {
		job.Result[jobs.RILOperationReceiptKey] = receipt
	}
	if receipt := jobs.RestoreActivationRequestReceipt(normalized); receipt != nil {
		job.Result[controlplane.RestoreActivationRequestResultKey] = receipt
	}
	if normalized.Operation == jobs.StackKitLifecyclePublicTLSRepair {
		job.MaxAttempts = publicTLSRepairMaxAttempts
	}
	if restoreAdmission != nil {
		job.Result[jobs.RestoreActivationCheckpointResultKey] = restoreAdmission
	}
	if admission != nil {
		job.Payload[jobs.ManagedBackupAdmissionField] = admission
		job.Result[jobs.ManagedBackupAdmissionField] = admission
	}
	jobs.CopyEdgeFlagsFromContext(ctx, job.Payload)
	jobs.CaptureRequestAuthority(ctx, job, stack.tenantID, stack.ownerID)
	if jobs.IsApplicationLifecycleOperation(normalized.Operation) || jobs.IsApplicationConnectorLifecycleOperation(normalized.Operation) {
		jobs.CaptureRequestOperateAuthority(ctx, job, stack.tenantID, stack.ownerID)
	}
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

type RestoreActivationRequestConflict struct{ JobID, StackID string }

func (e *RestoreActivationRequestConflict) Error() string {
	return "Idempotency-Key belongs to another restore activation request"
}
func (e *RestoreActivationRequestConflict) Unwrap() error { return controlplane.ErrConflict }

// Replay reads an exact accepted intent before mutable runtime qualification.
// Only a missing local queue for a still-pending durable row may rehydrate it;
// running/terminal rows are returned without another dispatch or plan.
func (o *Orchestrator) ReplayRestoreActivationRequest(ctx context.Context, request jobs.StackKitLifecycleRequest) (string, bool, error) {
	if strings.ToLower(strings.TrimSpace(request.Operation)) != jobs.StackKitLifecycleBackupRestoreActivate || request.RestoreIdempotencyKeySHA256 == "" {
		return "", false, nil
	}
	if o == nil || o.jobStore == nil || request.DurableJobID != jobs.RestoreActivationJobIDForKeyHash(request.TenantID, request.OwnerID, request.RestoreIdempotencyKeySHA256) {
		return "", false, ErrStackKitLifecycleUnavailable
	}
	stack, err := o.findStackForJob(ctx, request.StackID, ProvisionStackOptions{RequestContext: ctx, TenantID: request.TenantID, OwnerID: request.OwnerID})
	if err != nil {
		return "", false, err
	}
	if stack.tenantID != request.TenantID || stack.ownerID != request.OwnerID {
		return "", false, controlplane.ErrNotFound
	}
	existing, err := o.jobStore.GetJob(ctx, request.TenantID, request.DurableJobID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if existing.StackID != request.StackID || existing.Type != string(jobs.JobTypeStackKitLifecycle) || !jobs.MatchesRestoreActivationRequestReceipt(existing.Result, request) {
		conflict := &RestoreActivationRequestConflict{}
		if owned, ownedErr := o.findStackForJob(ctx, existing.StackID, ProvisionStackOptions{RequestContext: ctx, TenantID: request.TenantID, OwnerID: request.OwnerID}); ownedErr == nil && owned.ownerID == request.OwnerID {
			conflict.JobID = existing.ID
			conflict.StackID = existing.StackID
		}
		return "", false, conflict
	}
	if strings.EqualFold(existing.State, persistentStatePending) {
		if _, queued := o.queue.Get(existing.ID); !queued {
			if len(existing.Payload) == 0 {
				return "", false, fmt.Errorf("%w: accepted restore payload is unavailable; retain original job %s", ErrStackKitLifecycleUnavailable, existing.ID)
			}
			job := &jobs.Job{ID: existing.ID, Type: jobs.JobTypeStackKitLifecycle, TargetType: targetTypeStack, TargetID: existing.StackID, TargetName: stack.name, Payload: existing.Payload, Result: existing.Result, MaxAttempts: 1}
			jobs.CaptureRequestAuthority(ctx, job, request.TenantID, request.OwnerID)
			if err := o.enqueueWithSync(job, request.TenantID); err != nil {
				return "", false, err
			}
		}
	}
	return existing.ID, true, nil
}

// A fresh recovery job inherits the original admitted release and node from
// the existing durable activation job, not from caller-supplied pin metadata.
func (o *Orchestrator) originalRestoreActivationAdmission(ctx context.Context, req jobs.StackKitLifecycleRequest) (jobs.StackKitLifecycleRequest, map[string]interface{}, error) {
	if o.jobStore == nil || !strings.HasPrefix(req.ActivationOperationID, "restore-") {
		return req, nil, fmt.Errorf("%w: original durable restore activation is required", ErrStackKitLifecycleUnavailable)
	}
	originalID := strings.TrimPrefix(req.ActivationOperationID, "restore-")
	store, supported := o.jobStore.(controlplane.OriginalRestoreActivationStore)
	if !supported {
		return req, nil, fmt.Errorf("%w: exact original restore directory authority is unavailable", ErrStackKitLifecycleUnavailable)
	}
	original, err := store.GetOriginalRestoreActivation(ctx, req.TenantID, req.StackID, originalID)
	if err != nil || original == nil {
		return req, nil, fmt.Errorf("%w: original restore admission and directory are unavailable", ErrStackKitLifecycleUnavailable)
	}
	admission := original.Admission
	for key, expected := range map[string]string{
		"tenant_id": req.TenantID, "owner_id": req.OwnerID, "stack_id": req.StackID, "stackkit_instance_id": req.StackKitInstanceID,
		"agent_id": req.AgentID, "node_id": req.NodeID, "node_ref": req.NodeRef, "spec_path": req.SpecPath,
		"restore_result_id": req.RestoreResultID, "activation_operation_id": req.ActivationOperationID, "plan_hash": req.RestorePlanHash,
	} {
		if stringFromAny(admission[key]) != expected {
			return req, nil, fmt.Errorf("%w: original restore admission differs from current owner, node or selected result", ErrStackKitLifecycleUnavailable)
		}
	}
	bound := make(map[string]interface{}, len(admission))
	for key, value := range admission {
		bound[key] = value
	}
	// Legacy admissions are hydrated only in memory from this exact durable
	// original row. Current defaults never choose an original node journal.
	req.WorkingDirectory = original.WorkingDirectory
	bound["working_directory"] = original.WorkingDirectory
	return req, bound, nil
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
	if !jobs.IsApplicationLifecycleOperation(request.Operation) && !jobs.IsApplicationConnectorLifecycleOperation(request.Operation) && !jobs.IsStackKitServiceLifecycleOperation(request.Operation) && request.Operation != jobs.StackKitLifecycleRemove &&
		!jobs.IsStackKitBackupOperation(request.Operation) && !(request.Operation == jobs.StackKitLifecycleGenerate && request.ManagedRuntimeLeaseID != "") {
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
	reservation := controlplane.UpsertJobRequest{
		ID: request.DurableJobID, TenantID: stack.tenantID, StackID: stack.id,
		Type: string(jobs.JobTypeStackKitLifecycle), State: persistentStatePending,
		Step:    "Queued typed StackKits " + request.Operation,
		Message: "Queued typed StackKits " + request.Operation,
		Result:  map[string]any{"service_action_receipt": receipt},
	}
	if restoreReceipt := jobs.RestoreActivationRequestReceipt(request); restoreReceipt != nil {
		reservation.Result[controlplane.RestoreActivationRequestResultKey] = restoreReceipt
		prepared := &jobs.Job{ID: request.DurableJobID, Type: jobs.JobTypeStackKitLifecycle, TargetID: stack.id, Payload: jobs.StackKitLifecyclePayload(request)}
		jobs.CopyEdgeFlagsFromContext(ctx, prepared.Payload)
		jobs.CaptureRequestAuthority(ctx, prepared, stack.tenantID, stack.ownerID)
		reservation.Payload = prepared.Payload
	}
	if receipt := jobs.RILStackKitOperationReceipt(request); receipt != nil {
		reservation.Result[jobs.RILOperationReceiptKey] = receipt
		reservation.Payload = jobs.StackKitLifecyclePayload(request)
	}
	var created *controlplane.Job
	var err error
	if request.ServiceID != "" {
		serviceJobs, ok := o.jobStore.(controlplane.ServiceOperationJobStore)
		if !ok {
			return "", false, ErrStackKitLifecycleUnavailable
		}
		created, err = serviceJobs.CreateServiceOperationJob(ctx, reservation, request.ServiceID)
	} else {
		created, err = o.jobStore.CreateJob(ctx, reservation)
	}
	if errors.Is(err, controlplane.ErrServiceOperationBusy) {
		return "", false, fmt.Errorf("%w: %w", controlplane.ErrConflict, err)
	}

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
	matches := jobs.MatchesStackKitServiceActionReceipt(existing.Result, request)
	if request.Operation == jobs.StackKitLifecycleBackupRestoreActivate {
		matches = jobs.MatchesRestoreActivationRequestReceipt(existing.Result, request)
	}
	if request.RILOperation != nil {
		matches = jobs.MatchesRILStackKitOperationReceipt(existing.Result, request)
	}
	if existing.StackID != stack.id || !strings.EqualFold(existing.Type, string(jobs.JobTypeStackKitLifecycle)) || !matches {
		return "", false, fmt.Errorf("%w: service action idempotency key belongs to another request", controlplane.ErrConflict)
	}
	if request.Operation == jobs.StackKitLifecycleBackupRestoreActivate {
		id, replayed, replayErr := o.ReplayRestoreActivationRequest(ctx, request)
		return id, replayed, replayErr
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

// resolveStackKitNodeAgent returns the approved agent that owns one node of a
// stack. Core binds each agent to the node its host name spells, so a node with
// no agent, or with two, is refused instead of guessed.
func (o *Orchestrator) resolveStackKitNodeAgent(ctx context.Context, tenantID, ownerID, stackID, nodeRef string) (string, error) {
	bindings, err := o.approvedStackKitLifecycleAgentBindings(ctx, &orchestratorStack{id: stackID, tenantID: tenantID, ownerID: ownerID})
	if err != nil {
		return "", err
	}
	agentID := ""
	for _, binding := range bindings {
		if !strings.EqualFold(binding.NodeRef, strings.TrimSpace(nodeRef)) || binding.AgentID == "" {
			continue
		}
		if agentID != "" {
			return "", fmt.Errorf("node %q of stack %s has more than one approved agent; refusing to guess", nodeRef, stackID)
		}
		agentID = binding.AgentID
	}
	return agentID, nil
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

// persistStackKitRolloutBinding copies a completed rollout or typed Apply binding into the
// stack config. The runtime summary is replaced by every later job result, so
// it cannot be the authority later operator operations read their StackSpec
// path and kit from.
func (o *Orchestrator) persistStackKitRolloutBinding(tenantID string, job jobs.JobSnapshot) {
	if job.State != jobs.JobStateCompleted {
		return
	}
	binding, ok := jobs.DecodeStackKitRolloutBinding(job.Result[jobs.StackKitRolloutBindingResultField])
	if job.Type != jobs.JobTypeDeploy {
		if job.Type != jobs.JobTypeStackKitLifecycle ||
			stringFromAny(job.Payload["operation"]) != jobs.StackKitLifecycleApply ||
			stringFromAny(job.Result["operation"]) != jobs.StackKitLifecycleApply ||
			job.Result["success"] != true ||
			binding.SpecPath != stringFromAny(job.Payload["spec_path"]) ||
			binding.StackKit != stringFromAny(job.Payload["stackkit"]) {
			return
		}
	}
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

// AuthorizeApplicationConnectorRead reuses dispatch admission for a read
// projection without granting mutation or reserving a job. It resolves current
// placement from the canonical stack and approved worker, never public metadata.
func (o *Orchestrator) AuthorizeApplicationConnectorRead(ctx context.Context, req jobs.StackKitLifecycleRequest) error {
	if o == nil || req.Connector == nil {
		return ErrStackKitLifecycleUnavailable
	}
	stack, err := o.findStackForJob(ctx, req.StackID, ProvisionStackOptions{RequestContext: ctx, TenantID: req.TenantID, OwnerID: req.OwnerID})
	if err != nil {
		return err
	}
	rollout := stackKitRolloutBinding(stack)
	req.StackKit, req.SpecPath = rollout.StackKit, rollout.SpecPath
	bindings, err := o.approvedStackKitLifecycleAgentBindings(ctx, stack)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.WorkerID == req.AgentID && binding.ServerID == req.Connector.ServerID {
			req.AgentID = binding.AgentID
			return o.authorizeApplicationConnector(ctx, req)
		}
	}
	return ErrStackKitLifecycleUnavailable
}

// Rejoin canonical backend authority before dispatch, read reconciliation and
// projection. Stored grants and caller-selected servers cannot substitute it.
func (o *Orchestrator) authorizeApplicationConnector(ctx context.Context, req jobs.StackKitLifecycleRequest) error {
	if req.Connector == nil || o.workerStore == nil {
		return ErrStackKitLifecycleUnavailable
	}
	b := req.Connector
	if b.OriginalOperationID != "" {
		o.mu.RLock()
		authorize := o.connectorReadAuthorize
		o.mu.RUnlock()
		if authorize == nil {
			return ErrStackKitLifecycleUnavailable
		}
		if err := authorize(ctx, req.TenantID, req.OwnerID, b.ServerID); err != nil {
			return err
		}
	}
	if err := o.authorizeDeclaredServiceRuntime(ctx, req, b.ServerID); err != nil {
		return err
	}
	stack, err := o.findStackForJob(ctx, req.StackID, ProvisionStackOptions{RequestContext: ctx, TenantID: req.TenantID, OwnerID: req.OwnerID})
	if err != nil {
		return err
	}
	rollout := stackKitRolloutBinding(stack)
	if rollout.SpecPath != req.SpecPath || rollout.StackKit != req.StackKit {
		return ErrStackKitLifecycleUnavailable
	}
	binding, err := o.admitStackKitLifecycleAgentOnServer(ctx, stack, req.AgentID, b.ServerID)
	if err != nil {
		return err
	}
	runtime, err := o.canonicalServerRuntimeStore().GetServerRuntime(ctx, req.TenantID, b.ServerID)
	if err != nil || runtime == nil || runtime.Generation != b.ServerGeneration || runtime.NodeID != b.NodeID || runtime.NodeID != req.NodeID || runtime.WorkerID != binding.WorkerID || runtime.LeaseID != b.LeaseID || runtime.ProviderRef != b.ProviderRef || !serverRuntimeAllowsStackDeploy(stack, *runtime, time.Now().UTC()) {
		return ErrStackKitLifecycleUnavailable
	}
	worker, err := o.workerStore.GetWorker(ctx, req.TenantID, binding.WorkerID)
	if err != nil || worker == nil {
		return ErrStackKitLifecycleUnavailable
	}
	credentials, err := controlplane.WorkerCredentialStateFromWorker(*worker)
	if err != nil || credentials.Generation <= 0 || credentials.Generation != b.WorkerGeneration {
		return ErrStackKitLifecycleUnavailable
	}
	// The generation alone survives key withdrawal. This capability requires
	// the existing authenticated host-key channel and its current bound key;
	// legacy bearer observations do not grant connector execution authority.
	identities, ok := o.workerStore.(controlplane.WorkerIdentityStore)
	if !ok || worker.Capabilities["identity_binding"] != controlplane.IdentityBindingHostKey || runtime.Metadata["identity_binding"] != controlplane.IdentityBindingHostKey {
		return ErrStackKitLifecycleUnavailable
	}
	identity, err := identities.GetWorkerIdentity(ctx, req.TenantID, binding.WorkerID)
	if err != nil || identity == nil || identity.State != controlplane.WorkerIdentityBound || !identity.AcceptsThumbprint(identity.Thumbprint, time.Now().UTC()) {
		return ErrStackKitLifecycleUnavailable
	}
	if runtime.LeaseID != "" {
		lease, err := o.exactManagedRuntimeLeaseForStack(ctx, stack, runtime.LeaseID)
		if err != nil || lease == nil {
			return ErrManagedRuntimeLeaseNotNativeActive
		}
		if _, ok := exactManagedRuntimeDeployEligibleRuntime(stack, runtime.LeaseID, runtime.ID, []controlplane.ServerRuntime{*runtime}, time.Now().UTC()); !ok {
			return ErrManagedRuntimeLeaseNotNativeActive
		}
	}
	return nil
}
