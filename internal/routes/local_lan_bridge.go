package routes

import (
	"context"
	"net"
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// LocalLANBridgeStatus is the loopback control-plane view of the private-LAN
// enrollment listener. The listener never serves the product UI or auth APIs.
type LocalLANBridgeStatus struct {
	Enabled bool   `json:"enabled"`
	Origin  string `json:"origin,omitempty"`
	Address string `json:"address,omitempty"`
	Pin     string `json:"pin,omitempty"`
	Port    int    `json:"port"`
	Mode    string `json:"mode"`
	Error   string `json:"error,omitempty"`
}

type localLANBridgeKey struct{}

type localLANBridgeRequest struct{ origin, pin string }

// WithLocalLANBridge marks a request as received by the private-LAN bridge
// listener, with the bridge's own origin and certificate pin. Only the bridge
// sets it; no header or Host value a client sends can.
func WithLocalLANBridge(ctx context.Context, origin, pin string) context.Context {
	return context.WithValue(ctx, localLANBridgeKey{}, localLANBridgeRequest{origin: origin, pin: pin})
}

func localLANBridgeFrom(ctx context.Context) (localLANBridgeRequest, bool) {
	bridge, ok := ctx.Value(localLANBridgeKey{}).(localLANBridgeRequest)
	return bridge, ok && bridge.origin != "" && bridge.pin != ""
}

type LocalLANBridgeControl interface {
	Status() LocalLANBridgeStatus
	Enable(context.Context) (LocalLANBridgeStatus, error)
	Disable(context.Context) (LocalLANBridgeStatus, error)
}

// RegisterLocalLANBridgeRoutes exposes only the authenticated loopback switch.
// The bridge itself has its own fail-closed route and source-address allowlist.
func RegisterLocalLANBridgeRoutes(r *httpx.Router, bridge LocalLANBridgeControl) {
	if r == nil || bridge == nil {
		return
	}
	r.GET("/api/v1/client/lan-bridge", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}
		if !requestIsLoopback(e.Request) {
			return httpx.Forbidden(e, "LAN bridge control requires a loopback request")
		}
		return httpx.Success(e, http.StatusOK, bridge.Status())
	})
	r.PUT("/api/v1/client/lan-bridge", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}
		if !requestIsLoopback(e.Request) {
			return httpx.Forbidden(e, "LAN bridge control requires a loopback request")
		}
		var request struct {
			Enabled bool `json:"enabled"`
		}
		if err := e.BindBody(&request); err != nil {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "invalid request body", nil)
		}
		var (
			status LocalLANBridgeStatus
			err    error
		)
		if request.Enabled {
			status, err = bridge.Enable(e.Request.Context())
		} else {
			status, err = bridge.Disable(e.Request.Context())
		}
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "LAN bridge state could not be changed", nil)
		}
		return httpx.Success(e, http.StatusOK, status)
	})
}

func requestIsLoopback(r *http.Request) bool {
	if r == nil {
		return false
	}
	host := strings.TrimSpace(r.RemoteAddr)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
