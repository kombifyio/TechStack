// Package authflow implements the browser-facing OIDC auth-code flow for
// kombify backend services. It is intentionally small and side-effect free:
// provider metadata comes from an [oidcclient.Registry], code exchange is
// abstracted behind [oidcclient.CodeExchanger] for testing, and successful
// callbacks mint a [authsession] cookie consumed by the session middleware.
//
// Donor: kombify-Techstack/pkg/v2/auth/flow (lifted 2026-05-03 and rebound
// to the shared oidcclient + authsession packages).
package authflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	"github.com/kombifyio/techstack/internal/gocommon/oidcclient"
)

const (
	defaultStateLifetime             = 5 * time.Minute
	defaultReauthenticationMaxAge    = 5 * time.Minute
	defaultReauthenticationClockSkew = 30 * time.Second
	maxReauthenticationValueLength   = 256
)

// Errors returned by the flow service.
var (
	ErrInvalidConfig = errors.New("authflow: invalid configuration")
	ErrInvalidState  = errors.New("authflow: invalid state")
	ErrMissingTenant = errors.New("authflow: tenant_id is required")
)

// TenantResolver maps verified OIDC claims to the tenant id stamped into the
// session cookie. Returning an empty string or non-nil error redirects the
// browser to the login error page with reason `tenant_resolve_failed`.
//
// Defaults to a function that returns the explicit tenant_id query
// parameter (or DefaultTenantID) when nil — see [Service.handleLogin].
type TenantResolver func(ctx context.Context, claims *oidcclient.Claims, providerID, requestedTenant string) (string, error)

// UserUpsert is an optional hook called after successful claim verification
// and before the session cookie is minted. It lets the consumer create or
// update its user record (e.g. into PocketBase / Postgres). Returning an
// error redirects to the login error page with reason `user_upsert_failed`.
type UserUpsert func(ctx context.Context, claims *oidcclient.Claims, tenantID, providerID string) error

// CallbackPath is the path appended to the configured public origin to derive the
// `redirect_uri` sent to identity providers. It MUST match the path
// registered with the IdP. Override via [Config.CallbackPath].
const CallbackPath = "/api/v1/auth/callback"

// Config configures a [Service].
type Config struct {
	// PublicBaseURL is the canonical external origin from the consumer service-URL
	// registry. Required; HTTPS except explicit loopback HTTP development. Request
	// Host and forwarding headers never determine the OIDC callback origin.
	PublicBaseURL string

	// Providers is the registry of configured OIDC providers. Required.
	Providers *oidcclient.Registry
	// Sessions mints HS256 session JWTs after a successful callback. Required.
	Sessions *authsession.Manager
	// StateSecret seals the short-lived state and browser-binding cookie.
	// Must be at least 32 bytes.
	StateSecret []byte

	// DefaultProviderID is used when /login is called without an explicit
	// `provider` query parameter and the registry has more than one entry.
	DefaultProviderID string
	// DefaultTenantID is stamped into session claims when neither the
	// `tenant_id` query parameter nor the [TenantResolver] supplies one.
	DefaultTenantID string
	// DefaultReturnTo is the post-login redirect path. Defaults to "/".
	DefaultReturnTo string
	// LoginErrorPath is where the callback redirects on failure. Defaults to
	// "/login".
	LoginErrorPath string
	// CallbackPath overrides the default OIDC callback path. Defaults to
	// [CallbackPath].
	CallbackPath string

	// SessionCookieName is the cookie minted on successful login. Defaults
	// to [authsession.DefaultSessionCookieName].
	SessionCookieName string
	// SessionCookieSecure controls the cookie's Secure flag. Set true in
	// production; false is reserved for loopback local HTTP development. The
	// secure browser-binding cookie uses __Host- and Path=/ to prevent sibling
	// subdomains from injecting a competing Domain cookie; its callback path
	// remains bound in the encrypted payload's additional authenticated data.
	SessionCookieSecure bool

	// Exchanger overrides the default RFC-6749 code exchanger. Tests inject
	// a fake; production uses [oidcclient.NewHTTPCodeExchanger].
	Exchanger oidcclient.CodeExchanger

	// TenantResolver maps verified OIDC claims to the session tenant id.
	// Optional; when nil the request `tenant_id` (or DefaultTenantID) is used.
	TenantResolver TenantResolver
	// UserUpsert is an optional post-verification hook for user provisioning.
	UserUpsert UserUpsert

	// Now is injectable for tests.
	Now func() time.Time
}

// Service serves the OIDC auth-code endpoints.
type Service struct {
	cfg       Config
	exchanger oidcclient.CodeExchanger
	now       func() time.Time
}

// NewService validates Config and returns a new auth-code flow service.
func NewService(cfg Config) (*Service, error) {
	publicBaseURL, err := validatedPublicBaseURL(cfg.PublicBaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.Providers == nil || cfg.Providers.Len() == 0 {
		return nil, fmt.Errorf("%w: at least one provider is required", ErrInvalidConfig)
	}
	if cfg.Sessions == nil {
		return nil, fmt.Errorf("%w: sessions manager is required", ErrInvalidConfig)
	}
	if len(cfg.StateSecret) < 32 {
		return nil, fmt.Errorf("%w: state secret must be at least 32 bytes", ErrInvalidConfig)
	}
	if cfg.DefaultReturnTo == "" {
		cfg.DefaultReturnTo = "/"
	}
	if cfg.LoginErrorPath == "" {
		cfg.LoginErrorPath = "/login"
	}
	if cfg.CallbackPath == "" {
		cfg.CallbackPath = CallbackPath
	}
	if err := validateBrowserCookiePath(cfg.CallbackPath); err != nil {
		return nil, err
	}
	if cfg.SessionCookieName == "" {
		cfg.SessionCookieName = authsession.DefaultSessionCookieName
	}
	exchanger := cfg.Exchanger
	if exchanger == nil {
		exchanger = oidcclient.NewHTTPCodeExchanger()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		cfg: Config{
			PublicBaseURL:       publicBaseURL,
			Providers:           cfg.Providers,
			Sessions:            cfg.Sessions,
			StateSecret:         cfg.StateSecret,
			DefaultProviderID:   cfg.DefaultProviderID,
			DefaultTenantID:     cfg.DefaultTenantID,
			DefaultReturnTo:     sanitizeReturnTo(cfg.DefaultReturnTo, "/"),
			LoginErrorPath:      sanitizeReturnTo(cfg.LoginErrorPath, "/login"),
			CallbackPath:        cfg.CallbackPath,
			SessionCookieName:   cfg.SessionCookieName,
			SessionCookieSecure: cfg.SessionCookieSecure,
			TenantResolver:      cfg.TenantResolver,
			UserUpsert:          cfg.UserUpsert,
		},
		exchanger: exchanger,
		now:       now,
	}, nil
}

// ProvidersHandler exposes the configured providers for frontend bootstrap.
func (s *Service) ProvidersHandler() http.Handler {
	return http.HandlerFunc(s.handleProviders)
}

// LoginHandler starts the auth-code redirect flow.
func (s *Service) LoginHandler() http.Handler {
	return http.HandlerFunc(s.handleLogin)
}

// CallbackHandler completes the auth-code flow, mints a session cookie, and
// redirects the browser to the requested return path.
func (s *Service) CallbackHandler() http.Handler {
	return http.HandlerFunc(s.handleCallback)
}

// LogoutHandler clears the session cookie and redirects to the login page
// (or a caller-provided next path).
func (s *Service) LogoutHandler() http.Handler {
	return http.HandlerFunc(s.handleLogout)
}

func (s *Service) handleProviders(w http.ResponseWriter, r *http.Request) {
	ids := s.cfg.Providers.IDs()
	sort.Strings(ids)
	type providerInfo struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Issuer string `json:"issuer"`
	}
	resp := struct {
		Providers []providerInfo `json:"providers"`
	}{Providers: make([]providerInfo, 0, len(ids))}
	for _, id := range ids {
		p, err := s.cfg.Providers.Get(id)
		if err != nil {
			continue
		}
		resp.Providers = append(resp.Providers, providerInfo{
			ID:     p.ID(),
			Kind:   string(p.Kind()),
			Issuer: p.Issuer(),
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	provider, err := s.resolveProvider(strings.TrimSpace(r.URL.Query().Get("provider")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if tenantID == "" {
		tenantID = s.cfg.DefaultTenantID
	}
	// TenantResolver runs again on callback after claims are verified; here
	// we only need a non-empty placeholder so /login can fail fast for
	// pure self-hosted configurations that lack a tenant.
	if tenantID == "" && s.cfg.TenantResolver == nil {
		http.Error(w, ErrMissingTenant.Error(), http.StatusBadRequest)
		return
	}
	returnTo := sanitizeReturnTo(r.URL.Query().Get("return_to"), s.cfg.DefaultReturnTo)
	reauthPurpose, reauthResource, err := reauthenticationBinding(r.URL.Query())
	if err != nil {
		http.Error(w, "invalid reauthentication binding", http.StatusBadRequest)
		return
	}
	verifier, challenge, err := oidcclient.PKCEVerifier()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	binding, err := s.newBrowserBinding(verifier)
	if err != nil {
		http.Error(w, "create browser binding failed", http.StatusInternalServerError)
		return
	}
	state, err := s.issueState(loginState{
		ProviderID:     provider.ID(),
		TenantID:       tenantID,
		ReturnTo:       returnTo,
		BrowserBinding: binding.stateHash(),
		IssuedAt:       binding.IssuedAt,
		ExpiresAt:      binding.ExpiresAt,
		ReauthPurpose:  reauthPurpose,
		ReauthResource: reauthResource,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redirectURI := s.callbackURL()
	authorizationURL, err := authorizationURL(provider.AuthCodeURL(redirectURI, state, challenge), reauthPurpose != "", binding.oidcNonce())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.setBrowserCookie(w, binding); err != nil {
		http.Error(w, "seal browser binding failed", http.StatusInternalServerError)
		return
	}
	// #nosec G710 -- authorizationURL is based on the validated, server-configured
	// provider endpoint and only adds URL-encoded parameters.
	http.Redirect(w, r, authorizationURL, http.StatusFound)
}

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	state, binding, err := s.consumeBrowserBinding(w, r)
	if err != nil {
		s.redirectToLoginError(w, r, "invalid_state")
		return
	}
	if errorCode := strings.TrimSpace(r.URL.Query().Get("error")); errorCode != "" {
		s.redirectToLoginError(w, r, errorCode)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.redirectToLoginError(w, r, "missing_authorization_code")
		return
	}
	provider, err := s.resolveProvider(state.ProviderID)
	if err != nil {
		s.redirectToLoginError(w, r, "unknown_provider")
		return
	}
	claims, authenticatedAt, failure := s.authenticateCallback(r, provider, state, binding, code)
	if failure != "" {
		s.redirectToLoginError(w, r, failure)
		return
	}
	tenantID := state.TenantID
	if s.cfg.TenantResolver != nil {
		resolved, resolveErr := s.cfg.TenantResolver(r.Context(), claims, provider.ID(), state.TenantID)
		if resolveErr != nil || strings.TrimSpace(resolved) == "" {
			s.redirectToLoginError(w, r, "tenant_resolve_failed")
			return
		}
		tenantID = strings.TrimSpace(resolved)
	}
	if tenantID == "" {
		s.redirectToLoginError(w, r, "tenant_resolve_failed")
		return
	}
	if s.cfg.UserUpsert != nil {
		if upsertErr := s.cfg.UserUpsert(r.Context(), claims, tenantID, provider.ID()); upsertErr != nil {
			s.redirectToLoginError(w, r, "user_upsert_failed")
			return
		}
	}
	token, err := s.cfg.Sessions.Issue(authsession.Claims{
		Subject:         claims.Subject,
		TenantID:        tenantID,
		OrgID:           firstClaim(claims.Raw, "org_id", "org", "project_id"),
		Email:           claims.Email,
		Provider:        provider.ID(),
		Role:            firstClaim(claims.Raw, "role", "local_role"),
		ReauthPurpose:   state.ReauthPurpose,
		ReauthResource:  state.ReauthResource,
		AuthenticatedAt: authenticatedAt,
	})
	if err != nil {
		s.redirectToLoginError(w, r, "session_issue_failed")
		return
	}
	authsession.SetSessionCookie(w, s.cfg.SessionCookieName, token, s.cfg.SessionCookieSecure)
	http.Redirect(w, r, state.ReturnTo, http.StatusFound)
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	next := sanitizeReturnTo(r.URL.Query().Get("next"), s.cfg.LoginErrorPath)
	authsession.ClearSessionCookie(w, s.cfg.SessionCookieName, s.cfg.SessionCookieSecure)
	// #nosec G710 -- sanitizeReturnTo accepts only local absolute-path references
	// and rejects backslashes, control characters, hosts, and schemes.
	http.Redirect(w, r, next, http.StatusFound)
}

func (s *Service) resolveProvider(id string) (*oidcclient.Provider, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		if s.cfg.DefaultProviderID != "" {
			id = s.cfg.DefaultProviderID
		} else if s.cfg.Providers.Len() == 1 {
			ids := s.cfg.Providers.IDs()
			if len(ids) == 1 {
				id = ids[0]
			}
		}
	}
	if id == "" {
		return nil, fmt.Errorf("%w: provider is required", ErrInvalidConfig)
	}
	return s.cfg.Providers.Get(id)
}

type loginState struct {
	ProviderID     string `json:"provider_id"`
	TenantID       string `json:"tenant_id"`
	ReturnTo       string `json:"return_to"`
	BrowserBinding string `json:"browser_binding"`
	ReauthPurpose  string `json:"reauth_purpose,omitempty"`
	ReauthResource string `json:"reauth_resource,omitempty"`
	IssuedAt       int64  `json:"iat"`
	ExpiresAt      int64  `json:"exp"`
}

func (s *Service) issueState(state loginState) (string, error) {
	claims := loginState{
		ProviderID:     state.ProviderID,
		TenantID:       state.TenantID,
		ReturnTo:       sanitizeReturnTo(state.ReturnTo, s.cfg.DefaultReturnTo),
		BrowserBinding: state.BrowserBinding,
		ReauthPurpose:  state.ReauthPurpose,
		ReauthResource: state.ReauthResource,
		IssuedAt:       state.IssuedAt,
		ExpiresAt:      state.ExpiresAt,
	}
	if !validStateFields(&claims) || !s.validLoginLifetime(claims.IssuedAt, claims.ExpiresAt) {
		return "", ErrInvalidState
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal state: %w", err)
	}
	return s.sealLoginValue(stateRole, payload)
}

func (s *Service) parseState(raw string) (*loginState, error) {
	payload, err := s.openLoginValue(stateRole, raw)
	if err != nil {
		return nil, ErrInvalidState
	}
	claims := &loginState{}
	if err := json.Unmarshal(payload, claims); err != nil {
		return nil, ErrInvalidState
	}
	if !validStateFields(claims) {
		return nil, ErrInvalidState
	}
	if _, _, err := normalizedReauthenticationBinding(claims.ReauthPurpose, claims.ReauthResource); err != nil {
		return nil, ErrInvalidState
	}
	if !s.validLoginLifetime(claims.IssuedAt, claims.ExpiresAt) {
		// Retain only authenticated, bounded state to retire its expired cookie.
		return claims, ErrInvalidState
	}
	claims.ReturnTo = sanitizeReturnTo(claims.ReturnTo, s.cfg.DefaultReturnTo)
	return claims, nil
}

func reauthenticationBinding(query url.Values) (string, string, error) {
	return normalizedReauthenticationBinding(query.Get("reauth_purpose"), query.Get("reauth_resource"))
}

func normalizedReauthenticationBinding(purpose, resource string) (string, string, error) {
	purpose = strings.TrimSpace(purpose)
	resource = strings.TrimSpace(resource)
	if purpose == "" && resource == "" {
		return "", "", nil
	}
	if purpose == "" || resource == "" || !validReauthenticationValue(purpose) || !validReauthenticationValue(resource) {
		return "", "", ErrInvalidState
	}
	return purpose, resource, nil
}

func validReauthenticationValue(value string) bool {
	if len(value) > maxReauthenticationValueLength {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func authorizationURL(raw string, reauthenticate bool, nonce string) (string, error) {
	target, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse provider authorization URL: %w", err)
	}
	query := target.Query()
	query.Set("nonce", nonce)
	if reauthenticate {
		query.Set("prompt", "login")
		query.Set("max_age", "0")
	}
	target.RawQuery = query.Encode()
	return target.String(), nil
}

func freshAuthenticationTime(claims map[string]interface{}, now time.Time) (int64, error) {
	unix, ok := unixClaim(claims["auth_time"])
	if !ok {
		return 0, ErrInvalidState
	}
	authenticatedAt := time.Unix(unix, 0).UTC()
	if authenticatedAt.After(now.Add(defaultReauthenticationClockSkew)) || now.Sub(authenticatedAt) > defaultReauthenticationMaxAge {
		return 0, ErrInvalidState
	}
	return unix, nil
}

func unixClaim(raw interface{}) (int64, bool) {
	switch value := raw.(type) {
	case float64:
		return int64(value), value > 0 && value == float64(int64(value))
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil && parsed > 0
	case int64:
		return value, value > 0
	case int:
		return int64(value), value > 0
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return parsed, err == nil && parsed > 0
	default:
		return 0, false
	}
}

func (s *Service) redirectToLoginError(w http.ResponseWriter, r *http.Request, code string) {
	target := s.cfg.LoginErrorPath
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	http.Redirect(w, r, target+sep+"error="+url.QueryEscape(code), http.StatusFound)
}

func sanitizeReturnTo(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if strings.ContainsAny(raw, "\\\r\n") {
		return fallback
	}
	target, err := url.Parse(raw)
	if err != nil || target.IsAbs() || target.Host != "" ||
		!strings.HasPrefix(target.Path, "/") || strings.HasPrefix(target.Path, "//") {
		return fallback
	}
	return raw
}

func validatedPublicBaseURL(raw string) (string, error) {
	origin, err := url.Parse(raw)
	if err != nil || origin.Hostname() == "" || origin.Opaque != "" || origin.User != nil {
		return "", fmt.Errorf("%w: public base URL must be an absolute origin without credentials", ErrInvalidConfig)
	}
	if (origin.Path != "" && origin.Path != "/") || origin.RawPath != "" ||
		origin.RawQuery != "" || origin.ForceQuery || strings.Contains(raw, "#") ||
		strings.ContainsAny(raw, "\\\r\n\t ") {
		return "", fmt.Errorf("%w: public base URL must not contain a path, query or fragment", ErrInvalidConfig)
	}
	if err := validatePublicOriginTransport(origin); err != nil {
		return "", err
	}
	origin.Path = ""
	return origin.String(), nil
}

func validatePublicOriginPort(origin *url.URL) error {
	if strings.HasSuffix(origin.Host, ":") {
		return fmt.Errorf("%w: public base URL has an invalid port", ErrInvalidConfig)
	}
	if port := origin.Port(); port != "" {
		number, portErr := strconv.Atoi(port)
		if portErr != nil || number < 1 || number > 65535 {
			return fmt.Errorf("%w: public base URL has an invalid port", ErrInvalidConfig)
		}
	}
	return nil
}

func validatePublicOriginTransport(origin *url.URL) error {
	if err := validatePublicOriginPort(origin); err != nil {
		return err
	}

	host := strings.ToLower(origin.Hostname())
	ip := net.ParseIP(host)
	if strings.HasPrefix(origin.Host, "[") && (!strings.Contains(host, ":") || ip == nil) {
		return fmt.Errorf("%w: bracketed public host must be an IPv6 literal", ErrInvalidConfig)
	}
	if strings.Contains(host, ":") && (!strings.HasPrefix(origin.Host, "[") || ip == nil) {
		return fmt.Errorf("%w: public base URL has an invalid host", ErrInvalidConfig)
	}
	switch origin.Scheme {
	case "https":
		return nil
	case "http":
		if host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("%w: public base URL requires HTTPS except for loopback development", ErrInvalidConfig)
}

func (s *Service) callbackURL() string {
	// CallbackPath is validated separately; direct concatenation keeps even a
	// path beginning with // inside the configured origin rather than resolving
	// it as a replacement URL authority.
	return s.cfg.PublicBaseURL + s.cfg.CallbackPath
}

func firstClaim(raw map[string]interface{}, keys ...string) string {
	if raw == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if s, ok := value.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func (s *Service) authenticateCallback(r *http.Request, provider *oidcclient.Provider, state *loginState, binding *browserBinding, code string) (*oidcclient.Claims, int64, string) {
	redirectURI := s.callbackURL()
	exchange, err := s.exchanger.ExchangeCode(r.Context(), provider, oidcclient.CodeExchangeRequest{
		Code:         code,
		RedirectURI:  redirectURI,
		PKCEVerifier: binding.PKCEVerifier,
	})
	if err != nil || exchange == nil || strings.TrimSpace(exchange.IDToken) == "" {
		return nil, 0, "token_exchange_failed"
	}
	claims, err := provider.Verify(r.Context(), exchange.IDToken)
	if err != nil {
		return nil, 0, "invalid_id_token"
	}
	if !binding.matchesOIDCNonce(claims.Raw["nonce"]) {
		return nil, 0, "invalid_id_token"
	}
	authenticatedAt := int64(0)
	if state.ReauthPurpose != "" {
		authenticatedAt, err = freshAuthenticationTime(claims.Raw, s.now().UTC())
		if err != nil {
			return nil, 0, "reauthentication_required"
		}
	}
	return claims, authenticatedAt, ""
}
