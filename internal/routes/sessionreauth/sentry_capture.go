//go:build !kombify_full_local

package sessionreauth

import (
	"github.com/getsentry/sentry-go"

	"github.com/kombifyio/techstack/pkg/httpx"
)

func captureSentry(e *httpx.Event, level string, message string, tags map[string]string) {
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
		scope.SetLevel(sentry.Level(level))
		for key, value := range tags {
			scope.SetTag(key, value)
		}
		hub.CaptureMessage(message)
	})
}
