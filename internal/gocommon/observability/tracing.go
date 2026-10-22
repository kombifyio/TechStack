package observability

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TraceHTTP continues W3C trace context and records an HTTP server span.
// A nil tracer uses the process OpenTelemetry provider, which is a no-op until
// configured by the consumer. It can be composed with Middleware for Sentry.
//
// Only a bounded method is recorded. URLs, paths, headers, bodies, identity,
// baggage and error text never become span attributes or events here. The
// original ResponseWriter is passed through, preserving streaming and upgrades.
func TraceHTTP(tracer trace.Tracer, next http.Handler) http.Handler {
	tracer = httpTracer(tracer)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		method := traceMethod(r.Method)
		ctx, span := tracer.Start(ctx, "HTTP "+method, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", method)))
		// SDK End directly deferred during a panic records its raw value. Call
		// it indirectly so only the fixed status below enters telemetry.
		defer func() { span.End() }()
		defer func() {
			if value := recover(); value != nil {
				span.SetStatus(codes.Error, "handler_panic")
				panic(value)
			}
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TraceTransport records HTTP client spans and injects W3C trace context into
// a cloned request. Use it on the http.Client passed to servicecall.WithHTTPClient.
// A nil base uses http.DefaultTransport. Requests and responses retain their
// original bodies, cancellation and streaming/upgrade behavior.
//
// Client spans measure time to response headers, including transport failure.
// Only method and response status are recorded; transport errors use a fixed
// status description because error messages can contain credentials and URLs.
// Neither baggage nor request metadata is added to telemetry by these helpers.
func TraceTransport(tracer trace.Tracer, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &tracingTransport{tracer: httpTracer(tracer), base: base}
}

type tracingTransport struct {
	tracer trace.Tracer
	base   http.RoundTripper
}

func (t *tracingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	method := traceMethod(r.Method)
	ctx, span := t.tracer.Start(r.Context(), "HTTP "+method, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("http.request.method", method)))
	// Keep SDK panic recording out of End, as in TraceHTTP.
	defer func() { span.End() }()
	defer func() {
		if value := recover(); value != nil {
			span.SetStatus(codes.Error, "transport_panic")
			panic(value)
		}
	}()
	request := r.Clone(ctx)
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(request.Header))
	response, err := t.base.RoundTrip(request)
	if err != nil {
		span.SetStatus(codes.Error, "transport_error")
	} else if response != nil {
		span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
		if response.StatusCode >= http.StatusBadRequest {
			span.SetStatus(codes.Error, "http_error")
		}
	}
	return response, err
}

func httpTracer(tracer trace.Tracer) trace.Tracer {
	if tracer == nil {
		return otel.Tracer("github.com/kombifyio/techstack/internal/gocommon/observability")
	}
	return tracer
}

func traceMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace, http.MethodPatch:
		return method
	default:
		return "_OTHER"
	}
}
