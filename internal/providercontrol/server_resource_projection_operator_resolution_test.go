package providercontrol

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestAdoptedProvisionHeadAuthorizesExactDecommissionCustody(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	mock.ExpectBegin()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	resource := boundProviderResource{
		BindingID: "server", Kind: "centron.server", NativeRef: "centron-server://xv1/1",
		OwnershipHash: digest("owner"), Disposition: providerexecutor.DispositionDelete,
	}
	command := providerexecutor.Command{
		TenantID: "tenant-1", RuntimeServerID: "server-1", LeaseID: "lease-1",
		ResourceGenerationID: "0d72bfce-ea04-4516-9205-3cf1e78f7990",
		Operation:            providerexecutor.OperationDecommission,
		Targets: []providerexecutor.ResourceTarget{{
			BindingID: resource.BindingID, Kind: resource.Kind, NativeRef: resource.NativeRef,
			OwnershipHash: resource.OwnershipHash, Disposition: resource.Disposition,
		}},
	}
	decisionPredicate := regexp.QuoteMeta("FROM provider_provision_resolution_decisions AS decision") +
		`[\s\S]+` + regexp.QuoteMeta("decision.outcome = 'adopted_exact_candidate'")
	resourceRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{
			"binding_id", "kind", "native_ref", "parent_binding_id", "ownership_hash", "disposition",
		}).AddRow(resource.BindingID, resource.Kind, resource.NativeRef, "", resource.OwnershipHash, resource.Disposition)
	}
	mock.ExpectQuery(decisionPredicate).
		WithArgs(command.TenantID, command.RuntimeServerID, command.LeaseID, command.ResourceGenerationID, command.Operation).
		WillReturnRows(resourceRows())
	if err := requireExactMutationTargetsTx(t.Context(), tx, command); err != nil {
		t.Fatalf("adopted exact mutation custody: %v", err)
	}

	mock.ExpectQuery(decisionPredicate).
		WithArgs(command.TenantID, command.RuntimeServerID, command.LeaseID, command.ResourceGenerationID).
		WillReturnRows(resourceRows())
	absent := providerexecutor.ResourceBinding{
		BindingID: resource.BindingID, Kind: resource.Kind, NativeRef: resource.NativeRef,
		OwnershipHash: resource.OwnershipHash, Disposition: resource.Disposition,
		Observation: providerexecutor.ObservationAbsent, Cleanup: providerexecutor.CleanupComplete,
	}
	if err := requireDefinitiveGenerationAbsenceTx(t.Context(), tx, OperationRecord{Command: command}, providerexecutor.Receipt{
		Resources: []providerexecutor.ResourceBinding{absent},
	}); err != nil {
		t.Fatalf("adopted definitive absence custody: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
