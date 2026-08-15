// OTel middleware for httpx request tracing.
package middleware

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/kombifyio/techstack/pkg/httpx"
)

var tracer = otel.Tracer("kombify-techstack")

// OTelMiddleware extracts trace context from incoming headers and creates
// a span for each request. No-op when no TracerProvider is configured.
func OTelMiddleware(e *httpx.Event) error {
	r := e.Request
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

	spanName := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
	ctx, span := tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			semconv.HTTPMethod(r.Method),
			semconv.HTTPTarget(r.URL.Path),
			attribute.String("http.host", r.Host),
		),
	)
	defer span.End()

	e.Request = r.WithContext(ctx)

	err := e.Next()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}

	return err
}
