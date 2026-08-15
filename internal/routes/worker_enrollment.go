package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/privatechannel"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

type workerEnrollmentContext struct {
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
	controlPath := "/api/v1/workers/" + ctx.RuntimeAgentID + "/control/ws"
	commandPath := "/api/v1/workers/" + ctx.RuntimeAgentID + "/commands/next"
	commandResultPath := "/api/v1/workers/" + ctx.RuntimeAgentID + "/commands/result"
	stackKitReleasePath := "/api/v1/agent/stackkit-release/linux/amd64"
	resp := map[string]any{
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
		"control_urls":          []string{absoluteRouteURL(websocketBaseURL(baseURL), controlPath)},
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
			"websocket_url":        absoluteRouteURL(websocketBaseURL(baseURL), controlPath),
			"grpc_hint":            "mtls-if-http2-network-allows",
			"ssh_liveness_role":    "bootstrap-diagnostics-only",
		},
	}
	if privateOrigin, ok := requestPrivateLANHTTPOrigin(e); ok {
		resp["allow_private_lan_http"] = true
		resp["private_lan_http_origin"] = privateOrigin
		resp["transport_security"] = "private-lan-http"
		resp["channel_bootstrap"].(map[string]any)["allow_private_lan_http"] = true
		resp["channel_bootstrap"].(map[string]any)["private_lan_http_origin"] = privateOrigin
		resp["channel_bootstrap"].(map[string]any)["transport_security"] = "private-lan-http"
	}
	if strings.TrimSpace(ctx.AgentToken) != "" {
		resp["agent_token"] = strings.TrimSpace(ctx.AgentToken)
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

func requestPrivateLANHTTPOrigin(e *httpx.Event) (string, bool) {
	if e == nil || e.Request == nil || e.Request.TLS != nil || e.Request.Header.Get("X-Techstack-LAN-Bridge") != "1" {
		return "", false
	}
	origin, err := privatechannel.NormalizeLANOrigin("http://" + strings.TrimSpace(e.Request.Host))
	return origin, err == nil
}

func requestPublicBaseURL(e *httpx.Event) string {
	if e == nil || e.Request == nil {
		return ""
	}
	req := e.Request
	proto := strings.TrimSpace(req.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		proto = "http"
		if req.TLS != nil {
			proto = "https"
		}
	}
	host := strings.TrimSpace(req.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(req.Host)
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

func websocketBaseURL(baseURL string) string {
	switch {
	case strings.HasPrefix(baseURL, "https://"):
		return "wss://" + strings.TrimPrefix(baseURL, "https://")
	case strings.HasPrefix(baseURL, "http://"):
		return "ws://" + strings.TrimPrefix(baseURL, "http://")
	default:
		return baseURL
	}
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
