package providercontroljobs

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type rejectingOperationApplication struct{ t *testing.T }

func (a rejectingOperationApplication) Start(context.Context, providercontrol.StartRequest) (providercontrol.OperationRecord, bool, error) {
	a.t.Fatal("terminal proof replay must not start a provider operation")
	return providercontrol.OperationRecord{}, false, nil
}

func (a rejectingOperationApplication) Get(context.Context, string, string) (providercontrol.OperationRecord, error) {
	a.t.Fatal("terminal proof replay must not read a provider operation")
	return providercontrol.OperationRecord{}, nil
}

func (a rejectingOperationApplication) Advance(context.Context, string, string) (providercontrol.OperationRecord, bool, error) {
	a.t.Fatal("terminal proof replay must not advance a provider operation")
	return providercontrol.OperationRecord{}, false, nil
}

type existingDecommissionApplication struct {
	record              providercontrol.OperationRecord
	start, get, advance int
}

func (a *existingDecommissionApplication) Start(context.Context, providercontrol.StartRequest) (providercontrol.OperationRecord, bool, error) {
	a.start++
	return providercontrol.OperationRecord{}, false, providercontrol.ErrOperationNotFound
}

func (a *existingDecommissionApplication) Get(_ context.Context, _, operationID string) (providercontrol.OperationRecord, error) {
	a.get++
	if operationID != a.record.Command.OperationID {
		return providercontrol.OperationRecord{}, providercontrol.ErrOperationNotFound
	}
	return a.record, nil
}

func (a *existingDecommissionApplication) Advance(context.Context, string, string) (providercontrol.OperationRecord, bool, error) {
	a.advance++
	return a.record, false, nil
}

func TestNativeDecommissionContinuesExistingOperationWithoutReconstructingItsRequest(t *testing.T) {
	candidate := nativeDecommissionCandidate{TenantID: "tenant", LeaseID: "lease", ResourceGeneration: "11111111-2222-4333-8444-555555555555"}
	key := nativeDecommissionIdempotencyKey(candidate)
	application := &existingDecommissionApplication{record: providercontrol.OperationRecord{
		Command: providerexecutor.Command{Operation: providerexecutor.OperationDecommission, OperationID: providerexecutor.ComputeOperationID(candidate.TenantID, candidate.LeaseID, candidate.ResourceGeneration, providerexecutor.OperationDecommission, key)},
		Head:    providerexecutor.Receipt{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted},
	}}
	_, terminal, handled, err := (&NativeDecommissioner{application: application}).convergeCandidate(t.Context(), candidate)
	if err != nil || terminal || !handled || application.start != 0 || application.get != 1 || application.advance != 1 {
		t.Fatalf("existing operation continuation = terminal:%t handled:%t err:%v calls:%d/%d/%d", terminal, handled, err, application.start, application.get, application.advance)
	}
}

func TestDecommissionManagedLeasesReplaysTerminalProofWithoutLifecycleRollback(t *testing.T) {
	const (
		tenantID    = "tenant-replay"
		ownerID     = "owner-replay"
		stackID     = "stack-replay"
		leaseID     = "lease-replay"
		serverID    = "server-replay"
		generation  = "11111111-2222-4333-8444-555555555555"
		provisionOp = "operation-provision-replay"
		destroyOp   = "operation-destroy-replay"
		receipt     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	now := time.Date(2026, 8, 9, 16, 48, 0, 0, time.UTC)
	lease := vmlease.Lease{
		ID:             leaseID,
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: ownerID, OrgID: tenantID},
		Resource:       vmlease.ResourceRef{ProviderID: "centron", VMID: "provider-server-replay"},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		Metadata:       map[string]string{vmleases.MetadataKeyResourceGenerationID: generation, "stack_id": stackID},
	}
	leaseJSON, err := json.Marshal(lease)
	if err != nil {
		t.Fatalf("marshal lease: %v", err)
	}
	generationDigest, err := vmleases.ResourceGenerationDigest(tenantID, lease)
	if err != nil {
		t.Fatalf("resource generation digest: %v", err)
	}
	updatedLease := lease
	updatedLease.DesiredState = vmlease.DesiredStateArchived
	updatedLease.CancelledAt = &now
	updatedLease.Metadata = map[string]string{
		vmleases.MetadataKeyResourceGenerationID:    generation,
		vmleases.MetadataKeyDecommissionClaimDigest: generationDigest,
		"stack_id": stackID,
	}
	updatedLeaseJSON, err := json.Marshal(updatedLease)
	if err != nil {
		t.Fatalf("marshal updated lease: %v", err)
	}

	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer database.Close()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).
		WithArgs(tenantID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT\s+lease\.id,.*FROM techstack_vm_leases AS lease`).
		WithArgs(tenantID, leaseID, stackID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "lease_revision", "server_id", "resource_generation_id", "provider_id",
			"owner_subject_id", "lease_json", "cancelled_at", "revision", "generation",
			"lifecycle_state", "desired_state", "stack_id",
		}).AddRow(leaseID, int64(1), serverID, generation, "centron", ownerID,
			string(leaseJSON), now, int64(7), int64(3), "decommissioned", "absent", stackID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT clock_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(now))
	mock.ExpectQuery(`(?s)WITH cancellation AS.*UPDATE techstack_vm_leases AS lease.*'\{desired_state\}'.*to_jsonb\('archived'::text\).*'\{cancelled_at\}'.*RETURNING lease\.cancelled_at, lease\.lease_json::text`).
		WithArgs(tenantID, leaseID, uint64(1), generation, generationDigest).
		WillReturnRows(sqlmock.NewRows([]string{"cancelled_at", "lease_json"}).AddRow(now, string(updatedLeaseJSON)))
	mock.ExpectQuery(`(?s)SELECT count\(\*\).*FROM provider_operations`).
		WithArgs(tenantID, leaseID, nativeDecommissionBaseIdempotencyKey(leaseID, generation), nativeDecommissionAttemptWindow.Seconds()).
		WillReturnRows(sqlmock.NewRows([]string{"count", "recent"}).AddRow(0, 0))
	mock.ExpectQuery(`(?s)SELECT DISTINCT.*FROM provider_operations AS operation`).
		WithArgs(tenantID, leaseID, generation).
		WillReturnRows(sqlmock.NewRows([]string{
			"operation_id", "binding_id", "kind", "native_ref", "parent_binding_id", "ownership_hash", "disposition",
		}).AddRow(provisionOp, "server", "server", "provider-server-replay", "", "ownership-replay", "delete"))
	mock.ExpectCommit()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).
		WithArgs(tenantID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT release\.release_operation_id,.*FROM managed_runtime_capacity_release_facts AS release`).
		WithArgs(tenantID, leaseID, generation, serverID).
		WillReturnRows(sqlmock.NewRows([]string{
			"release_operation_id", "receipt_sequence", "receipt_digest", "released_at", "release_authority", "lifecycle_state",
		}).AddRow(destroyOp, uint64(5), receipt, now, "provider_absence", "decommissioned"))
	mock.ExpectCommit()

	decommissioner, err := NewNativeDecommissioner(NativeDecommissionConfig{
		Database: database, Application: rejectingOperationApplication{t: t}, Ledger: &providercontrol.PostgresLedger{},
		ProvisionResolution: unavailableProvisionResolution{}, ResolutionSubject: "test:provider-control-worker",
	})
	if err != nil {
		t.Fatalf("NewNativeDecommissioner: %v", err)
	}
	result, err := decommissioner.DecommissionManagedLeases(t.Context(), jobs.ManagedLeaseDecommissionRequest{
		TenantID: tenantID, OwnerID: ownerID, StackID: stackID, LeaseID: leaseID,
		ResourceGenerationDigest: generationDigest,
	})
	if err != nil {
		t.Fatalf("DecommissionManagedLeases: %v", err)
	}
	if result.Decommissioned != 1 || len(result.LeaseIDs) != 1 || result.LeaseIDs[0] != leaseID || len(result.Proofs) != 1 {
		t.Fatalf("terminal replay result = %+v", result)
	}
	proof := result.Proofs[0]
	if proof.ObservedState != jobs.ManagedLeaseDecommissionObservedDecommissioned ||
		proof.ReceiptDigest != receipt[len("sha256:"):] || proof.ResourceGenerationDigest != generationDigest {
		t.Fatalf("terminal replay proof = %+v", proof)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}
