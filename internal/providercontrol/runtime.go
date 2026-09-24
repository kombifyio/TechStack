package providercontrol

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

// FailClosedProfileResolver is the safe bootstrap resolver used until an
// authoritative catalog and credential-custody resolver is injected.
type FailClosedProfileResolver struct{ Reason string }

// ResolveExecutionProfile always rejects because no authoritative resolver exists.
func (r FailClosedProfileResolver) ResolveExecutionProfile(context.Context, ProfileRequest) (ExecutionProfile, error) {
	reason := strings.TrimSpace(r.Reason)
	if reason == "" {
		reason = "no authoritative execution profile resolver is configured"
	}
	return ExecutionProfile{}, fmt.Errorf("%w: %s", ErrProfileUnavailable, reason)
}

// RuntimeConfig supplies durable, injectable provider-control dependencies. It
// deliberately has no Simulate URL, legacy executor, or fallback setting.
type RuntimeConfig struct {
	Database                            *sql.DB
	Registry                            *Registry
	ProfileResolver                     ExecutionProfileResolver
	ActivationGate                      MutationActivationGate
	EvidenceVerifier                    providerexecutor.EvidenceVerifier
	Now                                 func() time.Time
	ClaimOwner                          string
	ClaimTTL                            time.Duration
	ReconcileBatch                      int
	TenantSource                        RunnableTenantSource
	TenantBatch                         int
	WorkerInterval                      time.Duration
	OnWorkerError                       func(error)
	OnWorkerParked                      func(OperationRef)
	AutomaticProvisionResolutionSubject string
	TerminalFailureObserver             TerminalFailureObserver
}

// Runtime owns the durable ledger, adapter registry, application service, and
// bounded reconciler used by TechStack provider control.
type Runtime struct {
	registry    *Registry
	ledger      *PostgresLedger
	coordinator *Coordinator
	application *ApplicationService
	reconciler  *Reconciler
	worker      *Worker
	activation  MutationActivationGate
	auxiliary   []func(context.Context)
}

// NewRuntime constructs the fail-closed production composition. A verifier and
// database are mandatory even when no adapter is admitted yet.
func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	registry := cfg.Registry
	if registry == nil {
		registry = NewRegistry()
	}
	if nilEvidenceVerifier(cfg.EvidenceVerifier) {
		return nil, ErrEvidenceVerifier
	}
	ledger, err := NewPostgresLedger(cfg.Database, cfg.EvidenceVerifier)
	if err != nil {
		return nil, err
	}
	profiles := cfg.ProfileResolver
	if profiles == nil {
		profiles = FailClosedProfileResolver{}
	}
	activation := cfg.ActivationGate
	if nilMutationActivationGate(activation) {
		activation = ProductionBlockedGate{}
	}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: profiles, Ledger: ledger,
		ActivationGate:   activation,
		EvidenceVerifier: cfg.EvidenceVerifier, Now: cfg.Now,
		ClaimOwner: cfg.ClaimOwner, ClaimTTL: cfg.ClaimTTL,
		TerminalFailureObserver: cfg.TerminalFailureObserver,
	})
	if err != nil {
		return nil, err
	}
	application, err := NewApplicationService(coordinator, ledger)
	if err != nil {
		return nil, err
	}
	reconciler, err := NewReconciler(ReconcilerConfig{
		Application: application,
		Operations:  ledger,
		BatchSize:   cfg.ReconcileBatch,
	})
	if err != nil {
		return nil, err
	}
	tenantSource := cfg.TenantSource
	if tenantSource == nil {
		tenantSource = ledger
	}
	runtime := &Runtime{
		registry: registry, ledger: ledger, coordinator: coordinator, application: application,
		reconciler: reconciler, activation: activation,
	}
	var automaticResolver *automaticProvisionResolver
	if strings.TrimSpace(cfg.AutomaticProvisionResolutionSubject) != "" {
		automaticResolver, err = newAutomaticProvisionResolver(
			runtime, cfg.AutomaticProvisionResolutionSubject, cfg.OnWorkerError,
		)
		if err != nil {
			return nil, err
		}
	}
	worker, err := NewWorker(WorkerConfig{
		Reconciler: reconciler, TenantSource: tenantSource,
		TenantBatch: cfg.TenantBatch, Interval: cfg.WorkerInterval, OnError: cfg.OnWorkerError,
		OnParked: runtimeParkedHandler(cfg.OnWorkerParked, automaticResolver),
	})
	if err != nil {
		return nil, err
	}
	runtime.worker = worker
	if automaticResolver != nil {
		runtime.auxiliary = append(runtime.auxiliary, automaticResolver.run)
	}
	return runtime, nil
}

func runtimeParkedHandler(reporter func(OperationRef), resolver *automaticProvisionResolver) func(OperationRef) {
	return func(operation OperationRef) {
		if reporter != nil {
			reporter(operation)
		}
		if resolver != nil {
			resolver.enqueue(operation)
		}
	}
}

func nilEvidenceVerifier(verifier providerexecutor.EvidenceVerifier) bool {
	if verifier == nil {
		return true
	}
	value := reflect.ValueOf(verifier)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Registry returns the explicit crash-recoverable adapter registry.
func (r *Runtime) Registry() *Registry {
	if r == nil {
		return nil
	}
	return r.registry
}

// Ledger returns the durable native provider-control ledger.
func (r *Runtime) Ledger() *PostgresLedger {
	if r == nil {
		return nil
	}
	return r.ledger
}

// LoadProvisionResolutionOperation resolves the immutable operation target
// used by the hosted authorization layer before any provider read occurs.
func (r *Runtime) LoadProvisionResolutionOperation(ctx context.Context, tenantID, operationID string) (OperationRecord, error) {
	if r == nil || r.ledger == nil {
		return OperationRecord{}, fmt.Errorf("%w: provider-control runtime is not configured", ErrInvalidRequest)
	}
	return r.ledger.LoadOperation(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(operationID))
}

// DiscoverProvisionOperation performs the hosted workflow's first, read-only
// provider observation step.
func (r *Runtime) DiscoverProvisionOperation(ctx context.Context, request ProvisionDiscoveryWorkflowRequest) (ProvisionDiscoveryWorkflowResult, error) {
	workflow, err := r.provisionResolutionWorkflow(ctx, request.TenantID, request.OperationID)
	if err != nil {
		return ProvisionDiscoveryWorkflowResult{}, err
	}
	return workflow.Discover(ctx, request)
}

// CommitProvisionOperation performs the separately confirmed append-only CAS
// against an already persisted discovery observation.
func (r *Runtime) CommitProvisionOperation(ctx context.Context, request ProvisionResolutionWorkflowRequest) (ProvisionResolutionWorkflowResult, error) {
	workflow, err := r.provisionResolutionWorkflow(ctx, request.TenantID, request.OperationID)
	if err != nil {
		return ProvisionResolutionWorkflowResult{}, err
	}
	return workflow.Commit(ctx, request)
}

// ResolveProvisionOperation runs the existing certified discovery and
// append-only decision workflow as one internal application operation. It is
// used by autonomous convergence paths; hosted transports retain the separate
// Discover and Commit methods for explicit operator confirmation.
func (r *Runtime) ResolveProvisionOperation(ctx context.Context, request ProvisionResolutionWorkflowRequest) (ProvisionResolutionWorkflowResult, error) {
	workflow, err := r.provisionResolutionWorkflow(ctx, request.TenantID, request.OperationID)
	if err != nil {
		return ProvisionResolutionWorkflowResult{}, err
	}
	return workflow.Resolve(ctx, request)
}

func (r *Runtime) provisionResolutionWorkflow(ctx context.Context, tenantID, operationID string) (*ProvisionResolutionWorkflow, error) {
	if r == nil || r.ledger == nil || r.registry == nil {
		return nil, fmt.Errorf("%w: provider-control runtime is not configured", ErrInvalidRequest)
	}
	record, err := r.ledger.LoadOperation(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(operationID))
	if err != nil {
		return nil, err
	}
	provider, err := r.registry.ProvisionResolutionProviderForOperation(record)
	if err != nil {
		return nil, err
	}
	store, err := NewPostgresProvisionResolutionStore(r.ledger, provider)
	if err != nil {
		return nil, err
	}
	workflow, err := NewProvisionResolutionWorkflow(r.ledger, store, provider)
	if err != nil {
		return nil, err
	}
	return workflow, nil
}

// Coordinator returns the single claim and operation coordinator owned by
// this runtime. Native admission must use this exact instance so admission,
// reconciliation, and activation share one durable authority boundary.
func (r *Runtime) Coordinator() *Coordinator {
	if r == nil {
		return nil
	}
	return r.coordinator
}

// Application returns the internal Start/Get/Advance application service.
func (r *Runtime) Application() *ApplicationService {
	if r == nil {
		return nil
	}
	return r.application
}

// Reconciler returns the bounded native provider-operation reconciler.
func (r *Runtime) Reconciler() *Reconciler {
	if r == nil {
		return nil
	}
	return r.reconciler
}

// Worker returns the dynamically tenant-scoped native reconciliation worker.
func (r *Runtime) Worker() *Worker {
	if r == nil {
		return nil
	}
	return r.worker
}

// RegisterAuxiliaryWorker adds a lifecycle-bound recovery loop to the native
// provider-control process. Registration happens during composition, before
// Run starts; the activation gate still opens or rejects the whole runtime as
// one authority boundary.
func (r *Runtime) RegisterAuxiliaryWorker(run func(context.Context)) error {
	if r == nil || run == nil {
		return fmt.Errorf("providercontrol: auxiliary worker is required")
	}
	r.auxiliary = append(r.auxiliary, run)
	return nil
}

// Run performs an immediate bounded pass and reconciles until ctx is canceled.
// Provider side effects remain protected by operation claims and the runtime
// mutation-activation gate; starting this loop grants neither capability.
func (r *Runtime) Run(ctx context.Context) {
	if r == nil || r.worker == nil {
		return
	}
	if r.activation == nil {
		r.worker.report(ErrMutationActivationBlocked)
		return
	}
	if err := r.activation.Require(ctx); err != nil {
		r.worker.report(err)
		return
	}
	var auxiliary sync.WaitGroup
	for _, run := range r.auxiliary {
		auxiliary.Add(1)
		go func(run func(context.Context)) {
			defer auxiliary.Done()
			run(ctx)
		}(run)
	}
	r.worker.Run(ctx)
	auxiliary.Wait()
}
