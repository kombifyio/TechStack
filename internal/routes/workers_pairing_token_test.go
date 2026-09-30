package routes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

func TestPairingRevocationReadbackMatchesCapabilityRefusalAndPreservesOtherPairing(t *testing.T) {
	handler, store := newAgentBinaryTestHandler(t, "canonical-artifact", "amd64")
	foreignToken, foreignHash, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
		ID: "protected-pairing", TenantID: "tenant-1", OwnerSubjectID: "owner-1", TokenHash: foreignHash,
		Status: "active", ExpiresAt: timePointer(time.Now().UTC().Add(time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}
	router := agentBinaryTestRouter(handler)
	RegisterTrustRoutesWithStores(router, TrustRouteStores{Workers: store})
	if response := performAgentBinaryTestRequest(t, router, "/api/v1/agent/binary/linux/amd64"); response.Code != http.StatusOK {
		t.Fatalf("initial capability refused: %d", response.Code)
	}
	ownerRequest := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}
	if response := ownerRequest(http.MethodDelete, "/api/v1/trust/pairing-tokens/pairing-1"); response.Code != http.StatusOK {
		t.Fatalf("owner revocation failed: %d", response.Code)
	}
	if response := performAgentBinaryTestRequest(t, router, "/api/v1/agent/binary/linux/amd64"); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked capability still usable: %d", response.Code)
	}
	foreignReq := httptest.NewRequest(http.MethodPost, "/api/v1/agent/binary/linux/amd64", nil)
	foreignReq.Header.Set("Authorization", "Bearer "+foreignToken)
	foreignRecorder := httptest.NewRecorder()
	router.ServeHTTP(foreignRecorder, foreignReq)
	if foreignRecorder.Code != http.StatusOK {
		t.Fatalf("protected capability changed: %d", foreignRecorder.Code)
	}
	response := ownerRequest(http.MethodGet, "/api/v1/trust/pairing-tokens")
	var listed struct {
		Data struct {
			Tokens []struct{ ID, Status string } `json:"tokens"`
		} `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &listed) != nil {
		t.Fatal("owner revocation readback unavailable")
	}
	states := make(map[string]string)
	for _, row := range listed.Data.Tokens {
		states[row.ID] = row.Status
	}
	if states["pairing-1"] != "revoked" || states["protected-pairing"] != "active" {
		t.Fatalf("readback contradicts capability effects: %#v", states)
	}
}

func TestResolveStorePairingTokenUsesVersionedTenantScope(t *testing.T) {
	rawToken, tokenHash, generateErr := pairingtoken.Generate("tenant-1")
	if generateErr != nil {
		t.Fatal(generateErr)
	}
	store := &pairingLookupRecordingStore{result: activePairingToken("tenant-1", tokenHash)}
	event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/register", "")

	resolved, resolvedHash, resolveErr := (workerRouteHandlers{wst: store}).resolveStorePairingToken(event, rawToken)
	if resolveErr != nil || resolved == nil {
		t.Fatalf("resolve = %#v, %v; body=%s", resolved, resolveErr, recorder.Body.String())
	}
	if store.lookupTenant != "tenant-1" || store.lookupHash != tokenHash || resolvedHash != tokenHash {
		t.Fatalf("lookup tenant/hash = %q/%q, resolved hash %q", store.lookupTenant, store.lookupHash, resolvedHash)
	}
}

func TestResolveStorePairingTokenRejectsMalformedLocatorBeforeLookup(t *testing.T) {
	for _, rawToken := range []string{
		"kpt1.not-base64.short",
		"kpt2.dGVuYW50LTE.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"pairing-token-binary-test",
		strings.Repeat(".", pairingtoken.MaxWireBytes+1),
	} {
		t.Run(rawToken, func(t *testing.T) {
			store := &pairingLookupRecordingStore{}
			event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/register", "")
			resolved, resolvedHash, err := (workerRouteHandlers{wst: store}).resolveStorePairingToken(event, rawToken)
			if err != nil || resolved != nil || resolvedHash != "" {
				t.Fatalf("resolve = %#v/%q, %v", resolved, resolvedHash, err)
			}
			if store.lookupCalls != 0 {
				t.Fatalf("malformed token triggered %d store lookups", store.lookupCalls)
			}
			assertGenericPairingRejection(t, recorder.Code, recorder.Body.String())
		})
	}
}

func TestPairingConsumersStopAfterMalformedLocatorRejection(t *testing.T) {
	store := &pairingLookupRecordingStore{}
	handler := workerRouteHandlers{
		wst:                 store,
		binaryDownloadGuard: newAgentBinaryDownloadGuard(),
	}

	t.Run("binary", func(t *testing.T) {
		router := httpx.NewRouter()
		router.POST("/api/v1/agent/binary/{os}/{arch}", handler.agentBinary)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/binary/linux/amd64", nil)
		request.Header.Set("Authorization", "Bearer kpt1.not-base64.short")
		router.ServeHTTP(recorder, request)
		assertGenericPairingRejection(t, recorder.Code, recorder.Body.String())
	})

	t.Run("register", func(t *testing.T) {
		event, recorder := workerRouteTestEvent(
			http.MethodPost,
			"/api/v1/workers/register",
			`{"token":"kpt1.not-base64.short","hostname":"node-1"}`,
		)
		if err := handler.register(event); err != nil {
			t.Fatalf("register: %v", err)
		}
		assertGenericPairingRejection(t, recorder.Code, recorder.Body.String())
	})

	if store.lookupCalls != 0 || store.claimCalls != 0 {
		t.Fatalf("malformed consumers triggered %d lookups and %d claims", store.lookupCalls, store.claimCalls)
	}
}

func TestPairingConsumersFailClosedWhenStoreReturnsNilRow(t *testing.T) {
	rawToken, _, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	store := &pairingLookupRecordingStore{returnNil: true}
	handler := workerRouteHandlers{
		wst:                 store,
		binaryDownloadGuard: newAgentBinaryDownloadGuard(),
	}

	t.Run("binary", func(t *testing.T) {
		router := httpx.NewRouter()
		router.POST("/api/v1/agent/binary/{os}/{arch}", handler.agentBinary)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/binary/linux/amd64", nil)
		request.Header.Set("Authorization", "Bearer "+rawToken)
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("binary status/body = %d/%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("register", func(t *testing.T) {
		event, recorder := workerRouteTestEvent(
			http.MethodPost,
			"/api/v1/workers/register",
			`{"token":"`+rawToken+`","hostname":"node-1"}`,
		)
		if err := handler.register(event); err != nil {
			t.Fatalf("register: %v", err)
		}
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("register status/body = %d/%s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestResolveStorePairingTokenRejectsTenantMismatchWithoutDisclosure(t *testing.T) {
	rawToken, tokenHash, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	store := &pairingLookupRecordingStore{result: activePairingToken("tenant-2", tokenHash)}
	event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/register", "")

	resolved, resolvedHash, resolveErr := (workerRouteHandlers{wst: store}).resolveStorePairingToken(event, rawToken)
	if resolveErr != nil || resolved != nil || resolvedHash != "" {
		t.Fatalf("resolve = %#v/%q, %v", resolved, resolvedHash, resolveErr)
	}
	if store.lookupTenant != "tenant-1" {
		t.Fatalf("lookup tenant = %q, want locator tenant-1", store.lookupTenant)
	}
	assertGenericPairingRejection(t, recorder.Code, recorder.Body.String())
}

func TestResolveStorePairingTokenKeepsCanonicalLegacyFallback(t *testing.T) {
	for _, rawToken := range []string{
		base64.RawURLEncoding.EncodeToString(make([]byte, 24)),
		"ks_" + strings.Repeat("01", 32),
	} {
		t.Run(rawToken[:3], func(t *testing.T) {
			parsed, hashErr := pairingtoken.Parse(rawToken)
			if hashErr != nil {
				t.Fatal(hashErr)
			}
			tokenHash := parsed.TokenHash
			store := &pairingLookupRecordingStore{result: activePairingToken("tenant-1", tokenHash)}
			event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/register", "")

			resolved, resolvedHash, err := (workerRouteHandlers{wst: store}).resolveStorePairingToken(event, rawToken)
			if err != nil || resolved == nil || recorder.Code != http.StatusOK {
				t.Fatalf("legacy resolve = %#v, %v; status=%d body=%s", resolved, err, recorder.Code, recorder.Body.String())
			}
			if store.lookupTenant != "" || store.lookupHash != tokenHash || resolvedHash != tokenHash {
				t.Fatalf("legacy lookup tenant/hash = %q/%q, resolved hash %q", store.lookupTenant, store.lookupHash, resolvedHash)
			}
		})
	}
}

func TestResolveStorePairingTokenFailsClosedOnStoreFailures(t *testing.T) {
	rawToken, _, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		store      *pairingLookupRecordingStore
		wantStatus int
		wantBody   string
	}{
		{name: "not found", store: &pairingLookupRecordingStore{}, wantStatus: http.StatusUnauthorized, wantBody: "Invalid or expired token"},
		{name: "nil row without error", store: &pairingLookupRecordingStore{returnNil: true}, wantStatus: http.StatusInternalServerError, wantBody: "Failed to validate pairing token"},
		{name: "database failure", store: &pairingLookupRecordingStore{err: errors.New("database unavailable")}, wantStatus: http.StatusInternalServerError, wantBody: "Failed to validate pairing token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/register", "")
			resolved, resolvedHash, resolveErr := (workerRouteHandlers{wst: test.store}).resolveStorePairingToken(event, rawToken)
			if resolveErr != nil || resolved != nil || resolvedHash != "" {
				t.Fatalf("resolve = %#v/%q, %v", resolved, resolvedHash, resolveErr)
			}
			if recorder.Code != test.wantStatus || !strings.Contains(recorder.Body.String(), test.wantBody) {
				t.Fatalf("status/body = %d/%s, want %d containing %q", recorder.Code, recorder.Body.String(), test.wantStatus, test.wantBody)
			}
		})
	}
}

func TestWorkerRegisterRejectsOversizedBodyBeforeTokenClaim(t *testing.T) {
	store := &pairingLookupRecordingStore{}
	event, recorder := workerRouteTestEvent(
		http.MethodPost,
		"/api/v1/workers/register",
		`{"token":"unused","hostname":"node-1","tags":"`+strings.Repeat("x", int(workerRegistrationMaxBodyBytes))+`"}`,
	)
	if err := (workerRouteHandlers{wst: store}).register(event); err != nil {
		t.Fatalf("register: %v", err)
	}
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "Request body too large") {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	if store.claimCalls != 0 || store.lookupCalls != 0 {
		t.Fatalf("oversized body triggered %d claims and %d lookups", store.claimCalls, store.lookupCalls)
	}
}

func TestWorkerRegisterReleasesTokenAfterPostClaimEnrollmentFailure(t *testing.T) {
	ctx := context.Background()
	memory := controlplane.NewMemoryStore()
	rawToken, tokenHash, generateErr := pairingtoken.Generate("tenant-1")
	if generateErr != nil {
		t.Fatal(generateErr)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	if _, err := memory.UpsertPairingToken(ctx, controlplane.PairingToken{
		ID: "pair-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		TokenHash: tokenHash, Status: "active", ExpiresAt: &expiresAt,
	}); err != nil {
		t.Fatal(err)
	}

	event, recorder := workerRouteTestEvent(
		http.MethodPost,
		"/api/v1/workers/register",
		`{"token":"`+rawToken+`","hostname":"node-1"}`,
	)
	handler := workerRouteHandlers{wst: memory}
	if err := handler.register(event); err != nil {
		t.Fatalf("register: %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	// The transient failure produced no enrollment credential, so the
	// single-use capability returns to active for a retry instead of being
	// burned.
	released, err := memory.GetPairingTokenByHash(ctx, "tenant-1", tokenHash)
	if err != nil || released.Status != "active" || released.UsedAt != nil {
		t.Fatalf("token after failed enrollment = %#v, %v", released, err)
	}

	// The retry claims the same capability again and fails the same way; the
	// claim/release cycle stays repeatable and never marks it used.
	retryEvent, retryRecorder := workerRouteTestEvent(
		http.MethodPost,
		"/api/v1/workers/register",
		`{"token":"`+rawToken+`","hostname":"node-1"}`,
	)
	if err := handler.register(retryEvent); err != nil {
		t.Fatalf("retry register: %v", err)
	}
	if retryRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("retry status/body = %d/%s", retryRecorder.Code, retryRecorder.Body.String())
	}
	afterRetry, err := memory.GetPairingTokenByHash(ctx, "tenant-1", tokenHash)
	if err != nil || afterRetry.Status != "active" || afterRetry.UsedAt != nil {
		t.Fatalf("token after retry = %#v, %v", afterRetry, err)
	}
}

func TestWorkerRegisterReusesStackPlannedServer(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	plannedID := runtimeidentity.StackServerID("stack-1", "primary")
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: plannedID, TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		Name: "planned", LifecycleState: "planned", ConnectionState: "pending", HealthState: "unknown",
	}); err != nil {
		t.Fatal(err)
	}
	rawToken, tokenHash, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	if _, err := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
		ID: "pair-planned", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		TokenHash: tokenHash, Status: "active", ExpiresAt: &expiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	event, recorder := workerRouteTestEvent(
		http.MethodPost,
		"/api/v1/workers/register",
		`{"token":"`+rawToken+`","hostname":"node-1","os":"linux","arch":"amd64"}`,
	)
	if err := (workerRouteHandlers{wst: store, serverStore: store}).register(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	servers, err := store.ListServerRuntimesByTenant(t.Context(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].ID != plannedID || servers[0].WorkerID == "" {
		t.Fatalf("registration must adopt the planned server: %#v", servers)
	}
	worker, err := store.GetWorker(t.Context(), "tenant-1", servers[0].WorkerID)
	if err != nil {
		t.Fatal(err)
	}
	if worker.StackID != "stack-1" || worker.Capabilities["server_id"] != plannedID {
		t.Fatalf("worker/server binding = %#v", worker)
	}
}

func assertGenericPairingRejection(t *testing.T, status int, body string) {
	t.Helper()
	if status != http.StatusUnauthorized || !strings.Contains(body, "Invalid or expired token") {
		t.Fatalf("status/body = %d/%s, want generic 401", status, body)
	}
	if strings.Contains(body, "tenant-1") || strings.Contains(body, "tenant-2") {
		t.Fatalf("pairing rejection disclosed tenant identity: %s", body)
	}
}

func activePairingToken(tenantID, tokenHash string) *controlplane.PairingToken {
	expiresAt := time.Now().UTC().Add(time.Hour)
	return &controlplane.PairingToken{
		ID:             "pair-1",
		TenantID:       tenantID,
		OwnerSubjectID: "owner-1",
		TokenHash:      tokenHash,
		Status:         "active",
		ExpiresAt:      &expiresAt,
	}
}

type pairingLookupRecordingStore struct {
	controlplane.WorkerStore
	result       *controlplane.PairingToken
	err          error
	returnNil    bool
	lookupTenant string
	lookupHash   string
	lookupCalls  int
	claimCalls   int
}

func (s *pairingLookupRecordingStore) GetPairingTokenByHash(_ context.Context, tenantID, tokenHash string) (*controlplane.PairingToken, error) {
	s.lookupCalls++
	s.lookupTenant = tenantID
	s.lookupHash = tokenHash
	if s.err != nil {
		return nil, s.err
	}
	if s.returnNil {
		return nil, nil
	}
	if s.result == nil {
		return nil, controlplane.ErrNotFound
	}
	return s.result, nil
}

func (s *pairingLookupRecordingStore) ClaimPairingToken(_ context.Context, tenantID, tokenHash string, claimedAt time.Time) (*controlplane.PairingToken, error) {
	s.claimCalls++
	s.lookupTenant = tenantID
	s.lookupHash = tokenHash
	if s.err != nil {
		return nil, s.err
	}
	if s.returnNil {
		return nil, nil
	}
	if s.result == nil {
		return nil, controlplane.ErrNotFound
	}
	claimed := *s.result
	claimed.Status = "used"
	claimed.UsedAt = &claimedAt
	return &claimed, nil
}

var _ controlplane.WorkerStore = (*pairingLookupRecordingStore)(nil)
