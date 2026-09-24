// Package auth provides REST API routes for authentication configuration.
package auth

import (
	"crypto/subtle"
	"net/http"
	"os"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// RegisterInternalRoutes registers internal routes that are only accessible
// from trusted services (Edge API Gateway). These routes are NOT public.
//
// Trust is verified via the X-Kombify-SSO-Secret header.
// These routes are only registered for editions derived to deployment_mode=saas.
func RegisterInternalRoutes(r *httpx.Router, mode config.DeploymentMode) {
	if !mode.IsSaaS() {
		return // Internal routes only needed in SaaS mode
	}

	r.POST("/api/internal/feature-flags/apply", handleFeatureFlagsApply())
}

// verifyEdgeTrust validates the shared secret header from Edge.
// Accepts either KOMBIFY_SSO_SECRET (current) or KOMBIFY_SSO_SECRET_NEXT
// (rotation target) to enable zero-downtime rotation. Uses constant-time
// comparison to avoid timing attacks.
func verifyEdgeTrust(e *httpx.Event) error {
	current := os.Getenv("KOMBIFY_SSO_SECRET")
	next := os.Getenv("KOMBIFY_SSO_SECRET_NEXT")

	if current == "" && next == "" {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal,
			"SSO exchange not configured (KOMBIFY_SSO_SECRET not set)", nil)
	}

	provided := e.Request.Header.Get("X-Kombify-SSO-Secret")
	if provided == "" {
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Invalid or missing trust secret", nil)
	}

	if !sharedSecretMatches(provided, current, next) {
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Invalid or missing trust secret", nil)
	}

	return nil
}

// sharedSecretMatches returns true if provided equals either current or next
// (when non-empty), using constant-time comparison.
func sharedSecretMatches(provided, current, next string) bool {
	if provided == "" {
		return false
	}
	pb := []byte(provided)
	if current != "" && subtle.ConstantTimeCompare(pb, []byte(current)) == 1 {
		return true
	}
	if next != "" && subtle.ConstantTimeCompare(pb, []byte(next)) == 1 {
		return true
	}
	return false
}

// FeatureFlagApplyRequest is the request body for POST /api/internal/feature-flags/apply
type FeatureFlagApplyRequest struct {
	Flags []FeatureFlagOverride `json:"flags"`
}

// FeatureFlagOverride represents a single feature flag override from admin center.
type FeatureFlagOverride struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

type featureFlagApplyResult struct {
	Applied   []string `json:"applied"`
	Errors    []string `json:"errors"`
	Persisted bool     `json:"persisted"`
}

// handleFeatureFlagsApply handles POST /api/internal/feature-flags/apply
// Called by admin center (via Edge service account) to push feature flag
// overrides to this Stack instance.
func handleFeatureFlagsApply() func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		// Verify trust
		if err := verifyEdgeTrust(e); err != nil {
			return err
		}

		var req FeatureFlagApplyRequest
		if err := e.BindBody(&req); err != nil {
			return httpx.BadRequest(e, "Invalid request body")
		}

		if len(req.Flags) == 0 {
			return httpx.BadRequest(e, "No flags provided")
		}

		result := featureFlagApplyResult{Applied: make([]string, 0, len(req.Flags)), Errors: []string{}, Persisted: false}
		for _, flag := range req.Flags {
			result.Applied = append(result.Applied, flag.Key)
		}
		return httpx.Success(e, http.StatusAccepted, result)
	}
}
