// Package auth provides REST API routes for authentication configuration.
package auth

import (
	"crypto/subtle"
	"net/http"
	"os"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// EdgeSSOExchangeResponse is the response for POST /api/internal/sso/exchange
type EdgeSSOExchangeResponse struct {
	PBToken string     `json:"pb_token"`
	User    PBUserInfo `json:"user"`
}

// RegisterInternalRoutes registers internal routes that are only accessible
// from trusted services (Edge API Gateway). These routes are NOT public.
//
// Routes:
//   - POST /api/internal/sso/exchange - Exchange Edge identity headers for PB token
//
// Trust is verified via the X-Kombify-SSO-Secret header.
// These routes are only registered for editions derived to deployment_mode=saas.
func RegisterInternalRoutes(r *httpx.Router, app core.App, mode config.DeploymentMode) {
	if !mode.IsSaaS() {
		return // Internal routes only needed in SaaS mode
	}

	r.POST("/api/internal/sso/exchange", handleEdgeSSOExchange(app))
	r.POST("/api/internal/feature-flags/apply", handleFeatureFlagsApply(app, mode))
}

// handleEdgeSSOExchange handles POST /api/internal/sso/exchange
// Called by Edge after JWT validation. Edge injects identity headers:
//   - X-User-ID (cloud subject)
//   - X-Org-ID (cloud organization / tenant)
//   - X-User-Email
//   - X-User-Roles (comma-separated)
//   - X-Kombify-SSO-Secret (shared secret for trust verification)
//
// This endpoint finds or creates a PocketBase user corresponding to the
// cloud identity and returns a PB auth token for the session.
func handleEdgeSSOExchange(app core.App) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		// Step 1: Verify trust — shared secret from Edge
		if err := verifyEdgeTrust(e); err != nil {
			return err
		}

		// Step 2: Extract identity from Edge-injected headers
		userID := e.Request.Header.Get("X-User-ID")
		orgID := e.Request.Header.Get("X-Org-ID")
		email := e.Request.Header.Get("X-User-Email")
		name := e.Request.Header.Get("X-User-Name") // optional

		if userID == "" || email == "" {
			return httpx.BadRequest(e, "Missing required identity headers (X-User-ID, X-User-Email)")
		}

		// Step 3: Find or create PocketBase user via user_links
		pbUser, _, err := findOrCreateUserFromEdge(app, userID, orgID, email, name)
		if err != nil {
			app.Logger().Error("Edge SSO exchange: user lookup failed",
				"external_id", userID,
				"email", email,
				"error", err,
			)
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to resolve user identity", nil)
		}

		// Step 4: Generate PocketBase auth token
		pbToken, err := pbUser.NewAuthToken()
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to generate auth token", nil)
		}

		return httpx.Success(e, http.StatusOK, EdgeSSOExchangeResponse{
			PBToken: pbToken,
			User: PBUserInfo{
				ID:    pbUser.Id,
				Email: pbUser.Email(),
				Name:  pbUser.GetString("name"),
			},
		})
	}
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

// findOrCreateUserFromEdge resolves a Edge-identified user to a PocketBase user.
// Uses user_links to map external cloud identities to PB users.
func findOrCreateUserFromEdge(app core.App, externalID, orgID, email, name string) (*core.Record, *core.Record, error) {
	// Try existing user_link by external_id
	userLink, err := app.FindFirstRecordByFilter(
		"user_links",
		"external_id = {:external_id} && provider = 'cloud'",
		map[string]any{"external_id": externalID},
	)
	if err == nil && userLink != nil {
		pbUser, err := app.FindRecordById("users", userLink.GetString("user"))
		if err != nil {
			return nil, nil, err
		}
		// Update org_id if it changed
		if userLink.GetString("org_id") != orgID {
			userLink.Set("org_id", orgID)
			_ = app.Save(userLink) // non-critical
		}
		return pbUser, userLink, nil
	}

	// Fallback: find PB user by email
	pbUser, err := app.FindFirstRecordByFilter(
		"users",
		"email = {:email}",
		map[string]any{"email": email},
	)
	if err != nil {
		// Create new PB user
		usersCollection, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return nil, nil, err
		}
		pbUser = core.NewRecord(usersCollection)
		pbUser.SetEmail(email)
		if name != "" {
			pbUser.Set("name", name)
		}
		pbUser.SetVerified(true)
		pbUser.SetPassword(security.RandomString(32))
		if err := app.Save(pbUser); err != nil {
			return nil, nil, err
		}
	}

	// Create user_link
	userLinksCollection, err := app.FindCollectionByNameOrId("user_links")
	if err != nil {
		return nil, nil, err
	}
	userLink = core.NewRecord(userLinksCollection)
	userLink.Set("user", pbUser.Id)
	userLink.Set("provider", "cloud")
	userLink.Set("external_id", externalID)
	userLink.Set("external_email", email)
	userLink.Set("external_name", name)
	userLink.Set("org_id", orgID)
	userLink.Set("is_admin", false)
	if err := app.Save(userLink); err != nil {
		return nil, nil, err
	}

	return pbUser, userLink, nil
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
func handleFeatureFlagsApply(app core.App, mode config.DeploymentMode) func(e *httpx.Event) error {
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

		result, err := applyFeatureFlagOverrides(app, mode, req.Flags)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to apply feature flags", map[string]any{"error": err.Error()})
		}

		status := http.StatusOK
		if mode.IsSaaS() {
			status = http.StatusAccepted
		}

		return httpx.Success(e, status, result)
	}
}

func applyFeatureFlagOverrides(app core.App, mode config.DeploymentMode, flags []FeatureFlagOverride) (featureFlagApplyResult, error) {
	result := featureFlagApplyResult{
		Applied:   make([]string, 0, len(flags)),
		Errors:    []string{},
		Persisted: !mode.IsSaaS(),
	}

	// In SaaS mode, platform-owned feature rollout state must not be persisted
	// into the local per-user PocketBase preferences table.
	if mode.IsSaaS() {
		for _, flag := range flags {
			result.Applied = append(result.Applied, flag.Key)
		}
		return result, nil
	}

	for _, flag := range flags {
		record, err := app.FindFirstRecordByFilter(
			"feature_rollouts",
			"feature_key = {:key}",
			map[string]any{"key": flag.Key},
		)
		if err != nil {
			collection, cerr := app.FindCollectionByNameOrId("feature_rollouts")
			if cerr != nil {
				result.Errors = append(result.Errors, flag.Key+": collection not found")
				continue
			}
			record = core.NewRecord(collection)
		}

		for key, value := range featureRolloutRecordFields(flag) {
			record.Set(key, value)
		}

		if err := app.Save(record); err != nil {
			result.Errors = append(result.Errors, flag.Key+": "+err.Error())
		} else {
			result.Applied = append(result.Applied, flag.Key)
		}
	}

	return result, nil
}

func featureRolloutRecordFields(flag FeatureFlagOverride) map[string]any {
	return map[string]any{
		"feature_key": flag.Key,
		"enabled":     flag.Enabled,
		"reason":      flag.Reason,
		"applied_by":  "internal_apply",
	}
}
