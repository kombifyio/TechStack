package providercontrol

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReconcilerAdvancesEachNativeOperationAtMostOnce(t *testing.T) {
	failed := errors.New("provider unavailable")
	application := &recordingOperationApplication{failures: map[string]error{
		"operation-b": failed,
		"operation-c": ErrProvisionManualReconcile,
	}}
	lister := staticOperationLister{operations: []OperationRef{
		{TenantID: "tenant-1", OperationID: "operation-a"},
		{TenantID: "tenant-1", OperationID: "operation-b"},
		{TenantID: "tenant-1", OperationID: "operation-c"},
	}}
	reconciler, err := NewReconciler(ReconcilerConfig{Application: application, Operations: lister, BatchSize: 3})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	report, err := reconciler.RunTenantOnce(t.Context(), "tenant-1")
	if err != nil {
		t.Fatalf("RunTenantOnce: %v", err)
	}
	if report.Scanned != 3 || report.Advanced != 1 || report.ManualReconcileRequired != 1 ||
		len(report.Failures) != 1 || !errors.Is(report.Failures[0].Err, failed) {
		t.Fatalf("report = %+v", report)
	}
	if application.calls["operation-a"] != 1 || application.calls["operation-b"] != 1 || application.calls["operation-c"] != 1 {
		t.Fatalf("advance calls = %#v", application.calls)
	}
}

func TestReconcilerRejectsUnboundedBatch(t *testing.T) {
	_, err := NewReconciler(ReconcilerConfig{
		Application: &recordingOperationApplication{},
		Operations:  staticOperationLister{},
		BatchSize:   101,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("NewReconciler error = %v, want ErrInvalidRequest", err)
	}
}

func TestWorkerDiscoversRunnableTenantsByBoundedCursor(t *testing.T) {
	application := &recordingOperationApplication{}
	reconciler, err := NewReconciler(ReconcilerConfig{
		Application: application,
		Operations: tenantOperationLister{operations: map[string][]OperationRef{
			"tenant-a": {{TenantID: "tenant-a", OperationID: "operation-a"}},
			"tenant-b": {{TenantID: "tenant-b", OperationID: "operation-b"}},
		}},
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	source := &pagedTenantSource{pages: map[string]RunnableTenantPage{
		"":         {TenantIDs: []string{"tenant-a"}, NextCursor: "tenant-a"},
		"tenant-a": {TenantIDs: []string{"tenant-b"}},
	}}
	worker, err := NewWorker(WorkerConfig{
		Reconciler: reconciler, TenantSource: source, TenantBatch: 1, Interval: time.Second,
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	worker.RunOnce(t.Context())
	worker.RunOnce(t.Context())
	if got := source.cursors; len(got) != 2 || got[0] != "" || got[1] != "tenant-a" {
		t.Fatalf("tenant cursors = %#v", got)
	}
	if application.calls["operation-a"] != 1 || application.calls["operation-b"] != 1 {
		t.Fatalf("advance calls = %#v", application.calls)
	}
}

func TestWorkerReportsTenantDiscoveryFailureWithoutAdvancing(t *testing.T) {
	reconciler, err := NewReconciler(ReconcilerConfig{
		Application: &recordingOperationApplication{}, Operations: staticOperationLister{},
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	want := errors.New("runtime role cannot discover tenants")
	var reported error
	worker, err := NewWorker(WorkerConfig{
		Reconciler:   reconciler,
		TenantSource: &pagedTenantSource{err: want},
		Interval:     time.Second,
		OnError:      func(err error) { reported = err },
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	worker.RunOnce(t.Context())
	if !errors.Is(reported, want) {
		t.Fatalf("reported error = %v, want %v", reported, want)
	}
}

func TestWorkerRequiresDynamicTenantSource(t *testing.T) {
	_, err := NewWorker(WorkerConfig{Reconciler: &Reconciler{}, Interval: time.Second})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("NewWorker error = %v, want ErrInvalidRequest", err)
	}
}

type staticOperationLister struct {
	operations []OperationRef
	err        error
}

type tenantOperationLister struct {
	operations map[string][]OperationRef
}

func (l tenantOperationLister) ListRunnableOperations(_ context.Context, tenantID string, _ int) ([]OperationRef, error) {
	return append([]OperationRef(nil), l.operations[tenantID]...), nil
}

type pagedTenantSource struct {
	pages   map[string]RunnableTenantPage
	cursors []string
	err     error
}

func (s *pagedTenantSource) ListRunnableTenants(_ context.Context, cursor string, _ int) (RunnableTenantPage, error) {
	s.cursors = append(s.cursors, cursor)
	if s.err != nil {
		return RunnableTenantPage{}, s.err
	}
	return s.pages[cursor], nil
}

func (l staticOperationLister) ListRunnableOperations(context.Context, string, int) ([]OperationRef, error) {
	return append([]OperationRef(nil), l.operations...), l.err
}

type recordingOperationApplication struct {
	calls    map[string]int
	failures map[string]error
}

func (*recordingOperationApplication) Start(context.Context, StartRequest) (OperationRecord, bool, error) {
	return OperationRecord{}, false, nil
}

func (*recordingOperationApplication) Get(context.Context, string, string) (OperationRecord, error) {
	return OperationRecord{}, nil
}

func (a *recordingOperationApplication) Advance(_ context.Context, _, operationID string) (OperationRecord, bool, error) {
	if a.calls == nil {
		a.calls = make(map[string]int)
	}
	a.calls[operationID]++
	if err := a.failures[operationID]; err != nil {
		return OperationRecord{}, false, err
	}
	return OperationRecord{}, true, nil
}
