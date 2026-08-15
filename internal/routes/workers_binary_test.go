package routes

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

func TestWorkerRoutesPublishPairingBoundPOSTWithoutPublicGET(t *testing.T) {
	router := httpx.NewRouter()
	store := controlplane.NewMemoryStore()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store})

	postRecorder := httptest.NewRecorder()
	router.ServeHTTP(postRecorder, httptest.NewRequest(http.MethodPost, "/api/v1/agent/binary/linux/amd64", nil))
	if postRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("POST status = %d, want pairing-token challenge %d", postRecorder.Code, http.StatusUnauthorized)
	}

	getRecorder := httptest.NewRecorder()
	router.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/agent/binary/linux/amd64", nil))
	if getRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unauthenticated GET status = %d, want %d", getRecorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestAgentBinaryAcceptsInstallerContentTypeWithoutConsumingPairingToken(t *testing.T) {
	handler, store := newAgentBinaryTestHandler(t, "linux-agent-binary-fixture", "amd64")
	router := agentBinaryTestRouter(handler)

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/agent/binary/linux/x86_64", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/binary/linux/x86_64", nil)
	req.Header.Set("Authorization", "Bearer "+agentBinaryTestCredential())
	req.Header.Set("Content-Type", "application/octet-stream")
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	digest := sha256.Sum256([]byte(agentBinaryTestCredential()))
	token, err := store.GetPairingTokenByHash(t.Context(), "tenant-1", hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatalf("GetPairingTokenByHash: %v", err)
	}
	if token.Status != "active" || token.UsedAt != nil {
		t.Fatalf("binary authorization consumed pairing token: %#v", token)
	}
}

func TestAgentBinaryAcceptsExistingRuntimeAgentCredential(t *testing.T) {
	handler, store := newAgentBinaryTestHandler(t, "linux-agent-binary-fixture", "amd64")
	const (
		runtimeAgentID = "runtime-1"
		tenantID       = "tenant-1"
	)
	runtimeToken := runtimeAgentBinaryTestCredential()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID:             runtimeAgentID,
		TenantID:       tenantID,
		OwnerSubjectID: "owner-1",
		StackID:        "stack-1",
		Status:         "approved",
		Approved:       true,
		Resources: map[string]any{
			"agent_token_sha256": workerauth.SHA256Hex(runtimeToken),
		},
		Capabilities: map[string]any{
			"server_id": "server-1",
		},
	}); err != nil {
		t.Fatalf("seed runtime worker: %v", err)
	}

	recorder := performAgentBinaryRuntimeRequest(
		t,
		agentBinaryTestRouter(handler),
		"/api/v1/agent/binary/linux/amd64",
		runtimeToken,
		tenantID,
		runtimeAgentID,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
}

func TestAgentBinaryRejectsRuntimeAgentCredentialWithoutExactBinding(t *testing.T) {
	handler, store := newAgentBinaryTestHandler(t, "linux-agent-binary-fixture", "amd64")
	runtimeToken := runtimeAgentBinaryTestCredential()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID:             "runtime-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		StackID:        "stack-1",
		Status:         "approved",
		Approved:       true,
		Resources: map[string]any{
			"agent_token_sha256": workerauth.SHA256Hex(runtimeToken),
		},
	}); err != nil {
		t.Fatalf("seed runtime worker: %v", err)
	}

	for _, test := range []struct {
		name           string
		tenantID       string
		runtimeAgentID string
	}{
		{name: "missing tenant", runtimeAgentID: "runtime-1"},
		{name: "wrong tenant", tenantID: "tenant-2", runtimeAgentID: "runtime-1"},
		{name: "wrong runtime agent", tenantID: "tenant-1", runtimeAgentID: "runtime-2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := performAgentBinaryRuntimeRequest(
				t,
				agentBinaryTestRouter(handler),
				"/api/v1/agent/binary/linux/amd64",
				runtimeToken,
				test.tenantID,
				test.runtimeAgentID,
			)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
		})
	}
}

func TestAgentBinaryAcceptsLegacyRuntimeWorkerWithRedeemedPairingCredential(t *testing.T) {
	handler, store := newAgentBinaryTestHandler(t, "linux-agent-binary-fixture", "amd64")
	legacyToken := agentBinaryTestCredential()
	tokenDigest := sha256.Sum256([]byte(legacyToken))
	tokenHash := hex.EncodeToString(tokenDigest[:])
	if _, err := store.ClaimPairingToken(t.Context(), "tenant-1", tokenHash, time.Now().UTC()); err != nil {
		t.Fatalf("redeem legacy pairing credential: %v", err)
	}
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID:             "runtime-legacy",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		StackID:        "stack-1",
		TokenHash:      tokenHash,
		Status:         "approved",
		Approved:       true,
	}); err != nil {
		t.Fatalf("seed legacy runtime worker: %v", err)
	}

	recorder := performAgentBinaryRuntimeRequest(
		t,
		agentBinaryTestRouter(handler),
		"/api/v1/agent/binary/linux/amd64",
		legacyToken,
		"tenant-1",
		"runtime-legacy",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
}

func TestAgentBinaryServesExactArtifactWithIntegrityMetadata(t *testing.T) {
	const body = "linux-agent-binary-fixture"
	handler, _ := newAgentBinaryTestHandler(t, body, "amd64")
	recorder := performAgentBinaryTestRequest(t, agentBinaryTestRouter(handler), "/api/v1/agent/binary/linux/x86_64")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Body.String(); got != body {
		t.Fatalf("body = %q, want exact artifact %q", got, body)
	}
	digest := sha256.Sum256([]byte(body))
	if got, want := recorder.Header().Get(agentBinaryChecksumHeader), hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("checksum header = %q, want %q", got, want)
	}
	if got := recorder.Header().Get("Content-Disposition"); got != `attachment; filename="techstack-linux-amd64"` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(body))
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", got)
	}
	if strings.Contains(recorder.Body.String(), agentBinaryTestCredential()) || strings.Contains(recorder.Header().Get("Content-Disposition"), agentBinaryTestCredential()) {
		t.Fatal("artifact response leaked the pairing token")
	}
}

func TestAgentBinaryReportsArchitectureMismatchHonestly(t *testing.T) {
	handler, _ := newAgentBinaryTestHandler(t, "fixture", "amd64")
	recorder := performAgentBinaryTestRequest(t, agentBinaryTestRouter(handler), "/api/v1/agent/binary/linux/arm64")

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Kombify-Artifact-Arch"); got != "amd64" {
		t.Fatalf("available artifact arch = %q, want amd64", got)
	}
}

func TestAgentBinaryLimitsEachPairingTokenToTwoDownloads(t *testing.T) {
	handler, _ := newAgentBinaryTestHandler(t, "fixture", "amd64")
	router := agentBinaryTestRouter(handler)
	for attempt := 1; attempt <= agentBinaryMaxAttemptsPerToken; attempt++ {
		if recorder := performAgentBinaryTestRequest(t, router, "/api/v1/agent/binary/linux/amd64"); recorder.Code != http.StatusOK {
			t.Fatalf("attempt %d status = %d, want 200", attempt, recorder.Code)
		}
	}
	recorder := performAgentBinaryTestRequest(t, router, "/api/v1/agent/binary/linux/amd64")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("third attempt status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
}

func TestAgentBinaryNotModifiedChecksDoNotConsumeDownloadAllowance(t *testing.T) {
	const body = "current-agent"
	handler, _ := newAgentBinaryTestHandler(t, body, "amd64")
	router := agentBinaryTestRouter(handler)
	digest := sha256.Sum256([]byte(body))
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	for attempt := 0; attempt < 3; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/binary/linux/amd64", nil)
		request.Header.Set("Authorization", "Bearer "+agentBinaryTestCredential())
		request.Header.Set("If-None-Match", etag)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotModified {
			t.Fatalf("attempt %d status = %d, want 304", attempt, recorder.Code)
		}
	}
	if recorder := performAgentBinaryTestRequest(t, router, "/api/v1/agent/binary/linux/amd64"); recorder.Code != http.StatusOK {
		t.Fatalf("download after metadata checks = %d, want 200", recorder.Code)
	}
}

func TestAgentBinaryDownloadGuardBoundsConcurrentTransfers(t *testing.T) {
	guard := newAgentBinaryDownloadGuard()
	now := time.Now().UTC()
	expiresAt := now.Add(15 * time.Minute)
	releaseFirst, firstOK := guard.acquire("token-hash-1", expiresAt, now, agentBinaryTestArtifactBytes)
	releaseSecond, secondOK := guard.acquire("token-hash-2", expiresAt, now, agentBinaryTestArtifactBytes)
	if !firstOK || !secondOK {
		t.Fatal("first two concurrent downloads should be admitted")
	}
	if releaseThird, thirdOK := guard.acquire("token-hash-3", expiresAt, now, agentBinaryTestArtifactBytes); thirdOK || releaseThird != nil {
		t.Fatal("third concurrent download must be rejected")
	}
	releaseFirst()
	defer releaseSecond()
	if releaseThird, thirdOK := guard.acquire("token-hash-3", expiresAt, now, agentBinaryTestArtifactBytes); !thirdOK || releaseThird == nil {
		t.Fatal("download should be admitted after a slot is released")
	} else {
		releaseThird()
	}
}

// agentBinaryTestArtifactBytes stands in for the served artifact size, which is
// what the byte budget is derived from.
const agentBinaryTestArtifactBytes int64 = 1024

// A converging Agent has to fetch a ~78 MB artifact in slices, because the
// platform proxy resets a single large response part-way. The budget must
// therefore admit many small Range responses as long as they add up to less
// than the allowance a whole-artifact download would have spent.
func TestAgentBinaryDownloadBudgetAdmitsSlicedTransfers(t *testing.T) {
	guard := newAgentBinaryDownloadGuard()
	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)
	const slice = agentBinaryTestArtifactBytes / 16

	for served := int64(0); served < agentBinaryTestArtifactBytes*agentBinaryMaxAttemptsPerToken; served += slice {
		release, ok := guard.acquire("sliced-token", expiresAt, now, agentBinaryTestArtifactBytes)
		if !ok {
			t.Fatalf("slice at %d bytes was rejected while budget remained", served)
		}
		guard.record("sliced-token", slice)
		release()
	}

	if release, ok := guard.acquire("sliced-token", expiresAt, now, agentBinaryTestArtifactBytes); ok || release != nil {
		t.Fatal("budget must be exhausted once the allowance has been served")
	}
}

// A transfer the platform cuts short must not spend the whole allowance:
// otherwise a reset connection permanently denies the Agent its next attempt.
func TestAgentBinaryDownloadBudgetChargesOnlyDeliveredBytes(t *testing.T) {
	guard := newAgentBinaryDownloadGuard()
	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)

	for attempt := 0; attempt < 20; attempt++ {
		release, ok := guard.acquire("reset-token", expiresAt, now, agentBinaryTestArtifactBytes)
		if !ok {
			t.Fatalf("truncated attempt %d was rejected", attempt+1)
		}
		guard.record("reset-token", 16)
		release()
	}
}

func TestAgentBinaryDownloadAttemptsRemainBoundUntilTokenExpiry(t *testing.T) {
	guard := newAgentBinaryDownloadGuard()
	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)
	for attempt := 0; attempt < agentBinaryMaxAttemptsPerToken; attempt++ {
		release, ok := guard.acquire("long-lived-token", expiresAt, now.Add(time.Duration(attempt)*time.Minute), agentBinaryTestArtifactBytes)
		if !ok {
			t.Fatalf("attempt %d was rejected", attempt+1)
		}
		guard.record("long-lived-token", agentBinaryTestArtifactBytes)
		release()
	}
	if release, ok := guard.acquire("long-lived-token", expiresAt, now.Add(30*time.Minute), agentBinaryTestArtifactBytes); ok || release != nil {
		t.Fatal("attempt budget must not reset before the pairing token expires")
	}
	if release, ok := guard.acquire("long-lived-token", expiresAt.Add(time.Hour), expiresAt.Add(time.Second), agentBinaryTestArtifactBytes); !ok || release == nil {
		t.Fatal("expired tracking entry should be reusable only with a newly valid token window")
	} else {
		release()
	}
}

func TestAgentBinaryDownloadGuardEvictsDeterministicallyWhenTrackingIsFull(t *testing.T) {
	guard := newAgentBinaryDownloadGuard()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(30 * time.Minute)
	for i := 0; i < agentBinaryMaxTrackedTokens; i++ {
		guard.attempts["token-"+strconv.Itoa(i)] = agentBinaryDownloadAttempt{
			bytesServed: 1,
			resetAt:     resetAt,
		}
	}

	release, ok := guard.acquire("new-token", resetAt, now, agentBinaryTestArtifactBytes)
	if !ok || release == nil {
		t.Fatal("new token should be admitted by evicting one tracked token")
	}
	release()
	if len(guard.attempts) != agentBinaryMaxTrackedTokens {
		t.Fatalf("tracked attempts = %d, want %d", len(guard.attempts), agentBinaryMaxTrackedTokens)
	}
	if _, exists := guard.attempts["token-0"]; exists {
		t.Fatal("lexicographically first token should be evicted when expirations tie")
	}
	if attempt, exists := guard.attempts["new-token"]; !exists || attempt.bytesServed != 0 || !attempt.resetAt.Equal(resetAt) {
		t.Fatalf("new token attempt = %+v, exists=%v", attempt, exists)
	}
}

func newAgentBinaryTestHandler(t *testing.T, body, arch string) (workerRouteHandlers, *controlplane.MemoryStore) {
	t.Helper()
	path := t.TempDir() + "/techstack"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	artifact, err := agentBinaryArtifactForPath(path, "linux", arch)
	if err != nil {
		t.Fatalf("agentBinaryArtifactForPath: %v", err)
	}
	digest := sha256.Sum256([]byte(agentBinaryTestCredential()))
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
		ID:             "pairing-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		TokenHash:      hex.EncodeToString(digest[:]),
		Status:         "active",
		ExpiresAt:      timePointer(time.Now().UTC().Add(15 * time.Minute)),
	}); err != nil {
		t.Fatalf("UpsertPairingToken: %v", err)
	}
	return workerRouteHandlers{
		wst: store,
		binaryArtifact: func() (agentBinaryArtifact, error) {
			return artifact, nil
		},
		binaryDownloadGuard: newAgentBinaryDownloadGuard(),
	}, store
}

func agentBinaryTestRouter(handler workerRouteHandlers) *httpx.Router {
	router := httpx.NewRouter()
	router.POST("/api/v1/agent/binary/{os}/{arch}", handler.agentBinary)
	return router
}

func performAgentBinaryTestRequest(t *testing.T, router *httpx.Router, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, nil)
	req.Header.Set("Authorization", "Bearer "+agentBinaryTestCredential())
	req.Header.Set("Content-Type", "application/octet-stream")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func performAgentBinaryRuntimeRequest(
	t *testing.T,
	router *httpx.Router,
	target, token, tenantID, runtimeAgentID string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Kombify-Tenant-ID", tenantID)
	req.Header.Set(agentBinaryRuntimeAgentIDHeader, runtimeAgentID)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func timePointer(value time.Time) *time.Time { return &value }

func agentBinaryTestCredential() string {
	return strings.Join([]string{
		"kpt1",
		base64.RawURLEncoding.EncodeToString([]byte("tenant-1")),
		base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
	}, ".")
}

func runtimeAgentBinaryTestCredential() string {
	return "tsra.opaque." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
}
