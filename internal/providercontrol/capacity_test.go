package providercontrol

import (
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestNativeCapacityPolicyDigestBindsCanonicalProvider(t *testing.T) {
	request := nativeProvisionAdmissionFixture()
	policy := staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant(request.OwnerSubjectID, 3)}

	ionos, err := resolveNativeAdmissionCapacity(t.Context(), policy, request)
	if err != nil {
		t.Fatalf("resolve IONOS capacity: %v", err)
	}
	request.Lease.Resource.ProviderID = "centron"
	centron, err := resolveNativeAdmissionCapacity(t.Context(), policy, request)
	if err != nil {
		t.Fatalf("resolve Centron capacity: %v", err)
	}
	if ionos.PolicyDigest == centron.PolicyDigest {
		t.Fatalf("provider-specific policy digests are equal: %q", ionos.PolicyDigest)
	}
}

func TestNativeCapacityPolicyAcceptsCanonicalProviderSyntaxWithoutOwningTheCatalog(t *testing.T) {
	request := nativeProvisionAdmissionFixture()
	request.Lease.Resource.ProviderID = "future-provider"
	policy := staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant(request.OwnerSubjectID, 3)}

	resolved, err := resolveNativeAdmissionCapacity(t.Context(), policy, request)
	if err != nil {
		t.Fatalf("resolve syntactically canonical provider capacity: %v", err)
	}
	if resolved.PolicyDigest == "" {
		t.Fatal("capacity policy digest is empty")
	}
	request.Lease.Resource.ProviderID = "Future Provider"
	if _, err := resolveNativeAdmissionCapacity(t.Context(), policy, request); !errors.Is(err, ErrManagedRuntimeCapacityPolicyUnavailable) {
		t.Fatalf("invalid provider syntax error = %v, want policy unavailable", err)
	}
}

func TestNativeCapacityPolicyRejectsUnregisteredDecisionSource(t *testing.T) {
	request := nativeProvisionAdmissionFixture()
	policy := staticAdmissionCapacityPolicy{grant: CapacityGrant{
		ScopeKind:      managedRuntimeCapacityScopeOwner,
		ScopeID:        request.OwnerSubjectID,
		Mode:           CapacityModeLimited,
		Limit:          3,
		DecisionSource: "caller-selected-policy",
	}}
	_, err := resolveNativeAdmissionCapacity(t.Context(), policy, request)
	if !errors.Is(err, ErrManagedRuntimeCapacityPolicyUnavailable) {
		t.Fatalf("unregistered decision source error = %v, want policy unavailable", err)
	}
}

func TestManagedRuntimeCapacityReplayRejectsMigrationQuarantine(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	request := nativeProvisionAdmissionFixture()
	result := NativeProvisionAdmissionResult{
		LeaseID: "lease-native-1", ResourceGenerationID: "9cb4b2fe-037b-4d32-b5f9-b867d1a2a8a6",
	}
	result.Operation.Command.OperationID = "operation-native-1"
	mock.ExpectQuery(regexp.QuoteMeta("FROM managed_runtime_capacity_reservations")).
		WithArgs(request.TenantID, result.LeaseID, result.ResourceGenerationID).
		WillReturnRows(sqlmock.NewRows([]string{
			"owner_subject_id", "provider_id", "resource_generation_id", "operation_id",
			"reservation_mode", "capacity_limit", "reservation_origin",
			"policy_source", "policy_digest",
		}).AddRow(
			request.OwnerSubjectID, "ionos", result.ResourceGenerationID, result.Operation.Command.OperationID,
			"quarantine", nil, "migration_quarantine",
			"migration_quarantine:managed-runtime-capacity/v1",
			"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		))

	err = validateManagedRuntimeCapacityReplayTx(t.Context(), tx, request, result)
	if !errors.Is(err, ErrNativeAdmissionConflict) {
		t.Fatalf("quarantine replay error = %v, want native admission conflict", err)
	}
	mock.ExpectRollback()
	_ = tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}
