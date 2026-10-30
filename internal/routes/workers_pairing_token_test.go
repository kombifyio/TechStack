package routes

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardrelease"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

func TestPairingRevocationReadbackMatchesCapabilityRefusalAndPreservesOtherPairing(t *testing.T) {
	configureArtifactAdmissionTestIndex(t)
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

type artifactManifestShapeVerifier struct{}

func (artifactManifestShapeVerifier) VerifyManifest(raw, _ []byte) (*guardrelease.Manifest, error) {
	return guardrelease.ParseManifest(raw)
}

// This auth-boundary assertion crosses the real HTTP route and exercises only
// synthetic capability state. It is not a PostgreSQL or deployed R2 journey.
func TestArtifactAdmissionNeverServesPayloadOnRender(t *testing.T) {
	t.Setenv("RENDER", "true")
	binaryHash, runtimeHash := configureArtifactAdmissionTestIndex(t)
	cases := []struct {
		name, path, credential, rangeHeader, ifRange, etag string
		want                                               int
	}{
		{name: "binary metadata with unavailable payload", path: "binary", credential: agentBinaryTestCredential(), want: 200},
		{name: "runtime metadata with unavailable payload", path: "stackkit-release", credential: agentBinaryTestCredential(), want: 200},
		{name: "invalid capability", path: "binary", credential: "invalid", want: 401},
		{name: "missing capability", path: "binary", want: 401},
		{name: "genuine installed binary", path: "binary", credential: agentBinaryTestCredential(), etag: `"` + binaryHash + `"`, want: 304},
		{name: "genuine installed runtime", path: "stackkit-release", credential: agentBinaryTestCredential(), etag: `"` + runtimeHash + `"`, want: 304},
		{name: "single range metadata", path: "binary", credential: agentBinaryTestCredential(), rangeHeader: "bytes=10-19", want: 200},
		{name: "suffix range metadata", path: "binary", credential: agentBinaryTestCredential(), rangeHeader: "bytes=-10", want: 200},
		{name: "open range metadata", path: "binary", credential: agentBinaryTestCredential(), rangeHeader: "bytes=90-", want: 200},
		{name: "clipped range metadata", path: "binary", credential: agentBinaryTestCredential(), rangeHeader: "bytes=90-999", want: 200},
		{name: "outside range", path: "binary", credential: agentBinaryTestCredential(), rangeHeader: "bytes=100-", want: 416},
		{name: "multiple ranges", path: "binary", credential: agentBinaryTestCredential(), rangeHeader: "bytes=1-2,4-5", want: 416},
		{name: "if range mismatch selects full", path: "binary", credential: agentBinaryTestCredential(), rangeHeader: "bytes=100-", ifRange: `"other"`, want: 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, _ := newAgentBinaryTestHandler(t, "MUST_NEVER_BE_DELIVERED", "amd64")
			// A stale injected file artifact cannot bypass the configured index.
			router := agentBinaryTestRouter(handler)
			router.POST("/api/v1/agent/stackkit-release/{os}/{arch}", handler.agentStackKitRelease)
			server := httptest.NewServer(router)
			defer server.Close()
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/agent/"+tc.path+"/linux/amd64", nil)
			if tc.credential != "" {
				request.Header.Set("Authorization", "Bearer "+tc.credential)
			}
			request.Header.Set("Range", tc.rangeHeader)
			request.Header.Set("If-Range", tc.ifRange)
			request.Header.Set("If-None-Match", tc.etag)
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d body=%s", response.StatusCode, tc.want, body)
			}
			if len(body) > 4096 || strings.Contains(string(body), "MUST_NEVER_BE_DELIVERED") {
				t.Fatal("artifact payload crossed metadata route")
			}
			if tc.want == 304 && len(body) != 0 {
				t.Fatal("304 carried a body")
			}
			if tc.want == 200 {
				var result struct {
					Data struct {
						Schema, SHA256, Filename string
						Size                     int64
						ObjectKey                string `json:"object_key"`
					}
				}
				if json.Unmarshal(body, &result) != nil || result.Data.Schema != "techstack.artifact-admission/v1" || result.Data.Size <= 0 || !strings.Contains(result.Data.ObjectKey, "/sha256/"+result.Data.SHA256+"/") {
					t.Fatalf("invalid admission %s", body)
				}
			}
		})
	}
	t.Run("range budgets preserve chunks and stop excess", func(t *testing.T) {
		handler, _ := newAgentBinaryTestHandler(t, "unused", "amd64")
		router := agentBinaryTestRouter(handler)
		server := httptest.NewServer(router)
		defer server.Close()
		for i := 0; i < 21; i++ {
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/agent/binary/linux/amd64", nil)
			request.Header.Set("Authorization", "Bearer "+agentBinaryTestCredential())
			request.Header.Set("Range", "bytes=0-9")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			want := 200
			if i == 20 {
				want = 429
			}
			if response.StatusCode != want {
				t.Fatalf("range %d status %d want %d", i, response.StatusCode, want)
			}
		}
	})
	t.Run("renewed signed policy preserves immutable delivery", func(t *testing.T) {
		dir := os.Getenv("TECHSTACK_GUARD_RELEASE_DIR")
		seed, _, err := currentGuardManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		fresh := *seed.manifest
		fresh.IssuedAt = fresh.IssuedAt.Add(time.Minute)
		fresh.ExpiresAt = fresh.ExpiresAt.Add(time.Minute)
		freshRaw, _ := json.Marshal(fresh)
		refreshedGuardManifest.Lock()
		prior := refreshedGuardManifest.value
		refreshedGuardManifest.value = &guardManifestVersion{raw: freshRaw, sig: []byte(`{}`), manifest: &fresh}
		refreshedGuardManifest.Unlock()
		defer func() {
			refreshedGuardManifest.Lock()
			refreshedGuardManifest.value = prior
			refreshedGuardManifest.Unlock()
		}()
		handler, _ := newAgentBinaryTestHandler(t, "MUST_NEVER_BE_DELIVERED", "amd64")
		response := performAgentBinaryTestRequest(t, agentBinaryTestRouter(handler), "/api/v1/agent/binary/linux/amd64")
		if response.Code != 200 {
			t.Fatalf("renewed policy interrupted same artifact: %d %s", response.Code, response.Body.String())
		}
		// Revocation has effect before conditional304 and never waits on a new catalog.
		revoked := fresh
		revoked.RevokedDigests = []string{binaryHash}
		revokedRaw, _ := json.Marshal(revoked)
		refreshedGuardManifest.Lock()
		refreshedGuardManifest.value = &guardManifestVersion{raw: revokedRaw, sig: []byte(`{}`), manifest: &revoked}
		refreshedGuardManifest.Unlock()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/binary/linux/amd64", nil)
		request.Header.Set("Authorization", "Bearer "+agentBinaryTestCredential())
		request.Header.Set("If-None-Match", `"`+binaryHash+`"`)
		recorder := httptest.NewRecorder()
		agentBinaryTestRouter(handler).ServeHTTP(recorder, request)
		if recorder.Code != 403 {
			t.Fatalf("renewed revocation ignored: %d %s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("baked current policy revokes outer runtime before body or 304", func(t *testing.T) {
		dir := os.Getenv("TECHSTACK_GUARD_RELEASE_DIR")
		path := filepath.Join(dir, guardReleaseManifestFile)
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(path, original, 0600)
		var current guardrelease.Manifest
		if err := json.Unmarshal(original, &current); err != nil {
			t.Fatal(err)
		}
		current.IssuedAt = current.IssuedAt.Add(time.Minute)
		current.RevokedDigests = []string{runtimeHash}
		baked, err := json.Marshal(current)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, baked, 0600); err != nil {
			t.Fatal(err)
		}
		handler, _ := newAgentBinaryTestHandler(t, "MUST_NEVER_BE_DELIVERED", "amd64")
		router := agentBinaryTestRouter(handler)
		router.POST("/api/v1/agent/stackkit-release/{os}/{arch}", handler.agentStackKitRelease)
		for _, etag := range []string{"", `"` + runtimeHash + `"`} {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/stackkit-release/linux/amd64", nil)
			request.Header.Set("Authorization", "Bearer "+agentBinaryTestCredential())
			request.Header.Set("If-None-Match", etag)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("baked revocation with ETag %q returned %d: %s", etag, recorder.Code, recorder.Body.String())
			}
		}
	})

	t.Run("invalid index cannot select fallback", func(t *testing.T) {
		t.Setenv("TECHSTACK_ARTIFACT_INDEX_SHA256", strings.Repeat("0", 64))
		handler, _ := newAgentBinaryTestHandler(t, "MUST_NEVER_BE_DELIVERED", "amd64")
		response := performAgentBinaryTestRequest(t, agentBinaryTestRouter(handler), "/api/v1/agent/binary/linux/amd64")
		if response.Code != 503 || strings.Contains(response.Body.String(), "MUST_NEVER_BE_DELIVERED") {
			t.Fatalf("fallback: %d %s", response.Code, response.Body.String())
		}
	})
}

func configureArtifactAdmissionTestIndex(t *testing.T) (string, string) {
	t.Helper()
	previousVerifierFactory := guardManifestVerifierFactory
	guardManifestVerifierFactory = func(func() time.Time) (guardManifestVerifier, error) {
		return artifactManifestShapeVerifier{}, nil
	}
	t.Cleanup(func() { guardManifestVerifierFactory = previousVerifierFactory })
	dir := t.TempDir()
	binaryHash, runtimeHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	manifest := map[string]any{
		"schemaVersion": "guard.release-manifest/v1", "version": "0.43.6", "minimumAdmittedVersion": "0.42.19",
		"revokedVersions": []string{}, "revokedDigests": []string{},
		"artifacts": []map[string]any{{"os": "linux", "arch": "amd64", "sha256": binaryHash, "size": 100}},
		"stackkits": map[string]any{"version": "v0.54.4", "archiveSha256": runtimeHash, "binarySha256": runtimeHash, "serverSha256": runtimeHash, "bundleSha256": runtimeHash},
		"issuedAt":  time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339), "expiresAt": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	}
	rawManifest, _ := json.Marshal(manifest)
	for name, raw := range map[string][]byte{"release-manifest.json": rawManifest, "release-manifest.sigstore.json": []byte(`{}`),
		"release-manifest-seed.json": rawManifest, "release-manifest-seed.sigstore.json": []byte(`{}`)} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	index := map[string]any{"schema": "techstack.artifact-index/v1", "release_version": "0.43.6", "manifest_sha256": fmt.Sprintf("%x", sha256.Sum256(rawManifest)),
		"artifacts": []map[string]any{
			{"kind": "guard_binary", "os": "linux", "arch": "amd64", "sha256": binaryHash, "size": 100, "filename": "techstack-linux-amd64"},
			{"kind": "stackkit_runtime", "os": "linux", "arch": "amd64", "sha256": runtimeHash, "size": 200, "filename": "stackkit-runtime-linux-amd64.tar.gz"},
		},
	}
	rawIndex, _ := json.Marshal(index)
	indexPath := filepath.Join(dir, "index.json")
	if err := os.WriteFile(indexPath, rawIndex, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TECHSTACK_GUARD_RELEASE_DIR", dir)
	t.Setenv("TECHSTACK_ARTIFACT_INDEX_PATH", indexPath)
	t.Setenv("TECHSTACK_ARTIFACT_INDEX_SHA256", fmt.Sprintf("%x", sha256.Sum256(rawIndex)))

	return binaryHash, runtimeHash
}
