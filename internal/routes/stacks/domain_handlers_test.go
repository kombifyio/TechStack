package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/golang-jwt/jwt/v5"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

const testServiceSecret = "test-domain-attach-secret"

func signCloudServiceToken(t *testing.T, secret, sub, org string, scopes []string, exp time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":    cloudServiceIssuer,
		"aud":    cloudServiceAudience,
		"sub":    sub,
		"org_id": org,
		"scopes": scopes,
		"iat":    time.Now().Add(-time.Minute).Unix(),
		"exp":    exp.Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func attachDomainEvent(body, token string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/api/internal/stacks/domain-attach", strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Idempotency-Key", "domain-attach-test")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func seedManagedRoutingServer(t *testing.T, store *controlplane.MemoryStore, id, tenant, owner, stackID, leaseID string) {
	t.Helper()
	if _, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID: id, TenantID: tenant, OwnerSubjectID: owner, StackID: stackID, LeaseID: leaseID,
		ProviderRef: "ionos-managed:" + id, Name: "Managed server",
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
}

func seedStack(t *testing.T, store *controlplane.MemoryStore, id, tenant, owner, status string) {
	t.Helper()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             id,
		TenantID:       tenant,
		OwnerSubjectID: owner,
		Name:           "Demo",
		Status:         status,
		Config:         map[string]any{"user_config": map[string]any{"name": "demo"}},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
}

func TestAttachDomainPersistsExactDesiredStateAndReportsPendingRollout(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	routingStore := stackrouting.NewMemoryStore()
	seedStack(t, store, "stack-1", "tenant-1", "auth0|user-1", "running")
	seedManagedRoutingServer(t, store, routingTestServerID, "tenant-1", "auth0|user-1", "stack-1", "lease-1")

	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "tenant-1",
		[]string{stackDomainAttachScope}, time.Now().Add(2*time.Minute))
	body := strings.Replace(`{"stack_id":"stack-1","server_id":"server-1","lease_id":"lease-1","domain":"Kombified.COM","source":"byod","dns_provider":"cloudflare","cf_zone_id":"zone-1"}`, "server-1", routingTestServerID, 1)
	e, rec := attachDomainEvent(body, token)

	h := crudRouteHandlers{stackStore: store, serverStore: store, routingStore: routingStore,
		routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")}}, routingDispatch: &routingTestDispatcher{jobID: "job-domain-1"}}
	if err := h.attachDomainToStack(e); err != nil {
		t.Fatalf("attachDomainToStack: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var env ksapi.SuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data, _ := env.Data.(map[string]any)
	if data["stack_id"] != "stack-1" || data["server_id"] != routingTestServerID || data["lease_id"] != "lease-1" || data["domain"] != "kombified.com" {
		t.Fatalf("unexpected response data: %+v", data)
	}
	if data["attachId"] != "job-domain-1" || data["status"] != stackrouting.RolloutPending || data["applied"] != false || data["reason_code"] != "" {
		t.Fatalf("response falsely claims rollout: %+v", data)
	}

	desired, err := routingStore.Get(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("Get routing: %v", err)
	}
	if desired.Domain != "kombified.com" || desired.ServerID != routingTestServerID || desired.LeaseID != "lease-1" || desired.Revision != 1 {
		t.Fatalf("desired = %#v", desired)
	}
	// Immutable stack config is not rewritten; deploy derives the handoff from
	// the revisioned overlay after loading intent.
	updated, err := store.GetStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if _, exists := updated.Config["domain"]; exists {
		t.Fatalf("immutable stack config was rewritten: %#v", updated.Config)
	}
}

func TestAttachDomainAcceptsCurrentAndNextControlPlaneSecretsDuringRotation(t *testing.T) {
	t.Setenv("KOMBIFY_CLOUD_SERVICE_SECRET", "")
	t.Setenv("STACK_CONTROL_PLANE_SECRET", "current-domain-attach-secret")
	t.Setenv("STACK_CONTROL_PLANE_SECRET_NEXT", "next-domain-attach-secret")

	for name, signingSecret := range map[string]string{
		"current": "current-domain-attach-secret",
		"next":    "next-domain-attach-secret",
	} {
		t.Run(name, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			routingStore := stackrouting.NewMemoryStore()
			seedStack(t, store, "stack-1", "tenant-1", "auth0|user-1", "running")
			seedManagedRoutingServer(t, store, routingTestServerID, "tenant-1", "auth0|user-1", "stack-1", "lease-1")

			token := signCloudServiceToken(t, signingSecret, "auth0|user-1", "tenant-1",
				[]string{stackDomainAttachScope}, time.Now().Add(2*time.Minute))
			body := strings.Replace(`{"stack_id":"stack-1","server_id":"server-1","lease_id":"lease-1","domain":"rotation.example","source":"byod","dns_provider":"cloudflare","cf_zone_id":"zone-1"}`, "server-1", routingTestServerID, 1)
			e, rec := attachDomainEvent(body, token)

			h := crudRouteHandlers{
				stackStore:    store,
				serverStore:   store,
				routingStore:  routingStore,
				routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")}},
				routingDispatch: &routingTestDispatcher{
					jobID: "job-domain-rotation",
				},
			}
			if err := h.attachDomainToStack(e); err != nil {
				t.Fatalf("attachDomainToStack: %v", err)
			}
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
			}
		})
	}
}

func TestAttachDomainRejectsMissingToken(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	e, rec := attachDomainEvent(`{"domain":"acme.dev"}`, "")
	_ = crudRouteHandlers{stackStore: store, jobStore: store}.attachDomainToStack(e)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAttachDomainRejectsWrongScope(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "tenant-1",
		[]string{"some:other.scope"}, time.Now().Add(2*time.Minute))
	e, rec := attachDomainEvent(`{"domain":"acme.dev"}`, token)
	_ = crudRouteHandlers{stackStore: store, jobStore: store}.attachDomainToStack(e)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAttachDomainRequiresTenant(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "",
		[]string{stackDomainAttachScope}, time.Now().Add(2*time.Minute))
	e, rec := attachDomainEvent(`{"domain":"acme.dev"}`, token)
	_ = crudRouteHandlers{stackStore: store, jobStore: store}.attachDomainToStack(e)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
}

func TestAttachDomainRejectsWrongOwnerOnExactStack(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-1", "tenant-1", "auth0|other", "running")
	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "tenant-1",
		[]string{stackDomainAttachScope}, time.Now().Add(2*time.Minute))
	body := strings.Replace(`{"stack_id":"stack-1","server_id":"server-1","lease_id":"lease-1","domain":"acme.dev","source":"byod","dns_provider":"cloudflare","cf_zone_id":"zone-1"}`, "server-1", routingTestServerID, 1)
	e, rec := attachDomainEvent(body, token)
	_ = crudRouteHandlers{stackStore: store, serverStore: store, routingStore: stackrouting.NewMemoryStore()}.attachDomainToStack(e)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAttachDomainRequiresExactIDsAndNeverAutoPicks(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-newest", "tenant-1", "auth0|user-1", "running")
	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "tenant-1",
		[]string{stackDomainAttachScope}, time.Now().Add(2*time.Minute))
	e, rec := attachDomainEvent(`{"domain":"kombified.com","source":"byod","dns_provider":"cloudflare","cf_zone_id":"zone-1"}`, token)
	routingStore := stackrouting.NewMemoryStore()
	_ = crudRouteHandlers{stackStore: store, serverStore: store, routingStore: routingStore}.attachDomainToStack(e)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routing_target_required") || !strings.Contains(rec.Body.String(), "server_id") || !strings.Contains(rec.Body.String(), "lease_id") {
		t.Fatalf("missing actionable exact-target denial: %s", rec.Body.String())
	}
	if _, err := routingStore.Get(context.Background(), "tenant-1", "stack-newest"); !errors.Is(err, stackrouting.ErrNotFound) {
		t.Fatalf("auto-picked stack routing: %v", err)
	}
}

func TestAttachDomainRequiresIdempotencyKeyAndExactLease(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	routingStore := stackrouting.NewMemoryStore()
	seedStack(t, store, "stack-1", "tenant-1", "auth0|user-1", "running")
	seedManagedRoutingServer(t, store, routingTestServerID, "tenant-1", "auth0|user-1", "stack-1", "lease-1")
	lease := routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")
	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "tenant-1", []string{stackDomainAttachScope}, time.Now().Add(2*time.Minute))
	h := crudRouteHandlers{stackStore: store, serverStore: store, routingStore: routingStore, routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{lease}}, routingDispatch: &routingTestDispatcher{jobID: "job-1"}}
	body := strings.Replace(`{"stack_id":"stack-1","server_id":"server-1","lease_id":"lease-1","domain":"kombified.com","source":"cloud","dns_provider":"cloudflare","cf_zone_id":"zone-1"}`, "server-1", routingTestServerID, 1)

	missing, missingRecorder := attachDomainEvent(body, token)
	missing.Request.Header.Del("Idempotency-Key")
	_ = h.attachDomainToStack(missing)
	if missingRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing key status=%d body=%s", missingRecorder.Code, missingRecorder.Body.String())
	}

	wrongBody := strings.Replace(body, `"lease_id":"lease-1"`, `"lease_id":"lease-other"`, 1)
	wrong, wrongRecorder := attachDomainEvent(wrongBody, token)
	_ = h.attachDomainToStack(wrong)
	if wrongRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong lease status=%d body=%s", wrongRecorder.Code, wrongRecorder.Body.String())
	}
	if _, err := routingStore.Get(context.Background(), "tenant-1", "stack-1"); !errors.Is(err, stackrouting.ErrNotFound) {
		t.Fatalf("invalid attach persisted routing: %v", err)
	}
}

func ingressEvent(stackID, serverID, leaseID, token string) (*httpx.Event, *httptest.ResponseRecorder) {
	path := "/api/internal/stacks/" + stackID + "/ingress"
	if serverID != "" || leaseID != "" {
		path += "?server_id=" + serverID + "&lease_id=" + leaseID
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if stackID != "" {
		req.SetPathValue("id", stackID)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func seedApprovedWorker(t *testing.T, store *controlplane.MemoryStore, id, tenant, owner, stackID, ip string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID:             id,
		TenantID:       tenant,
		OwnerSubjectID: owner,
		StackID:        stackID,
		IP:             ip,
	}); err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}
	if _, err := store.ApproveWorker(ctx, tenant, id, owner, time.Now()); err != nil {
		t.Fatalf("ApproveWorker: %v", err)
	}
}

func TestStackIngressReturnsMainNodeIP(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-1", "tenant-1", "auth0|user-1", "running")
	seedApprovedWorker(t, store, "unrelated-worker", "tenant-1", "auth0|user-1", "stack-1", "198.51.100.99")
	lease := routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")
	lease.Metadata["runtime_public_ip"] = "203.0.113.7"
	serverID := runtimeidentity.LeaseServerID("lease-1")

	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "tenant-1",
		[]string{stackDomainAttachScope}, time.Now().Add(2*time.Minute))
	e, rec := ingressEvent("stack-1", serverID, "lease-1", token)
	if err := (crudRouteHandlers{stackStore: store, serverStore: store, routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{lease}}}).stackIngress(e); err != nil {
		t.Fatalf("stackIngress: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env ksapi.SuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data, _ := env.Data.(map[string]any)
	if data["ingress_ip"] != "203.0.113.7" || data["server_id"] != serverID || data["lease_id"] != "lease-1" {
		t.Fatalf("ingress_ip = %v, want 203.0.113.7", data["ingress_ip"])
	}
	if _, err := store.GetServerRuntime(context.Background(), "tenant-1", serverID); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("read-only ingress created a server projection: %v", err)
	}
}

func TestStackIngressPendingWhenNoWorker(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-1", "tenant-1", "auth0|user-1", "provisioning")
	lease := routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")
	lease.Metadata["runtime_ssh_host"] = "runtime.example.invalid"
	serverID := runtimeidentity.LeaseServerID("lease-1")
	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "tenant-1",
		[]string{stackDomainAttachScope}, time.Now().Add(2*time.Minute))
	e, rec := ingressEvent("stack-1", serverID, "lease-1", token)
	_ = crudRouteHandlers{stackStore: store, serverStore: store, routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{lease}}}.stackIngress(e)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (ingress_pending)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ingress_ipv4_pending") {
		t.Fatalf("hostname must not be emitted as an A-record IP: %s", rec.Body.String())
	}
}

func TestStackIngressFreshExactServerObservationOverridesStaleLeaseIP(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-1", "tenant-1", "auth0|user-1", "running")
	lease := routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")
	lease.Metadata["runtime_public_ip"] = "192.0.2.10"
	serverID := runtimeidentity.LeaseServerID("lease-1")
	now := time.Now().UTC()
	if _, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID: serverID, TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "auth0|user-1", LeaseID: "lease-1",
		LifecycleState: "active", ConnectionState: "connected", LastHeartbeatAt: &now,
		Metadata: map[string]any{"authority": "guard", "observed_host_state": "running", "host": map[string]any{"public_ip": "203.0.113.44"}},
	}); err != nil {
		t.Fatal(err)
	}
	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "tenant-1", []string{stackDomainAttachScope}, time.Now().Add(2*time.Minute))
	e, rec := ingressEvent("stack-1", serverID, "lease-1", token)
	_ = crudRouteHandlers{stackStore: store, serverStore: store, routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{lease}}}.stackIngress(e)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "203.0.113.44") || strings.Contains(rec.Body.String(), "192.0.2.10") {
		t.Fatalf("fresh exact observation did not win: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestStackIngressRequiresAndValidatesExactTarget(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-1", "tenant-1", "auth0|user-1", "running")
	lease := routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")
	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "tenant-1", []string{stackDomainAttachScope}, time.Now().Add(2*time.Minute))

	missing, missingRecorder := ingressEvent("stack-1", "", "", token)
	_ = crudRouteHandlers{stackStore: store, serverStore: store, routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{lease}}}.stackIngress(missing)
	if missingRecorder.Code != http.StatusUnprocessableEntity || !strings.Contains(missingRecorder.Body.String(), "routing_target_required") {
		t.Fatalf("missing target status=%d body=%s", missingRecorder.Code, missingRecorder.Body.String())
	}

	wrong, wrongRecorder := ingressEvent("stack-1", runtimeidentity.LeaseServerID("lease-other"), "lease-other", token)
	_ = crudRouteHandlers{stackStore: store, serverStore: store, routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{lease}}}.stackIngress(wrong)
	if wrongRecorder.Code != http.StatusNotFound {
		t.Fatalf("wrong target status=%d body=%s", wrongRecorder.Code, wrongRecorder.Body.String())
	}
}

func TestStackIngressRejectsMissingToken(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	e, rec := ingressEvent("stack-1", "", "", "")
	_ = crudRouteHandlers{stackStore: store}.stackIngress(e)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestAttachDomainRejectsExpiredToken(t *testing.T) {
	t.Setenv("STACK_CONTROL_PLANE_SECRET", testServiceSecret)
	store := controlplane.NewMemoryStore()
	token := signCloudServiceToken(t, testServiceSecret, "auth0|user-1", "tenant-1",
		[]string{stackDomainAttachScope}, time.Now().Add(-time.Minute))
	e, rec := attachDomainEvent(`{"domain":"acme.dev"}`, token)
	_ = crudRouteHandlers{stackStore: store, jobStore: store}.attachDomainToStack(e)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
