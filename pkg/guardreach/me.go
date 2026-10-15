package guardreach

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/kombifyio/techstack/pkg/kombifyme"
)

const meZoneDomain = "kombify.me"

// Me is the kombify.me client of the control plane. It acts only with the
// platform kombify.me identity (the Techstack `kbi_` key), the single identity
// Me's Guard binding API accepts.
type Me struct{ client *kombifyme.Client }

// NewMe wraps a kombify.me client. The client must carry the platform key.
func NewMe(client *kombifyme.Client) *Me { return &Me{client: client} }

// MeError is a structured kombify.me denial; it never carries a body.
type MeError struct {
	Status     int
	ReasonCode string
	Retryable  bool
}

func (e *MeError) Error() string {
	return fmt.Sprintf("kombify.me answered HTTP %d (%s)", e.Status, firstNonEmpty(e.ReasonCode, "no reason code"))
}

func meFailure(err error) error {
	var upstream *kombifyme.UpstreamError
	if !errors.As(err, &upstream) {
		return err
	}
	out := &MeError{Status: upstream.Status, Retryable: upstream.Status >= 500 || upstream.Status == http.StatusTooManyRequests}
	if body, ok := upstream.Body.(map[string]any); ok {
		out.ReasonCode, _ = body["reason_code"].(string)
		if out.ReasonCode == "" {
			out.ReasonCode, _ = body["error_code"].(string)
		}
		if retryable, ok := body["retryable"].(bool); ok {
			out.Retryable = retryable
		}
	}
	return out
}

// Route is one tunnel route kombify.me holds for a server.
type Route struct {
	Name string
	FQDN string
}

func field(response *kombifyme.UpstreamResponse, key string) string {
	if response == nil {
		return ""
	}
	body, _ := response.Body.(map[string]any)
	value, _ := body[key].(string)
	return strings.TrimSpace(value)
}

func boundTo(response *kombifyme.UpstreamResponse, address kombifyme.ManagedAddress) bool {
	return field(response, "binding_owner_ref") == address.OwnerRef && field(response, "binding_stack_ref") == address.StackRef
}

// EnsureRoute makes the server's default route exist under the stack's managed
// base address as an exposed tunnel route: base address, tunnel service bound
// to the same owner and stack, then exposure. Every step is idempotent, and a
// response that does not prove the binding is refused instead of trusted.
func (m *Me) EnsureRoute(ctx context.Context, address kombifyme.ManagedAddress, service, localAddr, description string) (Route, error) {
	profile, err := m.client.Forward(ctx, http.MethodGet, "auth/me", nil, "")
	if err != nil {
		return Route{}, meFailure(err)
	}
	fingerprint := field(profile, "device_fingerprint")
	if fingerprint == "" {
		return Route{}, &MeError{Status: http.StatusBadGateway, ReasonCode: "platform_identity_without_fingerprint"}
	}
	base, err := m.client.Forward(ctx, http.MethodPost, "subdomains/auto-register", map[string]any{
		"homelab_name": address.Label, "kind": "self-hosted", "device_fingerprint": fingerprint,
		"description": description, "binding": address.Binding(),
	}, "")
	if err != nil {
		return Route{}, meFailure(err)
	}
	baseID, baseName := field(base, "id"), field(base, "name")
	if baseID == "" || baseName == "" || field(base, "subdomain_kind") != "base" ||
		field(base, "fqdn") != baseName+"."+meZoneDomain || !boundTo(base, address) {
		return Route{}, &MeError{Status: http.StatusBadGateway, ReasonCode: "base_binding_unproven"}
	}
	if field(base, "layout") == "zone" {
		// A zone install serves proxy routes only; a tunnel route cannot join it.
		return Route{}, &MeError{Status: http.StatusConflict, ReasonCode: ReasonLayoutUnsupported}
	}
	service = strings.ToLower(strings.TrimSpace(service))
	route, err := m.client.Forward(ctx, http.MethodPost, "subdomains/auto-register/service", map[string]any{
		"base_subdomain_name": baseName, "service_name": service, "local_addr": localAddr,
		"target_type": "tunnel", "description": description, "binding": address.Binding(),
	}, "")
	if err != nil {
		return Route{}, meFailure(err)
	}
	routeID, routeName := field(route, "id"), field(route, "name")
	if routeID == "" || routeName != baseName+"-"+service || field(route, "parent_id") != baseID ||
		field(route, "fqdn") != routeName+"."+meZoneDomain || field(route, "target_type") != "tunnel" || !boundTo(route, address) {
		return Route{}, &MeError{Status: http.StatusBadGateway, ReasonCode: "route_binding_unproven"}
	}
	exposed, err := m.client.Forward(ctx, http.MethodPut,
		"subdomains/"+url.PathEscape(baseID)+"/services/"+url.PathEscape(routeID)+"/expose", map[string]any{"exposed": true}, "")
	if err != nil {
		return Route{}, meFailure(err)
	}
	if body, _ := exposed.Body.(map[string]any); body["id"] != routeID || body["exposed"] != true {
		return Route{}, &MeError{Status: http.StatusBadGateway, ReasonCode: "route_not_exposed"}
	}
	return Route{Name: routeName, FQDN: routeName + "." + meZoneDomain}, nil
}

// MeRegistration is the Guard binding of one server at kombify.me.
type MeRegistration struct {
	TenantID, ServerID, WorkerID string
	Address                      kombifyme.ManagedAddress
	KeyThumbprint                string
	Generation                   int64
	Routes                       []string
}

// RegisterServer binds the server, its key and its routes at kombify.me. A
// higher generation replaces worker and key and closes the old key's sessions.
func (m *Me) RegisterServer(ctx context.Context, in MeRegistration) error {
	response, err := m.client.Forward(ctx, http.MethodPut, "guard/servers", map[string]any{
		"tenant_id": in.TenantID, "server_id": in.ServerID, "owner_ref": in.Address.OwnerRef, "stack_ref": in.Address.StackRef,
		"worker_id": in.WorkerID, "guard_key_thumbprint": in.KeyThumbprint, "generation": in.Generation, "routes": in.Routes,
	}, "")
	if err != nil {
		return meFailure(err)
	}
	body, _ := response.Body.(map[string]any)
	if generation, _ := body["generation"].(float64); int64(generation) != in.Generation || body["state"] != "active" {
		return &MeError{Status: http.StatusBadGateway, ReasonCode: "binding_unproven"}
	}
	return nil
}

// RevokeServer detaches one server. A server kombify.me never bound is not an
// error: there is nothing to revoke.
func (m *Me) RevokeServer(ctx context.Context, tenantID, serverID, reason string) error {
	_, err := m.client.Forward(ctx, http.MethodPost, "guard/servers/revoke", map[string]any{
		"tenant_id": tenantID, "server_id": serverID, "reason": reason,
	}, "")
	if err == nil {
		return nil
	}
	failure := meFailure(err)
	var denial *MeError
	if errors.As(failure, &denial) && denial.Status == http.StatusNotFound && denial.ReasonCode == "binding_not_found" {
		return nil
	}
	return failure
}
