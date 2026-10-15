package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/privatechannel"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/trustedproxy"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

type workerEnrollmentContext struct {
	GuardRole            string
	WorkerID             string
	ServerID             string
	RuntimeAgentID       string
	TenantID             string
	OwnerID              string
	StackID              string
	LeaseID              string
	Accepted             bool
	AgentToken           string
	CredentialGeneration int64
	GRPCIdentity         *grpcserver.IssuedIdentity
	// GuardIdentity is set for a registration that bound a host key.
	GuardIdentity *guardEnrollmentIdentity
}

// guardEnrollmentIdentity describes the host-key binding a registration
// created; the credential is confirmed by the first proof-authenticated request.
type guardEnrollmentIdentity struct {
	State      string
	AttemptID  string
	Thumbprint string
}

// issueRuntimeAgentToken creates the persistent per-worker credential returned
// after an authorized connect or pairing redemption. It is intentionally
// stateful and opaque: the control plane stores only its exact SHA-256 hash, so
// deleting/rejecting the worker or replacing the hash revokes it immediately.
// Signed, expiring tokens remain a separate bootstrap capability.
func (h workerRouteHandlers) issueRuntimeAgentToken() (string, error) {
	return workerauth.OpaqueToken()
}

func (h workerRouteHandlers) workerEnrollmentResponse(e *httpx.Event, ctx workerEnrollmentContext) map[string]any {
	ctx.WorkerID = strings.TrimSpace(ctx.WorkerID)
	ctx.LeaseID = strings.TrimSpace(ctx.LeaseID)
	if ctx.LeaseID != "" {
		ctx.ServerID = runtimeidentity.LeaseServerID(ctx.LeaseID)
	} else {
		ctx.ServerID = firstNonEmpty(ctx.ServerID, runtimeServerIDForWorker(ctx.WorkerID))
	}
	ctx.RuntimeAgentID = firstNonEmpty(ctx.RuntimeAgentID, ctx.WorkerID)
	baseURL := requestPublicBaseURL(e)
	heartbeatPath := "/api/v1/workers/" + ctx.RuntimeAgentID + "/heartbeat"
	inventoryPath := "/api/v1/workers/" + ctx.RuntimeAgentID + "/inventory"
	commandPath := "/api/v1/workers/" + ctx.RuntimeAgentID + "/commands/next"
	commandResultPath := "/api/v1/workers/" + ctx.RuntimeAgentID + "/commands/result"
	stackKitReleasePath := "/api/v1/agent/stackkit-release/linux/amd64"
	identityTokenPath := "/api/v1/workers/" + ctx.RuntimeAgentID + workerIdentityTokenSuffix
	identityBindPath := "/api/v1/workers/" + ctx.RuntimeAgentID + workerIdentityBindSuffix
	identityRotatePath := "/api/v1/workers/" + ctx.RuntimeAgentID + workerIdentityRotateSuffix
	// Only channels with a served handler are advertised: the outbound HTTPS
	// poll endpoints below and, when an mTLS identity was issued, gRPC. The
	// identity_* URLs belong to the host-key protocol (workers_identity.go).
	resp := map[string]any{
		"guard_role":            ctx.GuardRole,
		"worker_id":             ctx.WorkerID,
		"server_id":             ctx.ServerID,
		"runtime_agent_id":      ctx.RuntimeAgentID,
		runtimeLeaseIDKey:       ctx.LeaseID,
		"accepted":              ctx.Accepted,
		"credential_generation": ctx.CredentialGeneration,
		"heartbeat_url":         absoluteRouteURL(baseURL, heartbeatPath),
		"inventory_url":         absoluteRouteURL(baseURL, inventoryPath),
		"command_url":           absoluteRouteURL(baseURL, commandPath),
		"command_result_url":    absoluteRouteURL(baseURL, commandResultPath),
		"stackkit_release_url":  absoluteRouteURL(baseURL, stackKitReleasePath),
		"identity_token_url":    absoluteRouteURL(baseURL, identityTokenPath),
		"identity_bind_url":     absoluteRouteURL(baseURL, identityBindPath),
		"identity_rotate_url":   absoluteRouteURL(baseURL, identityRotatePath),
		"channel_bootstrap": map[string]any{
			"tenant_id":            ctx.TenantID,
			"owner_id":             ctx.OwnerID,
			"stack_id":             ctx.StackID,
			runtimeLeaseIDKey:      ctx.LeaseID,
			"server_id":            ctx.ServerID,
			"runtime_agent_id":     ctx.RuntimeAgentID,
			"heartbeat_url":        absoluteRouteURL(baseURL, heartbeatPath),
			"inventory_url":        absoluteRouteURL(baseURL, inventoryPath),
			"command_url":          absoluteRouteURL(baseURL, commandPath),
			"command_result_url":   absoluteRouteURL(baseURL, commandResultPath),
			"stackkit_release_url": absoluteRouteURL(baseURL, stackKitReleasePath),
			"identity_token_url":   absoluteRouteURL(baseURL, identityTokenPath),
			"identity_bind_url":    absoluteRouteURL(baseURL, identityBindPath),
			"identity_rotate_url":  absoluteRouteURL(baseURL, identityRotatePath),
			"grpc_hint":            "mtls-if-http2-network-allows",
			"ssh_liveness_role":    "bootstrap-diagnostics-only",
		},
	}
	if bridge, ok := requestPrivateLANBridge(e); ok {
		// The Guard pinned this key for the redemption itself, so the pin it
		// stores here is the one its connection already proved.
		for _, target := range []map[string]any{resp, resp["channel_bootstrap"].(map[string]any)} {
			target["private_lan_origin"] = bridge.origin
			target["private_lan_pin"] = bridge.pin
			target["transport_security"] = "private-lan-tls-pinned"
		}
	}
	if strings.TrimSpace(ctx.AgentToken) != "" {
		resp["agent_token"] = strings.TrimSpace(ctx.AgentToken)
		// Only the Guard that receives this worker's credential learns its
		// binding key. Without it the Guard stays unpinned, as before signing.
		signer, err := h.bindingSignerFor(ctx.TenantID, ctx.RuntimeAgentID)
		if err != nil {
			slog.Error("guard_binding_key_unavailable", "tenant_id", ctx.TenantID, "runtime_agent_id", ctx.RuntimeAgentID, "error", err)
		} else if keys := signer.PublicKeys(); len(keys) > 0 {
			resp["binding_signing_keys"] = keys
		}
	}
	if ctx.GuardIdentity != nil {
		resp["identity"] = map[string]any{
			"binding":        controlplane.IdentityBindingHostKey,
			"state":          ctx.GuardIdentity.State,
			"attempt_id":     ctx.GuardIdentity.AttemptID,
			"key_thumbprint": ctx.GuardIdentity.Thumbprint,
			// The credential is active for the token exchange immediately and
			// confirmed by its first successful, proof-authenticated use.
			"confirmation": "first-proof-authenticated-request",
		}
	}
	if ctx.GRPCIdentity != nil {
		resp["grpc_mtls"] = map[string]any{
			"schema_version":  "techstack.agent-mtls-enrollment/v1",
			"agent_id":        ctx.GRPCIdentity.AgentID,
			"tenant_id":       ctx.GRPCIdentity.TenantID,
			"certificate_pem": string(ctx.GRPCIdentity.CertPEM),
			"private_key_pem": string(ctx.GRPCIdentity.KeyPEM),
			"ca_pem":          string(ctx.GRPCIdentity.CACertPEM),
			"serial":          ctx.GRPCIdentity.Serial,
			"expires_at":      ctx.GRPCIdentity.ExpiresAt.UTC().Format(time.RFC3339),
		}
	}
	return resp
}

// requestPrivateLANBridge reports the bridge a TLS request arrived through.
func requestPrivateLANBridge(e *httpx.Event) (localLANBridgeRequest, bool) {
	if e == nil || e.Request == nil || e.Request.TLS == nil {
		return localLANBridgeRequest{}, false
	}
	bridge, ok := localLANBridgeFrom(e.Request.Context())
	if !ok {
		return localLANBridgeRequest{}, false
	}
	if _, err := privatechannel.NormalizeLANOrigin(bridge.origin); err != nil {
		return localLANBridgeRequest{}, false
	}
	return bridge, true
}

// requestPublicBaseURL is the origin this control plane tells a Guard to use
// and the origin a proof's htu may name. A request never chooses it: the
// configured public origin wins, a LAN bridge request keeps its own private
// origin, and only without either does the URL fall back to the Host header,
// with forwarded protocol and host honored solely from a trusted proxy
// (trustedproxy, TECHSTACK_TRUSTED_PROXIES).
func requestPublicBaseURL(e *httpx.Event) string {
	if e == nil || e.Request == nil {
		return ""
	}
	if bridge, ok := requestPrivateLANBridge(e); ok {
		return bridge.origin
	}
	if origin := config.PublicOriginFromEnv(); origin != "" {
		return origin
	}
	req := e.Request
	proto, host := "http", strings.TrimSpace(req.Host)
	if req.TLS != nil {
		proto = "https"
	}
	if forwardedProto, forwardedHost, ok := trustedproxy.FromEnv().ForwardedOrigin(req); ok {
		if forwardedProto == "http" || forwardedProto == "https" {
			proto = forwardedProto
		}
		if forwardedHost != "" {
			host = forwardedHost
		}
	}
	if host == "" {
		return ""
	}
	return strings.TrimRight(proto+"://"+host, "/")
}

func absoluteRouteURL(baseURL, path string) string {
	path = "/" + strings.TrimLeft(strings.TrimSpace(path), "/")
	if strings.TrimSpace(baseURL) == "" {
		return path
	}
	return strings.TrimRight(baseURL, "/") + path
}

func bearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[len("bearer "):])
	}
	return ""
}

func runtimeServerIDForWorker(workerID string) string {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return ""
	}
	return "server_" + stableRouteID(workerID)
}

func plannedServerIDFromPairingToken(token controlplane.PairingToken) string {
	if token.Metadata != nil {
		if plannedID := strings.TrimSpace(stringFromAny(token.Metadata["planned_server_id"])); plannedID != "" {
			return plannedID
		}
		if nodeID := strings.TrimSpace(stringFromAny(token.Metadata["spec_node_id"])); nodeID != "" {
			return runtimeidentity.StackServerID(token.StackID, nodeID)
		}
	}
	return ""
}

func runtimeLeaseIDFromMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	return firstNonEmpty(stringFromAny(metadata["lease_id"]), stringFromAny(metadata["runtime_lease_id"]))
}

func stableRouteID(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])[:24]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
