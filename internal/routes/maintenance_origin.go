package routes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kombifyio/techstack/internal/gocommon/mcporiginauth"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/middleware"
)

const maintenanceOperationID = "runServerMaintenanceAction"
const maintenanceToolName = "run_server_maintenance_action"

// MaintenanceOriginAuthority uses the existing dedicated Techstack OBO client.
// A current grant and a nonce reservation are distinct operations.
type MaintenanceOriginAuthority interface {
	CheckMaintenanceOrigin(context.Context, guardreach.MaintenanceOriginProof, string, string, time.Time) error
	ReserveMaintenanceOrigin(context.Context, guardreach.MaintenanceOriginProof, string, string, time.Time) error
}

type MaintenanceOriginConfig struct {
	Keys         map[string]string
	Authority    MaintenanceOriginAuthority
	Entitlements EntitlementGate
}

type maintenanceOriginEvidence struct {
	proof     guardreach.MaintenanceOriginProof
	scope     inventoryScope
	requestID string
	verified  *mcporiginauth.Context
}
type maintenanceOriginEvidenceKey struct{}
type maintenanceOriginMarkedKey struct{}

type maintenanceOriginAuthorityError struct{ cause error }

func (e *maintenanceOriginAuthorityError) Error() string {
	return "maintenance origin current authority unavailable"
}
func (e *maintenanceOriginAuthorityError) Unwrap() error { return e.cause }

func maintenanceOriginFromContext(ctx context.Context) (*maintenanceOriginEvidence, bool) {
	evidence, ok := ctx.Value(maintenanceOriginEvidenceKey{}).(*maintenanceOriginEvidence)
	principal, verified := mcporiginauth.FromContext(ctx)
	return evidence, ok && verified && len(principal.DelegatedAuthority) != 0 && evidence.verified != nil
}

func validMaintenanceIdempotency(key string) bool {
	return key != "" && len(key) <= 128 && utf8.ValidString(key) && strings.TrimSpace(key) == key &&
		strings.IndexFunc(key, unicode.IsControl) < 0
}

func maintenanceSignedIdempotency(principal mcporiginauth.Context) (string, error) {
	key := principal.IdempotencyKey
	if key == "" {
		key = principal.RequestID
	}
	if !validMaintenanceIdempotency(key) {
		return "", errors.New("signed maintenance idempotency invalid")
	}
	return key, nil
}

func verifyMaintenancePrincipal(ctx context.Context, principal mcporiginauth.Context, cfg MaintenanceOriginConfig) error {
	evidence, ok := ctx.Value(maintenanceOriginEvidenceKey{}).(*maintenanceOriginEvidence)
	if !ok || cfg.Authority == nil || !maintenancePrincipalMatchesEvidence(principal, evidence) ||
		!entitlementGateOrHosted(cfg.Entitlements).SignedGrant(ctx, ServerMaintenanceEntitlement) {
		return errors.New("maintenance origin principal mismatch")
	}
	if _, err := maintenanceSignedIdempotency(principal); err != nil {
		return err
	}
	var arguments struct {
		ServerID string `json:"server_id"`
		Action   string `json:"action"`
	}
	if json.Unmarshal(principal.Arguments, &arguments) != nil || arguments.ServerID == "" || !ordinaryMaintenanceAction(arguments.Action) {
		return errors.New("maintenance origin target invalid")
	}
	if err := cfg.Authority.CheckMaintenanceOrigin(ctx, evidence.proof, principal.Subject, principal.OrganizationID, principal.ExpiresAt); err != nil {
		return err
	}
	evidence.verified = &principal
	return nil
}

func maintenancePrincipalMatchesEvidence(principal mcporiginauth.Context, evidence *maintenanceOriginEvidence) bool {
	return principal.Subject == evidence.scope.ownerID && principal.OrganizationID == evidence.scope.tenantID &&
		principal.RequestID != "" && principal.RequestID == evidence.requestID && principal.ToolName == maintenanceToolName &&
		len(principal.OperationIDs) == 1 && principal.OperationIDs[0] == "techstack."+maintenanceOperationID &&
		principal.AgentInstanceID != "" && principal.CatalogRevision != ""
}

func ordinaryMaintenanceAction(action string) bool {
	switch action {
	case "os_update_plan", "os_update", "reboot":
		return true
	default:
		return false
	}
}

// The shared verifier owns signature/body/tool validation and nonce ordering.
// This wrapper retains evidence only; it does not parse or trust claims itself.
func maintenanceOriginMiddleware(cfg MaintenanceOriginConfig) func(http.Handler) http.Handler {
	return mcporiginauth.Middleware(mcporiginauth.Config{
		Audience: "mcp-use:techstack", ServerKey: "techstack", Keys: cfg.Keys,
		VerifyDelegatedAuthority: func(ctx context.Context, principal mcporiginauth.Context) error {
			return verifyMaintenancePrincipal(ctx, principal, cfg)
		},
		ReserveNonce: func(ctx context.Context, key string, expires time.Time) (bool, error) {
			evidence, ok := ctx.Value(maintenanceOriginEvidenceKey{}).(*maintenanceOriginEvidence)
			if !ok || evidence.verified == nil || cfg.Authority == nil || key != "mcp-use:techstack:techstack:"+evidence.proof.Nonce || !expires.Equal(evidence.verified.ExpiresAt) {
				return false, errors.New("maintenance nonce binding invalid")
			}
			err := cfg.Authority.ReserveMaintenanceOrigin(ctx, evidence.proof, evidence.scope.ownerID, evidence.scope.tenantID, expires)
			return err == nil, err
		},
	})
}

func hasMaintenanceOriginMarker(header http.Header) bool {
	for _, name := range []string{"X-Kombify-Mcp-Origin-Context", "X-Kombify-Mcp-Origin-Signature", "X-Kombify-Mcp-Origin-Key-Id", "X-Kombify-Mcp-Origin-Timestamp", "X-Kombify-Mcp-Origin-Nonce", "X-Kombify-Mcp-Origin-Body-Sha256"} {
		if header.Get(name) != "" {
			return true
		}
	}
	return false
}

func maintenanceOriginMarkedWithoutVerification(ctx context.Context, header http.Header) bool {
	marked, _ := ctx.Value(maintenanceOriginMarkedKey{}).(bool)
	if !marked && !hasMaintenanceOriginMarker(header) {
		return false
	}
	principal, verified := mcporiginauth.FromContext(ctx)
	if !verified {
		return true
	}
	if len(principal.DelegatedAuthority) != 0 {
		_, currentVerified := maintenanceOriginFromContext(ctx)
		return !currentVerified
	}
	return false
}

// Only the actual maintenance call enters the dedicated authority verifier.
// Other tools retain the existing authenticated Edge identity and policy. An
// unverified marker remains tainted through routing, never owner authority.
func captureMaintenanceOrigin(request *http.Request, scope inventoryScope) (*http.Request, bool, error) {
	if !hasMaintenanceOriginMarker(request.Header) {
		return request, false, nil
	}
	request = request.WithContext(context.WithValue(request.Context(), maintenanceOriginMarkedKey{}, true))
	body, err := io.ReadAll(io.LimitReader(request.Body, (4<<20)+1))
	request.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > 4<<20 {
		return nil, false, errors.New("MCP origin body invalid")
	}
	var call struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &call) != nil {
		return nil, false, errors.New("MCP origin call invalid")
	}
	if call.Method != "tools/call" || call.Params.Name != maintenanceToolName {
		return request, false, nil
	}
	return captureMaintenanceCallOrigin(request, scope, body)
}

func captureMaintenanceCallOrigin(request *http.Request, scope inventoryScope, body []byte) (*http.Request, bool, error) {
	proof := guardreach.MaintenanceOriginProof{Method: request.Method, Path: request.URL.RequestURI(),
		Context: request.Header.Get("X-Kombify-Mcp-Origin-Context"), Signature: request.Header.Get("X-Kombify-Mcp-Origin-Signature"),
		KeyID: request.Header.Get("X-Kombify-Mcp-Origin-Key-Id"), Timestamp: request.Header.Get("X-Kombify-Mcp-Origin-Timestamp"),
		Nonce: request.Header.Get("X-Kombify-Mcp-Origin-Nonce"), BodySHA256: request.Header.Get("X-Kombify-Mcp-Origin-Body-Sha256")}
	marked, incomplete := false, false
	for _, value := range []string{proof.Context, proof.Signature, proof.KeyID, proof.Timestamp, proof.Nonce, proof.BodySHA256} {
		if value == "" {
			incomplete = true
		} else {
			marked = true
		}
	}
	if !marked {
		return request, true, nil
	}
	if incomplete {
		return nil, false, errors.New("incomplete MCP origin")
	}
	if !strings.HasPrefix(proof.Signature, "v2=") {
		return request, true, nil
	}
	if request.Method != http.MethodPost || (proof.Path != "/api/v1/mcp" && proof.Path != "/v1/mcp/public/techstack") {
		return nil, false, errors.New("maintenance transport invalid")
	}
	if len(body) > 32<<10 {
		return nil, false, errors.New("maintenance origin body invalid")
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	proof.BodyBase64url = base64.RawURLEncoding.EncodeToString(body)
	evidence := &maintenanceOriginEvidence{proof: proof, scope: scope, requestID: request.Header.Get("X-Request-ID")}
	return request.WithContext(context.WithValue(request.Context(), maintenanceOriginEvidenceKey{}, evidence)), true, nil
}

func checkMaintenanceOriginCurrent(ctx context.Context, authority MaintenanceOriginAuthority, owner, tenant, serverID, action string) error {
	principal, marked := mcporiginauth.FromContext(ctx)
	if !marked || len(principal.DelegatedAuthority) == 0 {
		if facts, verified := middleware.VerifiedStepUpFromContext(ctx); verified && facts.PrincipalType != middleware.PrincipalTypeUser {
			return errors.New("non-owner maintenance requires delegated authority")
		}
	}
	if !marked {
		return nil
	} // Owner transport relies on the authenticated Gateway producer fence.
	if len(principal.DelegatedAuthority) == 0 {
		if principal.ActorSubject != "" || principal.AgentInstanceID != "" || principal.AgentClass != "" || (principal.PrincipalType != "" && principal.PrincipalType != "user") {
			return errors.New("legacy delegated maintenance denied")
		}
		return nil
	}
	evidence, ok := maintenanceOriginFromContext(ctx)
	var arguments struct {
		ServerID string `json:"server_id"`
		Action   string `json:"action"`
	}
	if !ok || authority == nil || principal.Subject != owner || principal.OrganizationID != tenant ||
		json.Unmarshal(principal.Arguments, &arguments) != nil || arguments.ServerID != serverID || arguments.Action != action {
		return errors.New("maintenance origin target mismatch")
	}
	if err := authority.CheckMaintenanceOrigin(ctx, evidence.proof, owner, tenant, principal.ExpiresAt); err != nil {
		return &maintenanceOriginAuthorityError{cause: err}
	}
	return nil
}

func writeServerMaintenanceOriginDenial(e *httpx.Event, err error) error {
	status, reason := http.StatusForbidden, maintenanceReasonOriginDenied
	var upstream *maintenanceOriginAuthorityError
	if errors.As(err, &upstream) {
		status, reason = http.StatusServiceUnavailable, maintenanceReasonOriginUnavailable
		var denial *guardreach.GatewayError
		if errors.As(err, &denial) && denial.Status < 500 && denial.Status != http.StatusTooManyRequests {
			status, reason = http.StatusForbidden, maintenanceReasonOriginDenied
		}
	}
	return writeServerMaintenanceDenial(e, status, reason, nil)
}
