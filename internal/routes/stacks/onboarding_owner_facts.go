package stacks

import (
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/internal/onboarding"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	ownerFactsAuthority      = "techstack"
	ownerFactsCallerCloud    = "cloud"
	ownerFactsFreshness      = 5 * time.Minute
	ownerFactsErrorPrefix    = "techstack.onboarding_facts."
	ownerFactsUnavailable    = "owner_facts_unavailable"
	ownerFactsInvalidSurface = "owner_facts_surface_invalid"
)

type ownerFactsSnapshot struct {
	Version     string          `json:"version"`
	Authority   string          `json:"authority"`
	Revision    string          `json:"revision"`
	GeneratedAt string          `json:"generated_at"`
	ValidUntil  string          `json:"valid_until"`
	Facts       map[string]bool `json:"facts"`
}

// getOwnerFacts is a private Cloud servicecall boundary. The service token is
// verified before the conditional response is considered, so a guessed ETag
// never becomes an authentication oracle.
func (h onboardingHandlers) getOwnerFacts(e *httpx.Event) error {
	if h.serviceAuthSecret == "" {
		return writeOwnerFactsDenial(e, http.StatusServiceUnavailable, ownerFactsUnavailable, true,
			"Retry after Techstack service authentication is configured.")
	}

	auth := servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    ownerFactsAuthority,
		Secret:         h.serviceAuthSecret,
		SecretNext:     h.serviceAuthNext,
		AllowedCallers: []string{ownerFactsCallerCloud},
		Enabled:        true,
	})
	auth(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		e.Request = request
		h.serveOwnerFacts(e)
	})).ServeHTTP(e.Response, e.Request)
	return nil
}

func (h onboardingHandlers) serveOwnerFacts(e *httpx.Event) {
	caller := servicecall.FromContext(e.Request.Context())
	if caller == nil || caller.Service != ownerFactsCallerCloud || caller.OnBehalfOf == nil ||
		strings.TrimSpace(caller.OnBehalfOf.Sub) == "" || strings.TrimSpace(caller.OnBehalfOf.OrgID) == "" {
		_ = writeOwnerFactsDenial(e, http.StatusForbidden, "tenant_context_required", false,
			"Open Techstack from the authenticated Kombify Cloud organization and retry.")
		return
	}
	if e.Request.URL.RawQuery != "" || e.Request.ContentLength != 0 {
		_ = writeOwnerFactsDenial(e, http.StatusForbidden, "principal_injection_denied", false,
			"Do not send principal fields; Techstack derives them from the signed Cloud service identity.")
		return
	}
	if strings.TrimSpace(e.Request.PathValue("authority")) != ownerFactsAuthority {
		_ = writeOwnerFactsDenial(e, http.StatusNotFound, ownerFactsInvalidSurface, false,
			"Request the Techstack authority snapshot from this endpoint.")
		return
	}

	resolution := onboarding.ResolveOwnerFacts(
		e.Request.Context(),
		h.service.Signals,
		strings.TrimSpace(caller.OnBehalfOf.OrgID),
		strings.TrimSpace(caller.OnBehalfOf.Sub),
	)
	if len(resolution.Facts) == 0 && len(resolution.UnavailablePredicates) > 0 {
		_ = writeOwnerFactsDenial(e, http.StatusServiceUnavailable, ownerFactsUnavailable, true,
			"Retry after the Techstack onboarding fact authority recovers.")
		return
	}

	etag := `"` + resolution.Revision + `"`
	e.Response.Header().Set("Cache-Control", "private, no-cache")
	e.Response.Header().Set("Vary", "Authorization, X-Kombify-Service-Auth")
	e.Response.Header().Set("ETag", etag)
	if ifNoneMatch(e.Request.Header.Get("If-None-Match"), etag) {
		e.Response.WriteHeader(http.StatusNotModified)
		e.MarkResponseStreamed()
		return
	}

	now := time.Now().UTC()
	_ = e.JSON(http.StatusOK, ownerFactsSnapshot{
		Version:     "1",
		Authority:   ownerFactsAuthority,
		Revision:    resolution.Revision,
		GeneratedAt: now.Format(time.RFC3339Nano),
		ValidUntil:  now.Add(ownerFactsFreshness).Format(time.RFC3339Nano),
		Facts:       resolution.Facts,
	})
}

func ifNoneMatch(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

func writeOwnerFactsDenial(e *httpx.Event, status int, reasonCode string, retryable bool, nextStep string) error {
	return e.JSON(status, map[string]any{
		"error_code":        ownerFactsErrorPrefix + reasonCode,
		"reason_code":       reasonCode,
		"required_features": []string{},
		"missing_features":  []string{},
		"retryable":         retryable,
		"user_guidance": map[string]any{
			"title":      "Onboarding facts unavailable",
			"body":       "Techstack could not resolve this onboarding snapshot safely.",
			"next_steps": []string{nextStep},
		},
	})
}
