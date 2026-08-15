package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/getsentry/sentry-go"
)

const sentryServerErrorFlushTimeout = 2 * time.Second
const sentryErrorDetailsMaxBytes = 16 * 1024

type sentryCapturedContextKey struct{}

// MarkSentryCaptured marks the request as already reported to Sentry. It is
// used by middleware and response helpers to avoid duplicate events when an
// error is first captured as a returned error and then rendered as a 500 JSON
// envelope.
func MarkSentryCaptured(e *Event) {
	if e == nil || e.Request == nil {
		return
	}
	e.Request = e.Request.WithContext(context.WithValue(e.Request.Context(), sentryCapturedContextKey{}, true))
}

// SentryCaptured reports whether this request already emitted a Sentry event.
func SentryCaptured(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	captured, _ := ctx.Value(sentryCapturedContextKey{}).(bool)
	return captured
}

func captureInternalServerError(e *Event, message string, details any) {
	if e == nil || e.Request == nil || SentryCaptured(e.Request.Context()) {
		return
	}
	hub := sentry.GetHubFromContext(e.Request.Context())
	if hub == nil {
		hub = sentry.CurrentHub()
	}
	if hub == nil || hub.Client() == nil {
		return
	}

	hub.WithScope(func(scope *sentry.Scope) {
		scope.SetRequest(e.Request)
		scope.SetTag("capture_source", "httpx_internal_error")
		scope.SetTag("route", e.Request.URL.Path)
		scope.SetTag("method", e.Request.Method)
		if requestID := e.Request.Header.Get("X-Request-ID"); requestID != "" {
			scope.SetTag("request_id", requestID)
		}
		scope.SetContext("httpx_error", sentry.Context{
			"message": message,
			"details": redactSentryDetails(details),
		})
		hub.CaptureException(errors.New(message))
	})
	MarkSentryCaptured(e)
	hub.Flush(sentryServerErrorFlushTimeout)
}

func redactSentryDetails(details any) any {
	if details == nil {
		return nil
	}
	var raw string
	switch v := details.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			raw = fmt.Sprintf("%v", v)
			break
		}
		raw = string(data)
	}
	clean := truncateSentryDetails(secrets.Redact(raw))
	if clean == "" {
		return ""
	}
	var structured any
	if json.Unmarshal([]byte(clean), &structured) == nil {
		return structured
	}
	return clean
}

func truncateSentryDetails(value string) string {
	if len(value) <= sentryErrorDetailsMaxBytes {
		return value
	}
	omitted := len(value) - sentryErrorDetailsMaxBytes
	return strings.TrimRight(value[:sentryErrorDetailsMaxBytes], "\x00\r\n\t ") +
		fmt.Sprintf("... [truncated %d bytes]", omitted)
}
