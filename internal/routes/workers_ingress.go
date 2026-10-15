package routes

import (
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Request-body bounds of the worker and agent routes. They apply before any
// decode or authentication work, so an unauthenticated peer cannot make the
// control plane buffer more than the route's bound. Every bound is the largest
// body a healthy Guard sends plus headroom.
const (
	workerPollBodyLimit      int64 = 256 << 10
	workerHeartbeatBodyLimit int64 = 1 << 20
	workerInventoryBodyLimit int64 = 4 << 20
	workerProviderBodyLimit  int64 = 5 << 20
	workerBootstrapBodyLimit int64 = 16 << 10
	workerPostureBodyLimit   int64 = guardposture.MaxReportBytes
	workerTelemetryBodyLimit int64 = guardtelemetry.MaxBatchBytes
	// workerSmallBodyLimit covers owner and download routes whose bodies are a
	// few fields.
	workerSmallBodyLimit int64 = 64 << 10
)

// workerBodyBound caps the request body before the handler runs. A declared
// length over the bound is refused without reading a byte; an undeclared or
// understated length is cut off by http.MaxBytesReader at the bound.
func workerBodyBound(limit int64) httpx.MiddlewareFunc {
	return func(e *httpx.Event) error {
		if e.Request.ContentLength > limit {
			return refuseOversizedBody(e)
		}
		if e.Request.Body != nil && e.Request.Body != http.NoBody {
			e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, limit)
		}
		return e.Next()
	}
}

func refuseOversizedBody(e *httpx.Event) error {
	// The connection cannot be reused after an unread oversized body.
	e.Response.Header().Set("Connection", "close")
	return httpx.Error(e, http.StatusRequestEntityTooLarge, ksapi.ErrCodeBadRequest, "request body exceeds size limit", nil)
}

// workerAgentIngress bounds the body and refuses a request that carries no
// usable credential before its body is read. A key-bound access token is also
// checked for its signature and lifetime, which is pure computation; the
// handler then performs the full authentication (state, proof, tenant).
func (h workerRouteHandlers) workerAgentIngress(limit int64) httpx.MiddlewareFunc {
	bound := workerBodyBound(limit)
	return func(e *httpx.Event) error {
		scheme, credentials := authorizationScheme(e.Request)
		switch {
		case credentials == "":
			return unauthenticatedWorkerRequest(e)
		case scheme == strings.ToLower(guardidentity.TokenType):
			if _, err := h.verifyGuardAccessToken(credentials, time.Now().UTC()); err != nil {
				denyGuardError(e, err)
				return nil
			}
		case scheme != "bearer":
			return unauthenticatedWorkerRequest(e)
		}
		return bound(e)
	}
}

func unauthenticatedWorkerRequest(e *httpx.Event) error {
	e.Response.Header().Set("Connection", "close")
	return httpx.Unauthorized(e, "Runtime agent credential required")
}

// requireKeyBoundWorker refuses work that changes state on behalf of a worker
// that authenticated with its bearer credential alone. A legacy worker may
// heartbeat, report inventory and posture, update itself and bind a key, but a
// copied credential must never drive commands, provider control or managed
// operations; the structured denial tells the owner how to upgrade.
func requireKeyBoundWorker(e *httpx.Event, authCtx runtimeAgentAuthContext) bool {
	if authCtx.IdentityBinding == controlplane.IdentityBindingHostKey {
		return true
	}
	denyGuard(e, guardidentity.CodeIdentityUpgradeRequired, "")
	return false
}
