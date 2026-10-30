// OTel middleware for httpx request tracing.
package middleware

import (
	"fmt"
	"io"
	"net/http"

	"github.com/felixge/httpsnoop"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
)

// OTelMiddleware extracts trace context from incoming headers and creates
// a span for each request. No-op when no TracerProvider is configured.
func OTelMiddleware(e *httpx.Event) error {
	r := e.Request
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

	spanName := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
	ctx, span := otel.Tracer("kombify-techstack").Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			semconv.HTTPMethod(r.Method),
			semconv.HTTPTarget(r.URL.Path),
			attribute.String("http.host", r.Host),
		),
	)
	defer span.End()

	e.Request = r.WithContext(ctx)
	// Keep request correlation on the local structured logger independently
	// of export configuration. Only validated OTel identifiers and flags enter
	// this record, never request headers, paths, payloads or identity data.
	if sc := span.SpanContext(); sc.IsValid() {
		logger.Get().InfoContext(ctx, "http_trace_context",
			"trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String(),
			"trace_sampled", sc.IsSampled(), "span_remote", sc.IsRemote(),
			"span_recording", span.IsRecording(),
		)
	}
	// Observe committed responses without removing streaming interfaces from
	// the writer. Informational headers do not commit the final status.
	status := 0
	recordStatus := func(code int) {
		if status == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
			status = code
		}
	}
	response := e.Response
	e.Response = httpsnoop.Wrap(response, httpsnoop.Hooks{
		WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(code int) {
				next(code)
				recordStatus(code)
			}
		},
		Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(body []byte) (int, error) {
				recordStatus(http.StatusOK)
				return next(body)
			}
		},
		ReadFrom: func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
			return func(reader io.Reader) (int64, error) {
				n, err := next(reader)
				if n > 0 {
					recordStatus(http.StatusOK)
				}
				return n, err
			}
		},
		Flush: func(next httpsnoop.FlushFunc) httpsnoop.FlushFunc {
			return func() {
				recordStatus(http.StatusOK)
				next()
			}
		},
		FlushError: func(next httpsnoop.FlushErrorFunc) httpsnoop.FlushErrorFunc {
			return func() error {
				recordStatus(http.StatusOK)
				return next()
			}
		},
	})
	defer func() { e.Response = response }()

	err := e.Next()
	if status == 0 {
		// The router renders returned errors after middleware unwinds.
		// Its structured response branch accepts only a direct APIError.
		if _, ok := err.(*httpx.APIError); ok {
			status = httpx.StatusOf(err)
		} else if err != nil {
			status = http.StatusInternalServerError
		}
	}
	if status >= http.StatusInternalServerError {
		span.SetStatus(codes.Error, "")
	}

	return err
}
