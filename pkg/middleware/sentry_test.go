package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/getsentry/sentry-go"
)

func TestSentryMiddlewareReportsFailuresWithoutReportingRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler httpx.HandlerFunc
		status  int
		report  bool
	}{
		{
			name: "refusal",
			handler: func(e *httpx.Event) error {
				return httpx.RejectForbidden(e, "Access denied")
			},
			status: http.StatusForbidden,
		},
		{
			name: "wrapped refusal",
			handler: func(e *httpx.Event) error {
				return fmt.Errorf("authorize request: %w", httpx.RejectForbidden(e, "Access denied"))
			},
			status: http.StatusForbidden,
		},
		{
			name: "wrapped failure",
			handler: func(e *httpx.Event) error {
				return fmt.Errorf("read inventory: %w", errors.New("database unavailable"))
			},
			status: http.StatusInternalServerError,
			report: true,
		},
		{
			name: "rendered failure",
			handler: func(e *httpx.Event) error {
				return httpx.Reject(e, http.StatusInternalServerError, api.ErrCodeInternal, "database unavailable", nil)
			},
			status: http.StatusInternalServerError,
			report: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &sentryRequestTransport{}
			client, err := sentry.NewClient(sentry.ClientOptions{
				Dsn: "https://public@example.com/1", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			previousClient := sentry.CurrentHub().Client()
			sentry.CurrentHub().BindClient(client)
			t.Cleanup(func() { sentry.CurrentHub().BindClient(previousClient) })

			router := httpx.NewRouter()
			router.BindFunc(SentryMiddleware)
			router.GET("/inventory", tc.handler)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/inventory", nil))
			if response.Code != tc.status {
				t.Fatalf("HTTP status = %d, want %d", response.Code, tc.status)
			}
			if tc.report {
				if len(transport.events) != 1 || !transport.flushed {
					t.Fatalf("failure must be reported once and flushed: events=%d flushed=%t", len(transport.events), transport.flushed)
				}
			} else if len(transport.events) != 0 || transport.flushed {
				t.Fatalf("refusal must not be reported or flushed: events=%d flushed=%t", len(transport.events), transport.flushed)
			}
		})
	}
}

type sentryRequestTransport struct {
	events  []*sentry.Event
	flushed bool
}

func (*sentryRequestTransport) Configure(sentry.ClientOptions) {}
func (t *sentryRequestTransport) SendEvent(event *sentry.Event) {
	t.events = append(t.events, event)
}
func (t *sentryRequestTransport) Flush(time.Duration) bool {
	t.flushed = true
	return true
}
func (t *sentryRequestTransport) FlushWithContext(context.Context) bool {
	t.flushed = true
	return true
}
func (*sentryRequestTransport) Close() {}
