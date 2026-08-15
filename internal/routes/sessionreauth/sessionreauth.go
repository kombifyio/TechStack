// Package sessionreauth is the single response classifier for the
// "signature-valid session whose identity/tenant projection cannot resolve"
// class (owner rule 2026-08-12: tools recover sessions themselves, users are
// never asked to re-login manually).
//
// When server-side re-projection (claims re-derivation, owner-tenant
// fallback, owner/demo bootstrap) cannot recover the session, every surface
// answers with ONE signal: 401 + reason_code=session_reprojection_required +
// retryable=true + user_guidance (FEATURE-ENTITLEMENT-UX-STANDARD envelope)
// and clears the v2 session cookie so the client's central interceptor can
// silently re-enter SSO. No route may hand-roll its own dead-end 500/403 for
// this class.
package sessionreauth

import (
	"strings"
	"sync"

	"github.com/kombifyio/go-common/authsession"
	"github.com/getsentry/sentry-go"

	"github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
)

// ReasonCode is the wire signal for the session-recovery class. The client
// fetch-core interceptor keys its silent re-auth flow off this exact value.
const ReasonCode = "session_reprojection_required"

// RecoveredMessage is the structured-log message emitted when server-side
// re-projection repaired the session without any client involvement.
const RecoveredMessage = "session_reprojection_recovered"

var state struct {
	mu         sync.RWMutex
	cookieName string
	secure     bool
	counter    func(tenantID, outcome string)
}

// Configure sets the v2 session cookie parameters once at route wiring so
// Denial can clear the cookie with the same attributes the login flow set.
func Configure(cookieName string, secure bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.cookieName = cookieName
	state.secure = secure
}

// SetCounterHook installs the metrics sink (see routes.RegisterMetricsRoutes).
// The hook receives the tenant label and the outcome ("recovered" or
// "reauth_required") for techstack_auth_session_reprojections_total.
func SetCounterHook(hook func(tenantID, outcome string)) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.counter = hook
}

func snapshot() (string, bool, func(tenantID, outcome string)) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.cookieName, state.secure, state.counter
}

// RecordRecovered emits the telemetry for an occurrence that server-side
// re-projection repaired in place: structured log, tenant-labeled counter,
// and an info-level Sentry event so incident rate spikes stay visible.
func RecordRecovered(e *httpx.Event, fromTenant, toTenant, rung string) {
	_, _, counter := snapshot()
	logger.Get().Warn(RecoveredMessage,
		"reason_code", ReasonCode,
		"from_tenant_id", fromTenant,
		"to_tenant_id", toTenant,
		"recovery_rung", rung,
		"path", requestPath(e),
	)
	if counter != nil {
		counter(tenantLabel(fromTenant), "recovered")
	}
	captureSentry(e, sentry.LevelInfo, RecoveredMessage, map[string]string{
		"reason_code":    ReasonCode,
		"from_tenant_id": tenantLabel(fromTenant),
		"to_tenant_id":   tenantLabel(toTenant),
		"recovery_rung":  rung,
	})
}

// Denial is the terminal classifier response: the session is signature-valid
// but unrecoverable server-side. It clears the v2 session cookie, emits the
// structured log + tenant-labeled counter + warning-level Sentry event, and
// returns the retryable 401 envelope the central client interceptor consumes.
func Denial(e *httpx.Event, tenantID string, cause error) *httpx.APIError {
	cookieName, secure, counter := snapshot()
	if e != nil && e.Response != nil && cookieName != "" {
		authsession.ClearSessionCookie(e.Response, cookieName, secure)
	}
	causeText := ""
	if cause != nil {
		causeText = cause.Error()
	}
	logger.Get().Warn(ReasonCode,
		"reason_code", ReasonCode,
		"tenant_id", tenantID,
		"path", requestPath(e),
		"error", causeText,
	)
	if counter != nil {
		counter(tenantLabel(tenantID), "reauth_required")
	}
	captureSentry(e, sentry.LevelWarning, ReasonCode, map[string]string{
		"reason_code": ReasonCode,
		"tenant_id":   tenantLabel(tenantID),
	})
	return httpx.NewAPIError(401, api.ErrCodeUnauthorized, "Session requires re-authentication", map[string]any{
		"error_code":  ReasonCode,
		"reason_code": ReasonCode,
		"retryable":   true,
		"user_guidance": map[string]any{
			"title": "Session refresh required",
			"body":  "Your session could not be re-linked to its workspace, likely after a platform update. TechStack renews it automatically; no data was lost.",
			"next_steps": []string{
				"Wait a moment - the session renews automatically",
				"If it does not recover, use the renew-session action to sign in again",
			},
		},
		"remediation":     "The client must re-enter SSO (/auth/sso or /api/v2/auth/login) to re-mint the session from the upstream identity; the projection is rebuilt at login time.",
		"support_context": map[string]any{"feature_source": "v2-session/tenant-projection", "cost_bearing": false},
	})
}

func requestPath(e *httpx.Event) string {
	if e == nil || e.Request == nil || e.Request.URL == nil {
		return ""
	}
	return e.Request.URL.Path
}

func tenantLabel(tenantID string) string {
	if tenantID = strings.TrimSpace(tenantID); tenantID != "" {
		return tenantID
	}
	return "unknown"
}

func captureSentry(e *httpx.Event, level sentry.Level, message string, tags map[string]string) {
	hub := sentry.CurrentHub()
	if e != nil && e.Request != nil {
		if ctxHub := sentry.GetHubFromContext(e.Request.Context()); ctxHub != nil {
			hub = ctxHub
		}
	}
	if hub == nil || hub.Client() == nil {
		return // self-hosted no-op path (empty SENTRY_DSN)
	}
	hub.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(level)
		for key, value := range tags {
			scope.SetTag(key, value)
		}
		hub.CaptureMessage(message)
	})
}
