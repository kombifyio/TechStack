package substrateruntime_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	providerexecutor "github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/kombifyio/techstack/internal/substrateruntime"
)

type retainedWorker struct {
	results  map[string]providerexecutor.WorkerExecutionResult
	creates  int
	imported substrate.ImageImport
}

func (w *retainedWorker) ReadWorkerExecutionResult(_ context.Context, _ string, id string) (providerexecutor.WorkerExecutionResult, bool, error) {
	result, ok := w.results[id]
	return result, ok, nil
}
func (w *retainedWorker) SendWorkerExecution(_ context.Context, command providerexecutor.WorkerExecution) (providerexecutor.WorkerExecutionResult, error) {
	result := providerexecutor.WorkerExecutionResult{InvocationID: command.InvocationID, CommandDigest: command.Command.CommandDigest, Accepted: command.Phase == providerexecutor.WorkerPhaseSubmit, Complete: true, Succeeded: true}
	if command.Phase == providerexecutor.WorkerPhaseObserve {
		if command.Stage == "guest" {
			result.Observation, _ = json.Marshal(substrate.GuestObservation{Present: true, ID: 1100, Status: "running", DiskAttached: true, Addresses: []string{"192.0.2.10"}})
		}
		return result, nil
	}
	switch command.Stage {
	case "image_import":
		result.TaskRef = w.imported.TaskRef
		result.Observation, _ = json.Marshal(w.imported)
	case "create":
		w.creates++
		result.TaskRef = "UPID:pve:create"
	}
	w.results[command.InvocationID] = result
	return result, nil
}

// Stable core invariant: losing the provider ledger append after all Guard
// stages completed must reuse the retained stages rather than create again.
func TestProvisionRecoversDurableStagesAfterLostLedgerAppend(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	image, _ := substrate.DefaultImage("haos")
	spec := substrate.ExecutionSpec{Action: "provision", Guest: substrate.GuestSpec{GuestIdentity: substrate.GuestIdentity{ID: 1100, Name: "kombify-home", OperationTag: "kombify-op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, ProfileID: "haos", ImageImport: substrate.ImageImport{Image: image, Storage: "local"}}}
	payload, _ := json.Marshal(spec)
	for range 2 {
		mock.ExpectBegin()
		mock.ExpectExec("SELECT set_config").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("SELECT spec.spec_json").WillReturnRows(sqlmock.NewRows([]string{"spec_json", "native_ref"}).AddRow(payload, "substrate/1100"))
		mock.ExpectRollback()
	}
	worker := &retainedWorker{results: map[string]providerexecutor.WorkerExecutionResult{}, imported: substrate.ImageImport{Image: image, Storage: "local", TaskRef: "UPID:pve:import"}}
	adapter, err := substrateruntime.NewAdapter(database, worker)
	if err != nil {
		t.Fatal(err)
	}
	inv := providercontrol.AdapterInvocation{Key: "head-one", Request: providerexecutor.ExecutionRequest{Command: providerexecutor.Command{Operation: providerexecutor.OperationProvision, OperationID: "provision-one", TenantID: "tenant", LeaseID: "lease", CommandDigest: "digest", ConnectionRef: "provider-connection://substrate/worker/lease", ResourceGenerationID: "generation"}, Previous: providerexecutor.Receipt{Phase: providerexecutor.PhaseAccepted}}}
	for range 2 {
		result := adapter.ExecuteCrashRecoverableMutation(context.Background(), inv)
		if result.Phase != providerexecutor.PhaseResourcesBound {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	if worker.creates != 1 {
		t.Fatalf("recovery submitted %d creates", worker.creates)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
