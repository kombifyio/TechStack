package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

func TestMonthlyRuntimeErrorMapsMissingRuntimeClientToUnavailable(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/monthly-runtimes/lease-1/start", nil)
	event := &httpx.Event{Response: rr, Request: req}

	if err := monthlyRuntimeError(event, monthlyruntime.ErrRuntimeClient); err != nil {
		t.Fatalf("monthlyRuntimeError: %v", err)
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", rr.Code, rr.Body.String())
	}
}

func TestMonthlyRuntimeErrorMapsUnsupportedNativeStopToActionableConflict(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/monthly-runtimes/lease-1/stop", nil)
	event := &httpx.Event{Response: rr, Request: req}

	err := &monthlyruntime.NativeRuntimeActionUnsupportedError{Action: serverruntime.RuntimeActionStop}
	if mapErr := monthlyRuntimeError(event, err); mapErr != nil {
		t.Fatalf("monthlyRuntimeError: %v", mapErr)
	}
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want 409", rr.Code, rr.Body.String())
	}
	details := decodeErrorDetails(t, rr)
	if details["error_code"] != monthlyruntime.NativeRuntimeActionUnsupportedErrorCode ||
		details["reason_code"] != "provider_pause_unsupported" {
		t.Fatalf("details = %#v", details)
	}
}

func TestMonthlyRuntimeActionIgnoresQueryTenantOverride(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now:            func() time.Time { return now },
		SnapshotSecret: []byte("secret"),
	})
	lease := testRouteMonthlyLease(now)
	lease.Subject = vmlease.Subject{Kind: vmlease.SubjectOrg, ID: "org-2", OrgID: "org-2"}
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: lease}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}

	handler := monthlyRuntimeActionHandler(&monthlyruntime.Service{
		Leases:  leases,
		Runtime: routeRuntimeClient{},
	}, serverruntime.RuntimeActionStatus)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/monthly-runtimes/lease-1?tenant_id=org-2", nil)
	req.SetPathValue("id", "lease-1")
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "user-1", OrgID: "org-1"}))
	rr := httptest.NewRecorder()
	event := &httpx.Event{Response: rr, Request: req}

	if err := handler(event); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404 when query tenant override is ignored", rr.Code, rr.Body.String())
	}
}

func TestMonthlyRuntimeTenantIDPrefersIdentityOverQuery(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/monthly-runtimes/lease-1?tenant_id=attacker-org", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "user-1", OrgID: "identity-org"}))
	event := &httpx.Event{Response: httptest.NewRecorder(), Request: req}

	if got := monthlyRuntimeTenantID(event, "fallback-user"); got != "identity-org" {
		t.Fatalf("monthlyRuntimeTenantID = %q, want identity-org", got)
	}
}

func TestMonthlyRuntimeOperationsAPIDeliversResourceGenerationDigest(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	leases := routeLeaseService(t, now, "enrolled")
	lease, err := leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	digest, err := vmleases.ResourceGenerationDigest("org-1", *lease)
	if err != nil {
		t.Fatalf("ResourceGenerationDigest: %v", err)
	}
	if err := leases.RecordOperationStrict(t.Context(), vmleases.OperationEvent{
		TenantID:                 "org-1",
		LeaseID:                  "lease-1",
		EventType:                vmleases.OperationEventDecommission,
		Status:                   vmleases.OperationStatusDecommissioned,
		ResourceGenerationDigest: digest,
		CreatedAt:                now,
	}); err != nil {
		t.Fatalf("RecordOperationStrict: %v", err)
	}

	e, rr := authedRouteEvent(http.MethodGet, "/api/v1/monthly-runtimes/lease-1/operations", "")
	if err := monthlyRuntimeOperationsHandler(&monthlyruntime.Service{Leases: leases})(e); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var envelope struct {
		Data []vmleases.OperationEvent `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].ResourceGenerationDigest != digest {
		t.Fatalf("operations data = %+v, want digest %q", envelope.Data, digest)
	}
}

func TestMonthlyRuntimeCleanupReadbackIsOwnerScopedAndRedacted(t *testing.T) {
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	leases := routeLeaseService(t, now, "enrolled")
	archived := vmlease.DesiredStateArchived
	if _, err := leases.Patch(t.Context(), "org-1", "lease-1", vmleases.PatchRequest{
		DesiredState: &archived,
		Metadata:     map[string]string{"runtime_observed_state": "not_found"},
	}); err != nil {
		t.Fatalf("archive lease: %v", err)
	}
	readback := &routeCleanupReadbackSource{facts: &monthlyruntime.CleanupReadbackFacts{
		ServerBound:               true,
		ServerTerminal:            true,
		ProviderOperationFound:    true,
		ProviderOperationTerminal: true,
		AbsenceEvidenceRef:        "provider-evidence://centron/decommission/opaque-proof",
		CapacityReleased:          true,
	}}
	router := httpx.NewRouter()
	RegisterMonthlyRuntimeRoutes(router, &monthlyruntime.Service{
		Leases: leases, CleanupReadback: readback,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/monthly-runtimes/lease-1/cleanup-readback", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "user-1", OrgID: "org-1"}))
	rr := httptest.NewRecorder()
	router.BuildMux().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	if readback.calls != 1 || readback.tenantID != "org-1" || readback.leaseID != "lease-1" {
		t.Fatalf("readback call = %+v, want exact owner tenant and lease", readback)
	}
	var envelope struct {
		Data monthlyruntime.CleanupReadback `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.Data.Lease.DesiredTerminal || !envelope.Data.Lease.ObservedTerminal ||
		!envelope.Data.Server.Bound || !envelope.Data.Server.Terminal ||
		!envelope.Data.ProviderOperation.Found || !envelope.Data.ProviderOperation.Terminal ||
		!envelope.Data.ProviderOperation.CapacityReleased {
		t.Fatalf("cleanup readback = %+v, want terminal facts", envelope.Data)
	}
	if envelope.Data.ProviderOperation.AbsenceEvidenceRef != "provider-evidence://centron/decommission/opaque-proof" {
		t.Fatalf("absence ref = %q", envelope.Data.ProviderOperation.AbsenceEvidenceRef)
	}
	for _, forbidden := range []string{"operation_id", "native_ref", "credential", "command_json", "evidence_json"} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("cleanup readback leaked %q: %s", forbidden, rr.Body.String())
		}
	}
}

func TestMonthlyRuntimeCleanupReadbackRejectsUnboundCustodyWithoutReaderCall(t *testing.T) {
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	base := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }})
	if _, err := base.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: testRouteMonthlyLease(now)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	readback := &routeCleanupReadbackSource{facts: &monthlyruntime.CleanupReadbackFacts{}}
	e, rr := authedRouteEvent(http.MethodGet, "/api/v1/monthly-runtimes/lease-1/cleanup-readback", "")
	if err := monthlyRuntimeCleanupReadbackHandler(&monthlyruntime.Service{
		Leases: &legacyRouteLeaseService{Service: base}, CleanupReadback: readback,
	})(e); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want 409", rr.Code, rr.Body.String())
	}
	if readback.calls != 0 {
		t.Fatalf("unbound custody invoked provider readback %d times", readback.calls)
	}
}

func TestMonthlyRuntimeCleanupReadbackRejectsForeignOwnerWithoutReaderCall(t *testing.T) {
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	leases := routeLeaseService(t, now, "enrolled")
	readback := &routeCleanupReadbackSource{facts: &monthlyruntime.CleanupReadbackFacts{}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/monthly-runtimes/lease-1/cleanup-readback", nil)
	req.SetPathValue("id", "lease-1")
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "other-user", OrgID: "org-1"}))
	rr := httptest.NewRecorder()
	if err := monthlyRuntimeCleanupReadbackHandler(&monthlyruntime.Service{
		Leases: leases, CleanupReadback: readback,
	})(&httpx.Event{Response: rr, Request: req}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", rr.Code, rr.Body.String())
	}
	if readback.calls != 0 {
		t.Fatalf("foreign owner invoked provider readback %d times", readback.calls)
	}
}

func testRouteMonthlyLease(now time.Time) vmlease.Lease {
	metadata := monthlyruntime.NormalizeMetadata(map[string]string{}, serverruntime.RuntimeOfferingStandard)
	metadata["runtime_enrollment_status"] = "enrolled"
	return vmlease.Lease{
		ID:             "lease-1",
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "user-1", OrgID: "org-1"},
		Resource:       vmlease.ResourceRef{ProviderID: monthlyruntime.ProviderCentron, EngineVMID: "node-1"},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(time.Hour),
		RenewedAt:      now,
		Metadata:       metadata,
	}
}

type routeRuntimeClient struct{}

func (routeRuntimeClient) RuntimeAction(_ context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	return &serverruntime.LeaseRuntimeActionResponse{
		TenantID:   req.TenantID,
		LeaseID:    req.LeaseID,
		Action:     req.Action,
		OfferingID: req.OfferingID,
		Status:     &serverruntime.NodeStatus{ID: "node-1", State: "running"},
	}, nil
}

type errRouteRuntimeClient struct{ err error }

func (c errRouteRuntimeClient) RuntimeAction(context.Context, serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	return nil, c.err
}

type routeCleanupReadbackSource struct {
	tenantID string
	leaseID  vmlease.LeaseID
	facts    *monthlyruntime.CleanupReadbackFacts
	err      error
	calls    int
}

func (s *routeCleanupReadbackSource) ReadManagedRuntimeCleanup(_ context.Context, tenantID string, leaseID vmlease.LeaseID) (*monthlyruntime.CleanupReadbackFacts, error) {
	s.calls++
	s.tenantID = tenantID
	s.leaseID = leaseID
	return s.facts, s.err
}

type fakeRouteReconciler struct {
	calls   int
	request monthlyruntime.ReconciliationRequest
}

type blockedRouteReconciler struct{}

func (blockedRouteReconciler) DurableReconciliationReady() bool { return true }

func (blockedRouteReconciler) CheckProviderReconciliationReady(context.Context) error {
	return monthlyruntime.ErrReconciliationUnavailable
}

func (blockedRouteReconciler) EnqueueProviderReconciliation(context.Context, monthlyruntime.ReconciliationRequest) error {
	return errors.New("blocked reconciliation must not enqueue")
}

type legacyRouteLeaseService struct{ *vmleases.Service }

func (s *legacyRouteLeaseService) GetInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error) {
	lease, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	return &vmleases.LeaseInventoryRecord{
		Lease: *lease, ExecutionAuthority: vmleases.LeaseExecutionAuthorityLegacySimulate,
		AuthorityState: vmleases.LeaseAuthorityStateLegacyQuarantined,
	}, nil
}

func (f *fakeRouteReconciler) EnqueueProviderReconciliation(_ context.Context, request monthlyruntime.ReconciliationRequest) error {
	f.calls++
	f.request = request
	return nil
}

func (f *fakeRouteReconciler) DurableReconciliationReady() bool { return true }

func TestMonthlyRuntimeResolveCustodyArchivesConfirmedLegacyRecord(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }})
	if _, err := leases.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: testRouteMonthlyLease(now)}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	e, rr := authedRouteEvent(http.MethodPost, "/api/v1/monthly-runtimes/lease-1/resolve-custody", `{"provider_cleanup_confirmed":true}`)
	if err := monthlyRuntimeResolveCustodyHandler(&monthlyruntime.Service{Leases: &legacyRouteLeaseService{Service: leases}})(e); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	stored, err := leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get resolved lease: %v", err)
	}
	if stored.CancelledAt == nil || stored.DesiredState != vmlease.DesiredStateArchived || stored.Metadata["custody_resolution_status"] != "resolved" {
		t.Fatalf("resolved lease = %+v", stored)
	}
}

func authedRouteEvent(method, target, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.SetPathValue("id", "lease-1")
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "user-1", OrgID: "org-1"}))
	rr := httptest.NewRecorder()
	return &httpx.Event{Response: rr, Request: req}, rr
}

func routeLeaseService(t *testing.T, now time.Time, status string) *nativeTestLeaseService {
	t.Helper()
	leases := &nativeTestLeaseService{Service: vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{Now: func() time.Time { return now }, SnapshotSecret: []byte("secret")})}
	lease := testRouteMonthlyLease(now)
	lease.Metadata["runtime_enrollment_status"] = status
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: lease}); err != nil {
		t.Fatalf("CreateOrUpdate: %v", err)
	}
	return leases
}

func decodeErrorCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	details := decodeErrorDetails(t, rr)
	code, _ := details["error_code"].(string)
	return code
}

func decodeErrorDetails(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error body %q: %v", rr.Body.String(), err)
	}
	return env.Error.Details
}

func TestMonthlyRuntimeDecommissionInput(t *testing.T) {
	cases := []struct {
		name   string
		target string
		body   string
		want   bool
	}{
		{"query true", "/x?force=true", "", true},
		{"query false", "/x?force=false", "", false},
		{"body true", "/x", `{"force":true}`, true},
		{"body false", "/x", `{"force":false}`, false},
		{"empty body", "/x", "", false},
		{"no param", "/x", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := authedRouteEvent(http.MethodPost, tc.target, tc.body)
			input, err := monthlyRuntimeDecommissionRequested(e)
			if err != nil {
				t.Fatalf("monthlyRuntimeDecommissionRequested: %v", err)
			}
			if got := input.Force; got != tc.want {
				t.Fatalf("force = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMonthlyRuntimeDecommissionInputRejectsAmbiguousJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		body   string
	}{
		{name: "malformed", target: "/x", body: `{"force":`},
		{name: "wrong digest type", target: "/x", body: `{"expected_resource_generation_digest":123}`},
		{name: "unknown field", target: "/x", body: `{"force":false,"unexpected":true}`},
		{name: "multiple values", target: "/x", body: `{"force":false}{"force":true}`},
		{name: "null", target: "/x", body: `null`},
		{name: "invalid force query", target: "/x?force=sometimes", body: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := authedRouteEvent(http.MethodPost, tc.target, tc.body)
			if _, err := monthlyRuntimeDecommissionRequested(e); err == nil {
				t.Fatal("ambiguous decommission request was accepted")
			}
		})
	}
}

func TestMonthlyRuntimeDecommissionHandlerRejectsInvalidBodyBeforeService(t *testing.T) {
	e, rr := authedRouteEvent(http.MethodPost, "/api/v1/monthly-runtimes/lease-1/decommission", `{"expected_resource_generation_digest":123}`)
	if err := monthlyRuntimeDecommissionHandler(nil)(e); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
}

func TestMonthlyRuntimeDecommissionInputCarriesGenerationClaim(t *testing.T) {
	digest := strings.Repeat("a", 64)
	e, _ := authedRouteEvent(http.MethodPost, "/x?force=false", `{"force":true,"expected_resource_generation_digest":" `+digest+` "}`)
	input, err := monthlyRuntimeDecommissionRequested(e)
	if err != nil {
		t.Fatalf("monthlyRuntimeDecommissionRequested: %v", err)
	}
	if input.Force {
		t.Fatal("query force=false must override body force=true")
	}
	if input.ExpectedResourceGenerationDigest != digest {
		t.Fatalf("expected digest = %q, want %q", input.ExpectedResourceGenerationDigest, digest)
	}
}

func TestMonthlyRuntimeDecommissionUnreachableReturns409(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	leases := routeLeaseService(t, now, "enrolled")
	svc := &monthlyruntime.Service{Leases: leases, Runtime: errRouteRuntimeClient{err: errors.New("dial tcp: no route to host")}}
	e, rr := authedRouteEvent(http.MethodPost, "/api/v1/monthly-runtimes/lease-1/decommission", "")

	if err := monthlyRuntimeDecommissionHandler(svc)(e); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want 409", rr.Code, rr.Body.String())
	}
	if code := decodeErrorCode(t, rr); code != monthlyruntime.DecommissionBlockedUnreachableErrorCode {
		t.Fatalf("error_code = %q, want %q", code, monthlyruntime.DecommissionBlockedUnreachableErrorCode)
	}
	if got := decodeErrorDetails(t, rr)["force_offered"]; got != false {
		t.Fatalf("force_offered = %v, want false without durable reconciliation", got)
	}
}

func TestMonthlyRuntimeDecommissionUsesDurableNativeCustodyWithoutContactingRuntime(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	leases := routeLeaseService(t, now, "enrolled")
	wantLease, err := leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get lease: %v", err)
	}
	wantDigest, err := vmleases.ResourceGenerationDigest("org-1", *wantLease)
	if err != nil {
		t.Fatalf("ResourceGenerationDigest: %v", err)
	}
	reconciler := &fakeRouteReconciler{}
	svc := &monthlyruntime.Service{
		Leases:    leases,
		Runtime:   errRouteRuntimeClient{err: errors.New("dial tcp: no route to host")},
		Reconcile: reconciler,
	}
	e, rr := authedRouteEvent(http.MethodPost, "/api/v1/monthly-runtimes/lease-1/decommission", "")

	if err := monthlyRuntimeDecommissionHandler(svc)(e); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	if reconciler.calls != 1 {
		t.Fatalf("reconciler calls = %d, want one durable native decommission call", reconciler.calls)
	}
	if got := reconciler.request; got.TenantID != "org-1" || got.OwnerID != "user-1" ||
		got.LeaseID != "lease-1" || got.ResourceGenerationDigest != wantDigest {
		t.Fatalf("reconciliation request = %+v, want exact lease-generation binding", got)
	}
	if !strings.Contains(rr.Body.String(), `"observed_state":"reconciliation_pending"`) ||
		!strings.Contains(rr.Body.String(), `"lease_state":"reconciliation_pending"`) {
		t.Fatalf("body=%s, want honest reconciliation_pending state", rr.Body.String())
	}
}

func TestMonthlyRuntimeDecommissionClosedProviderControlReturns503WithoutClaim(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	leases := routeLeaseService(t, now, "enrolled")
	svc := &monthlyruntime.Service{
		Leases: leases, Runtime: errRouteRuntimeClient{err: errors.New("runtime must not be called")},
		Reconcile: blockedRouteReconciler{},
	}
	e, rr := authedRouteEvent(http.MethodPost, "/api/v1/monthly-runtimes/lease-1/decommission", "")

	if err := monthlyRuntimeDecommissionHandler(svc)(e); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", rr.Code, rr.Body.String())
	}
	stored, err := leases.Get(t.Context(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CancelledAt != nil || stored.Metadata[vmleases.MetadataKeyDecommissionClaimDigest] != "" {
		t.Fatalf("closed provider control mutated lease: %#v", stored)
	}
}

func TestMonthlyRuntimeDecommissionForceReturnsAcceptedPending(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	leases := routeLeaseService(t, now, "enrolled")
	reconciler := &fakeRouteReconciler{}
	svc := &monthlyruntime.Service{Leases: leases, Runtime: errRouteRuntimeClient{err: errors.New("dial tcp: no route to host")}, Reconcile: reconciler}
	e, rr := authedRouteEvent(http.MethodPost, "/api/v1/monthly-runtimes/lease-1/decommission?force=true", "")

	if err := monthlyRuntimeDecommissionHandler(svc)(e); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	if reconciler.calls != 1 {
		t.Fatalf("reconciler calls = %d, want 1", reconciler.calls)
	}
	if !strings.Contains(rr.Body.String(), `"observed_state":"reconciliation_pending"`) ||
		!strings.Contains(rr.Body.String(), `"lease_state":"reconciliation_pending"`) {
		t.Fatalf("body=%s, want honest reconciliation_pending state", rr.Body.String())
	}
}

type unsafeRouteReconciler struct{ calls int }

func (f *unsafeRouteReconciler) EnqueueProviderReconciliation(context.Context, monthlyruntime.ReconciliationRequest) error {
	f.calls++
	return nil
}

func (f *unsafeRouteReconciler) DurableReconciliationReady() bool { return false }

func TestMonthlyRuntimeDecommissionForceReturns503WithoutDurableCustodyAndMutatesNothing(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	leases := routeLeaseService(t, now, "enrolled")
	reconciler := &unsafeRouteReconciler{}
	svc := &monthlyruntime.Service{Leases: leases, Runtime: errRouteRuntimeClient{err: errors.New("dial tcp: no route to host")}, Reconcile: reconciler}
	e, rr := authedRouteEvent(http.MethodPost, "/api/v1/monthly-runtimes/lease-1/decommission?force=true", "")

	if err := monthlyRuntimeDecommissionHandler(svc)(e); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", rr.Code, rr.Body.String())
	}
	if reconciler.calls != 0 {
		t.Fatalf("unsafe reconciler calls = %d, want zero", reconciler.calls)
	}
	stored, err := leases.Get(context.Background(), "org-1", "lease-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CancelledAt != nil || stored.Metadata[vmleases.MetadataKeyDecommissionClaimDigest] != "" ||
		stored.Metadata["runtime_observed_state"] == "reconciliation_pending" {
		t.Fatalf("503 mutated lease: %#v", stored)
	}
}

func TestMonthlyRuntimeReconnectHandlerReturns200(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	leases := routeLeaseService(t, now, "pending")
	svc := &monthlyruntime.Service{Leases: leases, Runtime: routeRuntimeClient{}}
	e, rr := authedRouteEvent(http.MethodPost, "/api/v1/monthly-runtimes/lease-1/reconnect", "")

	if err := monthlyRuntimeReconnectHandler(svc)(e); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
}
