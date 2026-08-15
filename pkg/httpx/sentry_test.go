package httpx

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api"
	"github.com/getsentry/sentry-go"
)

func TestInternalErrorCapturesSentryEvent(t *testing.T) {
	transport := &recordingSentryTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.com/1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("new sentry client: %v", err)
	}
	previousClient := sentry.CurrentHub().Client()
	sentry.CurrentHub().BindClient(client)
	defer sentry.CurrentHub().BindClient(previousClient)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/stacks", nil)
	req.Header.Set("X-Request-ID", "req-123")
	e := &Event{Response: rec, Request: req}

	if err := InternalError(e, "database unavailable"); err != nil {
		t.Fatalf("InternalError() = %v", err)
	}
	if len(transport.events) != 1 {
		t.Fatalf("expected one sentry event, got %d", len(transport.events))
	}
	if !transport.flushed {
		t.Fatalf("expected sentry flush")
	}
	event := transport.events[0]
	if got := event.Tags["capture_source"]; got != "httpx_internal_error" {
		t.Fatalf("capture_source = %q", got)
	}
	if got := event.Tags["request_id"]; got != "req-123" {
		t.Fatalf("request_id = %q", got)
	}
	if len(event.Exception) == 0 || event.Exception[0].Value != "database unavailable" {
		t.Fatalf("unexpected exception payload: %+v", event.Exception)
	}
	if !SentryCaptured(e.Request.Context()) {
		t.Fatalf("expected request to be marked captured")
	}
}

func TestInternalErrorSkipsAlreadyCapturedRequest(t *testing.T) {
	transport := &recordingSentryTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.com/1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("new sentry client: %v", err)
	}
	previousClient := sentry.CurrentHub().Client()
	sentry.CurrentHub().BindClient(client)
	defer sentry.CurrentHub().BindClient(previousClient)

	req := httptest.NewRequest("GET", "/api/v1/stacks", nil)
	e := &Event{Response: httptest.NewRecorder(), Request: req}
	MarkSentryCaptured(e)

	if err := InternalError(e, "already reported"); err != nil {
		t.Fatalf("InternalError() = %v", err)
	}
	if len(transport.events) != 0 {
		t.Fatalf("expected no sentry event, got %d", len(transport.events))
	}
}

func TestErrorCapturesSentryEventForDirectServerEnvelope(t *testing.T) {
	transport := &recordingSentryTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.com/1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("new sentry client: %v", err)
	}
	previousClient := sentry.CurrentHub().Client()
	sentry.CurrentHub().BindClient(client)
	defer sentry.CurrentHub().BindClient(previousClient)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/stacks", nil)
	req.Header.Set("X-Request-ID", "req-create")
	e := &Event{Response: rec, Request: req}

	details := map[string]any{
		"provider": "ionos-managed",
		"stack_id": "stack-123",
		"api_key":  "ghp_1234567890123456789012345678901234567890",
	}
	if err := Error(e, 500, api.ErrCodeInternal, "provisioning failed", details); err != nil {
		t.Fatalf("Error() = %v", err)
	}
	if len(transport.events) != 1 {
		t.Fatalf("expected one sentry event, got %d", len(transport.events))
	}
	if !transport.flushed {
		t.Fatalf("expected sentry flush")
	}
	event := transport.events[0]
	if got := event.Tags["capture_source"]; got != "httpx_internal_error" {
		t.Fatalf("capture_source = %q", got)
	}
	if got := event.Tags["request_id"]; got != "req-create" {
		t.Fatalf("request_id = %q", got)
	}
	contextValue := fmt.Sprintf("%v", event.Contexts["httpx_error"])
	if !containsAll(contextValue, "ionos-managed", "stack-123", "[REDACTED]") {
		t.Fatalf("sentry context missing expected redacted details: %s", contextValue)
	}
	if containsAll(contextValue, "1234567890123456789012345678901234567890") {
		t.Fatalf("sentry context leaked token: %s", contextValue)
	}
	if !SentryCaptured(e.Request.Context()) {
		t.Fatalf("expected request to be marked captured")
	}
}

func TestErrorDoesNotCaptureClientEnvelope(t *testing.T) {
	transport := &recordingSentryTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.com/1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("new sentry client: %v", err)
	}
	previousClient := sentry.CurrentHub().Client()
	sentry.CurrentHub().BindClient(client)
	defer sentry.CurrentHub().BindClient(previousClient)

	e := &Event{
		Response: httptest.NewRecorder(),
		Request:  httptest.NewRequest("GET", "/api/v1/stacks", nil),
	}
	if err := Error(e, 400, api.ErrCodeBadRequest, "invalid payload", nil); err != nil {
		t.Fatalf("Error() = %v", err)
	}
	if len(transport.events) != 0 {
		t.Fatalf("expected no sentry event for 4xx, got %d", len(transport.events))
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}

type recordingSentryTransport struct {
	events  []*sentry.Event
	flushed bool
}

func (t *recordingSentryTransport) Configure(sentry.ClientOptions) {}

func (t *recordingSentryTransport) SendEvent(event *sentry.Event) {
	t.events = append(t.events, event)
}

func (t *recordingSentryTransport) Flush(time.Duration) bool {
	t.flushed = true
	return true
}

func (t *recordingSentryTransport) FlushWithContext(context.Context) bool {
	t.flushed = true
	return true
}

func (t *recordingSentryTransport) Close() {}
