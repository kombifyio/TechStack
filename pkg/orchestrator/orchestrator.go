// Package orchestrator provides the central orchestration layer for kombifyTechstack.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/nodehandoff"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/stackrouting"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

// ErrNoAssignedWorkers is retained as the public compatibility sentinel for a
// deploy without any current canonical Guard runtime target. Approval or a
// managed lease alone never satisfies this rollout gate.
var ErrNoAssignedWorkers = errors.New("no connected Guard runtime assigned to stack")

// ErrDeployRuntimeEvidenceUnavailable distinguishes a missing or failing
// canonical runtime store from a healthy store that proves no connected target.
var ErrDeployRuntimeEvidenceUnavailable = errors.New("canonical deploy runtime evidence unavailable")

// ErrManagedLeaseReconciliationDurabilityUnavailable keeps forced
// decommission fail-closed until reconcile_lease jobs have restart-safe,
// HA-fenced durable custody. Persisting an in-process queue projection is not
// enough because the current jobs table cannot safely reclaim a running
// provider side effect after process loss.
var ErrManagedLeaseReconciliationDurabilityUnavailable = errors.New("managed lease reconciliation durable custody unavailable")

// ErrManagedRuntimeLeaseNotNativeActive keeps rollout fail-closed when a
// stack still references historical, unbound, expired, or otherwise inactive
// lease inventory. Only an explicitly bound TechStack provider-control lease
// may become a rollout target.
var ErrManagedRuntimeLeaseNotNativeActive = errors.New("managed runtime lease is not native-active")

const (
	persistentJobTypeProvision  = "provision"
	persistentJobTypeDeploy     = "deploy"
	persistentJobTypeDestroy    = "destroy"
	persistentStatePending      = "pending"
	persistentStateRunning      = "running"
	persistentStateProvisioning = "provisioning"
	persistentStateError        = "error"
	stackOwnerIDField           = "owner_id"
	stackTenantIDField          = "tenant_id"
	runtimeFieldLane            = "runtime_lane"
	runtimeFieldConnectionMode  = "server_connection_mode"
	runtimeFieldInstallCommand  = "server_install_command_required"
	runtimeFieldIONOSDatacenter = "ionos_datacenter"
	runtimeFieldLeaseID         = "lease_id"
	runtimeFieldLeaseProvider   = "lease_provider"
	runtimeFieldOfferingID      = "runtime_offering_id"
	runtimeFieldProviderRegion  = "provider_region"
	runtimeFieldProvisionMode   = "server_provisioning_mode"
	runtimeFieldSimProviderID   = "simulate_provider_id"
	runtimeFieldNodeLifecycle   = "simulate_node_lifecycle"
	runtimeFieldBillingCadence  = "billing_cadence"
	runtimeFieldDesiredState    = "desired_state"
	runtimeFieldVerification    = "verification_status"
	runtimeFieldPrivateIP       = "runtime_private_ip"
	runtimeFieldSSHHost         = "runtime_ssh_host"
	runtimeFieldSSHUser         = "runtime_ssh_user"
	runtimeFieldSSHPort         = "runtime_ssh_port"
	runtimeFieldStackKitRef     = "stackkit_catalog_ref"
	runtimeLaneMonthly          = "monthly-runtime"
	runtimeConnectionManaged    = "managed-subscription"
	runtimeProvisionKombify     = "kombify-cloud"
	runtimeRoleFoundation       = "foundation"
	runtimeRoleMain             = "main"
	stackModeEasy               = "easy"
	targetTypeStack             = "stack"
)

// Orchestrator coordinates job execution with canonical control-plane state.
type Orchestrator struct {
	queue                    *jobs.Queue
	log                      *logger.Logger
	cfg                      Config
	stackStore               controlplane.StackStore
	jobStore                 controlplane.JobStore
	workerStore              controlplane.WorkerStore
	walletStore              controlplane.WalletStore
	driftStore               controlplane.DriftResultStore
	activityStore            controlplane.ActivityStore
	registry                 controlplane.RegistryStore
	leaseLister              ManagedRuntimeLeaseLister
	routingStore             stackrouting.Store
	stackKitCommander        jobs.StackKitCommandSender
	managedStackKitInventory jobs.ManagedStackKitInventoryBuilder
	terminalJobObservers     []TerminalJobObserver
	now                      func() time.Time
	// mu guards configuration mutation. Lifecycle dispatch takes the read
	// side for its duration so unrelated stacks dispatch concurrently while a
	// late Configure* call still waits for every in-flight dispatch.
	mu              sync.RWMutex
	stackLocks      stackLockTable
	durableResumeMu sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup // Track active goroutines for graceful shutdown
}

// stackLockTable serializes lifecycle dispatch per stack without serializing
// unrelated stacks. The durable per-stack execution claim in the job store
// remains the cross-replica barrier; this table only keeps same-process
// dispatches from racing into that claim.
type stackLockTable struct {
	mu    sync.Mutex
	locks map[string]*stackLockEntry
}

type stackLockEntry struct {
	mu   sync.Mutex
	refs int
}

// lock acquires the per-stack lock and returns its release function. An empty
// key falls back to a shared lock so a malformed request cannot bypass
// serialization.
func (t *stackLockTable) lock(key string) func() {
	key = strings.TrimSpace(key)
	t.mu.Lock()
	if t.locks == nil {
		t.locks = map[string]*stackLockEntry{}
	}
	entry, ok := t.locks[key]
	if !ok {
		entry = &stackLockEntry{}
		t.locks[key] = entry
	}
	entry.refs++
	t.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		t.mu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(t.locks, key)
		}
		t.mu.Unlock()
	}
}

// Config holds orchestrator configuration.
type Config struct {
	Workers        int
	WorkDir        string
	RuntimeActions jobs.RuntimeActions
	// BackupScheduleProjector records the cadence a managed rollout selected.
	// Without it the schedule projection stays empty and no backup is ever due.
	BackupScheduleProjector  jobs.BackupScheduleProjector
	StackStore               controlplane.StackStore
	JobStore                 controlplane.JobStore
	WorkerStore              controlplane.WorkerStore
	WalletStore              controlplane.WalletStore
	DriftStore               controlplane.DriftResultStore
	ActivityStore            controlplane.ActivityStore
	RegistryStore            controlplane.RegistryStore
	LeaseLister              ManagedRuntimeLeaseLister
	RoutingStore             stackrouting.Store
	StackKitCommander        jobs.StackKitCommandSender
	ManagedStackKitInventory jobs.ManagedStackKitInventoryBuilder
	PortInventory            portinventory.LifecycleAuthority
	// RemoteEnrollment drives the durable connect-remote enrollment job. It is
	// late-bound by ConfigureRemoteEnrollment once route custody is wired.
	RemoteEnrollment jobs.RemoteEnrollmentExecutor
	// Now is an optional clock used only by durable recovery admission. Queue
	// execution retains wall-clock timestamps; injecting this clock lets the
	// stale-heartbeat fence be deterministically verified without sleeping.
	Now func() time.Time
}

type ManagedRuntimeLeaseLister interface {
	ListInventoryByTenant(ctx context.Context, tenantID string) ([]vmleases.LeaseInventoryRecord, error)
}

type ProvisionStackOptions struct {
	AutoDeploy            bool
	OwnerSpecBootstrap    *jobs.OwnerSpecBootstrap
	IdempotencyKey        string
	RequestContext        context.Context
	TenantID              string
	OwnerID               string
	StackName             string
	PreparedManagedLease  *jobs.ManagedLeaseRequest
	CustomerGuest         *jobs.CustomerGuestBinding
	RequiredLeaseID       string
	RequiredServerID      string
	RoutingRevision       int64
	RoutingIdempotencyKey string
	enrollmentResumeJobID string
	enrollmentResumeAt    string
	enrollmentResumeKind  string
	enrollmentResumeKey   string
	enrollmentResumeNewID string
	rolloutRetryJobID     string
	rolloutRetryKey       string
	rolloutRetryNewID     string
}

type orchestratorStack struct {
	id                 string
	stackKitInstanceID string
	name               string
	status             string
	driftStatus        string
	ownerID            string
	tenantID           string
	config             map[string]any
	runtimeSummary     map[string]any
}

func (o *Orchestrator) effectiveStackStore() controlplane.StackStore {
	if o.stackStore != nil {
		return o.stackStore
	}
	if store, ok := o.jobStore.(controlplane.StackStore); ok {
		return store
	}
	return nil
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() *Config {
	return &Config{
		Workers: workerCountFromEnvironment(),
		WorkDir: "data/provision",
	}
}

// workerCountFromEnvironment keeps the historical four workers as the default
// and lets a deployment size its in-process job pool without a code change.
// The bound stays finite: an unbounded pool would multiply provider and
// control-plane pressure per replica.
func workerCountFromEnvironment() int {
	const (
		defaultWorkers = 4
		maxWorkers     = 64
	)
	raw := strings.TrimSpace(os.Getenv("TECHSTACK_JOB_WORKERS"))
	if raw == "" {
		return defaultWorkers
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 || parsed > maxWorkers {
		return defaultWorkers
	}
	return parsed
}

// New creates an Orchestrator backed by the canonical stores in Config.
func New(cfg *Config, log *logger.Logger) *Orchestrator {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if log == nil {
		log = logger.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())

	o := &Orchestrator{
		queue:                    jobs.NewQueue(cfg.Workers, log),
		log:                      log.WithComponent("orchestrator"),
		cfg:                      *cfg,
		stackStore:               cfg.StackStore,
		jobStore:                 cfg.JobStore,
		workerStore:              cfg.WorkerStore,
		walletStore:              cfg.WalletStore,
		driftStore:               cfg.DriftStore,
		activityStore:            cfg.ActivityStore,
		registry:                 cfg.RegistryStore,
		leaseLister:              cfg.LeaseLister,
		routingStore:             cfg.RoutingStore,
		stackKitCommander:        cfg.StackKitCommander,
		managedStackKitInventory: cfg.ManagedStackKitInventory,
		now:                      cfg.Now,
		ctx:                      ctx,
		cancel:                   cancel,
	}
	if o.now == nil {
		o.now = func() time.Time { return time.Now().UTC() }
	}
	if o.jobStore != nil {
		o.queue.SetExecutionClaimer(o.claimDurableJobExecution)
		o.queue.SetExecutionSnapshotSyncer(o.syncDurableJobExecutionSnapshot)
	}

	// Register default handlers.
	jobs.RegisterDefaultHandlers(o.queue, o.provisionConfig(cfg.RuntimeActions))

	return o
}

// provisionConfig keeps every handler registration on the same control-plane
// reconciliation boundary. In particular, an explicit destroy that finds no
// local workspace must not leave its independently stored stack card behind.
func (o *Orchestrator) provisionConfig(actions jobs.RuntimeActions) *jobs.ProvisionConfig {
	if o == nil {
		return &jobs.ProvisionConfig{RuntimeActions: actions}
	}
	return &jobs.ProvisionConfig{
		WorkDir:                      o.cfg.WorkDir,
		RuntimeActions:               actions,
		StackKitCommander:            o.stackKitCommander,
		ManagedStackKitInventory:     o.managedStackKitInventory,
		PortInventory:                o.cfg.PortInventory,
		RoutingStore:                 o.routingStore,
		AutoDeployAdmission:          o.admitProvisionAutoDeploy,
		BackupScheduleProjector:      o.cfg.BackupScheduleProjector,
		BackupAgentResolver:          o.resolveBackupAgent,
		NoWorkspaceDestroyReconciler: o.reconcileNoWorkspaceDestroy,
		RemoteEnrollment:             o.cfg.RemoteEnrollment,
	}
}

func (o *Orchestrator) claimDurableJobExecution(ctx context.Context, claim jobs.ExecutionClaim) error {
	if o.jobStore == nil || strings.TrimSpace(claim.TenantID) == "" {
		return nil
	}
	if _, err := o.jobStore.StartJob(ctx, claim.TenantID, claim.JobID, claim.StartedAt); err != nil {
		if errors.Is(err, controlplane.ErrStackExecutionBusy) {
			return fmt.Errorf("%w: %s", jobs.ErrExecutionTargetBusy, claim.TargetID)
		}
		if errors.Is(err, controlplane.ErrConflict) || errors.Is(err, controlplane.ErrNotFound) {
			return fmt.Errorf("%w: %s", jobs.ErrExecutionClaimFenced, claim.JobID)
		}
		return fmt.Errorf("claim durable job execution: %w", err)
	}
	return nil
}

// ConfigureRuntimeActions rewires job handlers with runtime integrations that
// become available after route/bootstrap setup, such as the VM Lease Authority.
func (o *Orchestrator) ConfigureRuntimeActions(actions jobs.RuntimeActions) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.cfg.RuntimeActions = actions
	jobs.RegisterDefaultHandlers(o.queue, o.provisionConfig(actions))
}

// ConfigureRemoteEnrollment late-binds the connect-remote enrollment executor
// once route custody (wallet, pairing store, origin) is wired, and re-registers
// the default handlers so the durable remote_enrollment type becomes runnable.
func (o *Orchestrator) ConfigureRemoteEnrollment(executor jobs.RemoteEnrollmentExecutor) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.cfg.RemoteEnrollment = executor
	jobs.RegisterDefaultHandlers(o.queue, o.provisionConfig(o.cfg.RuntimeActions))
}

// EnqueuePreparedJob admits an already-persisted pending job into this
// process's queue. The durable row must exist in pending state; the queue's
// execution claim stays the single authority for running it.
func (o *Orchestrator) EnqueuePreparedJob(job *jobs.Job, tenantID string) error {
	if o == nil || o.queue == nil || job == nil {
		return fmt.Errorf("orchestrator is not configured for prepared jobs")
	}
	unlockStack := o.stackLocks.lock(job.TargetID)
	defer unlockStack()
	return o.enqueueWithSync(job, tenantID)
}

// admitProvisionAutoDeploy is the read-only control-plane fence for the one
// remaining provision-to-deploy handoff. It reuses the same native lease and
// canonical Guard predicates as explicit and recovery deploys.
func (o *Orchestrator) admitProvisionAutoDeploy(ctx context.Context, req jobs.AutoDeployAdmissionRequest) error {
	stackID := strings.TrimSpace(req.StackID)
	tenantID := strings.TrimSpace(req.TenantID)
	ownerID := strings.TrimSpace(req.OwnerID)
	leaseID := strings.TrimSpace(req.LeaseID)
	if stackID == "" || tenantID == "" || ownerID == "" || leaseID == "" {
		return fmt.Errorf("%w: exact stack, tenant, owner, and lease are required", ErrDeployRuntimeEvidenceUnavailable)
	}
	stack, err := o.findControlPlaneStackForJob(ctx, stackID, ProvisionStackOptions{
		TenantID:         tenantID,
		OwnerID:          ownerID,
		RequiredLeaseID:  leaseID,
		RequiredServerID: runtimeidentity.LeaseServerID(leaseID),
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDeployRuntimeEvidenceUnavailable, err)
	}
	if stack.tenantID != tenantID || stack.ownerID != ownerID {
		return fmt.Errorf("%w: auto-deploy identity binding mismatch", ErrDeployRuntimeEvidenceUnavailable)
	}
	lease, err := o.exactManagedRuntimeLeaseForStack(ctx, stack, leaseID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrManagedRuntimeLeaseNotNativeActive, err)
	}
	if lease == nil {
		return ErrManagedRuntimeLeaseNotNativeActive
	}
	serverID := runtimeidentity.LeaseServerID(leaseID)
	if serverID == "" {
		return fmt.Errorf("%w: canonical lease server id is unavailable", ErrDeployRuntimeEvidenceUnavailable)
	}
	return o.requireExactManagedRuntimeDeploy(ctx, stack, leaseID, serverID)
}

func (o *Orchestrator) ConfigureManagedRuntimeLeases(lister ManagedRuntimeLeaseLister) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.cfg.LeaseLister = lister
	o.leaseLister = lister
}

// Start begins job processing.
func (o *Orchestrator) Start() {
	o.log.Info("starting_orchestrator")
	o.queue.Start(o.ctx)
}

// Stop gracefully stops job processing.
func (o *Orchestrator) Stop() {
	o.log.Info("stopping_orchestrator")
	o.queue.Stop()

	syncDone := make(chan struct{})
	go func() {
		o.wg.Wait()
		close(syncDone)
	}()
	select {
	case <-syncDone:
	case <-time.After(5 * time.Second):
		o.log.Warn("job_sync_shutdown_deadline_reached")
	}
	o.cancel()
	<-syncDone
}

// Queue returns the underlying job queue for direct access.
func (o *Orchestrator) Queue() *jobs.Queue {
	return o.queue
}

func (o *Orchestrator) findStackForJob(ctx context.Context, stackID string, opts ProvisionStackOptions) (*orchestratorStack, error) {
	stack, err := o.findControlPlaneStackForJob(ctx, stackID, opts)
	if err != nil {
		return nil, err
	}
	if stack.tenantID != strings.TrimSpace(opts.TenantID) || stack.ownerID != strings.TrimSpace(opts.OwnerID) {
		return nil, controlplane.ErrNotFound
	}
	return stack, nil
}

func (o *Orchestrator) findControlPlaneStackForJob(ctx context.Context, stackID string, opts ProvisionStackOptions) (*orchestratorStack, error) {
	tenantID := strings.TrimSpace(opts.TenantID)
	stackStore := o.effectiveStackStore()
	if tenantID == "" || stackStore == nil {
		return nil, fmt.Errorf("stack not found")
	}
	stack, err := stackStore.GetStack(ctx, tenantID, stackID)
	if err != nil {
		return nil, fmt.Errorf("stack not found: %w", err)
	}
	return controlPlaneOrchestratorStack(stack, opts), nil
}

func (o *Orchestrator) findArchivedControlPlaneStackForRecovery(ctx context.Context, stackID string, opts ProvisionStackOptions) (*orchestratorStack, error) {
	tenantID := strings.TrimSpace(opts.TenantID)
	reader, ok := o.effectiveStackStore().(controlplane.ArchivedStackReceiptReader)
	if tenantID == "" || !ok {
		return nil, fmt.Errorf("stack recovery receipt not found")
	}
	stack, err := reader.GetStackIncludingDeleted(ctx, tenantID, stackID)
	if err != nil {
		return nil, fmt.Errorf("stack recovery receipt not found: %w", err)
	}
	return controlPlaneOrchestratorStack(stack, opts), nil
}

func controlPlaneOrchestratorStack(stack *controlplane.Stack, opts ProvisionStackOptions) *orchestratorStack {
	return &orchestratorStack{
		id:                 stack.ID,
		stackKitInstanceID: stack.StackKitInstanceID,
		name:               firstNonEmptyString(opts.StackName, stack.Name),
		status:             stack.Status,
		driftStatus:        stack.DriftStatus,
		ownerID:            stack.OwnerSubjectID,
		tenantID:           stack.TenantID,
		config:             stack.Config,
		runtimeSummary:     stack.RuntimeSummary,
	}
}

func (o *Orchestrator) createJobRecordForStack(ctx context.Context, stack *orchestratorStack, jobType, currentStep string) (string, error) {
	if o.jobStore == nil || stack == nil || strings.TrimSpace(stack.tenantID) == "" {
		return "", fmt.Errorf("durable job store is required")
	}
	jobID := jobs.NewID()
	if _, err := o.jobStore.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID:       jobID,
		TenantID: stack.tenantID,
		StackID:  stack.id,
		Type:     jobType,
		State:    persistentStatePending,
		Progress: 0,
		Step:     currentStep,
		Message:  currentStep,
	}); err != nil {
		return "", fmt.Errorf("failed to create control-plane job record: %w", err)
	}
	return jobID, nil
}

// persistDestroyAdmissionReceipt writes every server-generated destroy
// authority before the job can enter the process-local queue. A later queue
// snapshot may change job_wait, but the exact provider-recovery marker and
// port-release batch remain durable result evidence rather than client input.
func (o *Orchestrator) persistDestroyAdmissionReceipt(
	ctx context.Context,
	stack *orchestratorStack,
	jobID string,
	result map[string]any,
) error {
	if o == nil || o.jobStore == nil || stack == nil {
		return fmt.Errorf("destroy admission receipt requires a durable job store")
	}
	if strings.TrimSpace(stack.tenantID) == "" || strings.TrimSpace(stack.id) == "" || strings.TrimSpace(jobID) == "" {
		return fmt.Errorf("destroy admission receipt requires exact tenant, stack, and job identity")
	}
	_, err := o.jobStore.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID:           jobID,
		TenantID:     stack.tenantID,
		StackID:      stack.id,
		Type:         persistentJobTypeDestroy,
		State:        persistentStatePending,
		Progress:     0,
		Step:         "Queued for destruction",
		Message:      "Queued for destruction",
		Result:       result,
		ScheduledFor: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("persist destroy admission receipt: %w", err)
	}
	return nil
}

// persistDeployDispatchReceipt makes an exact-target dispatch durable before
// the process-local queue can execute it.
func (o *Orchestrator) persistDeployDispatchReceipt(ctx context.Context, stack *orchestratorStack, jobID string, result map[string]any, opts ProvisionStackOptions) error {
	if len(result) == 0 {
		return nil
	}
	dispatchName, unavailable := deployDispatchAuthority(opts)
	if !deployDispatchReceiptReady(o, stack, jobID) {
		return fmt.Errorf("%w: durable job store is required for %s dispatch receipts", unavailable, dispatchName)
	}
	request := controlplane.UpsertJobRequest{
		ID:       jobID,
		TenantID: stack.tenantID,
		StackID:  stack.id,
		Type:     persistentJobTypeDeploy,
		State:    persistentStatePending,
		Progress: 0,
		Step:     "Queued for deployment",
		Message:  "Queued for deployment",
		Result:   result,
	}
	if isDeterministicExactRecovery(opts) {
		return o.persistDeterministicDispatchReceipt(ctx, stack, request, result, opts, unavailable, dispatchName)
	}
	if _, err := o.jobStore.UpsertJob(ctx, request); err != nil {
		return fmt.Errorf("%w: persist %s dispatch receipt: %v", unavailable, dispatchName, err)
	}
	return nil
}

func deployDispatchAuthority(opts ProvisionStackOptions) (string, error) {
	if isEnrollmentResume(opts) {
		return "enrollment resume", ErrEnrollmentResumeUnavailable
	}
	if isRolloutRetry(opts) {
		return "exact rollout retry", ErrRolloutRetryUnavailable
	}
	return "routing", stackrouting.ErrUnavailable
}

func deployDispatchReceiptReady(o *Orchestrator, stack *orchestratorStack, jobID string) bool {
	return o != nil && o.jobStore != nil && stack != nil && strings.TrimSpace(stack.tenantID) != "" && strings.TrimSpace(jobID) != ""
}

func (o *Orchestrator) persistDeterministicDispatchReceipt(
	ctx context.Context,
	stack *orchestratorStack,
	request controlplane.UpsertJobRequest,
	result map[string]any,
	opts ProvisionStackOptions,
	unavailable error,
	dispatchName string,
) error {
	if _, createErr := o.jobStore.CreateJob(ctx, request); createErr == nil {
		return nil
	} else if !errors.Is(createErr, controlplane.ErrConflict) {
		return fmt.Errorf("%w: persist %s dispatch receipt: %v", unavailable, dispatchName, createErr)
	}
	existing, getErr := o.jobStore.GetJob(ctx, stack.tenantID, request.ID)
	if getErr != nil {
		return fmt.Errorf("%w: load existing %s dispatch: %v", unavailable, dispatchName, getErr)
	}
	exact, exactErr := exactRecoveryDispatchReceipt(existing.Result, result, opts)
	if exactErr != nil || !exact || existing.StackID != stack.id ||
		strings.ToLower(strings.TrimSpace(existing.Type)) != string(jobs.JobTypeDeploy) {
		return fmt.Errorf("%w: deterministic %s job ID belongs to another dispatch", stackrouting.ErrIdempotencyConflict, dispatchName)
	}
	if canonicalEnrollmentJobState(existing.State) != persistentStatePending {
		return fmt.Errorf("%w: deterministic %s job is already %s", unavailable, dispatchName, existing.State)
	}
	return nil
}

func (o *Orchestrator) updateStackStatusForJob(ctx context.Context, stack *orchestratorStack, status string) {
	if err := o.persistStackStatusForJob(ctx, stack, status); err != nil {
		o.log.Error("failed_to_update_stack_status", "stack_id", stack.id, "error", err)
	}
}

func (o *Orchestrator) persistStackStatusForJob(ctx context.Context, stack *orchestratorStack, status string) error {
	if stack == nil || status == "" {
		return nil
	}
	store := o.effectiveStackStore()
	if store == nil || strings.TrimSpace(stack.tenantID) == "" {
		return fmt.Errorf("canonical stack store is required")
	}
	if _, err := store.UpdateStackRuntime(ctx, stack.tenantID, stack.id, controlplane.RuntimeUpdate{
		Status: status,
	}); err != nil {
		return fmt.Errorf("persist control-plane stack status: %w", err)
	}
	stack.status = status
	return nil
}

// ProvisionStackWithOptions creates and enqueues a provisioning job with flow
// controls for wizard-owned paths such as managed runtime auto-rollout.
func (o *Orchestrator) ProvisionStackWithOptions(stackID string, spec map[string]interface{}, opts ProvisionStackOptions) (string, error) {
	if err := requireStackLifecycleIdentity(opts); err != nil {
		return "", err
	}
	ctx := opts.RequestContext
	if ctx == nil {
		ctx = o.ctx
	}
	// The synchronous create boundary may already have admitted the managed
	// provider operation. Keep that sealed request outside the mutable StackKit
	// spec so spec canonicalization/persistence cannot reinterpret or discard it.
	preparedManagedLeaseRequest := spec[jobs.PreparedManagedLeaseRequestPayloadKey]
	if opts.PreparedManagedLease != nil {
		preparedManagedLeaseRequest = jobs.ManagedLeaseRequestPayload(*opts.PreparedManagedLease)
	}
	if preparedManagedLeaseRequest != nil {
		spec = cloneProvisionSpec(spec)
		delete(spec, jobs.PreparedManagedLeaseRequestPayloadKey)
	}

	unlockStack := o.stackLocks.lock(stackID)
	defer unlockStack()
	o.mu.RLock()
	defer o.mu.RUnlock()

	stack, stackErr := o.findStackForJob(ctx, stackID, opts)
	if stackErr != nil {
		return "", fmt.Errorf("stack not found: %w", stackErr)
	}

	canonicalSpec, providerErr := canonicalizeProvisionProviderIdentity(spec, stack.config)
	if providerErr != nil {
		return "", fmt.Errorf("invalid provider selection: %w", providerErr)
	}
	spec = canonicalSpec
	idempotency, idempotencyErr := newProvisionIdempotency(stack, spec, opts.IdempotencyKey)
	if opts.CustomerGuest != nil {
		if err := o.requireCustomerGuestLease(ctx, stack, *opts.CustomerGuest); err != nil {
			return "", err
		}
		idempotency, idempotencyErr = newStackLifecycleIdempotency(stack, map[string]any{"spec": spec, jobs.CustomerGuestResultKey: *opts.CustomerGuest}, opts.IdempotencyKey, provisionIdempotencySchema, "provision", persistentJobTypeProvision, jobs.ProvisionIdempotencyReceiptResultField, "Queued for provisioning", ErrProvisionIdempotencyConflict)
		if idempotency != nil {
			idempotency.initialResult = map[string]any{jobs.CustomerGuestResultKey: *opts.CustomerGuest, "operation_id": opts.CustomerGuest.OperationID, "lease_id": opts.CustomerGuest.LeaseID, "auto_deploy": true}
		}
	}
	if idempotencyErr != nil {
		return "", idempotencyErr
	}
	if replayID, replay, replayErr := o.findStackLifecycleIdempotencyReplay(ctx, stack, idempotency); replayErr != nil || replay {
		return replayID, replayErr
	}

	// Validate stack status
	status := stack.status
	if (status == persistentStateRunning || status == persistentStateProvisioning) && opts.CustomerGuest == nil {
		return "", fmt.Errorf("stack is already %s", status)
	}
	if opts.CustomerGuest != nil {
		if idempotency == nil {
			return "", fmt.Errorf("customer guest rollout requires durable idempotency")
		}
		if err := o.requireNoActiveCustomerGuestRollout(ctx, stack); err != nil {
			return "", err
		}
	}

	var jobID string
	var createErr error
	receipt := map[string]any(nil)
	if idempotency != nil {
		var replay bool
		jobID, replay, createErr = o.createStackLifecycleIdempotencyJob(ctx, stack, idempotency)
		if createErr == nil && replay {
			return jobID, nil
		}
		receipt = map[string]any{jobs.ProvisionIdempotencyReceiptResultField: idempotency.receipt}
	} else {
		jobID, createErr = o.createJobRecordForStack(ctx, stack, persistentJobTypeProvision, "Queued for provisioning")
	}
	if createErr != nil {
		return "", createErr
	}

	o.updateStackStatusForJob(ctx, stack, persistentStateProvisioning)

	// Create in-memory job
	job := &jobs.Job{
		ID:         jobID,
		Type:       jobs.JobTypeProvision,
		TargetType: targetTypeStack,
		TargetID:   stackID,
		TargetName: stack.name,
		Payload: map[string]interface{}{
			"spec":                 spec,
			stackOwnerIDField:      stack.ownerID,
			stackTenantIDField:     stack.tenantID,
			"auto_deploy":          opts.AutoDeploy,
			"owner_spec_bootstrap": opts.OwnerSpecBootstrap,
		},
		Result:      receipt,
		MaxAttempts: 3,
	}
	if preparedManagedLeaseRequest != nil {
		job.Payload[jobs.PreparedManagedLeaseRequestPayloadKey] = preparedManagedLeaseRequest
	}
	if opts.CustomerGuest != nil {
		job.Payload[jobs.CustomerGuestResultKey] = opts.CustomerGuest
		if job.Result == nil {
			job.Result = map[string]interface{}{}
		}
		job.Result[jobs.CustomerGuestResultKey] = opts.CustomerGuest
		job.Result["operation_id"] = opts.CustomerGuest.OperationID
		job.Result["lease_id"] = opts.CustomerGuest.LeaseID
		job.Result["auto_deploy"] = opts.AutoDeploy
	}
	if id := identity.FromContext(ctx); id != nil {
		job.Payload["actor"] = jobs.ActorPayloadFromIdentity(id)
	}
	jobs.CopyEdgeFlagsFromContext(ctx, job.Payload)
	jobs.CaptureRequestAuthority(ctx, job, stack.tenantID, stack.ownerID)

	// If we have a stored raw user config (imported YAML), pass it through so
	// the provision job can persist it byte-exact as kombination.yaml.
	if raw := strings.TrimSpace(stringFromAny(stack.config["user_config_raw"])); raw != "" {
		job.Payload["intent_raw"] = raw
	}

	// Enqueue with progress sync
	if enqueueErr := o.enqueueWithSync(job, stack.tenantID); enqueueErr != nil {
		return "", enqueueErr
	}

	o.log.Info("provision_job_enqueued", "job_id", job.ID, "stack_id", stackID)

	return job.ID, nil
}

// DeployStackWithOptions creates and enqueues a deploy job with explicit
// request identity context for Postgres-backed control-plane stacks.
func (o *Orchestrator) DeployStackWithOptions(stackID string, opts ProvisionStackOptions) (string, error) {
	if err := requireStackLifecycleIdentity(opts); err != nil {
		return "", err
	}
	ctx := o.deployRequestContext(opts)

	unlockStack := o.stackLocks.lock(stackID)
	defer unlockStack()
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.deployStackWithOptionsLocked(ctx, stackID, opts)
}

func (o *Orchestrator) deployStackWithOptionsLocked(ctx context.Context, stackID string, opts ProvisionStackOptions) (string, error) {
	stack, stackErr := o.findStackForJob(ctx, stackID, opts)
	if stackErr != nil {
		return "", fmt.Errorf("stack not found: %w", stackErr)
	}
	idempotency, idempotencyErr := newDeployIdempotency(stack, opts.IdempotencyKey)
	if idempotencyErr != nil {
		return "", idempotencyErr
	}
	if replayID, replay, replayErr := o.findStackLifecycleIdempotencyReplay(ctx, stack, idempotency); replayErr != nil || replay {
		return replayID, replayErr
	}
	if replayID, found, replayErr := o.findRoutingDispatchReplay(ctx, stack, opts); replayErr != nil {
		return "", replayErr
	} else if found {
		return replayID, nil
	}

	if statusErr := validateDeployStackStatus(stack.status, opts); statusErr != nil {
		return "", statusErr
	}

	payload, payloadErr := o.deployJobPayload(ctx, stackID, stack, opts)
	if payloadErr != nil {
		return "", payloadErr
	}
	jobs.CaptureStackOwnerEmail(ctx, payload, stack.ownerID)

	dispatchResult := deployDispatchResult(opts)
	if idempotency != nil {
		if dispatchResult == nil {
			dispatchResult = map[string]any{}
		}
		dispatchResult[idempotency.resultField] = idempotency.receipt
	}
	if isDeterministicExactRecovery(opts) {
		jobID := deterministicRecoveryJobID(opts)
		if receiptErr := o.persistDeployDispatchReceipt(ctx, stack, jobID, dispatchResult, opts); receiptErr != nil {
			return "", receiptErr
		}
		o.updateStackStatusForJob(ctx, stack, persistentStateProvisioning)
		job := buildDeployQueueJob(stackID, stack, payload, dispatchResult, jobID)
		jobs.CopyEdgeFlagsFromContext(ctx, job.Payload)
		jobs.CaptureRequestAuthority(ctx, job, stack.tenantID, stack.ownerID)
		if enqueueErr := o.enqueueWithSync(job, stack.tenantID); enqueueErr != nil {
			return "", enqueueErr
		}
		o.log.Info("deploy_job_enqueued", "job_id", job.ID, "stack_id", stackID)
		return job.ID, nil
	}

	var jobID string
	var createErr error
	if idempotency != nil {
		var replay bool
		jobID, replay, createErr = o.createStackLifecycleIdempotencyJob(ctx, stack, idempotency)
		if createErr == nil && replay {
			return jobID, nil
		}
	} else {
		jobID, createErr = o.createJobRecordForStack(ctx, stack, persistentJobTypeDeploy, "Queued for deployment")
	}
	if createErr != nil {
		return "", createErr
	}
	if receiptErr := o.persistDeployDispatchReceipt(ctx, stack, jobID, dispatchResult, opts); receiptErr != nil {
		return "", receiptErr
	}
	o.updateStackStatusForJob(ctx, stack, persistentStateProvisioning)

	job := buildDeployQueueJob(stackID, stack, payload, dispatchResult, jobID)
	jobs.CopyEdgeFlagsFromContext(ctx, job.Payload)
	jobs.CaptureRequestAuthority(ctx, job, stack.tenantID, stack.ownerID)

	if enqueueErr := o.enqueueWithSync(job, stack.tenantID); enqueueErr != nil {
		return "", enqueueErr
	}

	o.log.Info("deploy_job_enqueued", "job_id", job.ID, "stack_id", stackID)
	return job.ID, nil
}

func buildDeployQueueJob(stackID string, stack *orchestratorStack, payload, result map[string]any, jobID string) *jobs.Job {
	return &jobs.Job{
		ID: jobID, Type: jobs.JobTypeDeploy, TargetType: targetTypeStack,
		TargetID: stackID, TargetName: stack.name, Payload: payload, Result: result, MaxAttempts: 1,
	}
}

func (o *Orchestrator) deployRequestContext(opts ProvisionStackOptions) context.Context {
	if opts.RequestContext != nil {
		return opts.RequestContext
	}
	return o.ctx
}

func validateDeployStackStatus(status string, opts ProvisionStackOptions) error {
	if (status == persistentStateProvisioning && !isEnrollmentResume(opts) && !isRolloutRetry(opts)) ||
		(status == persistentStateRunning && !isExactRoutingReconciliation(opts)) {
		return fmt.Errorf("stack is already %s", status)
	}
	return nil
}

func isExactRoutingReconciliation(opts ProvisionStackOptions) bool {
	return strings.TrimSpace(opts.RoutingIdempotencyKey) != "" && opts.RoutingRevision > 0 &&
		strings.TrimSpace(opts.RequiredServerID) != "" && strings.TrimSpace(opts.RequiredLeaseID) != ""
}

func (o *Orchestrator) deployJobPayload(
	ctx context.Context,
	stackID string,
	stack *orchestratorStack,
	opts ProvisionStackOptions,
) (map[string]any, error) {
	managedPayload, err := o.managedRuntimePayloadForStackWithRequirements(ctx, stack, opts)
	if err != nil {
		return nil, err
	}
	runtimes, observedAt, err := o.canonicalDeployRuntimeEvidence(ctx, stackID, stack)
	if err != nil {
		return nil, err
	}
	approvedWorkers, err := o.approvedDeployWorkers(ctx, stackID, stack, runtimes, observedAt)
	if err != nil {
		return nil, err
	}
	managedRuntime, managedRuntimeOK := managedRuntimeDeployEligibleRuntime(stack, managedPayload, opts, runtimes, observedAt)
	if payloadHasManagedRuntimeLease(managedPayload) && !managedRuntimeOK {
		return nil, ErrNoAssignedWorkers
	}
	if len(approvedWorkers) == 0 && !managedRuntimeOK {
		return nil, ErrNoAssignedWorkers
	}
	if managedRuntimeOK {
		allowedServerBindings, err := o.activeManagedRuntimeDeployServerBindings(ctx, stack, managedPayload, opts)
		if err != nil {
			return nil, err
		}
		if leaseID, ok := allowedServerBindings[managedRuntime.ID]; !ok || leaseID != managedRuntime.LeaseID {
			return nil, ErrManagedRuntimeLeaseNotNativeActive
		}
		approvedWorkers = deployWorkersForLeaseServerBindings(approvedWorkers, runtimes, allowedServerBindings)
		// Bind the command channel to the same canonical lease/server aggregate
		// that admitted the rollout. Falling back to approvedWorkers[0] here can
		// dispatch typed StackKits commands to a different node than the managed
		// SSH target when a stack has multiple healthy Guards.
		managedPayload["server_id"] = managedRuntime.ID
		managedPayload["runtime_agent_id"] = managedRuntime.WorkerID
	}
	return buildDeployJobPayload(stack, approvedWorkers, managedPayload, opts), nil
}

// activeManagedRuntimeDeployServerBindings returns the canonical server/lease
// set owned by the current native-active lease inventory. A normal rollout
// admits the selected foundation plus active supplemental nodes; exact
// reconciliation admits only its required pair. Duplicate foundations, stale
// leases, and foreign stack or owner projections are excluded.
func (o *Orchestrator) activeManagedRuntimeDeployServerBindings(
	ctx context.Context,
	stack *orchestratorStack,
	managedPayload map[string]interface{},
	opts ProvisionStackOptions,
) (map[string]string, error) {
	if !managedRuntimeLeaseLookupReady(o, stack) {
		return nil, ErrManagedRuntimeLeaseNotNativeActive
	}
	records, err := o.leaseLister.ListInventoryByTenant(ctx, stack.tenantID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManagedRuntimeLeaseNotNativeActive, err)
	}
	selectedLeaseID := strings.TrimSpace(stringFromAny(managedPayload[runtimeFieldLeaseID]))
	exactServerID := strings.TrimSpace(opts.RequiredServerID)
	allowed := map[string]string{}
	for _, record := range records {
		lease := record.Lease
		leaseID := strings.TrimSpace(string(lease.ID))
		if !record.NativeActive() || !managedRuntimeLeaseMatchesStack(lease, stack) {
			continue
		}
		serverID := runtimeidentity.LeaseServerID(leaseID)
		if serverID == "" {
			continue
		}
		if exactServerID != "" {
			if leaseID == selectedLeaseID && serverID == exactServerID {
				allowed[serverID] = leaseID
			}
			continue
		}
		if isFoundationRoleLease(lease) && leaseID != selectedLeaseID {
			continue
		}
		allowed[serverID] = leaseID
	}
	return allowed, nil
}

func deployWorkersForLeaseServerBindings(
	workers []map[string]any,
	runtimes []controlplane.ServerRuntime,
	allowed map[string]string,
) []map[string]any {
	selected := make([]map[string]any, 0, len(workers))
	for _, worker := range workers {
		serverID := strings.TrimSpace(stringFromAny(worker["server_id"]))
		leaseID, ok := allowed[serverID]
		if !ok {
			continue
		}
		workerID := strings.TrimSpace(stringFromAny(worker["id"]))
		for _, runtime := range runtimes {
			if runtime.ID == serverID && runtime.WorkerID == workerID && runtime.LeaseID == leaseID {
				selected = append(selected, worker)
				break
			}
		}
	}
	return selected
}

// approvedDeployWorkers loads explicitly assigned workers from the mandatory
// control-plane projection and keeps the deployment selection owner-scoped.
func (o *Orchestrator) approvedDeployWorkers(
	ctx context.Context,
	stackID string,
	stack *orchestratorStack,
	runtimes []controlplane.ServerRuntime,
	observedAt time.Time,
) ([]map[string]any, error) {
	approved := []map[string]any{}
	if o.workerStore == nil || stack.tenantID == "" || stack.ownerID == "" {
		return approved, nil
	}
	workers, err := o.workerStore.ListWorkersByTenant(ctx, stack.tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to load assigned workers: %w", err)
	}
	candidates := make([]controlplane.Worker, 0, len(workers))
	for _, worker := range workers {
		if worker.OwnerSubjectID != stack.ownerID || worker.StackID != stackID || !workerIsDeployCandidate(worker) {
			continue
		}
		candidates = append(candidates, worker)
	}
	if len(candidates) == 0 {
		return approved, nil
	}
	for _, worker := range candidates {
		runtime, ok := workerDeployEligibleRuntime(worker, runtimes, observedAt)
		if !ok {
			continue
		}
		approved = append(approved, deployWorkerPayloadFromControlPlane(worker, runtime))
	}
	return approved, nil
}

func (o *Orchestrator) canonicalDeployRuntimeEvidence(
	ctx context.Context,
	stackID string,
	stack *orchestratorStack,
) ([]controlplane.ServerRuntime, time.Time, error) {
	serverStore := o.canonicalServerRuntimeStore()
	if serverStore == nil {
		return nil, time.Time{}, fmt.Errorf("%w: canonical ServerRuntime store is not configured", ErrDeployRuntimeEvidenceUnavailable)
	}
	runtimes, err := serverStore.ListServerRuntimesByTenant(ctx, stack.tenantID, stackID)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("%w: load canonical ServerRuntime evidence: %v", ErrDeployRuntimeEvidenceUnavailable, err)
	}
	return runtimes, time.Now().UTC(), nil
}

// canonicalServerRuntimeStore keeps deploy admission on the same aggregate
// adapter that owns Guard heartbeat, connection, and health evidence. Current
// Postgres and memory control-plane stores implement both interfaces. A split
// adapter that does not expose the canonical runtime projection fails closed.
func (o *Orchestrator) canonicalServerRuntimeStore() controlplane.ServerRuntimeStore {
	for _, candidate := range []any{o.workerStore, o.stackStore, o.jobStore} {
		if store, ok := candidate.(controlplane.ServerRuntimeStore); ok && store != nil {
			return store
		}
	}
	return nil
}

func (o *Orchestrator) DispatchRoutingRollout(ctx context.Context, req stackrouting.RolloutRequest) (*stackrouting.RolloutResult, error) {
	jobID, err := o.DeployStackWithOptions(req.StackID, ProvisionStackOptions{
		RequestContext: ctx, TenantID: req.TenantID, OwnerID: req.OwnerSubjectID,
		RequiredLeaseID: req.LeaseID, RequiredServerID: req.ServerID,
		RoutingRevision: req.RoutingRevision, RoutingIdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	return &stackrouting.RolloutResult{JobID: jobID}, nil
}

const (
	routingDispatchKindField     = "routing_dispatch_kind"
	routingDispatchKindExact     = "exact-stack-routing"
	routingDispatchKeyField      = "routing_idempotency_key"
	routingDispatchRevisionField = "routing_revision"
	routingDispatchServerField   = "routing_server_id"
	routingDispatchLeaseField    = "routing_lease_id"
)

func routingDispatchResult(opts ProvisionStackOptions) map[string]any {
	if strings.TrimSpace(opts.RoutingIdempotencyKey) == "" {
		return nil
	}
	return map[string]any{
		routingDispatchKindField:     routingDispatchKindExact,
		routingDispatchKeyField:      strings.TrimSpace(opts.RoutingIdempotencyKey),
		routingDispatchRevisionField: opts.RoutingRevision,
		routingDispatchServerField:   strings.TrimSpace(opts.RequiredServerID),
		routingDispatchLeaseField:    strings.TrimSpace(opts.RequiredLeaseID),
	}
}

func (o *Orchestrator) findRoutingDispatchReplay(ctx context.Context, stack *orchestratorStack, opts ProvisionStackOptions) (string, bool, error) {
	key := strings.TrimSpace(opts.RoutingIdempotencyKey)
	if key == "" {
		return "", false, nil
	}
	if o.jobStore == nil || stack == nil || strings.TrimSpace(stack.tenantID) == "" {
		return "", false, fmt.Errorf("%w: durable job store is required for routing dispatch idempotency", stackrouting.ErrUnavailable)
	}
	jobsForStack, err := o.jobStore.ListJobsByStack(ctx, stack.tenantID, stack.id, 100)
	if err != nil {
		return "", false, fmt.Errorf("%w: load routing dispatch receipt: %v", stackrouting.ErrUnavailable, err)
	}
	for _, job := range jobsForStack {
		if strings.TrimSpace(stringFromAny(job.Result[routingDispatchKindField])) != routingDispatchKindExact {
			continue
		}
		if strings.TrimSpace(stringFromAny(job.Result[routingDispatchKeyField])) != key {
			continue
		}
		if strings.TrimSpace(stringFromAny(job.Result[routingDispatchServerField])) != strings.TrimSpace(opts.RequiredServerID) ||
			strings.TrimSpace(stringFromAny(job.Result[routingDispatchLeaseField])) != strings.TrimSpace(opts.RequiredLeaseID) ||
			int64(intFromAny(job.Result[routingDispatchRevisionField])) != opts.RoutingRevision {
			return "", false, fmt.Errorf("%w: routing dispatch key belongs to a different exact target", stackrouting.ErrIdempotencyConflict)
		}
		return job.ID, true, nil
	}
	return "", false, nil
}

// buildDeployJobPayload assembles the deploy job payload from the assigned
// workers, the managed-runtime projection, and the request options.
//
//nolint:goconst // Payload keys intentionally mirror the job wire schema.
func buildDeployJobPayload(stack *orchestratorStack, approvedWorkers []map[string]any, managedPayload map[string]interface{}, opts ProvisionStackOptions) map[string]interface{} {
	payload := map[string]interface{}{
		"workers":          approvedWorkers,
		"apply":            true,
		stackOwnerIDField:  stack.ownerID,
		stackTenantIDField: stack.tenantID,
	}
	if stackKitID := firstNonEmptyString(stackStringValue(stack, runtimeFieldStackKitRef), stackStringValue(stack, "stackkit")); stackKitID != "" {
		payload["stackkit_id"] = stackKitID
	}
	for _, worker := range approvedWorkers {
		if nodeID := firstNonEmptyString(stringFromAny(worker["node_id"]), stringFromAny(worker["server_id"])); nodeID != "" {
			payload["node_id"] = nodeID
			break
		}
	}
	if opts.OwnerSpecBootstrap != nil {
		// The deploy handler enforces the StackKit identity handoff whenever a
		// bootstrap token rides in the payload, so this is what arms the
		// self-hosted owner handoff contract.
		payload["owner_spec_bootstrap"] = opts.OwnerSpecBootstrap
	}
	for key, value := range managedPayload {
		payload[key] = value
	}
	if stringFromAny(payload["stackkit_id"]) == "" {
		if stackKitID := stringFromAny(managedPayload[runtimeFieldStackKitRef]); stackKitID != "" {
			payload["stackkit_id"] = stackKitID
		}
	}
	if stringFromAny(payload["node_id"]) == "" {
		if nodeID := firstNonEmptyString(stringFromAny(managedPayload["node_id"]), stringFromAny(managedPayload["server_id"])); nodeID != "" {
			payload["node_id"] = nodeID
		}
	}
	for key, value := range deployDispatchResult(opts) {
		payload[key] = value
	}
	// config_json is the durable authority for a native-v2 Wizard projection.
	// Snapshot the exact value already loaded with the stack so a resumed bare
	// deploy can re-materialize its ephemeral disk sibling without another
	// repository or an unfenced second read.
	if projection, ok := stack.config["stack_spec_v2"]; ok {
		payload["stack_spec_v2"] = projection
	}
	return payload
}

func (o *Orchestrator) managedRuntimePayloadForStackWithRequirements(ctx context.Context, stack *orchestratorStack, opts ProvisionStackOptions) (map[string]interface{}, error) {
	requiredLeaseID := strings.TrimSpace(opts.RequiredLeaseID)
	requiredServerID := strings.TrimSpace(opts.RequiredServerID)
	if requiredLeaseID == "" && requiredServerID == "" {
		return o.managedRuntimePayloadForStack(ctx, stack)
	}
	if requiredLeaseID == "" || requiredServerID == "" {
		return nil, fmt.Errorf("%w: exact managed rollout requires both lease and server IDs", stackrouting.ErrInvalid)
	}
	lease, err := o.exactManagedRuntimeLeaseForStack(ctx, stack, requiredLeaseID)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve exact routing lease: %v", stackrouting.ErrUnavailable, err)
	}
	if lease == nil {
		return nil, fmt.Errorf("%w: exact active foundation lease was not found", stackrouting.ErrInvalid)
	}
	if canonical := runtimeidentity.LeaseServerID(requiredLeaseID); canonical == "" || canonical != requiredServerID {
		return nil, fmt.Errorf("%w: canonical server does not match required routing lease", stackrouting.ErrInvalid)
	}
	payload := stackManagedRuntimePayload(stack)
	for key, value := range managedRuntimePayloadFromLease(*lease) {
		payload[key] = value
	}
	return payload, nil
}

func (o *Orchestrator) exactManagedRuntimeLeaseForStack(ctx context.Context, stack *orchestratorStack, leaseID string) (*vmlease.Lease, error) {
	if o == nil || o.leaseLister == nil || stack == nil || strings.TrimSpace(stack.tenantID) == "" || strings.TrimSpace(stack.ownerID) == "" || strings.TrimSpace(stack.id) == "" {
		return nil, fmt.Errorf("managed runtime lease authority is not configured")
	}
	leaseID = strings.TrimSpace(leaseID)
	records, err := o.leaseLister.ListInventoryByTenant(ctx, stack.tenantID)
	if err != nil {
		return nil, err
	}
	for i := range records {
		record := records[i]
		lease := record.Lease
		if strings.TrimSpace(string(lease.ID)) != leaseID {
			continue
		}
		if !record.NativeActive() ||
			(!monthlyruntime.IsMonthlyRuntimeMetadata(lease.Metadata) && !customerUbuntuLease(lease)) ||
			!managedRuntimeLeaseVisibleToStackOwner(lease, stack) ||
			strings.TrimSpace(lease.Metadata["stack_id"]) != strings.TrimSpace(stack.id) ||
			(!isFoundationRoleLease(lease) && !customerUbuntuLease(lease)) {
			return nil, fmt.Errorf("%w: requested lease is not a native-active foundation target for this stack", stackrouting.ErrInvalid)
		}
		copy := lease
		return &copy, nil
	}
	return nil, nil
}

func (o *Orchestrator) managedRuntimePayloadForStack(ctx context.Context, stack *orchestratorStack) (map[string]interface{}, error) {
	stackPayload := stackManagedRuntimePayload(stack)
	// Prefer an active foundation lease resolved from the lease store over the
	// stack record's lease_id. The stack record can carry a stale or cancelled
	// subscription lease — e.g. after a failed rollout followed by an add-server
	// that minted a fresh foundation lease — and the deploy must resolve the
	// StackKit-rollout SSH target from the active foundation lease, never the
	// cancelled primary. managedRuntimeLeasePayload already filters to active,
	// owner-visible, stack-scoped leases and prefers the foundation role, so its
	// result supersedes the (possibly stale) stack-record fields when present.
	// Native-only provider control deliberately has no stack-record fallback:
	// a historical lease_id or provider alias is inventory, not execution
	// authority. Managed stacks without a native-active lease therefore stop
	// before a deploy job can be enqueued.
	leasePayload, err := o.managedRuntimeLeasePayload(ctx, stack)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManagedRuntimeLeaseNotNativeActive, err)
	}
	if payloadHasManagedRuntimeLease(leasePayload) {
		return leasePayload, nil
	}
	if payloadHasManagedRuntimeLease(stackPayload) {
		return nil, ErrManagedRuntimeLeaseNotNativeActive
	}
	return stackPayload, nil
}

func payloadHasManagedRuntimeLease(payload map[string]interface{}) bool {
	leaseID := stringFromAny(payload[runtimeFieldLeaseID])
	if strings.TrimSpace(leaseID) == "" {
		return false
	}
	return strings.TrimSpace(stringFromAny(payload[runtimeFieldLane])) == runtimeLaneMonthly ||
		strings.TrimSpace(stringFromAny(payload[runtimeFieldConnectionMode])) == runtimeConnectionManaged ||
		strings.TrimSpace(stringFromAny(payload[runtimeFieldProvisionMode])) == runtimeProvisionKombify ||
		strings.TrimSpace(stringFromAny(payload[runtimeFieldLeaseProvider])) != "" ||
		strings.TrimSpace(stringFromAny(payload[runtimeFieldOfferingID])) != ""
}

func (o *Orchestrator) managedRuntimeLeasePayload(ctx context.Context, stack *orchestratorStack) (map[string]interface{}, error) {
	selected, err := o.managedRuntimeLeaseForStack(ctx, stack)
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, nil
	}
	return managedRuntimePayloadFromLease(*selected), nil
}

func (o *Orchestrator) managedRuntimeLeaseForStack(ctx context.Context, stack *orchestratorStack) (*vmlease.Lease, error) {
	if !managedRuntimeLeaseLookupReady(o, stack) {
		return nil, fmt.Errorf("managed runtime lease authority is not configured")
	}
	records, err := o.leaseLister.ListInventoryByTenant(ctx, stack.tenantID)
	if err != nil {
		return nil, err
	}
	selected := selectManagedRuntimeLease(records, stack)
	if selected == nil {
		return nil, nil
	}
	copy := *selected
	return &copy, nil
}

func managedRuntimeLeaseLookupReady(o *Orchestrator, stack *orchestratorStack) bool {
	if o == nil || o.leaseLister == nil || stack == nil {
		return false
	}
	return strings.TrimSpace(stack.tenantID) != "" && strings.TrimSpace(stack.ownerID) != "" && strings.TrimSpace(stack.id) != ""
}

func selectManagedRuntimeLease(records []vmleases.LeaseInventoryRecord, stack *orchestratorStack) *vmlease.Lease {
	// Prefer the FOUNDATION lease. The deploy resolves the StackKit-rollout SSH
	// target from this lease, and that target must be the stack's control-plane
	// node — never a worker, and never a stale/cancelled subscription lease. A
	// stack with a foundation plus a worker exposes two active leases sharing the
	// same stack_id; without the role preference the most-recently-renewed one
	// wins, which can be the worker and sends the rollout to the wrong host.
	var selected, foundation *vmlease.Lease
	for i := range records {
		record := &records[i]
		if !record.NativeActive() {
			continue
		}
		lease := &record.Lease
		if !managedRuntimeLeaseMatchesStack(*lease, stack) {
			continue
		}
		if isFoundationRoleLease(*lease) {
			if foundation == nil || lease.RenewedAt.After(foundation.RenewedAt) {
				foundation = lease
			}
			continue
		}
		if selected == nil || lease.RenewedAt.After(selected.RenewedAt) {
			selected = lease
		}
	}
	if foundation != nil {
		return foundation
	}
	return selected
}

func managedRuntimeLeaseMatchesStack(lease vmlease.Lease, stack *orchestratorStack) bool {
	if !monthlyruntime.IsMonthlyRuntimeMetadata(lease.Metadata) || !managedRuntimeLeaseVisibleToStackOwner(lease, stack) {
		return false
	}
	return strings.TrimSpace(lease.Metadata["stack_id"]) == strings.TrimSpace(stack.id)
}

// isFoundationRoleLease reports whether a managed-runtime lease belongs to the
// stack's foundation / control-plane node (the valid StackKit-rollout SSH
// target) rather than a worker. An unset role is treated as foundation for
// backward compatibility with single-server stacks whose primary lease carries
// no explicit role.
func isFoundationRoleLease(lease vmlease.Lease) bool {
	switch strings.ToLower(strings.TrimSpace(lease.Metadata["role"])) {
	case "", runtimeRoleFoundation, "control-plane", "primary", runtimeRoleMain:
		return true
	default:
		return false
	}
}

func managedRuntimeLeaseVisibleToStackOwner(lease vmlease.Lease, stack *orchestratorStack) bool {
	if stack == nil {
		return false
	}
	if strings.TrimSpace(lease.Subject.OrgID) != strings.TrimSpace(stack.tenantID) {
		return false
	}
	if strings.TrimSpace(lease.Subject.ID) == strings.TrimSpace(stack.ownerID) {
		return true
	}
	return lease.Subject.Kind == vmlease.SubjectOrg
}

func managedRuntimePayloadFromLease(lease vmlease.Lease) map[string]interface{} {
	metadata := lease.Metadata
	payload := map[string]interface{}{
		runtimeFieldLeaseID: string(lease.ID),
	}
	put := func(key, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			payload[key] = value
		}
	}
	put(runtimeFieldLane, firstNonEmptyString(metadata[runtimeFieldLane], runtimeLaneMonthly))
	put(runtimeFieldOfferingID, metadata[runtimeFieldOfferingID])
	put(runtimeFieldLeaseProvider, firstNonEmptyString(lease.Resource.ProviderID, metadata[runtimeFieldLeaseProvider], metadata[runtimeFieldSimProviderID]))
	put(runtimeFieldProviderRegion, firstNonEmptyString(lease.Resource.Region, metadata[runtimeFieldProviderRegion], metadata["region"]))
	put(runtimeFieldSimProviderID, firstNonEmptyString(metadata[runtimeFieldSimProviderID], lease.Resource.ProviderID))
	put(runtimeFieldConnectionMode, firstNonEmptyString(metadata[runtimeFieldConnectionMode], runtimeConnectionManaged))
	put(runtimeFieldProvisionMode, firstNonEmptyString(metadata[runtimeFieldProvisionMode], runtimeProvisionKombify))
	put(runtimeFieldStackKitRef, metadata[runtimeFieldStackKitRef])
	put(runtimeFieldNodeLifecycle, metadata[runtimeFieldNodeLifecycle])
	put("billing_mode", string(lease.BillingMode))
	put(runtimeFieldBillingCadence, metadata[runtimeFieldBillingCadence])
	put(runtimeFieldDesiredState, string(lease.DesiredState))
	put(runtimeFieldVerification, metadata[runtimeFieldVerification])
	put(stackKitRuntimePublicIP, firstNonEmptyString(metadata[stackKitRuntimePublicIP], metadata["node_public_ip"], metadata["public_ip"]))
	put(runtimeFieldPrivateIP, firstNonEmptyString(metadata[runtimeFieldPrivateIP], metadata["node_private_ip"], metadata["private_ip"]))
	put(runtimeFieldSSHHost, firstNonEmptyString(metadata[runtimeFieldSSHHost], metadata["runtime_host"]))
	put(runtimeFieldSSHUser, metadata[runtimeFieldSSHUser])
	put(runtimeFieldSSHPort, metadata[runtimeFieldSSHPort])
	return payload
}

func stackManagedRuntimePayload(stack *orchestratorStack) map[string]interface{} {
	payload := map[string]interface{}{}
	for _, key := range []string{
		runtimeFieldLeaseID,
		runtimeFieldLane,
		runtimeFieldOfferingID,
		runtimeFieldLeaseProvider,
		runtimeFieldProviderRegion,
		runtimeFieldIONOSDatacenter,
		runtimeFieldSimProviderID,
		runtimeFieldConnectionMode,
		runtimeFieldProvisionMode,
		runtimeFieldInstallCommand,
		runtimeFieldStackKitRef,
		runtimeFieldNodeLifecycle,
		"billing_mode",
		runtimeFieldBillingCadence,
		runtimeFieldDesiredState,
		runtimeFieldVerification,
		stackKitRuntimePublicIP,
		runtimeFieldPrivateIP,
		runtimeFieldSSHHost,
		runtimeFieldSSHUser,
		runtimeFieldSSHPort,
	} {
		value := strings.TrimSpace(stackStringValue(stack, key))
		if value != "" {
			payload[key] = value
		}
	}
	return payload
}

func stackStringValue(stack *orchestratorStack, key string) string {
	if stack == nil || strings.TrimSpace(key) == "" {
		return ""
	}
	return stringFromAny(stack.config[key])
}

// workerIsDeployCandidate applies only the owner-approved assignment gate. It
// deliberately does not infer connection from Worker.Status; canonical Guard
// runtime evidence is checked separately by workerDeployEligibleRuntime.
func workerIsDeployCandidate(worker controlplane.Worker) bool {
	if !worker.Approved {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(worker.Status)) {
	case "approved", "connected":
		return true
	default:
		return false
	}
}

func workerDeployEligibleRuntime(worker controlplane.Worker, runtimes []controlplane.ServerRuntime, now time.Time) (controlplane.ServerRuntime, bool) {
	for _, runtime := range runtimes {
		if serverRuntimeAllowsWorkerDeploy(worker, runtime, now) {
			return runtime, true
		}
	}
	return controlplane.ServerRuntime{}, false
}

func managedRuntimeDeployEligibleRuntime(
	stack *orchestratorStack,
	payload map[string]interface{},
	opts ProvisionStackOptions,
	runtimes []controlplane.ServerRuntime,
	now time.Time,
) (controlplane.ServerRuntime, bool) {
	leaseID := strings.TrimSpace(stringFromAny(payload[runtimeFieldLeaseID]))
	if leaseID == "" || !payloadHasManagedRuntimeLease(payload) {
		return controlplane.ServerRuntime{}, false
	}
	serverID := runtimeidentity.LeaseServerID(leaseID)
	if serverID == "" ||
		(strings.TrimSpace(opts.RequiredLeaseID) != "" && strings.TrimSpace(opts.RequiredLeaseID) != leaseID) ||
		(strings.TrimSpace(opts.RequiredServerID) != "" && strings.TrimSpace(opts.RequiredServerID) != serverID) {
		return controlplane.ServerRuntime{}, false
	}
	return exactManagedRuntimeDeployEligibleRuntime(stack, leaseID, serverID, runtimes, now)
}

func exactManagedRuntimeHasDeployEligibleRuntime(
	stack *orchestratorStack,
	leaseID, serverID string,
	runtimes []controlplane.ServerRuntime,
	now time.Time,
) bool {
	_, ok := exactManagedRuntimeDeployEligibleRuntime(stack, leaseID, serverID, runtimes, now)
	return ok
}

func exactManagedRuntimeDeployEligibleRuntime(
	stack *orchestratorStack,
	leaseID, serverID string,
	runtimes []controlplane.ServerRuntime,
	now time.Time,
) (controlplane.ServerRuntime, bool) {
	leaseID = strings.TrimSpace(leaseID)
	serverID = strings.TrimSpace(serverID)
	if leaseID == "" || serverID == "" || runtimeidentity.LeaseServerID(leaseID) != serverID {
		return controlplane.ServerRuntime{}, false
	}
	for _, runtime := range runtimes {
		if runtime.ID == serverID && runtime.LeaseID == leaseID && serverRuntimeAllowsStackDeploy(stack, runtime, now) {
			return runtime, true
		}
	}
	return controlplane.ServerRuntime{}, false
}

func (o *Orchestrator) requireExactManagedRuntimeDeploy(
	ctx context.Context,
	stack *orchestratorStack,
	leaseID, serverID string,
) error {
	runtimes, observedAt, err := o.canonicalDeployRuntimeEvidence(ctx, stack.id, stack)
	if err != nil {
		return err
	}
	if !exactManagedRuntimeHasDeployEligibleRuntime(stack, leaseID, serverID, runtimes, observedAt) {
		return ErrNoAssignedWorkers
	}
	return nil
}

// serverRuntimeAllowsWorkerDeploy is the single deploy-admission predicate for
// Guard state. Approval and legacy Worker.LastSeenAt are not health evidence.
func serverRuntimeAllowsWorkerDeploy(worker controlplane.Worker, runtime controlplane.ServerRuntime, now time.Time) bool {
	if strings.TrimSpace(worker.ID) == "" ||
		runtime.WorkerID != worker.ID ||
		runtime.TenantID != worker.TenantID ||
		runtime.StackID != worker.StackID ||
		runtime.OwnerSubjectID != worker.OwnerSubjectID {
		return false
	}
	stack := &orchestratorStack{id: worker.StackID, tenantID: worker.TenantID, ownerID: worker.OwnerSubjectID}
	return serverRuntimeAllowsStackDeploy(stack, runtime, now)
}

func serverRuntimeAllowsStackDeploy(stack *orchestratorStack, runtime controlplane.ServerRuntime, now time.Time) bool {
	if stack == nil ||
		strings.TrimSpace(stack.id) == "" ||
		runtime.TenantID != stack.tenantID ||
		runtime.StackID != stack.id ||
		runtime.OwnerSubjectID != stack.ownerID ||
		serverregistry.LifecycleState(strings.ToLower(strings.TrimSpace(runtime.LifecycleState))) != serverregistry.LifecycleActive ||
		runtime.LastHeartbeatAt == nil || runtime.LastHeartbeatAt.IsZero() {
		return false
	}
	age := now.UTC().Sub(runtime.LastHeartbeatAt.UTC())
	if age < -30*time.Second || age > runtimehealth.FreshHeartbeatWindow {
		return false
	}
	connection := serverregistry.ConnectionState(strings.ToLower(strings.TrimSpace(runtime.ConnectionState)))
	if connection != serverregistry.ConnectionConnected && connection != serverregistry.ConnectionDegraded {
		return false
	}
	health := serverregistry.HealthState(strings.ToLower(strings.TrimSpace(runtime.HealthState)))
	return health == serverregistry.HealthHealthy || health == serverregistry.HealthDegraded
}

func deployWorkerPayloadFromControlPlane(worker controlplane.Worker, runtime controlplane.ServerRuntime) map[string]any {
	tags := ""
	if raw, ok := worker.Tags["raw"].(string); ok {
		tags = raw
	}
	metadata := nodehandoff.MergeMetadata(nodehandoff.MergeMetadata(worker.Resources, worker.Capabilities), nodehandoff.MetadataFromTags(tags))
	payload := map[string]any{
		"id":                  worker.ID,
		"server_id":           runtime.ID,
		"node_id":             firstNonEmptyString(runtime.NodeID, runtime.ID),
		stackKitNodeNameField: worker.Hostname,
		"type":                worker.Type,
		"provider":            worker.Provider,
		"ip":                  worker.IP,
		// core.Worker.Status is the legacy placement eligibility vocabulary:
		// the placement engine only accepts "online". Reaching this builder
		// already proves connected/degraded Guard admission; preserve that exact
		// observation separately instead of mislabeling it as placement status.
		"status":           "online",
		"connection_state": strings.ToLower(strings.TrimSpace(runtime.ConnectionState)),
		"capabilities": map[string]any{
			"cpu":            worker.CPUCores,
			"ram":            worker.RAMMB,
			"disk":           worker.DiskGB,
			"arch":           worker.Arch,
			"os":             worker.OS,
			"dockerVersion":  worker.DockerVersion,
			"hasNVMe":        worker.HasNVME,
			"hasHWTranscode": worker.HasHWTranscode,
		},
	}
	return applyDeployWorkerHandoff(payload, metadata)
}

func applyDeployWorkerHandoff(payload map[string]any, metadata map[string]any) map[string]any {
	capabilities, _ := payload["capabilities"].(map[string]any)
	if capabilities == nil {
		capabilities = map[string]any{}
		payload["capabilities"] = capabilities
	}
	if role := nodehandoff.StringFromMap(metadata, nodehandoff.KeyServerNodeRole); role != "" {
		role = nodehandoff.NormalizeNodeRole(role)
		payload[nodehandoff.KeyServerNodeRole] = role
		payload["node_role"] = role
		payload["type"] = nodehandoff.WorkerTypeForNodeRole(role)
		capabilities[nodehandoff.KeyServerNodeRole] = role
		capabilities["node_role"] = role
	}
	if services := nodehandoff.ServiceKeysFromAny(metadata[nodehandoff.KeyRequestedServices]); len(services) > 0 {
		payload[nodehandoff.KeyRequestedServices] = services
		payload["services"] = services
		capabilities[nodehandoff.KeyRequestedServices] = services
		capabilities["services"] = services
	}
	for _, key := range []string{
		nodehandoff.KeyServerRemoteHost,
		nodehandoff.KeyServerRemoteUser,
		nodehandoff.KeyServerRemoteAuthMethod,
		nodehandoff.KeyServerRemoteCredential,
		nodehandoff.KeyServerRemoteSSHKey,
	} {
		if value := nodehandoff.StringFromMap(metadata, key); value != "" {
			payload[key] = value
			capabilities[key] = value
		}
	}
	if port := nodehandoff.IntFromMap(metadata, nodehandoff.KeyServerRemotePort); port > 0 {
		payload[nodehandoff.KeyServerRemotePort] = port
		capabilities[nodehandoff.KeyServerRemotePort] = port
	}
	if nodehandoff.BoolFromMap(metadata, nodehandoff.KeyServerRemoteUseSudo) {
		payload[nodehandoff.KeyServerRemoteUseSudo] = true
		capabilities[nodehandoff.KeyServerRemoteUseSudo] = true
	}
	return payload
}

// DestroyStackWithOptions creates and enqueues a destroy job with explicit
// request identity context for Postgres-backed control-plane stacks.
func (o *Orchestrator) DestroyStackWithOptions(stackID string, opts ProvisionStackOptions) (string, error) {
	if err := requireStackLifecycleIdentity(opts); err != nil {
		return "", err
	}
	ctx := opts.RequestContext
	if ctx == nil {
		ctx = o.ctx
	}

	unlockStack := o.stackLocks.lock(stackID)
	defer unlockStack()
	o.mu.RLock()
	defer o.mu.RUnlock()

	stack, err := o.findStackForJob(ctx, stackID, opts)
	if err != nil {
		return "", fmt.Errorf("stack not found: %w", err)
	}
	managedRuntimeDecommissionRequired, err := o.managedRuntimeDecommissionRequired(ctx, stack)
	if err != nil {
		return "", fmt.Errorf("classify managed runtime before destroy: %w", err)
	}

	destroyResult := map[string]any{}
	if managedRuntimeDecommissionRequired {
		destroyResult[managedDecommissionRecoveryMarkerKey] = managedProviderDecommissionRecoveryMarker(stack.tenantID, stack.id)
	}
	// Cancel process-local work first, then retire every durable pending offer
	// for this exact tenant/stack. Running work deliberately keeps its execution
	// lease: that claim remains the cross-replica barrier until its handler exits
	// or the orphan reclaimer terminalizes it.
	for _, canceledJobID := range o.queue.CancelStackOffers(stackID) {
		o.log.Info("stack_offer_canceled_for_destroy", "job_id", canceledJobID, "stack_id", stackID)
	}
	if o.jobStore != nil {
		cancelled, cancelErr := o.jobStore.CancelPendingStackOperations(ctx, stack.tenantID, stack.id, time.Now().UTC())
		if cancelErr != nil {
			return "", fmt.Errorf("cancel pending stack operations before destroy: %w", cancelErr)
		}
		for _, cancelledJobID := range cancelled {
			o.log.Info("durable_stack_offer_canceled_for_destroy", "job_id", cancelledJobID, "stack_id", stackID)
		}
	}
	if o.cfg.PortInventory != nil {
		snapshot, snapshotErr := o.cfg.PortInventory.SnapshotForTeardown(ctx, portinventory.TeardownSnapshotRequest{
			TenantID: stack.tenantID, OwnerSubjectID: stack.ownerID, TechstackID: stack.id,
		})
		if snapshotErr == nil {
			snapshotErr = portinventory.ValidateTeardownSnapshot(snapshot)
		}
		if snapshotErr != nil {
			return "", fmt.Errorf("snapshot port claims before destroy: %w", snapshotErr)
		}
		destroyResult[jobs.PortTeardownSnapshotResultField] = snapshot
		if !managedRuntimeDecommissionRequired && len(snapshot.Generations) > 0 {
			if o.cfg.StackKitCommander == nil {
				return "", fmt.Errorf("snapshot port claims before local destroy: typed StackKits dispatcher is not configured")
			}
			bindings, bindingErr := o.localStackKitTeardownNodeBindings(ctx, stack)
			if bindingErr != nil {
				return "", fmt.Errorf("bind local StackKits teardown nodes: %w", bindingErr)
			}
			authority, authorityErr := jobs.BuildLocalStackKitTeardownAuthority(
				stack.id, stack.stackKitInstanceID, stack.tenantID, stack.ownerID, stack.runtimeSummary, snapshot, bindings,
			)
			if authorityErr != nil {
				return "", fmt.Errorf("bind local StackKits teardown authority: %w", authorityErr)
			}
			destroyResult[jobs.LocalStackKitTeardownAuthorityResultField] = authority
		}
	}
	jobID, err := o.createJobRecordForStack(ctx, stack, "destroy", "Queued for destruction")
	if err != nil {
		return "", err
	}
	if len(destroyResult) > 0 {
		if err := o.persistDestroyAdmissionReceipt(ctx, stack, jobID, destroyResult); err != nil {
			return "", err
		}
	}
	if err := o.persistStackStatusForJob(ctx, stack, "stopping"); err != nil {
		return "", fmt.Errorf("fence stack before destroy queue admission: %w", err)
	}

	// Create in-memory job
	job := &jobs.Job{
		ID:         jobID,
		Type:       jobs.JobTypeDestroy,
		TargetType: targetTypeStack,
		TargetID:   stackID,
		TargetName: stack.name,
		Payload: map[string]interface{}{
			stackOwnerIDField:  stack.ownerID,
			stackTenantIDField: stack.tenantID,
			jobs.ManagedRuntimeDecommissionRequiredField: managedRuntimeDecommissionRequired,
		},
		MaxAttempts: 3,
	}
	if len(destroyResult) > 0 {
		job.Result = destroyResult
	}
	jobs.CopyEdgeFlagsFromContext(ctx, job.Payload)

	if err := o.enqueueWithSync(job, stack.tenantID); err != nil {
		return "", err
	}

	o.log.Info("destroy_job_enqueued", "job_id", job.ID, "stack_id", stackID)

	return job.ID, nil
}

func requireStackLifecycleIdentity(opts ProvisionStackOptions) error {
	if strings.TrimSpace(opts.TenantID) == "" {
		return fmt.Errorf("stack lifecycle tenant id is required")
	}
	if strings.TrimSpace(opts.OwnerID) == "" {
		return fmt.Errorf("stack lifecycle owner id is required")
	}
	return nil
}

func (o *Orchestrator) managedRuntimeDecommissionRequired(ctx context.Context, stack *orchestratorStack) (bool, error) {
	if stack == nil {
		return false, errors.New("stack is missing")
	}
	if payloadHasManagedRuntimeLease(stackManagedRuntimePayload(stack)) {
		return true, nil
	}
	if o == nil || o.leaseLister == nil {
		// Local/BYO stacks can retire their own control-plane projection without
		// a managed lease authority. Explicit managed intent still waits
		// fail-closed; when a lister exists we always query it so legacy or
		// quarantined leases remain discoverable even on older stack configs.
		if !managedProviderIntent(stack.config) {
			return false, nil
		}
		return false, errors.New("authority-aware lease inventory is not configured")
	}
	tenantID := strings.TrimSpace(stack.tenantID)
	if tenantID == "" {
		return false, errors.New("stack tenant is missing")
	}
	records, err := o.leaseLister.ListInventoryByTenant(ctx, tenantID)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		lease := record.Lease
		if strings.TrimSpace(lease.Metadata["stack_id"]) != strings.TrimSpace(stack.id) ||
			!managedRuntimeLeaseVisibleToStackOwner(lease, stack) {
			continue
		}
		_, managedProvider := serverruntime.MonthlyRuntimeProfileForProvider(strings.ToLower(strings.TrimSpace(lease.Resource.ProviderID)))
		customerSubstrate := record.ExecutionAuthority == vmleases.LeaseExecutionAuthorityTechStackProviderControl &&
			lease.CustodyClass == vmlease.CustodyCustomerSubstrate
		if monthlyruntime.IsMonthlyRuntimeMetadata(lease.Metadata) || managedProvider || customerSubstrate {
			// Cancellation, archived desired state, and a missing local workspace do
			// not prove that the provider resource is gone. Historical/quarantined
			// rows therefore still require native terminal decommission read-back.
			return true, nil
		}
	}
	return false, nil
}

// DurableReconciliationReady reports whether exact reconcile_lease work has
// restart-safe and HA-fenced custody. The generic in-process queue currently
// does not: its Postgres job schema does not admit reconcile_lease and it has no
// safe stale-running provider-side-effect reclaim. Force must therefore stay
// disabled instead of pretending that an enqueue survived a restart.
func (o *Orchestrator) DurableReconciliationReady() bool {
	return false
}

// EnqueueManagedLeaseReconciliation remains the future adapter seam, but is
// deliberately fail-closed until DurableReconciliationReady can truthfully
// return true. In particular this method must not create an unpersistable
// reconcile_lease row or admit process-local work.
func (o *Orchestrator) EnqueueManagedLeaseReconciliation(ctx context.Context, stackID, tenantID, ownerID, leaseID, resourceGenerationDigest string) (string, error) {
	return "", ErrManagedLeaseReconciliationDurabilityUnavailable
}

// GetJobStatus returns the current status of a job.
func (o *Orchestrator) GetJobStatus(jobID string) (*jobs.Job, error) {
	job, ok := o.queue.Get(jobID)
	if !ok {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}
	return job.DetachedCopy(), nil
}

// GetQueueStats returns queue statistics.
func (o *Orchestrator) GetQueueStats() map[string]int {
	return o.queue.Stats()
}
