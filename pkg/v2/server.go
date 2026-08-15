package v2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/v2/auth/session"
	"github.com/kombifyio/techstack/pkg/v2/authmw"
)

// Server is the HTTP surface of the TechStack V2 core. At Phase 1 it serves
// /api/v2/health and reports the wired DB backend status. Phase 2 adds an
// optional authenticated /api/v2/whoami endpoint when a session manager is
// attached.
//
// Server is safe to construct without a DB or session manager; missing
// dependencies simply hide the dependent endpoints.
type Server struct {
	startedAt time.Time
	db        *db.DB
	sessions  *session.Manager
	auth      AuthHandlers
	cookie    string
}

// AuthHandlers groups optional V2 auth endpoints.
type AuthHandlers struct {
	Providers http.Handler
	Login     http.Handler
	Callback  http.Handler
	Logout    http.Handler

	// Local credential surface (V1-shaped routes for frontend parity with
	// kombify-Simulate). Optional; nil disables the local provider entirely.
	Methods          http.Handler
	LocalLogin       http.Handler
	LocalLogout      http.Handler
	BreakGlassClaim  http.Handler
	BreakGlassReveal http.Handler
}

// Option configures a Server.
type Option func(*Server)

// WithDB attaches a *db.DB handle so the health endpoint can report
// connectivity and the V2 services can use Postgres.
func WithDB(d *db.DB) Option {
	return func(s *Server) { s.db = d }
}

// WithSession attaches a session manager so authenticated routes
// (/api/v2/whoami and friends) become available.
func WithSession(m *session.Manager) Option {
	return func(s *Server) { s.sessions = m }
}

// WithSessionCookieName overrides the default cookie name used by authenticated
// browser routes like /api/v2/whoami.
func WithSessionCookieName(name string) Option {
	return func(s *Server) {
		if strings.TrimSpace(name) != "" {
			s.cookie = strings.TrimSpace(name)
		}
	}
}

// WithAuthHandlers wires optional auth endpoints under /api/v2/auth/*.
func WithAuthHandlers(h AuthHandlers) Option {
	return func(s *Server) { s.auth = h }
}

// MergeAuthHandlers updates the server's auth handlers in-place. Non-nil
// fields in h replace the corresponding existing handlers; nil fields are
// preserved. This lets late-binding setup (e.g. PocketBase OnServe hooks)
// add local-credential handlers after the server is constructed.
func (s *Server) MergeAuthHandlers(h AuthHandlers) {
	if h.Providers != nil {
		s.auth.Providers = h.Providers
	}
	if h.Login != nil {
		s.auth.Login = h.Login
	}
	if h.Callback != nil {
		s.auth.Callback = h.Callback
	}
	if h.Logout != nil {
		s.auth.Logout = h.Logout
	}
	if h.Methods != nil {
		s.auth.Methods = h.Methods
	}
	if h.LocalLogin != nil {
		s.auth.LocalLogin = h.LocalLogin
	}
	if h.LocalLogout != nil {
		s.auth.LocalLogout = h.LocalLogout
	}
	if h.BreakGlassClaim != nil {
		s.auth.BreakGlassClaim = h.BreakGlassClaim
	}
	if h.BreakGlassReveal != nil {
		s.auth.BreakGlassReveal = h.BreakGlassReveal
	}
}

// NewServer constructs a V2 HTTP server skeleton.
func NewServer(opts ...Option) *Server {
	s := &Server{startedAt: time.Now().UTC(), cookie: authmw.DefaultSessionCookieName}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Routes returns an http.Handler exposing the V2 surface under /api/v2/*.
//
// At Phase 1 the only route is GET /api/v2/health. Phase 2 adds
// GET /api/v2/whoami behind the bearer-token middleware when a session
// manager is attached. Response keys are part of the public contract;
// downstream consumers (CI smoke tests, uptime monitors, the strangler
// legacy-shim) depend on them.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/health", s.handleHealth)
	if s.sessions != nil {
		mw := authmw.New(s.sessions, authmw.WithCookieName(s.cookie))
		mux.Handle("GET /api/v2/whoami", mw.Wrap(http.HandlerFunc(s.handleWhoAmI)))
	}
	if s.auth.Providers != nil {
		mux.Handle("GET /api/v2/auth/providers", s.auth.Providers)
	}
	if s.auth.Login != nil {
		mux.Handle("GET /api/v2/auth/login", s.auth.Login)
	}
	if s.auth.Callback != nil {
		mux.Handle("GET /api/v2/auth/callback", s.auth.Callback)
	}
	if s.auth.Logout != nil {
		mux.Handle("GET /api/v2/auth/logout", s.auth.Logout)
	}
	if s.auth.Methods != nil {
		mux.Handle("GET /api/v1/auth/methods", s.auth.Methods)
	}
	if s.auth.LocalLogin != nil {
		mux.Handle("POST /api/v1/auth/login", s.auth.LocalLogin)
	}
	if s.auth.LocalLogout != nil {
		mux.Handle("POST /api/v1/auth/logout", s.auth.LocalLogout)
	}
	if s.auth.BreakGlassClaim != nil {
		mux.Handle("POST /api/v1/auth/breakglass/claim", s.auth.BreakGlassClaim)
	}
	if s.auth.BreakGlassReveal != nil {
		mux.Handle("GET /api/v1/auth/breakglass/reveal", s.auth.BreakGlassReveal)
	}
	return mux
}

// WhoAmIResponse is the JSON body returned by /api/v2/whoami.
type WhoAmIResponse struct {
	Subject  string `json:"subject"`
	TenantID string `json:"tenantId"`
	OrgID    string `json:"orgId,omitempty"`
	Email    string `json:"email,omitempty"`
	Provider string `json:"provider,omitempty"`
	Role     string `json:"role,omitempty"`
}

func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	c, err := authmw.ClaimsFrom(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	resp := WhoAmIResponse{
		Subject:  c.Subject,
		TenantID: c.TenantID,
		OrgID:    c.OrgID,
		Email:    c.Email,
		Provider: c.Provider,
		Role:     c.Role,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// HealthDBStatus reports the DB-side health snapshot.
type HealthDBStatus struct {
	Status  string `json:"status"`            // "ok" | "error" | "disabled"
	Backend string `json:"backend,omitempty"` // postgres | pocketbase | sqlite
	Error   string `json:"error,omitempty"`
}

// HealthResponse is the JSON body returned by /api/v2/health.
type HealthResponse struct {
	Status         string         `json:"status"`
	Version        string         `json:"version"`
	UptimeSeconds  int64          `json:"uptimeSeconds"`
	StartedAtRFC33 string         `json:"startedAt"`
	DB             HealthDBStatus `json:"db"`
}

func (s *Server) checkDB(ctx context.Context) HealthDBStatus {
	if s.db == nil || s.db.DB == nil {
		return HealthDBStatus{Status: "disabled"}
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(pingCtx); err != nil {
		return HealthDBStatus{
			Status:  "error",
			Backend: string(s.db.Backend()),
			Error:   err.Error(),
		}
	}
	return HealthDBStatus{Status: "ok", Backend: string(s.db.Backend())}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbStatus := s.checkDB(r.Context())
	overall := "ok"
	if dbStatus.Status == "error" {
		overall = "degraded"
	}
	resp := HealthResponse{
		Status:         overall,
		Version:        Version,
		UptimeSeconds:  int64(time.Since(s.startedAt).Seconds()),
		StartedAtRFC33: s.startedAt.Format(time.RFC3339),
		DB:             dbStatus,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if overall != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(resp)
}
