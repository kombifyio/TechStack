// Package auth provides REST API routes for authentication configuration.
package auth

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	authsession "github.com/kombifyio/go-common/authsession"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/auth/sso"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	session "github.com/kombifyio/techstack/pkg/v2/auth/session"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// PortalVerifyRequest is the request body for POST /api/v1/auth/portal-verify
type PortalVerifyRequest struct {
	Token string `json:"token"`
}

// PortalVerifyResponse is the response for POST /api/v1/auth/portal-verify
type PortalVerifyResponse struct {
	PBToken       string             `json:"pb_token"`
	User          PBUserInfo         `json:"user"`
	CloudUser     CloudUserInfo      `json:"cloud_user"`
	StackIdentity *sso.StackIdentity `json:"stack_identity,omitempty"`
}

// PBUserInfo contains PocketBase user information
type PBUserInfo struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// CloudUserInfo contains cloud user information from the SSO token.
type CloudUserInfo struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"is_admin"`
}

// RegisterSSORoutes registers SSO-related API routes.
// It registers the following routes:
//   - POST /api/v1/auth/portal-verify - Verify SSO token from kombify Cloud Portal
//   - GET /auth/sso - SSO landing page (served by SvelteKit frontend)
//
// PortalSession carries the V2 browser-session manager and cookie settings
// so the embedded SSO exchange (portal-verify) mints the same
// techstack_session session that the interactive OIDC login issues. Without
// it the exchange only returns a PocketBase token that the post-Edge-migration
// request path no longer accepts, leaving embedded callers unauthenticated (401).
type PortalSession struct {
	Manager       *session.Manager
	CookieName    string
	DefaultTenant string
	Secure        bool
	AuthStore     controlplane.AuthStore
}

func RegisterSSORoutes(r *httpx.Router, app core.App, ps PortalSession) {
	// Portal verify endpoint - validates SSO token and returns PB session
	r.POST("/api/v1/auth/portal-verify", handlePortalVerify(app, ps))

	// NOTE: GET /auth/sso is intentionally NOT registered here.
	// The SvelteKit frontend has a client-side SSO page at /auth/sso that
	// extracts the JWT token from the URL fragment and POSTs it to
	// /api/v1/auth/portal-verify. PocketBase serves the SPA index.html
	// for this route via pb_public, allowing SvelteKit's client router to handle it.
}

// handlePortalVerify handles POST /api/v1/auth/portal-verify
// Called by frontend when user arrives with SSO token from kombify Cloud Portal.
//
// Request:
//
//	{
//	  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
//	}
//
// Response:
//
//	{
//	  "data": {
//	    "pb_token": "pocketbase-jwt-token",
//	    "user": { "id": "...", "email": "...", "name": "..." },
//	    "cloud_user": { "sub": "...", "email": "...", "name": "...", "is_admin": true }
//	  }
//	}
func handlePortalVerify(app core.App, ps PortalSession) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		// Parse request body
		var req PortalVerifyRequest
		if err := e.BindBody(&req); err != nil {
			return httpx.BadRequest(e, "Invalid request body")
		}

		if req.Token == "" {
			return httpx.BadRequest(e, "Token is required")
		}

		// Get SSO secret from environment or auth_config
		ssoSecret, err := getSSOSecret(app)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"SSO configuration error", nil)
		}

		// Create SSO verifier
		verifier, err := sso.NewVerifier(sso.Config{
			Secret:       ssoSecret,
			AllowedTools: []string{"kombifystack"},
			ClockSkew:    30 * time.Second,
		})
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to initialize SSO verifier", nil)
		}

		// Verify the SSO token
		payload, err := verifier.Verify(req.Token)
		if err != nil {
			return handleSSOError(e, err)
		}

		// portal-verify is only a successful login when it can establish the
		// same V2 browser session consumed by /api/v2/whoami. Do this preflight
		// before mutating legacy compatibility records, and do not report the
		// PocketBase payload as an authenticated browser session without it.
		portalSessionToken, err := mintPortalSessionToken(ps, payload)
		if err != nil {
			app.Logger().Warn("Portal SSO: failed to prepare session cookie", "error", err)
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to establish browser session", nil)
		}

		// Find or create PocketBase user based on external_id
		pbUser, userLink, err := findOrCreateUserFromSSO(app, payload)
		if err != nil {
			app.Logger().Error("Portal SSO: user lookup failed",
				"sub", payload.Sub,
				"email", payload.Email,
				"error", err,
			)
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to create or find user", nil)
		}

		if payload.StackIdentity != nil {
			if err := saveUserStackIdentity(app, pbUser, payload.StackIdentity); err != nil {
				return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
					"Failed to persist stack identity", nil)
			}
		}

		// Generate PocketBase auth token for the user
		pbToken, err := generatePBAuthToken(app, pbUser)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to generate auth token", nil)
		}

		// Update last_login on user_link
		if err := updateLastLogin(app, userLink); err != nil {
			// Non-critical error, log but don't fail
			app.Logger().Warn("Failed to update last_login", "error", err)
		}

		// The V2 cookie is the authoritative browser-session proof. It is issued
		// only after every fallible portal-verify step above has completed.
		authsession.SetSessionCookie(e.Response, strings.TrimSpace(ps.CookieName), portalSessionToken, ps.Secure)

		// Provision the control-plane membership after the authenticated session
		// has been established. This remains best-effort because it is a separate
		// tenant-projection recovery concern.
		provisionSSOControlPlaneUser(app, ps, e, payload)

		// Build response
		response := PortalVerifyResponse{
			PBToken: pbToken,
			User: PBUserInfo{
				ID:    pbUser.Id,
				Email: pbUser.Email(),
				Name:  pbUser.GetString("name"),
			},
			CloudUser: CloudUserInfo{
				Sub:     payload.Sub,
				Email:   payload.Email,
				Name:    payload.Name,
				IsAdmin: userLink.GetBool("is_admin"),
			},
			StackIdentity: getStoredStackIdentity(pbUser),
		}

		return httpx.Success(e, http.StatusOK, response)
	}
}

// ssoProviderKey is the control-plane provider key for kombify Cloud SSO users.
const ssoProviderKey = "cloud"

// mintPortalSessionToken prepares the techstack_session V2 session that the
// embedded request path requires. A portal exchange must fail closed if this
// browser session cannot be issued; the legacy PocketBase token alone is not a
// valid session on the post-Edge request path.
func mintPortalSessionToken(ps PortalSession, payload *sso.SSOTokenPayload) (string, error) {
	if ps.Manager == nil {
		return "", errors.New("v2 session manager is not configured")
	}
	if strings.TrimSpace(ps.CookieName) == "" {
		return "", errors.New("v2 session cookie name is not configured")
	}
	token, err := ps.Manager.Issue(session.Claims{
		Subject:  strings.TrimSpace(payload.Sub),
		TenantID: portalTenantID(ps.DefaultTenant),
		Email:    strings.TrimSpace(payload.Email),
		Provider: ssoProviderKey,
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// provisionSSOControlPlaneUser upserts the control-plane tenant/user/membership
// for the SSO user, mirroring the interactive OIDC login (v2CloudUserUpsert), so
// a brand-new SSO-only user resolves entitlements. Best-effort: logged, never
// blocks the exchange.
func provisionSSOControlPlaneUser(app core.App, ps PortalSession, e *httpx.Event, payload *sso.SSOTokenPayload) {
	sub := strings.TrimSpace(payload.Sub)
	if ps.AuthStore == nil || sub == "" {
		return
	}
	ctx := e.Request.Context()
	tenant := portalTenantID(ps.DefaultTenant)
	if _, err := ps.AuthStore.UpsertTenant(ctx, controlplane.Tenant{ID: tenant}); err != nil {
		app.Logger().Warn("Portal SSO: control-plane tenant upsert failed", "error", err)
		return
	}
	if _, err := ps.AuthStore.UpsertUser(ctx, controlplane.User{
		ID:           sub,
		PrimaryEmail: strings.TrimSpace(payload.Email),
		DisplayName:  strings.TrimSpace(payload.Name),
	}); err != nil {
		app.Logger().Warn("Portal SSO: control-plane user upsert failed", "error", err)
		return
	}
	if _, err := ps.AuthStore.UpsertMembership(ctx, controlplane.Membership{
		ID:          tenant + ":" + sub,
		TenantID:    tenant,
		UserID:      sub,
		RoleKey:     "member",
		ProviderKey: ssoProviderKey,
		SubjectID:   sub,
	}); err != nil {
		app.Logger().Warn("Portal SSO: control-plane membership upsert failed", "error", err)
	}
}

// portalTenantID mirrors v2DefaultTenantIDFromEnv: an empty configured tenant
// falls back to the canonical "default" tenant used by the interactive login.
func portalTenantID(defaultTenant string) string {
	if t := strings.TrimSpace(defaultTenant); t != "" {
		return t
	}
	return "default"
}

// getSSOSecret retrieves the SSO JWT secret from environment or auth_config.
func getSSOSecret(app core.App) (string, error) {
	// First, try environment variable
	if secret := os.Getenv("SSO_JWT_SECRET"); secret != "" {
		return secret, nil
	}

	if secret := os.Getenv("KOMBIFY_SSO_SECRET"); secret != "" {
		return secret, nil
	}

	if app == nil {
		return "", errors.New("SSO_JWT_SECRET not configured")
	}

	// Fall back to auth_config record
	record, err := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
	if err != nil || record == nil {
		return "", errors.New("SSO_JWT_SECRET not configured")
	}

	secret := record.GetString("sso_jwt_secret")
	if secret == "" {
		return "", errors.New("SSO_JWT_SECRET not configured in auth_config")
	}

	return secret, nil
}

// findOrCreateUserFromSSO finds or creates a PocketBase user based on SSO payload.
// It uses the user_links collection to map external IDs to PB users.
func findOrCreateUserFromSSO(app core.App, payload *sso.SSOTokenPayload) (*core.Record, *core.Record, error) {
	// Try to find existing user_link by external_id
	userLink, err := app.FindFirstRecordByFilter(
		"user_links",
		"external_id = {:external_id} && provider = 'cloud'",
		map[string]any{
			"external_id": payload.Sub,
		},
	)

	if err == nil && userLink != nil {
		// Found existing link, get the PB user
		userID := userLink.GetString("user")
		pbUser, err := app.FindRecordById("users", userID)
		if err != nil {
			return nil, nil, errors.New("linked user not found")
		}
		updateCloudUserLinkFromSSO(userLink, payload)
		if err := app.Save(userLink); err != nil {
			return nil, nil, err
		}
		return pbUser, userLink, nil
	}

	// Auth0/Cloud subjects can rotate while the verified email remains stable.
	// Re-link the existing cloud identity instead of creating a duplicate link
	// that violates the user+provider uniqueness constraint.
	userLink, err = app.FindFirstRecordByFilter(
		"user_links",
		"external_email = {:email} && provider = 'cloud'",
		map[string]any{
			"email": payload.Email,
		},
	)
	if err == nil && userLink != nil {
		pbUser, err := app.FindRecordById("users", userLink.GetString("user"))
		if err != nil {
			return nil, nil, errors.New("email-linked user not found")
		}
		updateCloudUserLinkFromSSO(userLink, payload)
		if err := app.Save(userLink); err != nil {
			return nil, nil, err
		}
		return pbUser, userLink, nil
	}

	// No existing link found, check if user with this email exists
	pbUser, err := app.FindFirstRecordByFilter(
		"users",
		"email = {:email}",
		map[string]any{
			"email": payload.Email,
		},
	)

	if err != nil {
		// Create new PB user
		usersCollection, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return nil, nil, err
		}

		pbUser = core.NewRecord(usersCollection)
		pbUser.SetEmail(payload.Email)
		pbUser.Set("name", payload.Name)
		pbUser.SetVerified(true) // SSO users are pre-verified
		// Generate a random password since SSO users won't use it
		pbUser.SetPassword(security.RandomString(32))

		if err := app.Save(pbUser); err != nil {
			return nil, nil, err
		}
	}

	userLink, err = app.FindFirstRecordByFilter(
		"user_links",
		"user = {:user} && provider = 'cloud'",
		map[string]any{
			"user": pbUser.Id,
		},
	)
	if err == nil && userLink != nil {
		updateCloudUserLinkFromSSO(userLink, payload)
		if err := app.Save(userLink); err != nil {
			return nil, nil, err
		}
		return pbUser, userLink, nil
	}

	// Create user_link to connect PB user with external ID
	userLinksCollection, err := app.FindCollectionByNameOrId("user_links")
	if err != nil {
		return nil, nil, err
	}

	userLink = core.NewRecord(userLinksCollection)
	userLink.Set("user", pbUser.Id)
	userLink.Set("provider", "cloud")
	updateCloudUserLinkFromSSO(userLink, payload)
	userLink.Set("is_admin", false) // Default to non-admin, can be updated later

	if err := app.Save(userLink); err != nil {
		return nil, nil, err
	}

	return pbUser, userLink, nil
}

func updateCloudUserLinkFromSSO(userLink *core.Record, payload *sso.SSOTokenPayload) {
	userLink.Set("external_id", payload.Sub)
	userLink.Set("external_email", payload.Email)
	userLink.Set("external_name", payload.Name)
}

// generatePBAuthToken creates a PocketBase auth token for the given user.
func generatePBAuthToken(app core.App, user *core.Record) (string, error) {
	// PocketBase uses its own token generation based on the app's signing key
	// The token duration is configured in the collection settings
	return user.NewAuthToken()
}

// updateLastLogin updates the last_login field on a user_link record.
func updateLastLogin(app core.App, userLink *core.Record) error {
	// The AutodateField with OnUpdate: true will handle this automatically
	// We just need to trigger a save
	return app.Save(userLink)
}

// handleSSOError maps SSO verification errors to appropriate HTTP responses.
func handleSSOError(e *httpx.Event, err error) error {
	switch {
	case sso.IsExpired(err):
		return httpx.Error(e, http.StatusUnauthorized, ksapi.ErrCodeUnauthorized,
			"SSO token has expired", nil)
	case sso.IsInvalidTool(err):
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Invalid tool claim in SSO token", nil)
	case sso.IsInvalid(err):
		return httpx.Error(e, http.StatusUnauthorized, ksapi.ErrCodeUnauthorized,
			"Invalid SSO token", nil)
	default:
		return httpx.Error(e, http.StatusUnauthorized, ksapi.ErrCodeUnauthorized,
			"SSO token verification failed", nil)
	}
}
