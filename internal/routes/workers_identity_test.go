package routes

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

const guardTestHost = "http://example.com"

type guardHarness struct {
	t       *testing.T
	store   *controlplane.MemoryStore
	handler workerRouteHandlers
}

func newGuardHarness(t *testing.T) *guardHarness {
	t.Helper()
	store := controlplane.NewMemoryStore()
	return &guardHarness{t: t, store: store, handler: workerRouteHandlers{
		wst: store, serverStore: store, registryStore: store, rilStore: store, identityStore: store,
	}}
}

type guardReply struct {
	Code int
	Data map[string]any
	Err  struct {
		Code    string
		Reason  string
		Details map[string]any
	}
}

func decodeReply(t *testing.T, recorder *httptest.ResponseRecorder) guardReply {
	t.Helper()
	var envelope struct {
		Data  map[string]any `json:"data"`
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
		}
	}
	reply := guardReply{Code: recorder.Code, Data: envelope.Data}
	reply.Err.Code, reply.Err.Details = envelope.Error.Code, envelope.Error.Details
	reply.Err.Reason, _ = envelope.Error.Details["reason"].(string)
	return reply
}

func (g *guardHarness) call(handle func(*httpx.Event) error, method, path, body string, headers map[string]string) guardReply {
	g.t.Helper()
	event, recorder := workerRouteTestEvent(method, path, body)
	id := strings.Split(strings.TrimPrefix(path, "/api/v1/workers/"), "/")[0]
	event.Request.SetPathValue("id", id)
	event.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	for name, value := range headers {
		event.Request.Header.Set(name, value)
	}
	if err := handle(event); err != nil {
		g.t.Fatalf("%s %s: %v", method, path, err)
	}
	return decodeReply(g.t, recorder)
}

func guardProof(t *testing.T, key ed25519.PrivateKey, method, path, accessToken string, now time.Time) string {
	t.Helper()
	proof, err := guardidentity.SignProof(key, guardidentity.ProofInput{
		Method: method, URL: guardTestHost + path, AccessToken: accessToken, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func newGuardKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	key, err := guardidentity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// register redeems a pairing capability with a host key and returns the worker
// id and the credential the control plane issued.
func (g *guardHarness) register(rawToken, attempt string, key ed25519.PrivateKey) (string, string, guardReply) {
	g.t.Helper()
	jwk, _ := json.Marshal(guardidentity.PublicJWK(key))
	body := `{"token":"` + rawToken + `","hostname":"node-1","os":"linux","arch":"amd64","guard_public_key":` + string(jwk) + `,"attempt_id":"` + attempt + `"}`
	reply := g.call(g.handler.register, http.MethodPost, "/api/v1/workers/register", body, nil)
	if reply.Code != http.StatusOK {
		return "", "", reply
	}
	return reply.Data["worker_id"].(string), reply.Data["agent_token"].(string), reply
}

func (g *guardHarness) pairing() string {
	g.t.Helper()
	raw, hash, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		g.t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	if _, err := g.store.UpsertPairingToken(g.t.Context(), controlplane.PairingToken{
		ID: "pairing-" + hash[:8], TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		TokenHash: hash, Status: "active", ExpiresAt: &expires,
	}); err != nil {
		g.t.Fatal(err)
	}
	return raw
}

func (g *guardHarness) exchange(id, credential string, key ed25519.PrivateKey, now time.Time) guardReply {
	path := "/api/v1/workers/" + id + "/identity/token"
	return g.call(g.handler.workerIdentityToken, http.MethodPost, path, "", map[string]string{
		"Authorization": "Bearer " + credential,
		"DPoP":          guardProof(g.t, key, http.MethodPost, path, "", now),
	})
}

func (g *guardHarness) heartbeat(id string, headers map[string]string) guardReply {
	path := "/api/v1/workers/" + id + "/heartbeat"
	body := `{"source_epoch":"guard-identity","source_sequence":` + "1" + `,"observed_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	return g.call(g.handler.heartbeat, http.MethodPost, path, body, headers)
}

func (g *guardHarness) keyBoundHeartbeat(id, accessToken string, key ed25519.PrivateKey, proofPath string, now time.Time) guardReply {
	path := "/api/v1/workers/" + id + "/heartbeat"
	if proofPath == "" {
		proofPath = path
	}
	return g.heartbeat(id, map[string]string{
		"Authorization": "DPoP " + accessToken,
		"DPoP":          guardProof(g.t, key, http.MethodPost, proofPath, accessToken, now),
	})
}

func TestGuardHostKeyDeniesCopiedCredentialAndForgedOrReplayedProofs(t *testing.T) {
	g := newGuardHarness(t)
	key := newGuardKey(t)
	id, credential, _ := g.register(g.pairing(), "attempt-0123456789ab", key)
	if id == "" || credential == "" {
		t.Fatal("registration did not issue a worker credential")
	}

	// The copied bearer credential alone no longer authenticates the worker.
	if reply := g.heartbeat(id, map[string]string{"Authorization": "Bearer " + credential}); reply.Code != http.StatusUnauthorized || reply.Err.Code != guardidentity.CodeProofInvalid {
		t.Fatalf("bearer-only heartbeat = %d %s, want %s", reply.Code, reply.Err.Code, guardidentity.CodeProofInvalid)
	}
	// A proof signed by another key cannot confirm or exchange.
	if reply := g.exchange(id, credential, newGuardKey(t), time.Now()); reply.Err.Reason != guardidentity.ReasonKeyMismatch {
		t.Fatalf("foreign key exchange = %d %+v", reply.Code, reply.Err)
	}
	// The host key plus the credential mint a key-bound access token.
	exchange := g.exchange(id, credential, key, time.Now())
	if exchange.Code != http.StatusOK || exchange.Data["identity_state"] != controlplane.WorkerIdentityBound {
		t.Fatalf("exchange = %d %+v", exchange.Code, exchange.Data)
	}
	token := exchange.Data["access_token"].(string)
	if reply := g.keyBoundHeartbeat(id, token, key, "", time.Now()); reply.Code != http.StatusOK {
		t.Fatalf("key-bound heartbeat = %d %+v", reply.Code, reply.Err)
	}
	// Replay: the same proof is refused.
	replayed := guardProof(t, key, http.MethodPost, "/api/v1/workers/"+id+"/heartbeat", token, time.Now())
	first := g.heartbeat(id, map[string]string{"Authorization": "DPoP " + token, "DPoP": replayed})
	second := g.heartbeat(id, map[string]string{"Authorization": "DPoP " + token, "DPoP": replayed})
	if first.Code != http.StatusOK || second.Err.Code != guardidentity.CodeProofReplayed {
		t.Fatalf("replay: first %d, second %d %s", first.Code, second.Code, second.Err.Code)
	}
	// A stolen access token without the key is useless: a proof for another
	// endpoint, another key or a stale clock is refused.
	for name, reply := range map[string]guardReply{
		"wrong endpoint": g.keyBoundHeartbeat(id, token, key, "/api/v1/workers/"+id+"/inventory", time.Now()),
		"other key":      g.keyBoundHeartbeat(id, token, newGuardKey(t), "", time.Now()),
		"stale proof":    g.keyBoundHeartbeat(id, token, key, "", time.Now().Add(-5*time.Minute)),
	} {
		if reply.Code != http.StatusUnauthorized || reply.Err.Code != guardidentity.CodeProofInvalid {
			t.Fatalf("%s = %d %s, want %s", name, reply.Code, reply.Err.Code, guardidentity.CodeProofInvalid)
		}
	}
	// An expired access token is reported as such so the Guard re-exchanges.
	expired, _, err := guardidentity.IssueAccessToken(g.handler.guardAccessSecret(), guardidentity.AccessClaims{
		TenantID: "tenant-1", WorkerID: id, JKT: mustThumbprint(t, key), Cred: "x",
	}, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if reply := g.keyBoundHeartbeat(id, expired, key, "", time.Now()); reply.Err.Code != guardidentity.CodeTokenExpired {
		t.Fatalf("expired token = %d %s", reply.Code, reply.Err.Code)
	}
}

func mustThumbprint(t *testing.T, key ed25519.PrivateKey) string {
	t.Helper()
	thumbprint, err := guardidentity.PublicJWK(key).Thumbprint()
	if err != nil {
		t.Fatal(err)
	}
	return thumbprint
}

func TestGuardRepeatedRedemptionNeverActivatesTwoCredentials(t *testing.T) {
	g := newGuardHarness(t)
	key := newGuardKey(t)
	raw := g.pairing()
	id, _, _ := g.register(raw, "attempt-0123456789ab", key)
	if id == "" {
		t.Fatal("first redemption failed")
	}

	// Concurrent repetitions of the same capability, attempt and key: every
	// response carries a credential, but only the last one stored can work.
	var mu sync.Mutex
	credentials := []string{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, credential, reply := g.register(raw, "attempt-0123456789ab", key); reply.Code == http.StatusOK {
				mu.Lock()
				credentials = append(credentials, credential)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(credentials) == 0 {
		t.Fatal("no repetition was answered")
	}
	active := 0
	var confirmed string
	for _, credential := range credentials {
		if g.exchange(id, credential, key, time.Now()).Code == http.StatusOK {
			active++
			confirmed = credential
		}
	}
	if active != 1 {
		t.Fatalf("%d credentials authenticate, want exactly 1", active)
	}
	// After confirmation the capability cannot be redeemed again.
	if _, _, reply := g.register(raw, "attempt-0123456789ab", key); reply.Code != http.StatusUnauthorized {
		t.Fatalf("repeat after confirmation = %d, want 401", reply.Code)
	}
	if g.exchange(id, confirmed, key, time.Now()).Code != http.StatusOK {
		t.Fatal("the confirmed credential stopped working")
	}
	// A different key or attempt cannot take over an unconfirmed redemption.
	other := g.pairing()
	otherID, _, _ := g.register(other, "attempt-aaaaaaaaaaaa", key)
	if otherID == "" {
		t.Fatal("second pairing failed")
	}
	if _, _, reply := g.register(other, "attempt-bbbbbbbbbbbb", key); reply.Code != http.StatusUnauthorized {
		t.Fatalf("different attempt = %d, want 401", reply.Code)
	}
	if _, _, reply := g.register(other, "attempt-aaaaaaaaaaaa", newGuardKey(t)); reply.Code != http.StatusUnauthorized {
		t.Fatalf("different key = %d, want 401", reply.Code)
	}
}

func TestLegacyBearerWorkerKeyIsBoundOnlyWhenTheOwnerApprovesIt(t *testing.T) {
	g := newGuardHarness(t)
	const id = "legacy-worker"
	bearer, tokenErr := workerauth.OpaqueToken()
	if tokenErr != nil {
		t.Fatal(tokenErr)
	}
	if _, err := g.store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: id, TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1", Hostname: "legacy",
		Status: "approved", Approved: true, Capabilities: map[string]any{"server_id": "legacy-server"},
		Resources: map[string]any{"agent_token_sha256": workerauth.SHA256Hex(bearer)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "legacy-server", WorkerID: id, TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		Name: "legacy", LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy",
	}); err != nil {
		t.Fatal(err)
	}
	// Until it binds a key the worker keeps working and is flagged legacy.
	if reply := g.heartbeat(id, map[string]string{"Authorization": "Bearer " + bearer}); reply.Code != http.StatusOK {
		t.Fatalf("legacy heartbeat = %d %+v", reply.Code, reply.Err)
	}
	worker, err := g.store.GetWorker(t.Context(), "tenant-1", id)
	if err != nil || worker.Capabilities["identity_binding"] != controlplane.IdentityBindingLegacyBearer {
		t.Fatalf("legacy worker flag = %+v, %v", worker, err)
	}
	if reply := g.exchange(id, bearer, newGuardKey(t), time.Now()); reply.Err.Reason != guardidentity.ReasonIdentityUnbound {
		t.Fatalf("exchange before bind = %d %+v", reply.Code, reply.Err)
	}

	key := newGuardKey(t)
	bind := func(bearerToken string, bindKey ed25519.PrivateKey, proofKey ed25519.PrivateKey) guardReply {
		path := "/api/v1/workers/" + id + "/identity/bind"
		jwk, _ := json.Marshal(guardidentity.PublicJWK(bindKey))
		return g.call(g.handler.workerIdentityBind, http.MethodPost, path,
			`{"public_key":`+string(jwk)+`,"attempt_id":"attempt-0123456789ab"}`, map[string]string{
				"Authorization": "Bearer " + bearerToken,
				"DPoP":          guardProof(t, proofKey, http.MethodPost, path, "", time.Now()),
			})
	}
	if reply := bind(bearer, key, newGuardKey(t)); reply.Err.Reason != guardidentity.ReasonKeyMismatch {
		t.Fatalf("bind without possession = %d %+v", reply.Code, reply.Err)
	}
	stolen, _ := workerauth.OpaqueToken()
	if reply := bind(stolen, key, key); reply.Code != http.StatusUnauthorized {
		t.Fatalf("bind with a wrong credential = %d", reply.Code)
	}
	// Whoever holds the bearer may propose a key, but a proposal binds nothing:
	// the worker stays observe-only on its bearer and the key authenticates
	// nothing until the owner approves that exact key.
	if reply := bind(bearer, key, key); reply.Code != http.StatusAccepted || reply.Data["identity_state"] != controlplane.WorkerIdentityUpgradePending {
		t.Fatalf("bind = %d %+v %+v", reply.Code, reply.Data, reply.Err)
	}
	if reply := g.heartbeat(id, map[string]string{"Authorization": "Bearer " + bearer}); reply.Code != http.StatusOK {
		t.Fatalf("bearer heartbeat while the key is pending = %d %+v", reply.Code, reply.Err)
	}
	if reply := g.exchange(id, bearer, key, time.Now()); reply.Err.Code != guardidentity.CodeIdentityUpgradePending {
		t.Fatalf("exchange before approval = %d %+v", reply.Code, reply.Err)
	}
	attacker := newGuardKey(t)
	if reply := bind(bearer, attacker, attacker); reply.Err.Code != guardidentity.CodeIdentityUpgradePending || reply.Err.Reason != guardidentity.ReasonOtherKeyPending {
		t.Fatalf("a second key while one is pending = %d %+v", reply.Code, reply.Err)
	}

	owner := func(ownerID string, fresh bool, thumbprint string) int {
		h := g.handler
		h.entitlements = stepUpGate{EntitlementGate: selfHostedEntitlementGate{}, fresh: fresh}
		event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/workers/"+id+"/identity/upgrade/approve", ownerID, "tenant-1",
			map[string]any{"key_thumbprint": thumbprint})
		event.Request.SetPathValue("id", id)
		_ = h.identityUpgradeApprove(event)
		return recorder.Code
	}
	jwk, _ := guardidentity.ParseJWK(mustJSON(t, guardidentity.PublicJWK(key)))
	thumbprint, _ := jwk.Thumbprint()
	attackerJWK, _ := guardidentity.ParseJWK(mustJSON(t, guardidentity.PublicJWK(attacker)))
	attackerThumbprint, _ := attackerJWK.Thumbprint()
	if code := owner("owner-2", true, thumbprint); code != http.StatusNotFound {
		t.Fatalf("another owner got %d", code)
	}
	if code := owner("owner-1", false, thumbprint); code != http.StatusForbidden {
		t.Fatalf("approval without a fresh second factor got %d", code)
	}
	if code := owner("owner-1", true, attackerThumbprint); code != http.StatusConflict {
		t.Fatalf("approving a key that is not the pending one got %d", code)
	}
	if reply := g.exchange(id, bearer, key, time.Now()); reply.Err.Code != guardidentity.CodeIdentityUpgradePending {
		t.Fatalf("a refused approval bound the key: %d %+v", reply.Code, reply.Err)
	}
	if code := owner("owner-1", true, thumbprint); code != http.StatusOK {
		t.Fatalf("owner approval got %d", code)
	}

	// Bearer-only requests are denied from now on; a second key cannot bind.
	if reply := g.heartbeat(id, map[string]string{"Authorization": "Bearer " + bearer}); reply.Err.Code != guardidentity.CodeProofInvalid {
		t.Fatalf("bearer after approval = %d %s", reply.Code, reply.Err.Code)
	}
	second := newGuardKey(t)
	if reply := bind(bearer, second, second); reply.Err.Code != guardidentity.CodeIdentityAlreadyBound {
		t.Fatalf("replacing a bound key = %d %+v", reply.Code, reply.Err)
	}
	exchange := g.exchange(id, bearer, key, time.Now())
	if exchange.Code != http.StatusOK {
		t.Fatalf("exchange after approval = %d %+v", exchange.Code, exchange.Err)
	}
	if reply := g.keyBoundHeartbeat(id, exchange.Data["access_token"].(string), key, "", time.Now()); reply.Code != http.StatusOK {
		t.Fatalf("key-bound heartbeat = %d %+v", reply.Code, reply.Err)
	}
	worker, _ = g.store.GetWorker(t.Context(), "tenant-1", id)
	if worker.Capabilities["identity_binding"] != controlplane.IdentityBindingHostKey {
		t.Fatalf("bound worker flag = %v", worker.Capabilities["identity_binding"])
	}
}

func TestRevokedGuardIdentityStopsAuthenticatingImmediately(t *testing.T) {
	g := newGuardHarness(t)
	key := newGuardKey(t)
	id, credential, _ := g.register(g.pairing(), "attempt-0123456789ab", key)
	token := g.exchange(id, credential, key, time.Now()).Data["access_token"].(string)
	if reply := g.keyBoundHeartbeat(id, token, key, "", time.Now()); reply.Code != http.StatusOK {
		t.Fatalf("heartbeat before revoke = %d", reply.Code)
	}
	if err := g.store.RevokeWorkerIdentity(t.Context(), "tenant-1", id, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The still-valid access token and credential are both refused at once.
	if reply := g.keyBoundHeartbeat(id, token, key, "", time.Now()); reply.Code != http.StatusForbidden || reply.Err.Code != guardidentity.CodeIdentityRevoked {
		t.Fatalf("revoked heartbeat = %d %s", reply.Code, reply.Err.Code)
	}
	if reply := g.exchange(id, credential, key, time.Now()); reply.Err.Code != guardidentity.CodeIdentityRevoked {
		t.Fatalf("revoked exchange = %d %s", reply.Code, reply.Err.Code)
	}
}

func TestGuardKeyRotationIsProvenByBothKeysAndKeepsOnlyTheNewKey(t *testing.T) {
	g := newGuardHarness(t)
	oldKey := newGuardKey(t)
	id, credential, _ := g.register(g.pairing(), "attempt-0123456789ab", oldKey)
	token := g.exchange(id, credential, oldKey, time.Now()).Data["access_token"].(string)
	path := "/api/v1/workers/" + id + "/identity/rotate"
	newKey := newGuardKey(t)
	rotate := func(newPublic, proofKey ed25519.PrivateKey) guardReply {
		jwk, _ := json.Marshal(guardidentity.PublicJWK(newPublic))
		return g.call(g.handler.workerIdentityRotate, http.MethodPost, path,
			`{"new_public_key":`+string(jwk)+`,"new_key_proof":"`+guardProof(t, proofKey, http.MethodPost, path, "", time.Now())+`"}`,
			map[string]string{"Authorization": "DPoP " + token, "DPoP": guardProof(t, oldKey, http.MethodPost, path, token, time.Now())})
	}
	if reply := rotate(newKey, newGuardKey(t)); reply.Err.Reason != guardidentity.ReasonKeyMismatch {
		t.Fatalf("rotation without possession of the new key = %d %+v", reply.Code, reply.Err)
	}
	if reply := rotate(newKey, newKey); reply.Code != http.StatusOK {
		t.Fatalf("rotation = %d %+v", reply.Code, reply.Err)
	}
	if reply := g.exchange(id, credential, newKey, time.Now()); reply.Code != http.StatusOK {
		t.Fatalf("exchange with the new key = %d %+v", reply.Code, reply.Err)
	}
	identity, err := g.store.GetWorkerIdentity(t.Context(), "tenant-1", id)
	if err != nil || identity.Thumbprint != mustThumbprint(t, newKey) || identity.PreviousValidUntil == nil {
		t.Fatalf("identity after rotation = %+v, %v", identity, err)
	}
	// The predecessor is honoured only for the bounded overlap.
	if !identity.AcceptsThumbprint(mustThumbprint(t, oldKey), time.Now()) ||
		identity.AcceptsThumbprint(mustThumbprint(t, oldKey), identity.PreviousValidUntil.Add(time.Second)) {
		t.Fatal("the previous key overlap is not bounded")
	}
}

// Access tokens are signed with a key derived for that purpose, so the base
// worker secret (which, until the dedicated secret is provisioned, is also the
// platform service secret) cannot mint one: a token signed with the raw secret
// is refused even for a worker that has a bound key (F-11).
func TestGuardAccessTokenCannotBeMintedFromTheBaseSecret(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "base-worker-secret")
	g := newGuardHarness(t)
	g.handler.credentialSecret = workerauth.SecretFromEnv()
	key := newGuardKey(t)
	id, credential, _ := g.register(g.pairing(), "attempt-0123456789ab", key)
	jwk, _ := guardidentity.ParseJWK(mustJSON(t, guardidentity.PublicJWK(key)))
	thumbprint, _ := jwk.Thumbprint()
	claims := guardidentity.AccessClaims{
		TenantID: "tenant-1", WorkerID: id, JKT: thumbprint, Cred: guardidentity.CredentialFingerprint(workerauth.SHA256Hex(credential)),
	}
	forged, _, err := guardidentity.IssueAccessToken(workerauth.SecretFromEnv(), claims, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if reply := g.keyBoundHeartbeat(id, forged, key, "", time.Now()); reply.Code == http.StatusOK {
		t.Fatalf("a token signed with the base secret authenticated the worker: %+v", reply)
	}
	genuine, _, _ := guardidentity.IssueAccessToken(g.handler.guardAccessSecret(), claims, time.Now())
	if reply := g.keyBoundHeartbeat(id, genuine, key, "", time.Now()); reply.Code != http.StatusOK {
		t.Fatalf("the derived-key token was refused: %d %+v", reply.Code, reply.Err)
	}
}

// A Guard that suspects its key was copied rotates with emergency set: the old
// key stops authenticating at once, with no overlap window for a thief (F-19).
func TestEmergencyKeyRotationLeavesNoOverlapForTheOldKey(t *testing.T) {
	g := newGuardHarness(t)
	oldKey, newKey := newGuardKey(t), newGuardKey(t)
	id, credential, _ := g.register(g.pairing(), "attempt-0123456789ab", oldKey)
	token := g.exchange(id, credential, oldKey, time.Now()).Data["access_token"].(string)
	path := "/api/v1/workers/" + id + "/identity/rotate"
	jwk, _ := json.Marshal(guardidentity.PublicJWK(newKey))
	reply := g.call(g.handler.workerIdentityRotate, http.MethodPost, path,
		`{"emergency":true,"new_public_key":`+string(jwk)+`,"new_key_proof":"`+guardProof(t, newKey, http.MethodPost, path, "", time.Now())+`"}`,
		map[string]string{"Authorization": "DPoP " + token, "DPoP": guardProof(t, oldKey, http.MethodPost, path, token, time.Now())})
	if reply.Code != http.StatusOK {
		t.Fatalf("rotation = %d %+v", reply.Code, reply.Err)
	}
	identity, err := g.store.GetWorkerIdentity(t.Context(), "tenant-1", id)
	if err != nil || identity.AcceptsThumbprint(mustThumbprint(t, oldKey), time.Now().Add(time.Second)) {
		t.Fatalf("the old key is still accepted after an emergency rotation: %+v, %v", identity, err)
	}
	if reply := g.exchange(id, credential, oldKey, time.Now().Add(time.Second)); reply.Code == http.StatusOK {
		t.Fatal("the old key obtained an access token after an emergency rotation")
	}
}

// A lease named by the worker (capability or tag) proves nothing about whose
// key it is: a copied bearer credential must still wait for the owner, and the
// owner finds the waiting server in one listing.
func TestLeaseClaimDoesNotApproveALegacyKeyAndTheOwnerSeesItPending(t *testing.T) {
	g := newGuardHarness(t)
	const id, leaseID = "claimed-worker", "lease-managed-1"
	g.handler.managedRuntimeLeases = fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		createStackOperationsTestLease(leaseID, "tenant-1", "owner-1", "stack-1", monthlyRuntimeEnrollmentStatusEnrolled),
	}}
	bearer, tokenErr := workerauth.OpaqueToken()
	if tokenErr != nil {
		t.Fatal(tokenErr)
	}
	if _, err := g.store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: id, TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1", Hostname: "claimed",
		Status: "approved", Approved: true,
		Capabilities: map[string]any{"lease_id": leaseID}, Tags: map[string]any{"lease_id": leaseID},
		Resources: map[string]any{"agent_token_sha256": workerauth.SHA256Hex(bearer)},
	}); err != nil {
		t.Fatal(err)
	}
	key := newGuardKey(t)
	path := "/api/v1/workers/" + id + "/identity/bind"
	jwk, _ := json.Marshal(guardidentity.PublicJWK(key))
	reply := g.call(g.handler.workerIdentityBind, http.MethodPost, path,
		`{"public_key":`+string(jwk)+`,"attempt_id":"attempt-0123456789ab"}`, map[string]string{
			"Authorization": "Bearer " + bearer,
			"DPoP":          guardProof(t, key, http.MethodPost, path, "", time.Now()),
		})
	if reply.Code != http.StatusAccepted || reply.Data["identity_state"] != controlplane.WorkerIdentityUpgradePending {
		t.Fatalf("bind with a claimed lease = %d %+v %+v", reply.Code, reply.Data, reply.Err)
	}

	list := func(ownerID string) []any {
		event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/workers/identity-upgrades", ownerID, "tenant-1", nil)
		if err := g.handler.identityUpgradeList(event); err != nil {
			t.Fatal(err)
		}
		upgrades, _ := decodeReply(t, recorder).Data["upgrades"].([]any)
		return upgrades
	}
	if got := list("owner-1"); len(got) != 1 {
		t.Fatalf("owner sees %d pending upgrades, want 1", len(got))
	}
	if got := list("owner-2"); len(got) != 0 {
		t.Fatalf("another owner sees %d pending upgrades, want 0", len(got))
	}
}
