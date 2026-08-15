package vmleases

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

func inventoryTestServiceConfig(now func() time.Time) ServiceConfig {
	return ServiceConfig{
		Now: now,
		AllowedProviders: map[string]bool{
			"centron": true, "ionos": true, "centron-managed": true,
		},
	}
}

func testLease(now time.Time) vmlease.Lease {
	return vmlease.Lease{
		ID:             "lease-1",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: "centron-managed", EngineVMID: "ccloud-1", SimulationID: "sim-1", VMID: "vm-1"},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyNever,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(time.Hour),
	}
}

func TestServiceCreatesInventoryWithoutProviderExecutionState(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store, inventoryTestServiceConfig(func() time.Time { return now }))

	created, err := svc.CreateOrUpdate(t.Context(), CreateRequest{Lease: testLease(now), IdempotencyKey: "inventory-1"})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	if ResourceGenerationID(*created) == "" {
		t.Fatal("inventory lease has no resource generation")
	}
	for _, key := range []string{"runtime_enrollment_status", "executor_status", "executor_operation_id"} {
		if got := created.Metadata[key]; got != "" {
			t.Fatalf("legacy execution metadata %q = %q", key, got)
		}
	}
	if len(store.journal) != 0 {
		t.Fatalf("inventory create emitted operation events: %+v", store.journal)
	}
}

func TestServiceAcceptsHistoricalProviderInventoryOutsideNativeAdmission(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), inventoryTestServiceConfig(func() time.Time { return now }))
	lease := testLease(now)
	lease.Resource.ProviderID = "runpod-managed"
	created, err := svc.CreateOrUpdate(context.Background(), CreateRequest{Lease: lease})
	if err != nil {
		t.Fatalf("historical inventory CreateOrUpdate: %v", err)
	}
	if created.Resource.ProviderID != "runpod-managed" || strings.TrimSpace(created.Resource.EngineVMID) == "" {
		t.Fatalf("historical inventory projection = %#v", created.Resource)
	}
}

func TestServiceRejectsPlaceholderInventoryWithoutProviderResource(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store, inventoryTestServiceConfig(func() time.Time { return now }))
	lease := testLease(now)
	lease.Resource.EngineVMID = ""

	if _, err := svc.CreateOrUpdate(context.Background(), CreateRequest{Lease: lease}); !errors.Is(err, ErrProviderRefRequired) {
		t.Fatalf("CreateOrUpdate error = %v, want ErrProviderRefRequired", err)
	}
	if _, err := store.Get(context.Background(), lease.Subject.OrgID, lease.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("placeholder inventory persisted before native admission: %v", err)
	}
}

func TestLoadTechStackLeasingProviderSetUsesControlPlaneRows(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT DISTINCT profile\\.provider_id").WillReturnRows(sqlmock.NewRows([]string{"provider_id"}).AddRow("centron").AddRow("ionos"))

	got, err := LoadTechStackLeasingProviderSet(context.Background(), db)
	if err != nil {
		t.Fatalf("LoadTechStackLeasingProviderSet: %v", err)
	}
	want := map[string]bool{"centron": true, "ionos": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed providers = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestResolveTechStackLeasingProviderSetFailsClosedWhenCatalogTablesMissing(t *testing.T) {
	if !IsProviderControlPlaneUnavailable(errors.New(`query provider control plane: ERROR: relation "provider_catalog_profiles" does not exist (SQLSTATE 42P01)`)) {
		t.Fatal("expected missing provider_catalog_profiles relation to be unavailable")
	}
}

func TestServicePatchSupportsNativeGenerationFences(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), inventoryTestServiceConfig(func() time.Time { return now }))
	created, err := svc.CreateOrUpdate(t.Context(), CreateRequest{Lease: testLease(now)})
	if err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	digest, err := ResourceGenerationDigest("org-1", *created)
	if err != nil {
		t.Fatalf("ResourceGenerationDigest: %v", err)
	}
	stopped := vmlease.DesiredStateStopped
	updated, err := svc.Patch(t.Context(), "org-1", created.ID, PatchRequest{DesiredState: &stopped, ExpectedResourceGenerationDigest: digest})
	if err != nil || updated.DesiredState != stopped {
		t.Fatalf("Patch = %+v, %v", updated, err)
	}
}
