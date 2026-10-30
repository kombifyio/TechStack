// Package telemetry provides OpenTelemetry initialization for kombify services.
// It reads OTEL_* environment variables (standard OTel SDK config) and sets up
// trace exporters. Export is disabled only when neither OTLP endpoint is set.
package telemetry

import (
	"context"
	"os"
	"time"

	"github.com/kombifyio/techstack/pkg/fulllocal"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

// Init sets up OpenTelemetry tracing. Returns a shutdown function.
// The trace-specific endpoint takes precedence over the general endpoint.
// When neither is configured, local tracing stays active without an exporter.
func Init(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	endpoint, _, err := fulllocal.OTLPTraceEndpoint(os.Getenv, fullLocalOTelBuild || fulllocal.Active())
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, err
	}

	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if endpoint != "" {
		exporter, err := newOTLPExporter(ctx)
		if err != nil {
			return nil, err
		}
		options = append(options, sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(5*time.Second)))
	}
	tp := sdktrace.NewTracerProvider(options...)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}
