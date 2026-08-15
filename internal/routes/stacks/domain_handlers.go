package stacks

import (
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

// Domain handover (kombify-Cloud -> Techstack control-plane).
//
// kombify-Cloud buys a managed .com and hands it to the user's stack by calling this
// endpoint with a short-lived HS256 SERVICE JWT (iss=kombify-cloud, aud=kombify-techstack,
// scope stack:domain.attach, claims sub=owner, org_id=tenant). This compatibility
// endpoint delegates to the same desired-routing application service as the public API.
// It never edits immutable intent, auto-selects a target, fabricates a rollout job, or
// allocates a provider VM. StackKits consumes the overlay during a later exact rollout.
const (
	cloudServiceIssuer     = "kombify-cloud"
	cloudServiceAudience   = "kombify-techstack"
	stackDomainAttachScope = "stack:domain.attach"
	routingStatusKey       = "status"
	routingServerIDKey     = "server_id"
	routingLeaseIDKey      = "lease_id"
)

type cloudServiceClaims struct {
	Subject string
	OrgID   string
}

type attachDomainRequest struct {
	StackID          string `json:"stack_id"`
	ServerID         string `json:"server_id"`
	LeaseID          string `json:"lease_id"`
	Domain           string `json:"domain"`
	CFZoneID         string `json:"cf_zone_id"`
	DNSProvider      string `json:"dns_provider"`
	Source           string `json:"source"`
	ExternalDomainID string `json:"external_domain_id,omitempty"`
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	EnsureRollout    *bool  `json:"ensure_rollout,omitempty"`
}

// cloudServiceSecrets returns every explicitly configured verification slot.
// Current and NEXT are independent so rotation never relies on one value
// shadowing or falling back to another. KOMBIFY_CLOUD_SERVICE_SECRET remains a
// compatibility alias and is verified as its own slot.
func cloudServiceSecrets() []string {
	return []string{
		strings.TrimSpace(os.Getenv("STACK_CONTROL_PLANE_SECRET")),
		strings.TrimSpace(os.Getenv("STACK_CONTROL_PLANE_SECRET_NEXT")),
		strings.TrimSpace(os.Getenv("KOMBIFY_CLOUD_SERVICE_SECRET")),
	}
}

// verifyCloudServiceToken validates the inbound service JWT. On failure it writes the
// error response and returns ok=false (httpx.Error writes + returns nil, so callers
// must branch on ok, not on an error value).
func verifyCloudServiceToken(e *httpx.Event) (*cloudServiceClaims, bool) {
	secrets := cloudServiceSecrets()
	configured := false
	for _, secret := range secrets {
		configured = configured || secret != ""
	}
	if !configured {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal,
			"Domain attach is not configured", nil)
		return nil, false
	}

	authz := e.Request.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		_ = httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Missing service token", nil)
		return nil, false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))

	var claims jwt.MapClaims
	valid := false
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		candidateClaims := jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(raw, candidateClaims, func(_ *jwt.Token) (any, error) {
			return []byte(secret), nil
		},
			jwt.WithValidMethods([]string{"HS256"}),
			jwt.WithIssuer(cloudServiceIssuer),
			jwt.WithAudience(cloudServiceAudience),
			jwt.WithExpirationRequired(),
		)
		if err == nil && token.Valid && !valid {
			claims = candidateClaims
			valid = true
		}
	}
	if !valid {
		_ = httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Invalid service token", nil)
		return nil, false
	}
	if !claimsHaveScope(claims, stackDomainAttachScope) {
		_ = httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Insufficient scope for domain attach", nil)
		return nil, false
	}

	sub, _ := claims["sub"].(string)
	sub = strings.TrimSpace(sub)
	if sub == "" {
		_ = httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Service token missing subject", nil)
		return nil, false
	}
	org, _ := claims["org_id"].(string)
	return &cloudServiceClaims{Subject: sub, OrgID: strings.TrimSpace(org)}, true
}

func claimsHaveScope(claims jwt.MapClaims, want string) bool {
	if arr, ok := claims["scopes"].([]any); ok {
		for _, s := range arr {
			if str, ok := s.(string); ok && str == want {
				return true
			}
		}
	}
	if s, ok := claims["scope"].(string); ok {
		for _, sc := range strings.Fields(s) {
			if sc == want {
				return true
			}
		}
	}
	return false
}

// attachDomainToStack handles POST /api/internal/stacks/domain-attach.
func (h crudRouteHandlers) attachDomainToStack(e *httpx.Event) error {
	claims, ok := verifyCloudServiceToken(e)
	if !ok {
		return nil
	}

	var req attachDomainRequest
	if bindErr := e.BindBody(&req); bindErr != nil {
		return httpx.BadRequest(e, "Invalid request body")
	}
	tenant := claims.OrgID
	if tenant == "" {
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeBadRequest,
			"Tenant (org_id) is required to attach a domain",
			map[string]any{detailsKeyReasonCode: "tenant_unresolved"})
	}
	missingTarget := make([]string, 0, 4)
	if strings.TrimSpace(req.StackID) == "" {
		missingTarget = append(missingTarget, specResponseStackIDKey)
	}
	if strings.TrimSpace(req.ServerID) == "" {
		missingTarget = append(missingTarget, routingServerIDKey)
	}
	if strings.TrimSpace(req.LeaseID) == "" {
		missingTarget = append(missingTarget, routingLeaseIDKey)
	}
	if strings.TrimSpace(req.CFZoneID) == "" {
		missingTarget = append(missingTarget, "cf_zone_id")
	}
	if len(missingTarget) > 0 {
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
			"Cloud domain handover requires an exact stack, canonical server, managed lease, and Cloudflare zone target",
			map[string]any{detailsKeyReasonCode: "routing_target_required", "required_fields": missingTarget})
	}

	expectedRevision, err := parseRoutingIfMatch(e.Request.Header.Get("If-Match"))
	if err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, err.Error(), map[string]any{detailsKeyReasonCode: "invalid_if_match"})
	}
	if expectedRevision == nil {
		expectedRevision = req.ExpectedRevision
	} else if req.ExpectedRevision != nil && *expectedRevision != *req.ExpectedRevision {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest,
			"If-Match and expected_revision must identify the same routing revision",
			map[string]any{detailsKeyReasonCode: "routing_revision_mismatch"})
	}
	ensureRollout := true
	if req.EnsureRollout != nil {
		ensureRollout = *req.EnsureRollout
	}
	view, err := h.routingService().Ensure(e.Request.Context(), stackrouting.Principal{
		TenantID: tenant, OwnerSubjectID: claims.Subject,
	}, strings.TrimSpace(req.StackID), stackrouting.EnsureInput{
		ServerID:      req.ServerID,
		LeaseID:       req.LeaseID,
		Mode:          stackrouting.ModeCustomDomain,
		Domain:        req.Domain,
		Provenance:    stackrouting.Provenance{Source: req.Source, DNSProvider: req.DNSProvider, ZoneID: req.CFZoneID, ExternalDomainID: req.ExternalDomainID},
		EnsureRollout: ensureRollout,
		RequireLease:  true,
	}, stackrouting.MutationOptions{
		IdempotencyKey:   e.Request.Header.Get("Idempotency-Key"),
		ExpectedRevision: expectedRevision,
	})
	if err != nil {
		return writeRoutingError(e, err)
	}
	writeRoutingHeaders(e, view.Desired.Revision)
	status := http.StatusOK
	if ensureRollout && view.Rollout.Status == stackrouting.RolloutPending {
		status = http.StatusAccepted
	}
	return httpx.Success(e, status, map[string]any{
		"attachId":             view.Rollout.JobID,
		routingStatusKey:       view.Rollout.Status,
		specResponseStackIDKey: view.Desired.StackID,
		routingServerIDKey:     view.Desired.ServerID,
		routingLeaseIDKey:      view.Desired.LeaseID,
		"domain":               view.Desired.Domain,
		"revision":             view.Desired.Revision,
		"applied":              view.Observed.Applied,
		detailsKeyReasonCode:   view.Rollout.ReasonCode,
		"routing":              view,
	})
}

// stackIngress handles GET /api/internal/stacks/{id}/ingress.
//
// Returns the stack's main-node public IP (the server the managed domain's apex +
// wildcard A records must point at). kombify-Cloud's lifecycle reconciler polls this:
// 404 reason_code=ingress_pending while the server is still being provisioned.
func (h crudRouteHandlers) stackIngress(e *httpx.Event) error {
	claims, ok := verifyCloudServiceToken(e)
	if !ok {
		return nil
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	if stackID == "" {
		return httpx.BadRequest(e, "stack id is required")
	}
	tenant := claims.OrgID
	if tenant == "" {
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeBadRequest,
			"Tenant (org_id) is required", map[string]any{detailsKeyReasonCode: "tenant_unresolved"})
	}

	serverID := strings.TrimSpace(e.Request.URL.Query().Get(routingServerIDKey))
	leaseID := strings.TrimSpace(e.Request.URL.Query().Get(routingLeaseIDKey))
	if serverID == "" || leaseID == "" {
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
			"Exact server_id and lease_id query parameters are required",
			map[string]any{detailsKeyReasonCode: "routing_target_required", "required_fields": []string{routingServerIDKey, routingLeaseIDKey}})
	}
	target, err := h.routingService().ResolveTarget(e.Request.Context(), stackrouting.Principal{
		TenantID: tenant, OwnerSubjectID: claims.Subject,
	}, stackID, serverID, leaseID)
	if err != nil {
		return writeRoutingError(e, err)
	}
	address := strings.TrimSpace(target.Address)
	ip := net.ParseIP(address)
	if address == "" || ip == nil || ip.To4() == nil {
		return httpx.Error(e, http.StatusNotFound, ksapi.ErrCodeNotFound,
			"Exact routing target has no IPv4 ingress address yet",
			map[string]any{detailsKeyReasonCode: "ingress_ipv4_pending", specResponseStackIDKey: stackID, routingServerIDKey: target.ServerID, routingLeaseIDKey: target.LeaseID})
	}
	ingressIP := ip.To4().String()
	return httpx.Success(e, http.StatusOK, map[string]any{
		"ingress_ip":           ingressIP,
		"address":              ingressIP,
		specResponseStackIDKey: stackID,
		routingServerIDKey:     target.ServerID,
		routingLeaseIDKey:      target.LeaseID,
	})
}
