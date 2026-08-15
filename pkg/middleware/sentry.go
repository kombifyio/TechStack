package middleware

import (
	"fmt"
	"runtime/debug"
	"time"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/getsentry/sentry-go"
	"go.opentelemetry.io/otel/trace"
)

// SentryMiddleware installs a request-scoped hub for httpx routes and
// captures panics/errors when SENTRY_DSN is configured. With an empty DSN this
// stays a no-op adapter for self-hosted deployments.
func SentryMiddleware(e *httpx.Event) (err error) {
	hub := sentry.CurrentHub().Clone()
	e.Request = e.Request.WithContext(sentry.SetHubOnContext(e.Request.Context(), hub))
	configureSentryScope(hub, e)

	defer func() {
		if rec := recover(); rec != nil {
			configureSentryScope(hub, e)
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetContext("panic", sentry.Context{
					"value": fmt.Sprintf("%v", rec),
					"stack": string(debug.Stack()),
				})
				hub.RecoverWithContext(e.Request.Context(), rec)
			})
			httpx.MarkSentryCaptured(e)
			hub.Flush(sentryServerErrorFlushTimeout)
			panic(rec)
		}
	}()

	err = e.Next()
	if err != nil && httpx.IsServerError(err) {
		// Only report 5xx/unknown failures. Returned client errors (4xx:
		// validation, auth, not-found) are expected control flow, not bugs;
		// capturing them floods Sentry with noise (e.g. 404 router.ApiError,
		// KOMBIFY-TECHSTACK-1). The response envelope is still rendered by the
		// router regardless of capture.
		configureSentryScope(hub, e)
		hub.CaptureException(err)
		httpx.MarkSentryCaptured(e)
		hub.Flush(sentryServerErrorFlushTimeout)
	}
	return err
}

const sentryServerErrorFlushTimeout = 2 * time.Second

func configureSentryScope(hub *sentry.Hub, e *httpx.Event) {
	if hub == nil || e == nil || e.Request == nil {
		return
	}
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetRequest(e.Request)
		scope.SetTag("route", e.Request.URL.Path)
		scope.SetTag("method", e.Request.Method)
		if requestID := e.Request.Header.Get(requestIDHeader); requestID != "" {
			scope.SetTag("request_id", requestID)
		}
		if plan := UserPlanFromContext(e.Request.Context()); plan != "" {
			scope.SetTag("tier", plan)
		}
		if spanContext := trace.SpanContextFromContext(e.Request.Context()); spanContext.IsValid() {
			scope.SetTag("trace_id", spanContext.TraceID().String())
		}
		if id := identity.FromContext(e.Request.Context()); id != nil && id.IsAuthenticated() {
			scope.SetUser(sentry.User{ID: id.UserID, Username: id.UserID})
			if id.OrgID != "" {
				scope.SetTag("org_id", id.OrgID)
				scope.SetTag("tenant_id", id.OrgID)
			}
			if id.Tier != "" {
				scope.SetTag("tier", id.Tier)
			}
		}
	})
}
