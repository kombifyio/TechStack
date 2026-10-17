package routes

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

// Host-bound kombify Guard identity.
//
// A Guard generates an Ed25519 key on its own server. The control plane binds
// the key's RFC 7638 thumbprint to the worker (registration or a one-time bind
// from an upgraded legacy worker) and from then on accepts worker requests only
// as a short-lived access token bound to that key (cnf.jkt) plus a fresh
// DPoP-shaped proof per request. A copied credential or access token is
// therefore not enough to impersonate the server. Workers that never bound a
// key stay on the bearer credential and are flagged identity_binding=legacy_bearer.

const (
	workerIdentityTokenSuffix  = "/identity/token"
	workerIdentityBindSuffix   = "/identity/bind"
	workerIdentityRotateSuffix = "/identity/rotate"
	workerIdentityMaxBody      = 8 << 10
)

var guardAttemptIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// guardAccessSecret returns the signing secret of key-bound access tokens. A
// deployment without a configured worker secret gets a process-local random
// one: tokens then only survive a restart by re-exchange, which the Guard does
// transparently, but are never signed with a guessable value.
var (
	guardEphemeralSecretOnce sync.Once
	guardEphemeralSecret     []byte
)

// guardAccessPurpose labels the key access tokens are signed with: derived from
// the worker secret, so a secret that also signs another purpose (or the
// platform service identity, until the dedicated secret is provisioned) cannot
// mint a Guard access token by itself.
const guardAccessPurpose = "guard-access-token/v1"

func (h workerRouteHandlers) guardAccessSecret() []byte {
	if secret := h.configuredWorkerCredentialSecret(); len(secret) > 0 {
		return workerauth.PurposeKey(secret, guardAccessPurpose)
	}
	guardEphemeralSecretOnce.Do(func() {
		guardEphemeralSecret = make([]byte, 32)
		if _, err := rand.Read(guardEphemeralSecret); err != nil {
			guardEphemeralSecret = nil
			return
		}
		logger.Default().Warn("guard_access_token_secret_ephemeral",
			"reason", "no worker token secret configured; key-bound access tokens are process-local")
	})
	return guardEphemeralSecret
}

// verifyGuardAccessToken verifies a key-bound access token against the current
// worker secret and, while a SERVICE_AUTH_SECRET rotation is open, against the
// key derived from its successor. Tokens are only ever signed with the current
// one.
func (h workerRouteHandlers) verifyGuardAccessToken(token string, now time.Time) (guardidentity.AccessClaims, error) {
	claims, err := guardidentity.VerifyAccessToken(h.guardAccessSecret(), token, now)
	if err == nil || len(h.configuredWorkerCredentialSecret()) == 0 {
		return claims, err
	}
	if next := workerauth.NextSecretFromEnv(); len(next) > 0 {
		nextClaims, nextErr := guardidentity.VerifyAccessToken(workerauth.PurposeKey(next, guardAccessPurpose), token, now)
		var denied *guardidentity.Error
		if nextErr == nil || !errors.As(nextErr, &denied) || denied.Code != guardidentity.CodeProofInvalid {
			return nextClaims, nextErr
		}
	}
	return claims, err
}

type guardGuidance struct {
	title, body string
	steps       []string
	status      int
	retryable   bool
}

func guardGuidanceFor(code, reason string) guardGuidance {
	switch code {
	case guardidentity.CodeTokenExpired:
		return guardGuidance{
			title: "The kombify Guard access token expired", status: http.StatusUnauthorized, retryable: true,
			body:  "Short-lived access tokens are renewed automatically by kombify Guard.",
			steps: []string{"Exchange the worker credential for a new access token and retry."},
		}
	case guardidentity.CodeProofReplayed:
		return guardGuidance{
			title: "This kombify Guard request proof was already used", status: http.StatusUnauthorized, retryable: true,
			body:  "Every request needs its own fresh proof of possession.",
			steps: []string{"Sign a new proof for the retry. If this repeats, check for a proxy that replays requests."},
		}
	case guardidentity.CodeIdentityRevoked:
		return guardGuidance{
			title: "This server's kombify Guard identity was revoked", status: http.StatusForbidden,
			body:  "The server was detached or its identity revoked, so it may no longer act for this account.",
			steps: []string{"Pair the server again from the kombify dashboard if it should be managed."},
		}
	case guardidentity.CodeIdentityAlreadyBound:
		return guardGuidance{
			title: "This server already has a different kombify Guard key", status: http.StatusConflict,
			body:  "A confirmed host key cannot be replaced by a bind request.",
			steps: []string{"Rotate the key from the running Guard, or pair the server again from the dashboard."},
		}
	case guardidentity.CodeIdentityUpgradeRequired:
		return guardGuidance{
			title: "This server's kombify Guard must upgrade its identity before it can run commands", status: http.StatusForbidden,
			body: "The server still authenticates with a bearer credential alone. It may report its state and update itself, but the control plane sends it no commands until the owner has approved its identity key.",
			steps: []string{
				"Update kombify Guard on the server so it registers an identity key.",
				"Approve the key in the kombify dashboard after comparing its fingerprint with the one the Guard logs on the server.",
			},
		}
	case guardidentity.CodeIdentityUpgradePending:
		guidance := guardGuidance{
			title: "This server's kombify Guard key awaits owner approval", status: http.StatusConflict, retryable: true,
			body:  "The key was received. It is bound to the server only after the owner approves it in the kombify dashboard.",
			steps: []string{"Approve the key in the kombify dashboard after comparing its fingerprint with the one the Guard logs on the server."},
		}
		if reason == guardidentity.ReasonOtherKeyPending {
			guidance.body = "A different key is waiting for owner approval for this server. If it is not this server's key, reject it in the kombify dashboard and rotate the server credential."
		}
		return guidance
	case guardidentity.CodeIdentityUnavailable:
		return guardGuidance{
			title: "Host identity is temporarily unavailable", status: http.StatusServiceUnavailable, retryable: true,
			body:  "The control plane cannot enforce host identity right now.",
			steps: []string{"Retry shortly."},
		}
	}
	guidance := guardGuidance{
		title: "The kombify Guard proof of possession was not accepted", status: http.StatusUnauthorized,
		body:  "Requests from a server must prove possession of the key bound to it.",
		steps: []string{"Restart kombify Guard so it signs requests with its identity key."},
	}
	if reason == guardidentity.ReasonProofClock {
		guidance.retryable = true
		guidance.body = "The proof timestamp is outside the accepted window, which usually means the server clock is wrong."
		guidance.steps = []string{"Synchronize the server clock (NTP) and retry."}
	}
	return guidance
}

// denyGuard writes one structured, redacted identity denial.
func denyGuard(e *httpx.Event, code, reason string) {
	guidance := guardGuidanceFor(code, reason)
	steps := make([]string, len(guidance.steps))
	copy(steps, guidance.steps)
	details := map[string]any{
		"error_code":  code,
		"reason_code": firstNonEmpty(reason, code),
		"retryable":   guidance.retryable,
		"user_guidance": map[string]any{
			"title": guidance.title, "body": guidance.body, "next_steps": steps,
		},
	}
	if reason != "" {
		details["reason"] = reason
	}
	_ = httpx.Error(e, guidance.status, ksapi.ErrorCode(code), guidance.title, details)
}

func denyGuardError(e *httpx.Event, err error) {
	var denied *guardidentity.Error
	if errors.As(err, &denied) {
		denyGuard(e, denied.Code, denied.Reason)
		return
	}
	denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonProofMalformed)
}

func authorizationScheme(r *http.Request) (scheme, credentials string) {
	if r == nil {
		return "", ""
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, credentials, found := strings.Cut(value, " ")
	if !found {
		return "", ""
	}
	return strings.ToLower(scheme), strings.TrimSpace(credentials)
}

// requestProofURLs lists the absolute URLs a proof for this request may name:
// the forwarded public URL that enrollment handed to the Guard and the URL as
// received by this process.
func requestProofURLs(e *httpx.Event) []string {
	if e == nil || e.Request == nil || e.Request.URL == nil {
		return nil
	}
	path := e.Request.URL.EscapedPath()
	urls := make([]string, 0, 2)
	base := requestPublicBaseURL(e)
	if base != "" {
		urls = append(urls, base+path)
	}
	if config.PublicOriginFromEnv() != "" {
		// A configured public origin is the only URL a proof may name; the Host
		// header of the request does not widen it.
		return urls
	}
	scheme := "http"
	if e.Request.TLS != nil {
		scheme = "https"
	}
	if host := strings.TrimSpace(e.Request.Host); host != "" {
		urls = append(urls, scheme+"://"+host+path)
	}
	return urls
}

// verifyGuardProof validates the single DPoP header of a request.
func verifyGuardProof(e *httpx.Event, accessToken string, now time.Time) (*guardidentity.VerifiedProof, error) {
	values := e.Request.Header.Values(guardidentity.ProofHeader)
	if len(values) != 1 {
		return nil, guardidentity.Denied(guardidentity.CodeProofInvalid, guardidentity.ReasonProofRequired)
	}
	return guardidentity.VerifyProof(values[0], guardidentity.ProofExpectation{
		Method: e.Request.Method, URLs: requestProofURLs(e), AccessToken: accessToken, Now: now,
	})
}

func (h workerRouteHandlers) consumeGuardProof(e *httpx.Event, tenantID, workerID string, proof *guardidentity.VerifiedProof) bool {
	err := h.identityStore.ConsumeWorkerProof(e.Request.Context(), tenantID, workerID, proof.JTI, proof.ReplayUntil)
	switch {
	case err == nil:
		return true
	case errors.Is(err, controlplane.ErrWorkerProofReplayed):
		denyGuard(e, guardidentity.CodeProofReplayed, "")
	default:
		denyGuard(e, guardidentity.CodeIdentityUnavailable, "")
	}
	return false
}

// authenticateRuntimeAgent authenticates one worker request. A request that
// presents a key-bound access token must carry a valid proof; a bearer request
// is accepted only for a worker that has no host-key binding.
func (h workerRouteHandlers) authenticateRuntimeAgent(e *httpx.Event, id string, req workerInventoryRequest) (runtimeAgentAuthContext, bool) {
	if scheme, _ := authorizationScheme(e.Request); scheme == strings.ToLower(guardidentity.TokenType) {
		return h.authenticateKeyBoundRuntimeAgent(e, id, req)
	}
	authCtx, ok := h.authenticateRuntimeAgentCredential(e, id, req)
	if !ok {
		return authCtx, false
	}
	if h.identityStore == nil {
		authCtx.IdentityBinding = controlplane.IdentityBindingLegacyBearer
		return authCtx, true
	}
	identity, err := h.identityStore.GetWorkerIdentity(e.Request.Context(), authCtx.TenantID, authCtx.RuntimeAgentID)
	switch {
	case errors.Is(err, controlplane.ErrNotFound):
		authCtx.IdentityBinding = controlplane.IdentityBindingLegacyBearer
		return authCtx, true
	case err != nil:
		denyGuard(e, guardidentity.CodeIdentityUnavailable, "")
		return authCtx, false
	case identity.State == controlplane.WorkerIdentityRevoked:
		denyGuard(e, guardidentity.CodeIdentityRevoked, "")
	case identity.State == controlplane.WorkerIdentityUpgradePending:
		// A key is proposed but not approved: the worker stays on the bearer
		// credential, which is observe-only.
		authCtx.IdentityBinding = controlplane.IdentityBindingLegacyBearer
		return authCtx, true
	default:
		// The worker has a host key: its credential alone no longer
		// authenticates any worker endpoint.
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonProofRequired)
	}
	return authCtx, false
}

func (h workerRouteHandlers) authenticateKeyBoundRuntimeAgent(e *httpx.Event, id string, req workerInventoryRequest) (runtimeAgentAuthContext, bool) {
	var out runtimeAgentAuthContext
	if h.identityStore == nil || h.wst == nil {
		denyGuard(e, guardidentity.CodeIdentityUnavailable, "")
		return out, false
	}
	now := time.Now().UTC()
	_, accessToken := authorizationScheme(e.Request)
	claims, err := h.verifyGuardAccessToken(accessToken, now)
	if err != nil {
		denyGuardError(e, err)
		return out, false
	}
	if claims.WorkerID != id || (req.TenantID != "" && req.TenantID != claims.TenantID) ||
		(req.RuntimeAgentID != "" && req.RuntimeAgentID != id) {
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonTokenInvalid)
		return out, false
	}
	ctx := e.Request.Context()
	worker, workerErr := h.wst.GetWorker(ctx, claims.TenantID, id)
	if workerErr != nil || worker == nil {
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonTokenInvalid)
		return out, false
	}
	if !h.workerStatusAllowsAuthentication(e, *worker) {
		return out, false
	}
	identity, identityErr := h.identityStore.GetWorkerIdentity(ctx, claims.TenantID, id)
	switch {
	case errors.Is(identityErr, controlplane.ErrNotFound):
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonIdentityUnbound)
		return out, false
	case identityErr != nil:
		denyGuard(e, guardidentity.CodeIdentityUnavailable, "")
		return out, false
	case identity.State == controlplane.WorkerIdentityRevoked:
		denyGuard(e, guardidentity.CodeIdentityRevoked, "")
		return out, false
	case identity.State == controlplane.WorkerIdentityUpgradePending:
		denyGuard(e, guardidentity.CodeIdentityUpgradePending, "")
		return out, false
	}
	// A token outlived by a credential change or a finished key overlap is
	// treated as expired so the Guard re-exchanges instead of failing hard.
	if claims.Cred != guardidentity.CredentialFingerprint(workerCredentialHashOf(*worker)) ||
		!identity.AcceptsThumbprint(claims.JKT, now) {
		denyGuard(e, guardidentity.CodeTokenExpired, "")
		return out, false
	}
	proof, proofErr := verifyGuardProof(e, accessToken, now)
	if proofErr != nil {
		denyGuardError(e, proofErr)
		return out, false
	}
	if proof.Thumbprint != claims.JKT {
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonKeyMismatch)
		return out, false
	}
	placement := workerauth.Claims{
		TenantID: worker.TenantID, OwnerID: worker.OwnerSubjectID, StackID: worker.StackID,
		LeaseID:  runtimeLeaseIDFromMetadata(worker.Capabilities),
		ServerID: stringFromAny(worker.Capabilities[workerFieldServerID]), RuntimeAgentID: worker.ID,
	}
	if !validateRuntimeAgentClaims(e, id, req, placement) {
		return out, false
	}
	if !h.consumeGuardProof(e, claims.TenantID, id, proof) {
		return out, false
	}
	return runtimeAgentAuthContext{
		TenantID: worker.TenantID, OwnerID: worker.OwnerSubjectID, StackID: worker.StackID,
		LeaseID: placement.LeaseID, ServerID: placement.ServerID, RuntimeAgentID: worker.ID,
		Worker: worker, IdentityBinding: controlplane.IdentityBindingHostKey, ProofThumbprint: proof.Thumbprint,
	}, true
}

// workerStatusAllowsAuthentication denies rejected and revoked workers with the
// matching structured answer.
func (h workerRouteHandlers) workerStatusAllowsAuthentication(e *httpx.Event, worker controlplane.Worker) bool {
	switch {
	case strings.EqualFold(worker.Status, managedRuntimeStatusRejected):
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return false
	case strings.EqualFold(worker.Status, workerStatusRevoked):
		denyGuard(e, guardidentity.CodeIdentityRevoked, "")
		return false
	}
	return true
}

const (
	workerStatusRevoked = "revoked"
	// workerCapabilityIdentityBinding publishes how the worker last proved its
	// identity (host_key or legacy_bearer) in worker and server state.
	workerCapabilityIdentityBinding = "identity_binding"
)

func workerCredentialHashOf(worker controlplane.Worker) string {
	if hash := strings.TrimSpace(stringFromAny(worker.Resources[controlplane.WorkerCredentialTokenSHA256ResourceKey])); hash != "" {
		return hash
	}
	return strings.TrimSpace(worker.TokenHash)
}

// workerIdentityToken exchanges the worker credential plus a proof of key
// possession for a short-lived, key-bound access token. The first successful
// exchange of an unconfirmed redemption confirms the binding.
func (h workerRouteHandlers) workerIdentityToken(e *httpx.Event) error {
	id := strings.TrimSpace(e.Request.PathValue("id"))
	if h.identityStore == nil {
		denyGuard(e, guardidentity.CodeIdentityUnavailable, "")
		return nil
	}
	authCtx, ok := h.authenticateRuntimeAgentCredential(e, id, workerInventoryRequest{RuntimeAgentID: id})
	if !ok {
		return nil
	}
	now := time.Now().UTC()
	proof, proofErr := verifyGuardProof(e, "", now)
	if proofErr != nil {
		denyGuardError(e, proofErr)
		return nil
	}
	ctx := e.Request.Context()
	identity, err := h.identityStore.GetWorkerIdentity(ctx, authCtx.TenantID, authCtx.RuntimeAgentID)
	switch {
	case errors.Is(err, controlplane.ErrNotFound):
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonIdentityUnbound)
		return nil
	case err != nil:
		denyGuard(e, guardidentity.CodeIdentityUnavailable, "")
		return nil
	case identity.State == controlplane.WorkerIdentityRevoked:
		denyGuard(e, guardidentity.CodeIdentityRevoked, "")
		return nil
	case identity.State == controlplane.WorkerIdentityUpgradePending:
		denyGuard(e, guardidentity.CodeIdentityUpgradePending, "")
		return nil
	case !identity.AcceptsThumbprint(proof.Thumbprint, now):
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonKeyMismatch)
		return nil
	}
	if !h.consumeGuardProof(e, authCtx.TenantID, authCtx.RuntimeAgentID, proof) {
		return nil
	}
	identity, err = h.identityStore.ConfirmWorkerIdentity(ctx, authCtx.TenantID, authCtx.RuntimeAgentID, proof.Thumbprint, now)
	switch {
	case errors.Is(err, controlplane.ErrWorkerIdentityRevoked):
		denyGuard(e, guardidentity.CodeIdentityRevoked, "")
		return nil
	case err != nil:
		denyGuard(e, guardidentity.CodeIdentityUnavailable, "")
		return nil
	}
	// A confirmed host key is what reach needs; it runs beside enrollment and
	// never gates it.
	h.enrollGuardReach(ctx, authCtx, nil)
	token, expires, issueErr := guardidentity.IssueAccessToken(h.guardAccessSecret(), guardidentity.AccessClaims{
		TenantID: authCtx.TenantID, WorkerID: authCtx.RuntimeAgentID, JKT: proof.Thumbprint,
		Cred: guardidentity.CredentialFingerprint(workerCredentialHashOf(*authCtx.Worker)),
	}, now)
	if issueErr != nil {
		denyGuard(e, guardidentity.CodeIdentityUnavailable, "")
		return nil
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	return httpx.Success(e, http.StatusOK, map[string]any{
		"access_token":      token,
		"token_type":        guardidentity.TokenType,
		"expires_in":        int(guardidentity.AccessTokenTTL.Seconds()),
		"expires_at":        expires.UTC().Format(time.RFC3339),
		"identity_state":    identity.State,
		"identity_binding":  controlplane.IdentityBindingHostKey,
		"key_thumbprint":    identity.Thumbprint,
		"credential_epoch":  identity.CredentialEpoch,
		"rotation_deadline": rotationDeadline(identity),
	})
}

func rotationDeadline(identity *controlplane.WorkerIdentity) any {
	if identity == nil || identity.PreviousValidUntil == nil {
		return nil
	}
	return identity.PreviousValidUntil.UTC().Format(time.RFC3339)
}

type workerIdentityBindRequest struct {
	PublicKey json.RawMessage `json:"public_key"`
	AttemptID string          `json:"attempt_id"`
}

func readGuardKeyBody(e *httpx.Event, out any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(e.Response, e.Request.Body, workerIdentityMaxBody))
	if err != nil || json.Unmarshal(body, out) != nil {
		_ = httpx.BadRequest(e, "Invalid request body", nil)
		return false
	}
	return true
}

// workerIdentityBind is the key bind request of an upgraded legacy worker. The
// bearer credential authenticates the worker and a proof signed by the new key
// proves possession, but the bearer may be a copy: the key is therefore held
// pending (202) and binds the server only once the owner approves that exact
// key (workers_identity_upgrade.go). Until then the worker stays observe-only.
// After approval, bearer-only requests are denied.
func (h workerRouteHandlers) workerIdentityBind(e *httpx.Event) error {
	id := strings.TrimSpace(e.Request.PathValue("id"))
	if h.identityStore == nil {
		denyGuard(e, guardidentity.CodeIdentityUnavailable, "")
		return nil
	}
	authCtx, ok := h.authenticateRuntimeAgentCredential(e, id, workerInventoryRequest{RuntimeAgentID: id})
	if !ok {
		return nil
	}
	var req workerIdentityBindRequest
	if !readGuardKeyBody(e, &req) {
		return nil
	}
	jwk, jwkErr := guardidentity.ParseJWK(req.PublicKey)
	if jwkErr != nil || !guardAttemptIDPattern.MatchString(req.AttemptID) {
		return httpx.BadRequest(e, "A public Ed25519 key and an attempt id are required", nil)
	}
	thumbprint, _ := jwk.Thumbprint()
	canonical, _ := jwk.Canonical()
	now := time.Now().UTC()
	proof, proofErr := verifyGuardProof(e, "", now)
	if proofErr != nil {
		denyGuardError(e, proofErr)
		return nil
	}
	if proof.Thumbprint != thumbprint {
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonKeyMismatch)
		return nil
	}
	if !h.consumeGuardProof(e, authCtx.TenantID, authCtx.RuntimeAgentID, proof) {
		return nil
	}
	identity, err := h.identityStore.BindWorkerIdentity(e.Request.Context(), controlplane.WorkerIdentityBind{
		TenantID: authCtx.TenantID, WorkerID: authCtx.RuntimeAgentID, AttemptID: req.AttemptID,
		Thumbprint: thumbprint, PublicKeyJWK: canonical,
		ExpectedCredentialSHA256: workerCredentialHashOf(*authCtx.Worker), Now: now,
	})
	switch {
	case errors.Is(err, controlplane.ErrWorkerIdentityBound):
		denyGuard(e, guardidentity.CodeIdentityAlreadyBound, "")
		return nil
	case errors.Is(err, controlplane.ErrWorkerIdentityUpgradePending):
		denyGuard(e, guardidentity.CodeIdentityUpgradePending, guardidentity.ReasonOtherKeyPending)
		return nil
	case errors.Is(err, controlplane.ErrWorkerIdentityRevoked):
		denyGuard(e, guardidentity.CodeIdentityRevoked, "")
		return nil
	case errors.Is(err, controlplane.ErrConflict):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The worker credential changed during the key bind; retry", nil)
	case err != nil:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to bind the Guard identity key", nil)
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	if identity.State == controlplane.WorkerIdentityUpgradePending {
		slog.Warn("guard_identity_upgrade_requested", "tenant_id", authCtx.TenantID, "worker_id", authCtx.RuntimeAgentID,
			"key_thumbprint", identity.Thumbprint, "client_ip", getClientIP(e))
		return httpx.Success(e, http.StatusAccepted, map[string]any{
			"identity_state": identity.State, "identity_binding": controlplane.IdentityBindingLegacyBearer,
			"key_thumbprint": identity.Thumbprint, "approval_required": true,
		})
	}
	return httpx.Success(e, http.StatusOK, map[string]any{
		"identity_state": identity.State, "identity_binding": controlplane.IdentityBindingHostKey,
		"key_thumbprint": identity.Thumbprint,
	})
}

type workerIdentityRotateRequest struct {
	NewPublicKey json.RawMessage `json:"new_public_key"`
	NewKeyProof  string          `json:"new_key_proof"`
	// Emergency ends the predecessor key at once instead of after the short
	// overlap, for a Guard that suspects its key was copied.
	Emergency bool `json:"emergency,omitempty"`
}

// workerIdentityRotate swaps the key of a bound worker. The request is
// authenticated by the current key; the new key proves possession with its own
// proof. The predecessor stays valid for a bounded overlap.
func (h workerRouteHandlers) workerIdentityRotate(e *httpx.Event) error {
	id := strings.TrimSpace(e.Request.PathValue("id"))
	authCtx, ok := h.authenticateRuntimeAgent(e, id, workerInventoryRequest{RuntimeAgentID: id})
	if !ok {
		return nil
	}
	if authCtx.IdentityBinding != controlplane.IdentityBindingHostKey {
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonIdentityUnbound)
		return nil
	}
	var req workerIdentityRotateRequest
	if !readGuardKeyBody(e, &req) {
		return nil
	}
	jwk, jwkErr := guardidentity.ParseJWK(req.NewPublicKey)
	if jwkErr != nil {
		return httpx.BadRequest(e, "A public Ed25519 key is required", nil)
	}
	thumbprint, _ := jwk.Thumbprint()
	canonical, _ := jwk.Canonical()
	now := time.Now().UTC()
	newProof, proofErr := guardidentity.VerifyProof(req.NewKeyProof, guardidentity.ProofExpectation{
		Method: e.Request.Method, URLs: requestProofURLs(e), Now: now,
	})
	if proofErr != nil {
		denyGuardError(e, proofErr)
		return nil
	}
	if newProof.Thumbprint != thumbprint {
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonKeyMismatch)
		return nil
	}
	if !h.consumeGuardProof(e, authCtx.TenantID, authCtx.RuntimeAgentID, newProof) {
		return nil
	}
	overlap := controlplane.WorkerIdentityRotationOverlap
	if req.Emergency {
		overlap = 0
	}
	identity, err := h.identityStore.RotateWorkerIdentity(e.Request.Context(), controlplane.WorkerIdentityRotation{
		TenantID: authCtx.TenantID, WorkerID: authCtx.RuntimeAgentID, OldThumbprint: authCtx.ProofThumbprint,
		NewThumbprint: thumbprint, NewPublicJWK: canonical, Now: now, Overlap: overlap,
	})
	switch {
	case errors.Is(err, controlplane.ErrWorkerIdentityRevoked):
		denyGuard(e, guardidentity.CodeIdentityRevoked, "")
		return nil
	case errors.Is(err, controlplane.ErrConflict), errors.Is(err, controlplane.ErrNotFound):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The Guard key changed during the rotation; retry with the current key", nil)
	case err != nil:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to rotate the Guard identity key", nil)
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	return httpx.Success(e, http.StatusOK, map[string]any{
		"identity_state": identity.State, "identity_binding": controlplane.IdentityBindingHostKey,
		"key_thumbprint": identity.Thumbprint, "previous_key_valid_until": rotationDeadline(identity),
	})
}

// guardRegistration is the key material a registering Guard supplies.
type guardRegistration struct {
	attemptID    string
	thumbprint   string
	publicKeyJWK string
}

// parseGuardRegistration validates the optional host-key fields of a
// registration. Both fields travel together; a legacy registration has neither.
func parseGuardRegistration(req workerRegistrationRequest) (*guardRegistration, string, bool) {
	if len(req.GuardPublicKey) == 0 && req.AttemptID == "" {
		return nil, "", true
	}
	jwk, err := guardidentity.ParseJWK(req.GuardPublicKey)
	if err != nil || !guardAttemptIDPattern.MatchString(req.AttemptID) {
		return nil, "guard_public_key and attempt_id must be a public Ed25519 key and a 16-64 character attempt id", false
	}
	thumbprint, _ := jwk.Thumbprint()
	canonical, _ := jwk.Canonical()
	return &guardRegistration{attemptID: req.AttemptID, thumbprint: thumbprint, publicKeyJWK: canonical}, "", true
}

// verifyGuardRegistrationProof requires a DPoP proof, signed by the registering
// host key and bound to the redeemed capability, before that key is enrolled.
// It writes the denial and returns false when the proof does not hold.
func verifyGuardRegistrationProof(e *httpx.Event, token string, guard *guardRegistration, now time.Time) bool {
	proof, err := verifyGuardProof(e, strings.TrimSpace(token), now)
	if err != nil {
		denyGuardError(e, err)
		return false
	}
	if proof.Thumbprint != guard.thumbprint {
		denyGuard(e, guardidentity.CodeProofInvalid, guardidentity.ReasonProofSignature)
		return false
	}
	return true
}
