package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// VerifiedEdgeEnvelope carries the original signed Gateway request. It is not
// an identity or a reconstructed decision. Receivers must rerun the canonical
// middleware; decoding this wire object grants nothing.
type VerifiedEdgeEnvelope struct {
	Method     string      `json:"method"`
	RequestURI string      `json:"request_uri"`
	Header     http.Header `json:"header"`
}
type verifiedEdgeEnvelopeKey struct{}

func cloneEdgeEnvelope(r *http.Request) *VerifiedEdgeEnvelope {
	header := make(http.Header)
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		if (strings.HasPrefix(lower, "x-kombify-") && lower != "x-kombify-service-auth") || strings.HasPrefix(lower, "x-user-") || lower == "x-org-id" || lower == "x-request-id" {
			header[name] = append([]string{}, values...)
		}
	}
	return &VerifiedEdgeEnvelope{Method: r.Method, RequestURI: r.URL.RequestURI(), Header: header}
}

// VerifiedEdgeEnvelopeFromContext returns a detached copy only from the
// successful signed branch of EdgeIdentityMiddlewareWithConfig.
func VerifiedEdgeEnvelopeFromContext(ctx context.Context) *VerifiedEdgeEnvelope {
	if ctx == nil {
		return nil
	}
	envelope, _ := ctx.Value(verifiedEdgeEnvelopeKey{}).(*VerifiedEdgeEnvelope)
	if envelope == nil {
		return nil
	}
	out := *envelope
	out.Header = envelope.Header.Clone()
	return &out
}

// ReverifyEdgeEnvelope invokes the existing middleware, including the common
// identity nonce guard, decision sealing, signed entitlements and profile.
// Neither service authentication nor legacy shared-secret trust can enter it.
func ReverifyEdgeEnvelope(envelope *VerifiedEdgeEnvelope, cfg *config.Config) (context.Context, error) {
	if envelope == nil || cfg == nil || !cfg.DeploymentMode.IsSaaS() || !strings.HasPrefix(envelope.RequestURI, "/") || strings.HasPrefix(envelope.RequestURI, "//") {
		return nil, errors.New("signed Gateway envelope required")
	}
	r, err := http.NewRequestWithContext(context.Background(), envelope.Method, "https://gateway.invalid"+envelope.RequestURI, nil)
	if err != nil {
		return nil, err
	}
	r.Header = envelope.Header.Clone()
	// Reapply the same positive header projection even for an untrusted wire.
	r.Header = cloneEdgeEnvelope(r).Header
	e := &httpx.Event{Request: r, Response: httptest.NewRecorder()}
	if err := EdgeIdentityMiddlewareFromConfig(cfg)(e); err != nil {
		return nil, err
	}
	if !IsEdgeAuthenticated(e.Request.Context()) || VerifiedEdgeEnvelopeFromContext(e.Request.Context()) == nil {
		return nil, errors.New("Gateway envelope rejected")
	}
	return e.Request.Context(), nil
}
