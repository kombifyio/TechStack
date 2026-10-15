package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimeexecutor"
	"github.com/kombifyio/techstack/internal/managedstackkit"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

type recordingStackKitOperations struct {
	input managedstackkit.OperationsRequest
	err   error
}

func (service *recordingStackKitOperations) Execute(_ context.Context, input managedstackkit.OperationsRequest) (runtimeexecutor.ExecutionOutcome, error) {
	service.input = input
	if service.err != nil {
		return runtimeexecutor.ExecutionOutcome{}, service.err
	}
	return runtimeexecutor.ExecutionOutcome{Runtime: []runtimeexecutor.RuntimeOutcome{{RequirementID: "requirement-a", Status: runtimeexecutor.RuntimeStatusApplied}}}, nil
}

func TestStackKitOperationsReturnsStableActionableRejection(t *testing.T) {
	const token = "opaque-runtime-token"
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: "runtime-1", TenantID: "tenant-1", StackID: "stack-1", Status: "connected", Approved: true,
		Resources: map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
	}); err != nil {
		t.Fatal(err)
	}
	service := &recordingStackKitOperations{err: errors.New("opaque internal rejection")}
	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store, StackKitOperations: service})
	response := performStackKitOperationsRequest(t, router, keyBoundAuth(t, store, "tenant-1", "runtime-1", token), "tenant-1", workerStackKitOperationsRequest{
		SchemaVersion: stackKitOperationsRequestSchema, ChannelRef: stackKitOperationsChannelRef,
	})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Details managedstackkit.OperationRejectionDetails `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Details.ReasonCode != "managed_stackkit_operation_rejected" || envelope.Error.Details.UserGuidance == "" {
		t.Fatalf("details = %#v", envelope.Error.Details)
	}
}

func TestStackKitOperationsAuthenticatesAndReturnsProtocolEnvelope(t *testing.T) {
	const token = "opaque-runtime-token"
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: "runtime-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		Status: "connected", Approved: true,
		Resources: map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
	}); err != nil {
		t.Fatal(err)
	}
	service := &recordingStackKitOperations{}
	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store, StackKitOperations: service})
	recorder := performStackKitOperationsRequest(t, router, keyBoundAuth(t, store, "tenant-1", "runtime-1", token), "tenant-1", workerStackKitOperationsRequest{
		SchemaVersion: stackKitOperationsRequestSchema, ChannelRef: stackKitOperationsChannelRef,
		Request: runtimeexecutor.ExecutionRequest{RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.input.TenantID != "tenant-1" || service.input.StackID != "stack-1" || service.input.RuntimeAgentID != "runtime-1" {
		t.Fatalf("authenticated input = %#v", service.input)
	}
	var response workerStackKitOperationsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SchemaVersion != stackKitOperationsResponseSchema || response.ChannelRef != stackKitOperationsChannelRef || len(response.Outcome.Runtime) != 1 {
		t.Fatalf("response = %#v", response)
	}
}

func TestStackKitOperationsRejectsMissingBearerAndTenantMismatch(t *testing.T) {
	const token = "opaque-runtime-token"
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: "runtime-1", TenantID: "tenant-1", StackID: "stack-1", Status: "connected", Approved: true,
		Resources: map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
	}); err != nil {
		t.Fatal(err)
	}
	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store, StackKitOperations: &recordingStackKitOperations{}})
	envelope := workerStackKitOperationsRequest{SchemaVersion: stackKitOperationsRequestSchema, ChannelRef: stackKitOperationsChannelRef}
	if response := performStackKitOperationsRequest(t, router, bearerAuth(""), "tenant-1", envelope); response.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d body=%s", response.Code, response.Body.String())
	}
	if response := performStackKitOperationsRequest(t, router, bearerAuth(token), "tenant-other", envelope); response.Code != http.StatusUnauthorized {
		t.Fatalf("tenant mismatch status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestStackKitOperationsFailsClosedWithoutService(t *testing.T) {
	store := controlplane.NewMemoryStore()
	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store})
	response := performStackKitOperationsRequest(t, router, bearerAuth("opaque-runtime-token"), "tenant-1", workerStackKitOperationsRequest{})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
}

// bearerAuth authenticates a request with a bearer credential alone.
func bearerAuth(token string) func(*http.Request) {
	return func(request *http.Request) {
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
}

// keyBoundAuth binds a fresh Guard key to the stored worker and authenticates
// requests with the key-bound access token and a proof per request.
func keyBoundAuth(t *testing.T, store *controlplane.MemoryStore, tenantID, workerID, credential string) func(*http.Request) {
	t.Helper()
	key := newGuardKey(t)
	jwk, err := guardidentity.ParseJWK(mustJSON(t, guardidentity.PublicJWK(key)))
	if err != nil {
		t.Fatal(err)
	}
	thumbprint, _ := jwk.Thumbprint()
	canonical, _ := jwk.Canonical()
	now := time.Now().UTC()
	if _, err := store.IssueWorkerIdentity(t.Context(), controlplane.WorkerIdentityIssue{
		TenantID: tenantID, WorkerID: workerID, AttemptID: "attempt-0123456789ab", Thumbprint: thumbprint,
		PublicKeyJWK: canonical, CredentialSHA256: workerauth.SHA256Hex(credential), IssuedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmWorkerIdentity(t.Context(), tenantID, workerID, thumbprint, now); err != nil {
		t.Fatal(err)
	}
	token, _, err := guardidentity.IssueAccessToken(workerRouteHandlers{credentialSecret: workerauth.SecretFromEnv()}.guardAccessSecret(), guardidentity.AccessClaims{
		TenantID: tenantID, WorkerID: workerID, JKT: thumbprint, Cred: guardidentity.CredentialFingerprint(workerauth.SHA256Hex(credential)),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return func(request *http.Request) {
		request.Header.Set("Authorization", guardidentity.TokenType+" "+token)
		request.Header.Set(guardidentity.ProofHeader, guardProof(t, key, request.Method, request.URL.Path, token, time.Now().UTC()))
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func performStackKitOperationsRequest(t *testing.T, router http.Handler, authorize func(*http.Request), tenant string, envelope workerStackKitOperationsRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workers/runtime-1/stackkit/operations", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Kombify-Tenant-ID", tenant)
	authorize(request)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
