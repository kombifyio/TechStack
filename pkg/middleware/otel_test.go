package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestOTelMiddlewareClassifiesHTTPOutcomes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	for _, tc := range []struct {
		name    string
		handler httpx.HandlerFunc
		status  int
		span    codes.Code
	}{
		{"validation stops the chain", func(e *httpx.Event) error {
			return httpx.Reject(e, http.StatusBadRequest, api.ErrCodeBadRequest, "invalid inventory filter", nil)
		}, http.StatusBadRequest, codes.Unset},
		{"returned not found", func(e *httpx.Event) error {
			return httpx.NewNotFoundError("inventory item missing", nil)
		}, http.StatusNotFound, codes.Unset},
		{"returned server failure", func(e *httpx.Event) error {
			return errors.New("inventory unavailable")
		}, http.StatusInternalServerError, codes.Error},
		{"wrapped error follows router rendering", func(e *httpx.Event) error {
			return fmt.Errorf("read inventory: %w", httpx.NewNotFoundError("inventory item missing", nil))
		}, http.StatusInternalServerError, codes.Error},
		{"rendered server failure", func(e *httpx.Event) error {
			return httpx.Reject(e, http.StatusServiceUnavailable, api.ErrCodeInternal, "inventory unavailable", nil)
		}, http.StatusServiceUnavailable, codes.Error},
		{"direct server failure", func(e *httpx.Event) error {
			http.Error(e.Response, "inventory unavailable", http.StatusServiceUnavailable)
			return nil
		}, http.StatusServiceUnavailable, codes.Error},
		{"streamed response already committed", func(e *httpx.Event) error {
			if err := http.NewResponseController(e.Response).Flush(); err != nil {
				return err
			}
			e.MarkResponseStreamed()
			return httpx.ErrResponseWritten
		}, http.StatusOK, codes.Unset},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter.Reset()
			router := httpx.NewRouter()
			router.BindFunc(middleware.OTelMiddleware)
			router.GET("/inventory", func(e *httpx.Event) error {
				e.Response.Header().Set("X-Handler-Reached", "true")
				return httpx.Success(e, http.StatusOK, nil)
			}).BindFunc(httpx.MiddlewareFunc(tc.handler))
			server := httptest.NewServer(router)
			defer server.Close()
			client := server.Client()
			client.Timeout = 5 * time.Second
			response, err := client.Get(server.URL + "/inventory")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("HTTP status = %d, want %d", response.StatusCode, tc.status)
			}
			if tc.status == http.StatusBadRequest {
				decoder := json.NewDecoder(response.Body)
				var envelope api.ErrorResponse
				if err := decoder.Decode(&envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Error.Code != api.ErrCodeBadRequest || response.Header.Get("X-Handler-Reached") != "" {
					t.Fatal("validation must preserve its error envelope and stop the final handler")
				}
				if err := decoder.Decode(&envelope); err != io.EOF {
					t.Fatalf("validation appended another response: %v", err)
				}
			} else if _, err := io.Copy(io.Discard, response.Body); err != nil {
				t.Fatal(err)
			}
			// Draining the real response lets the synchronous exporter complete,
			// including a response whose headers were flushed before the span ended.
			if err := provider.ForceFlush(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, span := range exporter.GetSpans() {
				if span.Status.Code != tc.span {
					t.Fatalf("HTTP %d exported span status = %v, want %v", response.StatusCode, span.Status.Code, tc.span)
				}
				return
			}
			t.Fatal("HTTP request did not export its server span")
		})
	}
}
