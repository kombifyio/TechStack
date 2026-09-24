package providercontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultReconcileBatch    = 16
	defaultTenantBatch       = 32
	defaultReconcileInterval = 15 * time.Second
)

// ReconcilerConfig supplies the application boundary and runnable projection.
type ReconcilerConfig struct {
	Application OperationApplication
	Operations  RunnableOperationLister
	BatchSize   int
}

// ReconcileFailure records one operation error without aborting the batch.
type ReconcileFailure struct {
	Operation OperationRef
	Err       error
}

// ReconcileReport describes one bounded tenant reconciliation pass.
//
// ManualReconcileRequired counts operations the automation deliberately parked
// after dispatch. ManualReconcile names them: a parked provision advances no
// further on its own, so leaving it unnamed makes a stalled creation
// indistinguishable from an idle queue.
type ReconcileReport struct {
	Scanned                 int
	Advanced                int
	ManualReconcileRequired int
	ManualReconcile         []OperationRef
	Failures                []ReconcileFailure
}

// Reconciler advances each selected operation by at most one receipt.
type Reconciler struct {
	application OperationApplication
	operations  RunnableOperationLister
	batchSize   int
}

// NewReconciler creates a bounded native operation reconciler.
func NewReconciler(cfg ReconcilerConfig) (*Reconciler, error) {
	if cfg.Application == nil || cfg.Operations == nil {
		return nil, fmt.Errorf("%w: application and runnable-operation lister are required", ErrInvalidRequest)
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = defaultReconcileBatch
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > 100 {
		return nil, fmt.Errorf("%w: reconcile batch must be between 1 and 100", ErrInvalidRequest)
	}
	return &Reconciler{application: cfg.Application, operations: cfg.Operations, batchSize: cfg.BatchSize}, nil
}

// RunTenantOnce advances each selected operation by at most one receipt.
func (r *Reconciler) RunTenantOnce(ctx context.Context, tenantID string) (ReconcileReport, error) {
	if r == nil || r.application == nil || r.operations == nil {
		return ReconcileReport{}, fmt.Errorf("%w: reconciler is not configured", ErrInvalidRequest)
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return ReconcileReport{}, fmt.Errorf("%w: tenant id is required", ErrInvalidRequest)
	}
	operations, err := r.operations.ListRunnableOperations(ctx, tenantID, r.batchSize)
	if err != nil {
		return ReconcileReport{}, err
	}
	report := ReconcileReport{Scanned: len(operations)}
	for _, operation := range operations {
		_, advanced, advanceErr := r.application.Advance(ctx, operation.TenantID, operation.OperationID)
		if advanceErr != nil {
			if errors.Is(advanceErr, ErrProvisionManualReconcile) {
				report.ManualReconcileRequired++
				report.ManualReconcile = append(report.ManualReconcile, operation)
				continue
			}
			report.Failures = append(report.Failures, ReconcileFailure{Operation: operation, Err: advanceErr})
			continue
		}
		if advanced {
			report.Advanced++
		}
	}
	return report, nil
}

// RunnableTenantPage is one bounded, stable tenant-discovery page. NextCursor
// is empty after the final page; callers then start the next sweep at the
// beginning. Tenant identifiers are secret-free scheduler coordinates, not an
// authorization grant.
type RunnableTenantPage struct {
	TenantIDs  []string
	NextCursor string
}

// RunnableTenantSource discovers tenant scopes which currently own runnable
// native operations. Production implementations require the dedicated
// provider-control runtime role; a request-scoped tenant connection is not a
// valid global scheduler authority.
type RunnableTenantSource interface {
	ListRunnableTenants(context.Context, string, int) (RunnableTenantPage, error)
}

// DueProviderDecommissionTenantSource discovers only tenant IDs with a due
// durable destroy wait. It is intentionally separate from RunnableTenantSource:
// a provider operation may be terminal and leave the operation scheduler
// before the queue has consumed its persisted waiting receipt.
type DueProviderDecommissionTenantSource interface {
	ListDueProviderDecommissionTenants(context.Context, string, int) (RunnableTenantPage, error)
}

// WorkerConfig supplies durable tenant discovery, polling, and an error sink.
//
// OnParked observes operations the automation deliberately parked for manual
// reconciliation. It is separate from OnError because parking is a designed
// at-most-once outcome, not a failure; routing it through OnError would report
// safe behavior as a fault, and dropping it entirely hides a stalled create.
type WorkerConfig struct {
	Reconciler   *Reconciler
	TenantSource RunnableTenantSource
	TenantBatch  int
	Interval     time.Duration
	OnError      func(error)
	OnParked     func(OperationRef)
}

// Worker runs bounded, dynamically tenant-scoped reconciliation until
// cancellation. It retains only a pagination cursor; operation custody and
// cross-replica exclusion remain in the durable ledger claims.
type Worker struct {
	reconciler   *Reconciler
	tenantSource RunnableTenantSource
	tenantBatch  int
	interval     time.Duration
	onError      func(error)
	onParked     func(OperationRef)
	cursor       string
}

// NewWorker creates a dynamically tenant-scoped bounded reconciliation worker.
func NewWorker(cfg WorkerConfig) (*Worker, error) {
	if cfg.Reconciler == nil || cfg.TenantSource == nil {
		return nil, fmt.Errorf("%w: reconciler and tenant source are required", ErrInvalidRequest)
	}
	if cfg.Interval == 0 {
		cfg.Interval = defaultReconcileInterval
	}
	if cfg.Interval < time.Second {
		return nil, fmt.Errorf("%w: worker interval must be at least one second", ErrInvalidRequest)
	}
	if cfg.TenantBatch == 0 {
		cfg.TenantBatch = defaultTenantBatch
	}
	if cfg.TenantBatch < 1 || cfg.TenantBatch > 100 {
		return nil, fmt.Errorf("%w: tenant batch must be between 1 and 100", ErrInvalidRequest)
	}
	return &Worker{
		reconciler: cfg.Reconciler, tenantSource: cfg.TenantSource,
		tenantBatch: cfg.TenantBatch, interval: cfg.Interval, onError: cfg.OnError,
		onParked: cfg.OnParked,
	}, nil
}

// RunOnce performs one bounded tenant-discovery page. A later pass resumes at
// the returned cursor; the final page clears the cursor for the next sweep.
func (w *Worker) RunOnce(ctx context.Context) {
	if w == nil || w.reconciler == nil || w.tenantSource == nil {
		return
	}
	page, err := w.tenantSource.ListRunnableTenants(ctx, w.cursor, w.tenantBatch)
	if err != nil {
		w.report(err)
		return
	}
	w.cursor = strings.TrimSpace(page.NextCursor)
	seen := make(map[string]struct{}, len(page.TenantIDs))
	for _, tenantID := range page.TenantIDs {
		tenantID = strings.TrimSpace(tenantID)
		if tenantID == "" {
			w.report(fmt.Errorf("providercontrol: tenant source returned an empty tenant id"))
			continue
		}
		if _, duplicate := seen[tenantID]; duplicate {
			w.report(fmt.Errorf("providercontrol: tenant source returned duplicate tenant %q", tenantID))
			continue
		}
		seen[tenantID] = struct{}{}
		report, err := w.reconciler.RunTenantOnce(ctx, tenantID)
		if err != nil {
			w.report(err)
			continue
		}
		for _, failure := range report.Failures {
			w.report(fmt.Errorf("providercontrol: reconcile %s/%s: %w", failure.Operation.TenantID, failure.Operation.OperationID, failure.Err))
		}
		for _, parked := range report.ManualReconcile {
			w.reportParked(parked)
		}
	}
}

// Run performs an immediate pass and then polls until cancellation.
func (w *Worker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	w.RunOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

func (w *Worker) report(err error) {
	if err != nil && w.onError != nil {
		w.onError(err)
	}
}

func (w *Worker) reportParked(operation OperationRef) {
	if w.onParked != nil {
		w.onParked(operation)
	}
}
