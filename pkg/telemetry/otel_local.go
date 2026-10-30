package telemetry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/dataprofile"
	"github.com/kombifyio/techstack/pkg/fulllocal"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func newOTLPExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	var options []otlptracehttp.Option
	var client *http.Client
	if fullLocalOTelBuild || fulllocal.Active() {
		var err error
		client, err = localOTLPClient()
		if err != nil {
			return nil, err
		}
		options = append(options, otlptracehttp.WithHTTPClient(client))
	}
	// Leave endpoint precedence, /v1/traces, headers, compression and HTTP
	// encoding to the same official SDK in both profiles.
	exporter, err := otlptracehttp.New(ctx, options...)
	if client == nil {
		return exporter, err
	}
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	return &localHTTPExporter{SpanExporter: exporter, client: client}, nil
}

type localHTTPExporter struct {
	sdktrace.SpanExporter
	client *http.Client
}

func (e *localHTTPExporter) Shutdown(ctx context.Context) error {
	defer e.client.CloseIdleConnections()
	return e.SpanExporter.Shutdown(ctx)
}

func localOTLPClient() (*http.Client, error) {
	// Init admitted the effective endpoint through the shared boot/config seam.
	switch otlpEnv("PROTOCOL") {
	case "", "http/protobuf", "http/json":
	default:
		return nil, errors.New("invalid Full Local OTLP protocol: HTTP transport required")
	}
	timeout := 10 * time.Second // OTel HTTP SDK default, milliseconds in env.
	if raw := otlpEnv("TIMEOUT"); raw != "" {
		milliseconds, err := strconv.Atoi(raw)
		if err != nil {
			return nil, errors.New("invalid Full Local OTLP timeout")
		}
		timeout = time.Duration(milliseconds) * time.Millisecond
	}
	tlsConfig := localOTLPTLS()
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second,
			Resolver: dataprofile.LocalResolver()}).DialContext,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	dataprofile.GuardTransport(transport)
	return &http.Client{Transport: transport, Timeout: timeout,
		// Keep trace bodies and collector credentials at the configured sink.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func otlpEnv(name string) string {
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_" + name)); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_" + name))
}

// WithHTTPClient replaces the SDK's TLS transport. Mirror the pinned SDK's
// ordered application of valid CA pools and complete client pairs: an incomplete
// or unloadable override keeps earlier valid material for the same collector.
// The SDK constructor parses these variables too and reports loading failures.
func localOTLPTLS() *tls.Config {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	for _, prefix := range []string{"OTEL_EXPORTER_OTLP_", "OTEL_EXPORTER_OTLP_TRACES_"} {
		if file := strings.TrimSpace(os.Getenv(prefix + "CERTIFICATE")); file != "" {
			if pem, err := os.ReadFile(file); err == nil {
				pool := x509.NewCertPool()
				if pool.AppendCertsFromPEM(pem) {
					config.RootCAs = pool
				}
			}
		}
		certificate := strings.TrimSpace(os.Getenv(prefix + "CLIENT_CERTIFICATE"))
		key := strings.TrimSpace(os.Getenv(prefix + "CLIENT_KEY"))
		if certificate == "" || key == "" {
			continue
		}
		if pair, err := tls.LoadX509KeyPair(certificate, key); err == nil {
			config.Certificates = []tls.Certificate{pair}
		}
	}
	return config
}
