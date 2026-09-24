package providercontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

func TestNormalizeNativeProvisionAdmissionUsesCanonicalRequestDigest(t *testing.T) {
	first := nativeProvisionAdmissionFixture()
	first.DesiredSpec = json.RawMessage(`{ "region": "de", "shape": {"ram": 8, "cpu": 4} }`)
	first.Server.Metadata = map[string]any{"zone": "de/fra", "labels": map[string]any{"b": 2, "a": 1}}

	second := nativeProvisionAdmissionFixture()
	second.DesiredSpec = json.RawMessage(`{"shape":{"cpu":4,"ram":8},"region":"de"}`)
	second.Server.Metadata = map[string]any{"labels": map[string]any{"a": 1, "b": 2}, "zone": "de/fra"}

	normalizedFirst, digestFirst, textFirst, err := normalizeNativeProvisionAdmission(first)
	if err != nil {
		t.Fatalf("normalize first request: %v", err)
	}
	normalizedSecond, digestSecond, textSecond, err := normalizeNativeProvisionAdmission(second)
	if err != nil {
		t.Fatalf("normalize second request: %v", err)
	}
	if digestFirst != digestSecond || textFirst != textSecond {
		t.Fatalf("canonical-equivalent requests produced different digests: %q != %q", textFirst, textSecond)
	}
	if normalizedFirst.Lease.Resource.ProviderID != "ionos" || normalizedSecond.ValidFor != vmleasesDefaultValidityForTest() {
		t.Fatalf("normalization = provider %q, validity %s", normalizedFirst.Lease.Resource.ProviderID, normalizedSecond.ValidFor)
	}

	changed := second
	changed.Server.Name = "another-runtime"
	_, changedDigest, _, err := normalizeNativeProvisionAdmission(changed)
	if err != nil {
		t.Fatalf("normalize changed request: %v", err)
	}
	if changedDigest == digestFirst {
		t.Fatal("materially different request reused canonical digest")
	}
}

func TestNormalizeNativeProvisionAdmissionRejectsCallerAuthorityTime(t *testing.T) {
	request := nativeProvisionAdmissionFixture()
	request.Lease.ValidFrom = time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	if _, _, _, err := normalizeNativeProvisionAdmission(request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("normalize caller authority time error = %v, want ErrInvalidRequest", err)
	}
}

func TestNormalizeNativeProvisionAdmissionRejectsCallerSelectedSlotID(t *testing.T) {
	request := nativeProvisionAdmissionFixture()
	request.RuntimeSlotID = "runtime-slot-substituted"
	if _, _, _, err := normalizeNativeProvisionAdmission(request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("substituted slot id error = %v, want ErrInvalidRequest", err)
	}
}

func TestNewNativeAdmissionRequiresOneSharedGateAndTransactionalResolver(t *testing.T) {
	database, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	gate := &admissionTestGate{}
	capacity := staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 3)}
	coordinator := newNativeAdmissionTestCoordinator(t, database, admissionTestProfileResolver{profile: testProfile("ionos-v1")}, gate)
	if _, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ActivationGate: gate, ProviderCreates: AllowProviderCreate{}, CapacityPolicy: capacity,
	}); err != nil {
		t.Fatalf("NewNativeAdmission: %v", err)
	}

	if _, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ProviderCreates: AllowProviderCreate{}, CapacityPolicy: capacity,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("nil gate error = %v, want ErrInvalidRequest", err)
	}
	var typedNilGate *admissionTestGate
	if _, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ActivationGate: typedNilGate, ProviderCreates: AllowProviderCreate{}, CapacityPolicy: capacity,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("typed nil gate error = %v, want ErrInvalidRequest", err)
	}
	if _, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ActivationGate: &admissionTestGate{}, ProviderCreates: AllowProviderCreate{}, CapacityPolicy: capacity,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("different gate error = %v, want ErrInvalidRequest", err)
	}
	if _, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ActivationGate: gate, ProviderCreates: AllowProviderCreate{},
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("nil capacity policy error = %v, want ErrInvalidRequest", err)
	}

	nonTransactional := newNativeAdmissionTestCoordinator(t, database, staticProfileResolver{profile: testProfile("ionos-v1")}, gate)
	if _, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: nonTransactional, ActivationGate: gate, ProviderCreates: AllowProviderCreate{}, CapacityPolicy: capacity,
	}); !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("non-transactional resolver error = %v, want ErrProfileUnavailable", err)
	}
}

func TestNativeAdmissionBlockedGateLeavesTransactionReadOnly(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	gate := &admissionTestGate{err: MutationActivationBlockedError{}}
	coordinator := newNativeAdmissionTestCoordinator(t, database, admissionTestProfileResolver{profile: testProfile("ionos-v1")}, gate)
	admission, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ActivationGate: gate,
		ProviderCreates: AllowProviderCreate{},
		CapacityPolicy:  staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 3)},
	})
	if err != nil {
		t.Fatalf("NewNativeAdmission: %v", err)
	}
	request := nativeProvisionAdmissionFixture()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs(tenantContextKey, request.TenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))")).
		WithArgs(request.TenantID, nativeProvisionOperationScope+":"+request.IdempotencyKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT request_digest, lease_id, operation_id.*FROM runtime_lease_idempotency_records`).
		WithArgs(request.TenantID, nativeProvisionOperationScope, request.IdempotencyKey).
		WillReturnRows(sqlmock.NewRows([]string{"request_digest", "lease_id", "operation_id"}))
	mock.ExpectRollback()

	if _, err := admission.AdmitProvision(t.Context(), request); !errors.Is(err, ErrMutationActivationBlocked) {
		t.Fatalf("AdmitProvision error = %v, want ErrMutationActivationBlocked", err)
	}
	if gate.CallCount() != 1 {
		t.Fatalf("activation gate calls = %d, want 1", gate.CallCount())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("blocked admission performed an unexpected database action: %v", err)
	}
}

func TestNativeAdmissionPreflightPolicyDenialPerformsNoDatabaseAction(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	gate := &admissionTestGate{}
	coordinator := newNativeAdmissionTestCoordinator(t, database, admissionTestProfileResolver{profile: testProfile("ionos-v1")}, gate)
	admission, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ActivationGate: gate,
		ProviderCreates: AllowProviderCreate{},
		CapacityPolicy:  staticAdmissionCapacityPolicy{err: errors.New("decision unavailable")},
	})
	if err != nil {
		t.Fatalf("NewNativeAdmission: %v", err)
	}

	if err := admission.PreflightProvision(t.Context(), nativeProvisionAdmissionFixture()); !errors.Is(err, ErrManagedRuntimeCapacityPolicyUnavailable) {
		t.Fatalf("PreflightProvision error = %v, want ErrManagedRuntimeCapacityPolicyUnavailable", err)
	}
	if gate.CallCount() != 0 {
		t.Fatalf("activation gate calls = %d, want 0 after policy denial", gate.CallCount())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("policy-denied preflight performed an unexpected database action: %v", err)
	}
}

type staticAdmissionCapacityPolicy struct {
	grant CapacityGrant
	err   error
}

func (p staticAdmissionCapacityPolicy) ResolveCapacity(context.Context, CapacityPolicyRequest) (CapacityGrant, error) {
	return p.grant, p.err
}

func ownerLimitedCapacityGrant(ownerID string, limit int) CapacityGrant {
	return CapacityGrant{
		ScopeKind: managedRuntimeCapacityScopeOwner, ScopeID: ownerID,
		Mode: CapacityModeLimited, Limit: limit,
		DecisionSource: CapacityDecisionSourceSignedRuntimeBudget,
	}
}

func newNativeAdmissionTestCoordinator(
	t *testing.T,
	database *sql.DB,
	resolver ExecutionProfileResolver,
	gate MutationActivationGate,
) *Coordinator {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", &queueExecutor{}); err != nil {
		t.Fatalf("register test adapter: %v", err)
	}
	ledger, err := NewPostgresLedger(database, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: resolver, Ledger: ledger, ActivationGate: gate,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return coordinator
}

type admissionTestGate struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (g *admissionTestGate) Require(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return g.err
}

func (g *admissionTestGate) CallCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

type admissionTestProfileResolver struct {
	profile ExecutionProfile
	err     error
}

func (r admissionTestProfileResolver) ResolveExecutionProfile(context.Context, ProfileRequest) (ExecutionProfile, error) {
	return r.profile, r.err
}

func (r admissionTestProfileResolver) ResolveExecutionProfileTx(
	_ context.Context,
	tx *sql.Tx,
	_ ProfileRequest,
) (ExecutionProfile, error) {
	if tx == nil {
		return ExecutionProfile{}, ErrProfileUnavailable
	}
	return r.profile, r.err
}

func nativeProvisionAdmissionFixture() NativeProvisionAdmissionRequest {
	return NativeProvisionAdmissionRequest{
		TenantID:        "tenant-1",
		RuntimeSlotKey:  "foundation",
		RuntimeSlotID:   DeriveManagedRuntimeSlotID("tenant-1", "stack-native-1", "foundation"),
		RuntimeServerID: "server-native-1",
		OwnerSubjectID:  "owner-1",
		IdempotencyKey:  "native-admission-1",
		Lease: vmlease.Lease{
			ID:      "lease-native-1",
			Subject: vmlease.Subject{Kind: vmlease.SubjectOrg, ID: "org-1", OrgID: "tenant-1"},
			Resource: vmlease.ResourceRef{
				ProviderID: " IONOS ",
			},
			DesiredState:   vmlease.DesiredStateRunning,
			BillingMode:    vmlease.BillingModeSubscription,
			LifecycleClass: vmlease.LifecycleClassSubscription,
			RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
			RecreatePolicy: vmlease.RecreatePolicyManual,
		},
		Server:         NativeAdmissionServer{StackID: "stack-native-1", Name: "native-runtime"},
		DesiredSpecRef: "desired-spec://techstack/leases/lease-native-1/revisions/1",
		DesiredSpec:    json.RawMessage(`{"region":"de","cpu":4,"ram":8}`),
	}
}

// Keep the expected default local to this package test without exporting a
// second duration authority from providercontrol.
func vmleasesDefaultValidityForTest() time.Duration { return 30 * 24 * time.Hour }
