// Package auth provides REST API routes for authentication configuration.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	authsession "github.com/kombifyio/techstack/internal/gocommon/authsession"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/auth/sso"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/middleware"
	session "github.com/kombifyio/techstack/pkg/v2/auth/session"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// PortalVerifyRequest is the request body for POST /api/v1/auth/portal-verify
type PortalVerifyRequest struct {
	Token string `json:"token"`
	// PortalOrigin is the origin of the page embedding this app, when the
	// exchange runs inside a portal frame. It decides whether the browser
	// session must be usable cross-site (see setPortalSessionCookies).
	PortalOrigin string `json:"portal_origin,omitempty"`
}

// PortalVerifyResponse is the response for POST /api/v1/auth/portal-verify
type PortalVerifyResponse struct {
	CloudUser     CloudUserInfo      `json:"cloud_user"`
	StackIdentity *sso.StackIdentity `json:"stack_identity,omitempty"`
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
// it the exchange cannot establish authenticated embedded requests.
type PortalSession struct {
	Manager    *session.Manager
	CookieName string
	Secure     bool
	AuthStore  controlplane.AuthStore
}

func RegisterSSORoutes(r *httpx.Router, app core.App, ps PortalSession) {
	// Portal verify endpoint validates SSO and establishes the V2 session.
	r.POST("/api/v1/auth/portal-verify", handlePortalVerify(app, ps))

	// NOTE: GET /auth/sso is intentionally NOT registered here.
	// The SvelteKit frontend owns /auth/sso. It extracts the JWT token from the
	// URL fragment and POSTs it to /api/v1/auth/portal-verify; the Go router owns
	// only the exchange endpoint and the resulting session cookie.
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
// A successful response contains the cloud identity and sets the authoritative
// V2 browser-session cookie.
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
		ssoSecret, err := getSSOSecret()
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
		if _, err := portalTenantID(payload); err != nil {
			return handleSSOError(e, fmt.Errorf("%w: tenant_id", sso.ErrMissingClaims))
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
		membership, err := provisionSSOControlPlaneUser(e.Request.Context(), ps, payload)
		if err != nil {
			app.Logger().Error("Portal SSO: canonical identity projection failed", "error", err)
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to establish canonical identity", nil)
		}

		// Find or create PocketBase user based on external_id
		pbUser, err := findOrCreateUserFromSSO(app, payload)
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

		// The V2 cookie is the authoritative browser-session proof. It is issued
		// only after every fallible portal-verify step above has completed.
		if err := setPortalSessionCookies(e.Response, ps, portalSessionToken, req.PortalOrigin); err != nil {
			app.Logger().Error("Portal SSO: failed to issue browser session", "error", err)
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to establish browser session", nil)
		}

		// Build response
		response := PortalVerifyResponse{
			CloudUser: CloudUserInfo{
				Sub:     payload.Sub,
				Email:   payload.Email,
				Name:    payload.Name,
				IsAdmin: canonicalMembershipIsAdmin(membership),
			},
			StackIdentity: getStoredStackIdentity(pbUser),
		}

		return httpx.Success(e, http.StatusOK, response)
	}
}

// ssoProviderKey is the control-plane provider key for kombify Cloud SSO users.
const ssoProviderKey = "cloud"

// setPortalSessionCookies issues the browser session for a completed portal
// exchange.
//
// The production portal (kombify.io) and this app (techstack.kombify.io) are
// one site, so the SameSite=Lax session cookie every login issues reaches the
// embedded app. A kombify Cloud Render PR preview frames Techstack from
// *.onrender.com: that is cross-site, and a Lax cookie never accompanies the
// embedded requests, so the exchange succeeds and /api/v2/whoami still sees no
// session. For that one sanctioned portal origin the session and the CSRF
// cookie are issued with SameSite=None; Secure, which lets the preview embed
// authenticate and mutate exactly as production does. The CSRF double-submit
// keeps gating every unsafe request and frame-ancestors keeps limiting who may
// embed; only the sanctioned preview pattern and only secure deployments
// qualify, because browsers reject SameSite=None without Secure.
func setPortalSessionCookies(w http.ResponseWriter, ps PortalSession, token, portalOrigin string) error {
	name := strings.TrimSpace(ps.CookieName)
	if !ps.Secure || config.CloudPreviewFrameOrigin(portalOrigin) == "" {
		authsession.SetSessionCookie(w, name, token, ps.Secure)
		return nil
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
	})
	return middleware.IssueCrossSiteCSRFCookie(w)
}

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
	tenant, err := portalTenantID(payload)
	if err != nil {
		return "", err
	}
	token, err := ps.Manager.Issue(session.Claims{
		Subject:  strings.TrimSpace(payload.Sub),
		TenantID: tenant,
		Email:    strings.TrimSpace(payload.Email),
		Provider: ssoProviderKey,
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// provisionSSOControlPlaneUser upserts the canonical control-plane
// tenant/user/membership before a browser session is issued. PocketBase records
// are compatibility projections and cannot substitute for this authority.
func provisionSSOControlPlaneUser(ctx context.Context, ps PortalSession, payload *sso.SSOTokenPayload) (*controlplane.Membership, error) {
	sub := strings.TrimSpace(payload.Sub)
	if ps.AuthStore == nil {
		return nil, errors.New("control-plane auth store is not configured")
	}
	if sub == "" {
		return nil, errors.New("SSO subject is required")
	}
	tenant, err := portalTenantID(payload)
	if err != nil {
		return nil, err
	}
	if _, err := ps.AuthStore.UpsertTenant(ctx, controlplane.Tenant{ID: tenant}); err != nil {
		return nil, fmt.Errorf("upsert tenant: %w", err)
	}
	if _, err := ps.AuthStore.UpsertUser(ctx, controlplane.User{
		ID:           sub,
		PrimaryEmail: strings.TrimSpace(payload.Email),
		DisplayName:  strings.TrimSpace(payload.Name),
	}); err != nil {
		return nil, fmt.Errorf("upsert user: %w", err)
	}
	membership, err := ps.AuthStore.UpsertMembership(ctx, controlplane.Membership{
		ID:          tenant + ":" + sub,
		TenantID:    tenant,
		UserID:      sub,
		RoleKey:     "member",
		ProviderKey: ssoProviderKey,
		SubjectID:   sub,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert membership: %w", err)
	}
	return membership, nil
}

func canonicalMembershipIsAdmin(membership *controlplane.Membership) bool {
	if membership == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(membership.RoleKey)) {
	case "owner", "admin", "super_admin", "global_admin":
		return true
	default:
		return false
	}
}

// portalTenantID validates the signed Cloud tenant against the only supported
// SaaS namespaces. The embedded exchange must never fall back to the shared
// local/default tenant: a missing or inconsistent claim requires re-auth.
func portalTenantID(payload *sso.SSOTokenPayload) (string, error) {
	if payload == nil {
		return "", errors.New("SSO payload is required")
	}
	tenant := strings.TrimSpace(payload.TenantID)
	subject := strings.TrimSpace(payload.Sub)
	if tenant == "" || subject == "" {
		return "", errors.New("signed SSO tenant and subject are required")
	}
	if tenant == "usr:"+subject || strings.HasPrefix(tenant, "org_") {
		return tenant, nil
	}
	if tenant == demoguard.DemoTenantID() && demoguard.IsDemoUser(subject) {
		return tenant, nil
	}
	return "", errors.New("signed SSO tenant is inconsistent with subject")
}

// getSSOSecret retrieves the SSO JWT secret from canonical environment custody.
func getSSOSecret() (string, error) {
	// First, try environment variable
	if secret := os.Getenv("SSO_JWT_SECRET"); secret != "" {
		return secret, nil
	}

	if secret := os.Getenv("KOMBIFY_SSO_SECRET"); secret != "" {
		return secret, nil
	}

	return "", errors.New("SSO_JWT_SECRET not configured")
}

// findOrCreateUserFromSSO finds or creates a PocketBase user based on SSO payload.
// It uses the user_links collection to map external IDs to PB users.
func findOrCreateUserFromSSO(app core.App, payload *sso.SSOTokenPayload) (*core.Record, error) {
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
			return nil, errors.New("linked user not found")
		}
		updateCloudUserLinkFromSSO(userLink, payload)
		if err := app.Save(userLink); err != nil {
			return nil, err
		}
		return pbUser, nil
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
			return nil, errors.New("email-linked user not found")
		}
		updateCloudUserLinkFromSSO(userLink, payload)
		if err := app.Save(userLink); err != nil {
			return nil, err
		}
		return pbUser, nil
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
			return nil, err
		}

		pbUser = core.NewRecord(usersCollection)
		pbUser.SetEmail(payload.Email)
		pbUser.Set("name", payload.Name)
		pbUser.SetVerified(true) // SSO users are pre-verified
		// Generate a random password since SSO users won't use it
		pbUser.SetPassword(security.RandomString(32))

		if err := app.Save(pbUser); err != nil {
			return nil, err
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
			return nil, err
		}
		return pbUser, nil
	}

	// Create user_link to connect PB user with external ID
	userLinksCollection, err := app.FindCollectionByNameOrId("user_links")
	if err != nil {
		return nil, err
	}

	userLink = core.NewRecord(userLinksCollection)
	userLink.Set("user", pbUser.Id)
	userLink.Set("provider", "cloud")
	updateCloudUserLinkFromSSO(userLink, payload)
	if err := app.Save(userLink); err != nil {
		return nil, err
	}

	return pbUser, nil
}

func updateCloudUserLinkFromSSO(userLink *core.Record, payload *sso.SSOTokenPayload) {
	userLink.Set("external_id", payload.Sub)
	userLink.Set("external_email", payload.Email)
	userLink.Set("external_name", payload.Name)
}

// handleSSOError maps SSO verification errors to appropriate HTTP responses.
func handleSSOError(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, sso.ErrTokenExpired):
		return httpx.Error(e, http.StatusUnauthorized, ksapi.ErrCodeUnauthorized,
			"SSO token has expired", nil)
	case errors.Is(err, sso.ErrInvalidTool):
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Invalid tool claim in SSO token", nil)
	case errors.Is(err, sso.ErrTokenInvalid):
		return httpx.Error(e, http.StatusUnauthorized, ksapi.ErrCodeUnauthorized,
			"Invalid SSO token", nil)
	default:
		return httpx.Error(e, http.StatusUnauthorized, ksapi.ErrCodeUnauthorized,
			"SSO token verification failed", nil)
	}
}
