package httpx

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/api"
	"github.com/getsentry/sentry-go"
)

type sqlStateError string

func (e sqlStateError) Error() string {
	return "ERROR: terminating connection (SQLSTATE " + string(e) + ")"
}
func (e sqlStateError) SQLState() string { return string(e) }

func TestDependencyFailureClass(t *testing.T) {
	cases := map[string]error{
		"canceled":          fmt.Errorf("query: %w", context.Canceled),
		"deadline_exceeded": fmt.Errorf("query: %w", context.DeadlineExceeded),
		"sqlstate_57p01":    fmt.Errorf("query: %w", sqlStateError("57P01")),
		"sqlstate_53300":    sqlStateError("53300"),
		"connection":        fmt.Errorf("begin: %w", driver.ErrBadConn),
		"other":             errors.New("scan: unexpected column"),
	}
	for want, err := range cases {
		if got := DependencyFailureClass(err); got != want {
			t.Errorf("DependencyFailureClass(%v) = %q, want %q", err, got, want)
		}
	}
}

func newCaptureHub(t *testing.T) (*recordingSentryTransport, func()) {
	t.Helper()
	transport := &recordingSentryTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{Dsn: "https://public@example.invalid/1", Transport: transport})
	if err != nil {
		t.Fatalf("new sentry client: %v", err)
	}
	previous := sentry.CurrentHub().Client()
	sentry.CurrentHub().BindClient(client)
	return transport, func() { sentry.CurrentHub().BindClient(previous) }
}

func TestUnavailableReportNamesTheRecordedCause(t *testing.T) {
	transport, restore := newCaptureHub(t)
	defer restore()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	e := &Event{Response: httptest.NewRecorder(), Request: req}

	RecordDependencyFailure(e, "services.list.runtimes", fmt.Errorf("begin: %w", sqlStateError("57P01")))
	if err := Error(e, http.StatusServiceUnavailable, api.ErrCodeUnavailable, "Service inventory is unavailable", nil); err != nil {
		t.Fatalf("Error() = %v", err)
	}
	if len(transport.events) != 1 {
		t.Fatalf("expected one sentry event, got %d", len(transport.events))
	}
	event := transport.events[0]
	if event.Tags["cause_class"] != "sqlstate_57p01" || event.Tags["dependency_operation"] != "services.list.runtimes" {
		t.Fatalf("tags = %v", event.Tags)
	}
}

func TestUnavailableAnswerToAGoneClientIsNotReported(t *testing.T) {
	transport, restore := newCaptureHub(t)
	defer restore()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e := &Event{Response: rec, Request: req}
	cancel()

	RecordDependencyFailure(e, "services.list.runtimes", context.Canceled)
	if err := Error(e, http.StatusServiceUnavailable, api.ErrCodeUnavailable, "Service inventory is unavailable", nil); err != nil {
		t.Fatalf("Error() = %v", err)
	}
	if len(transport.events) != 0 {
		t.Fatalf("a departed client produced %d sentry events", len(transport.events))
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("the unavailable answer must stay truthful, got %d", rec.Code)
	}
}
