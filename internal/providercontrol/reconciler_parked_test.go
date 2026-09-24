package providercontrol

import (
	"context"
	"testing"
)

type parkedApplication struct{ advanced map[string]bool }

func (parkedApplication) Start(context.Context, StartRequest) (OperationRecord, bool, error) {
	return OperationRecord{}, false, nil
}

func (parkedApplication) Get(context.Context, string, string) (OperationRecord, error) {
	return OperationRecord{}, nil
}

func (a parkedApplication) Advance(_ context.Context, _ string, operationID string) (OperationRecord, bool, error) {
	if a.advanced[operationID] {
		return OperationRecord{}, true, nil
	}
	return OperationRecord{}, false, ErrProvisionManualReconcile
}

type staticOperations []OperationRef

func (o staticOperations) ListRunnableOperations(context.Context, string, int) ([]OperationRef, error) {
	return []OperationRef(o), nil
}

type staticTenants []string

func (t staticTenants) ListRunnableTenants(context.Context, string, int) (RunnableTenantPage, error) {
	return RunnableTenantPage{TenantIDs: []string(t)}, nil
}

// A parked provision advances no further on its own. Counting it without
// naming it made a stalled creation look identical to an idle queue.
func TestParkedOperationsAreNamedNotOnlyCounted(t *testing.T) {
	operations := staticOperations{
		{TenantID: "tenant-a", OperationID: "op-parked"},
		{TenantID: "tenant-a", OperationID: "op-moving"},
	}
	reconciler, err := NewReconciler(ReconcilerConfig{
		Application: parkedApplication{advanced: map[string]bool{"op-moving": true}},
		Operations:  operations,
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	report, err := reconciler.RunTenantOnce(t.Context(), "tenant-a")
	if err != nil {
		t.Fatalf("RunTenantOnce: %v", err)
	}
	if report.ManualReconcileRequired != 1 || len(report.ManualReconcile) != 1 {
		t.Fatalf("parked count=%d named=%d, want 1 and 1",
			report.ManualReconcileRequired, len(report.ManualReconcile))
	}
	if report.ManualReconcile[0].OperationID != "op-parked" {
		t.Fatalf("named the wrong operation: %+v", report.ManualReconcile[0])
	}
	if report.Advanced != 1 {
		t.Fatalf("advanced=%d, want 1 (parking must not stop the batch)", report.Advanced)
	}
	if len(report.Failures) != 0 {
		t.Fatalf("parking must not be reported as a failure: %+v", report.Failures)
	}
}

// Parking must reach an observer. Routing it through OnError would report
// designed at-most-once behavior as a fault, so it has its own sink.
func TestWorkerReportsParkedOperationsSeparatelyFromErrors(t *testing.T) {
	reconciler, err := NewReconciler(ReconcilerConfig{
		Application: parkedApplication{},
		Operations:  staticOperations{{TenantID: "tenant-a", OperationID: "op-parked"}},
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	var parked []OperationRef
	var errors []error
	worker, err := NewWorker(WorkerConfig{
		Reconciler:   reconciler,
		TenantSource: staticTenants{"tenant-a"},
		OnError:      func(e error) { errors = append(errors, e) },
		OnParked:     func(o OperationRef) { parked = append(parked, o) },
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	worker.RunOnce(t.Context())
	if len(parked) != 1 || parked[0].OperationID != "op-parked" {
		t.Fatalf("parked observations = %+v, want exactly op-parked", parked)
	}
	if len(errors) != 0 {
		t.Fatalf("parking must not surface as an error: %+v", errors)
	}
}
