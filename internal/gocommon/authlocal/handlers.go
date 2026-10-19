package authlocal

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	"github.com/kombifyio/techstack/internal/gocommon/oidcclient"
)

// ProviderInfo is one entry in the unified methods response.
type ProviderInfo struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// AuthURL is the relative URL the frontend should navigate to in order
	// to start an OIDC redirect. Empty for local providers.
	AuthURL string `json:"auth_url,omitempty"`
}

// MethodsResponse is returned by GET /api/v1/auth/methods (or similar).
type MethodsResponse struct {
	Providers  []ProviderInfo `json:"providers"`
	BreakGlass Status         `json:"breakglass"`
}

// Handlers groups HTTP handlers for the local credential service plus the
// methods-discovery endpoint that lists OIDC providers + break-glass.
type Handlers struct {
	svc *Service

	// providers is the OIDC provider registry consulted by the methods
	// endpoint. Optional — when nil the response only carries the
	// break-glass entry.
	providers *oidcclient.Registry

	// loginRedirectPath is the URL pattern the methods endpoint emits as
	// `auth_url` for OIDC providers. Defaults to
	// "/api/v1/auth/login?provider=".
	loginRedirectPath string

	// rate enforces a per-IP rate limit on /login and /claim attempts.
	rate *rateLimiter

	// accountRate enforces a per-account limit on /login attempts so that
	// rotating source addresses does not bypass the brute-force guard.
	accountRate *rateLimiter

	// trustedProxies lists the networks whose X-Forwarded-For / X-Real-IP
	// headers are honored. Empty means the headers are never trusted.
	trustedProxies []*net.IPNet

	// bootstrapToken, when set, authorizes reveal and claim from any
	// address. Without it only direct loopback callers may use them.
	bootstrapToken string
}

// BootstrapTokenHeader carries the one-time bootstrap token that authorizes
// the break-glass reveal and claim endpoints from a non-loopback address.
const BootstrapTokenHeader = "X-Breakglass-Token"

// WithTrustedProxies declares the proxy networks (CIDR or single IP) whose
// forwarding headers are honored when deriving the client address. Entries
// that do not parse are ignored, which keeps the headers untrusted.
func WithTrustedProxies(cidrs ...string) HandlersOption {
	return func(h *Handlers) {
		for _, c := range cidrs {
			c = strings.TrimSpace(c)
			if !strings.Contains(c, "/") {
				if ip := net.ParseIP(c); ip != nil {
					if ip.To4() != nil {
						c += "/32"
					} else {
						c += "/128"
					}
				}
			}
			if _, n, err := net.ParseCIDR(c); err == nil {
				h.trustedProxies = append(h.trustedProxies, n)
			}
		}
	}
}

// WithBootstrapToken requires the given secret in [BootstrapTokenHeader] for
// break-glass reveal and claim requests that do not come from a direct
// loopback connection. The token must be delivered to the operator out of
// band (local file or process log), never over HTTP.
func WithBootstrapToken(token string) HandlersOption {
	return func(h *Handlers) { h.bootstrapToken = token }
}

// HandlersOption configures [NewHandlers].
type HandlersOption func(*Handlers)

// WithLoginRedirectPath overrides the default `auth_url` prefix returned to
// the frontend for OIDC providers.
func WithLoginRedirectPath(prefix string) HandlersOption {
	return func(h *Handlers) {
		if prefix != "" {
			h.loginRedirectPath = prefix
		}
	}
}

// NewHandlers constructs HTTP handlers around the [Service]. Pass an
// [oidcclient.Registry] (or nil) to advertise OIDC providers alongside the
// break-glass entry in the methods response.
func NewHandlers(svc *Service, registry *oidcclient.Registry, opts ...HandlersOption) *Handlers {
	h := &Handlers{
		svc:               svc,
		providers:         registry,
		loginRedirectPath: "/api/v1/auth/login?provider=",
		rate:              newRateLimiter(5, time.Minute), // 5 attempts/min/IP
		accountRate:       newRateLimiter(5, time.Minute), // 5 attempts/min/account
	}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	return h
}

// MethodsHandler returns a handler for GET /api/v1/auth/methods.
func (h *Handlers) MethodsHandler() http.Handler { return http.HandlerFunc(h.handleMethods) }

// LoginHandler returns a handler for POST /api/v1/auth/login.
func (h *Handlers) LoginHandler() http.Handler { return http.HandlerFunc(h.handleLogin) }

// LogoutHandler returns a handler for POST /api/v1/auth/logout.
func (h *Handlers) LogoutHandler() http.Handler { return http.HandlerFunc(h.handleLogout) }

// RevealHandler returns a handler for GET /api/v1/auth/breakglass/reveal.
func (h *Handlers) RevealHandler() http.Handler { return http.HandlerFunc(h.handleReveal) }

// ClaimHandler returns a handler for POST /api/v1/auth/breakglass/claim.
func (h *Handlers) ClaimHandler() http.Handler { return http.HandlerFunc(h.handleClaim) }

func (h *Handlers) handleMethods(w http.ResponseWriter, r *http.Request) {
	resp := MethodsResponse{Providers: []ProviderInfo{}}

	if h.providers != nil {
		ids := h.providers.IDs()
		sort.Strings(ids)
		for _, id := range ids {
			p, err := h.providers.Get(id)
			if err != nil {
				continue
			}
			resp.Providers = append(resp.Providers, ProviderInfo{
				ID:      p.ID(),
				Kind:    string(p.Kind()),
				Label:   labelForProvider(string(p.Kind()), p.ID()),
				AuthURL: h.loginRedirectPath + p.ID(),
			})
		}
	}

	status, err := h.svc.CurrentStatus(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "status_failed", err.Error())
		return
	}
	resp.BreakGlass = status

	if status.Initialized {
		resp.Providers = append(resp.Providers, ProviderInfo{
			ID:    DefaultProviderID,
			Kind:  "breakglass",
			Label: "Break-glass admin",
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	OK       bool   `json:"ok"`
	Email    string `json:"email"`
	Provider string `json:"provider"`
}

type claimResponse struct {
	OK       bool `json:"ok"`
	LoggedIn bool `json:"logged_in"`
}

func (h *Handlers) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if !h.rate.allow("ip:" + h.clientIP(r)) {
		writeJSONError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
		return
	}
	if !h.accountRate.allow("acct:" + strings.ToLower(strings.TrimSpace(req.Email))) {
		writeJSONError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return
	}
	claims, err := h.svc.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		if errors.Is(err, ErrInvalidCreds) {
			writeJSONError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "auth_failed", err.Error())
		return
	}
	token, err := h.svc.cfg.Sessions.Issue(claims)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "session_issue_failed", err.Error())
		return
	}
	authsession.SetSessionCookie(w, h.svc.CookieName(), token, h.svc.CookieSecure())
	writeJSON(w, http.StatusOK, loginResponse{OK: true, Email: claims.Email, Provider: claims.Provider})
}

func (h *Handlers) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	authsession.ClearSessionCookie(w, h.svc.CookieName(), h.svc.CookieSecure())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// bootstrapAllowed reports whether the caller may use the break-glass reveal
// and claim endpoints: a matching bootstrap token, or a direct loopback
// connection that did not pass through a forwarding proxy.
func (h *Handlers) bootstrapAllowed(r *http.Request) bool {
	if h.bootstrapToken != "" {
		got := r.Header.Get(BootstrapTokenHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(h.bootstrapToken)) == 1 {
			return true
		}
	}
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" || r.Header.Get("Forwarded") != "" {
		return false
	}
	ip := net.ParseIP(remoteHost(r))
	return ip != nil && ip.IsLoopback()
}

func (h *Handlers) handleReveal(w http.ResponseWriter, r *http.Request) {
	if !h.bootstrapAllowed(r) {
		writeJSONError(w, http.StatusForbidden, "bootstrap_forbidden", "break-glass bootstrap is restricted to the local host")
		return
	}
	rev, err := h.svc.Reveal(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			writeJSONError(w, http.StatusNotFound, "not_initialized", "break-glass not initialized")
		case errors.Is(err, ErrPasswordExpired):
			writeJSONError(w, http.StatusGone, "envelope_expired", "break-glass password no longer available")
		case errors.Is(err, ErrLockedByOperator):
			writeJSONError(w, http.StatusForbidden, "locked", err.Error())
		case errors.Is(err, ErrConcurrentUpdate):
			writeJSONError(w, http.StatusConflict, "bootstrap_changed", err.Error())
		default:
			writeJSONError(w, http.StatusInternalServerError, "reveal_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

type claimRequest struct {
	CurrentPassword string `json:"current_password"`
	NewEmail        string `json:"new_email"`
	NewPassword     string `json:"new_password"`
}

func (h *Handlers) handleClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if !h.bootstrapAllowed(r) {
		writeJSONError(w, http.StatusForbidden, "bootstrap_forbidden", "break-glass bootstrap is restricted to the local host")
		return
	}
	if !h.rate.allow("ip:" + h.clientIP(r)) {
		writeJSONError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts")
		return
	}
	var req claimRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
		return
	}
	err := h.svc.Claim(r.Context(), ClaimRequest(req))
	if err != nil {
		switch {
		case errors.Is(err, ErrAlreadyClaimed):
			writeJSONError(w, http.StatusConflict, "already_claimed", err.Error())
		case errors.Is(err, ErrConcurrentUpdate):
			writeJSONError(w, http.StatusConflict, "bootstrap_changed", err.Error())
		case errors.Is(err, ErrLockedByOperator):
			writeJSONError(w, http.StatusForbidden, "locked", err.Error())
		case errors.Is(err, ErrNotFound):
			writeJSONError(w, http.StatusNotFound, "not_initialized", err.Error())
		case errors.Is(err, ErrInvalidCreds):
			writeJSONError(w, http.StatusUnauthorized, "invalid_credentials", err.Error())
		default:
			writeJSONError(w, http.StatusInternalServerError, "claim_failed", err.Error())
		}
		return
	}
	// Auto-login the operator after a successful claim.
	loginEmail := req.NewEmail
	if loginEmail == "" {
		loginEmail = h.svc.cfg.BootstrapEmail
	}
	claims, err := h.svc.Authenticate(r.Context(), loginEmail, req.NewPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, claimResponse{OK: true})
		return
	}
	token, err := h.svc.cfg.Sessions.Issue(claims)
	if err != nil {
		writeJSON(w, http.StatusOK, claimResponse{OK: true})
		return
	}
	authsession.SetSessionCookie(w, h.svc.CookieName(), token, h.svc.CookieSecure())
	writeJSON(w, http.StatusCreated, claimResponse{OK: true, LoggedIn: true})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func labelForProvider(kind, id string) string {
	switch kind {
	case "auth0":
		return "kombify Cloud"
	case "pocketid":
		return "Pocket ID"
	case "pocketbase":
		return "PocketBase"
	default:
		if id == "" {
			return "Cloud account"
		}
		return id
	}
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (h *Handlers) isTrustedProxy(ip net.IP) bool {
	for _, n := range h.trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP returns the connecting address. Forwarding headers are honored
// only when the direct peer is a configured trusted proxy, and then the
// right-most hop that is not itself a trusted proxy wins.
func (h *Handlers) clientIP(r *http.Request) string {
	remote := remoteHost(r)
	peer := net.ParseIP(remote)
	if peer == nil || !h.isTrustedProxy(peer) {
		return remote
	}
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		hops := strings.Split(xf, ",")
		for i := len(hops) - 1; i >= 0; i-- {
			hop := strings.TrimSpace(hops[i])
			ip := net.ParseIP(hop)
			if ip == nil {
				return remote
			}
			if !h.isTrustedProxy(ip) {
				return hop
			}
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" && net.ParseIP(xr) != nil {
		return xr
	}
	return remote
}

// rateLimiter is a tiny in-memory token-bucket per key.
type rateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	bucket map[string][]time.Time
}

// rateLimiterMaxKeys bounds the bucket map so spoofed or random keys cannot
// grow memory without limit.
const rateLimiterMaxKeys = 10000

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, bucket: make(map[string][]time.Time)}
}

func (rl *rateLimiter) allow(key string) bool {
	if key == "" {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	if _, known := rl.bucket[key]; !known && len(rl.bucket) >= rateLimiterMaxKeys {
		rl.evictLocked(cutoff)
	}
	hits := rl.bucket[key]
	pruned := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	if len(pruned) >= rl.max {
		rl.bucket[key] = pruned
		return false
	}
	rl.bucket[key] = append(pruned, now)
	return true
}

// evictLocked drops idle keys; if the map is still full it drops the key with
// the oldest most-recent hit.
func (rl *rateLimiter) evictLocked(cutoff time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, hits := range rl.bucket {
		last := time.Time{}
		if len(hits) > 0 {
			last = hits[len(hits)-1]
		}
		if !last.After(cutoff) {
			delete(rl.bucket, k)
			continue
		}
		if oldestKey == "" || last.Before(oldest) {
			oldestKey, oldest = k, last
		}
	}
	if len(rl.bucket) >= rateLimiterMaxKeys && oldestKey != "" {
		delete(rl.bucket, oldestKey)
	}
}
