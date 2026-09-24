package jobs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/kombifyme"
)

const stackKitAddressPlanV1 = "stackkit.address-plan/v1"

// ManagedAddressDeregistrationResultField records the kombify.me address
// cleanup evidence of a decommission in the job result.
const ManagedAddressDeregistrationResultField = "kombify_me_deregistration"

type stackKitAddressPlan struct {
	APIVersion      string                       `json:"api_version"`
	Provider        string                       `json:"provider"`
	StackName       string                       `json:"stack_name"`
	Domain          string                       `json:"domain"`
	SubdomainPrefix string                       `json:"subdomain_prefix,omitempty"`
	Services        []stackKitAddressPlanService `json:"services"`
}

type stackKitAddressPlanService struct {
	ServiceKey string `json:"service_key"`
	Host       string `json:"host"`
}

// managedAddressBinding is the registered address of one stack: flat hosts
// <Prefix>-<service>.kombify.me, or, with Zone set, the install zone
// <Prefix>.kombify.me with hosts <service>.<Prefix>.kombify.me (D-72).
type managedAddressBinding struct {
	Prefix string
	Zone   string
	Hosts  map[string]string
}

const (
	managedAddressLayoutFlat = "flat"
	managedAddressLayoutZone = "zone"
)

// allocateManagedAddressPlan registers the stack's managed kombify.me
// addresses with the platform identity. Every route is bound to the stack's
// owner and ID; kombify.me refuses to hand a route bound to anyone else to
// this stack, and the hostname label comes from stable identifiers only, so
// two tenants' stacks of the same name never share an address.
func allocateManagedAddressPlan(ctx context.Context, plan stackKitAddressPlan, target *ManagedRuntimeTarget, address kombifyme.ManagedAddress) (managedAddressBinding, error) {
	if plan.APIVersion != stackKitAddressPlanV1 || plan.Provider != addressModeKombifyMe || !strings.EqualFold(plan.Domain, addressModeKombifyMeDomain) || strings.TrimSpace(plan.StackName) == "" || len(plan.Services) == 0 {
		return managedAddressBinding{}, fmt.Errorf("StackKits address plan is incomplete or unsupported")
	}
	if address.Label == "" || address.OwnerRef == "" || address.StackRef == "" {
		return managedAddressBinding{}, kombifyme.ErrManagedAddressIdentity
	}
	origin, err := managedAddressOrigin(target)
	if err != nil {
		return managedAddressBinding{}, err
	}
	client := kombifyme.NewClient(kombifyme.DefaultConfigFromEnv())
	binding := address.Binding()
	boundPrefix := strings.TrimSpace(plan.SubdomainPrefix)
	registration := map[string]any{"base_subdomain_name": boundPrefix, "binding": binding}
	if boundPrefix == "" {
		profile, err := client.Forward(ctx, http.MethodGet, "auth/me", nil, "")
		if err != nil {
			return managedAddressBinding{}, fmt.Errorf("load kombify.me registration identity: %w", err)
		}
		deviceFingerprint := responseString(profile, "device_fingerprint")
		if deviceFingerprint == "" {
			return managedAddressBinding{}, fmt.Errorf("kombify.me registration identity has no authenticated device fingerprint")
		}
		registration = map[string]any{
			"homelab_name": address.Label, "kind": "self-hosted",
			"device_fingerprint": deviceFingerprint,
			"description":        "Managed by Kombify Techstack for " + plan.StackName,
			"binding":            binding,
		}
	}
	// An explicit plan binding is resolved through the registry's owner and
	// binding checks. A missing or foreign address never falls back to
	// name-based creation.
	baseResponse, err := client.Forward(ctx, http.MethodPost, "subdomains/auto-register", registration, "")
	if err != nil {
		return managedAddressBinding{}, fmt.Errorf("register kombify.me base address: %w", withUpstreamErrorCode(err))
	}
	baseID, baseName := responseString(baseResponse, "id"), responseString(baseResponse, "name")
	if boundPrefix != "" && baseName != boundPrefix {
		return managedAddressBinding{}, fmt.Errorf("kombify.me returned a different bound base address")
	}
	if baseID == "" || baseName == "" || responseString(baseResponse, "subdomain_kind") != "base" || responseString(baseResponse, "fqdn") != baseName+"."+addressModeKombifyMeDomain || !responseBoundTo(baseResponse, address) {
		return managedAddressBinding{}, fmt.Errorf("kombify.me returned an invalid base address binding")
	}

	result := managedAddressBinding{Prefix: baseName, Hosts: make(map[string]string, len(plan.Services))}
	// An install keeps the layout kombify.me already holds for it; the switch
	// only decides for an install without services (D-72).
	layout := managedAddressLayoutFlat
	if boundPrefix == "" {
		switch responseString(baseResponse, "layout") {
		case managedAddressLayoutZone:
			layout = managedAddressLayoutZone
		case "":
			if kombifyme.InstallZonesEnabled() {
				layout = managedAddressLayoutZone
			}
		}
	}
	if layout == managedAddressLayoutZone {
		result.Zone = baseName + "." + addressModeKombifyMeDomain
	}
	for _, planned := range plan.Services {
		serviceKey := strings.TrimSpace(planned.ServiceKey)
		if serviceKey == "" {
			return managedAddressBinding{}, fmt.Errorf("StackKits address plan contains an empty service key")
		}
		request := map[string]any{
			"base_subdomain_name": baseName,
			"service_name":        serviceKey,
			"local_addr":          origin,
			"target_type":         "proxy",
			"description":         "StackKit service " + serviceKey + " for " + plan.StackName,
			"binding":             binding,
		}
		expectedName := baseName + "-" + serviceKey
		if result.Zone != "" {
			request["layout"] = managedAddressLayoutZone
			expectedName = serviceKey + "." + baseName
		}
		serviceResponse, registerErr := client.Forward(ctx, http.MethodPost, "subdomains/auto-register/service", request, "")
		if registerErr != nil {
			return managedAddressBinding{}, fmt.Errorf("register kombify.me service %s: %w", serviceKey, withUpstreamErrorCode(registerErr))
		}
		serviceID := responseString(serviceResponse, "id")
		expectedFQDN := expectedName + "." + addressModeKombifyMeDomain
		if serviceID == "" || responseString(serviceResponse, "parent_id") != baseID || responseString(serviceResponse, "name") != expectedName || responseString(serviceResponse, "fqdn") != expectedFQDN || responseString(serviceResponse, "target_type") != "proxy" || responseString(serviceResponse, "target_addr") != origin || !responseBoundTo(serviceResponse, address) {
			return managedAddressBinding{}, fmt.Errorf("kombify.me returned an invalid binding for service %s", serviceKey)
		}
		exposed, exposeErr := client.Forward(ctx, http.MethodPut, "subdomains/"+url.PathEscape(baseID)+"/services/"+url.PathEscape(serviceID)+"/expose", map[string]any{"exposed": true}, "")
		if exposeErr != nil {
			return managedAddressBinding{}, fmt.Errorf("expose kombify.me service %s: %w", serviceKey, exposeErr)
		}
		if responseString(exposed, "id") != serviceID || !responseBool(exposed, "exposed") {
			return managedAddressBinding{}, fmt.Errorf("kombify.me did not expose service %s", serviceKey)
		}
		result.Hosts[serviceKey] = expectedFQDN
	}
	return result, nil
}

// ManagedAddressDeregistration names the stack whose managed kombify.me
// addresses a decommission removes. TargetOrigin narrows the removal to the
// addresses of one released runtime; empty removes every address of the stack.
type ManagedAddressDeregistration struct {
	TenantID     string
	OwnerID      string
	StackID      string
	TargetOrigin string
}

// DeregisterManagedAddresses removes the kombify.me addresses Techstack
// registered for a stack and proves their absence before a decommission may
// proceed: kombify.me's receipt names the deleted routes and the released
// origin DNS aliases, and an independent readback by binding must list none of
// the stack's routes. Any failure is returned so the decommission stays
// incomplete instead of leaving an address that routes to a released server.
func DeregisterManagedAddresses(ctx context.Context, req ManagedAddressDeregistration) (map[string]any, error) {
	cfg := kombifyme.DefaultConfigFromEnv()
	if strings.TrimSpace(cfg.APIKey) == "" {
		// Managed addresses are only ever registered with the platform
		// identity, so a control plane without it has none to remove.
		return map[string]any{"status": "not_configured"}, nil
	}
	address, err := kombifyme.NewManagedAddress(req.TenantID, req.OwnerID, req.StackID)
	if err != nil {
		return nil, err
	}
	targetOrigin := ""
	if strings.TrimSpace(req.TargetOrigin) != "" {
		if targetOrigin = normalizedHTTPOrigin(req.TargetOrigin); targetOrigin == "" {
			return nil, fmt.Errorf("kombify.me deregistration target %q is not an HTTP(S) origin", req.TargetOrigin)
		}
	}
	client := kombifyme.NewClient(cfg)
	payload := map[string]any{"binding": address.Binding()}
	if targetOrigin != "" {
		payload["target_addr"] = targetOrigin
	}
	receipt, err := client.Forward(ctx, http.MethodPost, "subdomains/deregister", payload, "")
	if err != nil {
		return nil, fmt.Errorf("deregister kombify.me addresses: %w", withUpstreamErrorCode(err))
	}
	body, _ := receipt.Body.(map[string]any)
	if remaining, ok := body["remaining"].(float64); !ok || remaining != 0 {
		return nil, fmt.Errorf("kombify.me deregistration receipt does not prove zero remaining routes")
	}
	deleted := receiptNames(body["deleted"])
	origins, err := releasedOriginRecords(body["origin_records"])
	if err != nil {
		return nil, err
	}
	zones, err := releasedZoneRecords(body["zone_records"])
	if err != nil {
		return nil, err
	}

	readback, err := client.Forward(ctx, http.MethodGet, "subdomains?binding_stack_ref="+url.QueryEscape(address.StackRef), nil, "")
	if err != nil {
		return nil, fmt.Errorf("read back kombify.me addresses after deregistration: %w", err)
	}
	routes, ok := readback.Body.([]any)
	if !ok {
		return nil, fmt.Errorf("kombify.me readback after deregistration is not a route list")
	}
	for _, raw := range routes {
		route, _ := raw.(map[string]any)
		routeTarget, _ := route["target_addr"].(string)
		kind, _ := route["subdomain_kind"].(string)
		if targetOrigin == "" || (kind == "service" && normalizedHTTPOrigin(routeTarget) == targetOrigin) {
			name, _ := route["name"].(string)
			return nil, fmt.Errorf("kombify.me address %s is still registered after deregistration", name)
		}
	}
	evidence := map[string]any{
		"status":             "absent",
		"owner_ref":          address.OwnerRef,
		"stack_ref":          address.StackRef,
		"deleted":            deleted,
		"origin_records":     origins,
		"readback_remaining": 0,
		"verified_at":        time.Now().UTC().Format(time.RFC3339),
	}
	if len(zones) > 0 {
		evidence["zone_records"] = zones
	}
	if targetOrigin != "" {
		evidence["target_origin"] = targetOrigin
	}
	return evidence, nil
}

// releasedZoneRecords accepts only proven-absent install zone records (D-72).
func releasedZoneRecords(raw any) ([]map[string]string, error) {
	items, _ := raw.([]any)
	records := make([]map[string]string, 0, len(items))
	for _, item := range items {
		entry, _ := item.(map[string]any)
		name, _ := entry["name"].(string)
		state, _ := entry["state"].(string)
		if name == "" || state != "absent" {
			return nil, fmt.Errorf("kombify.me did not prove install zone record %q released (state %q)", name, state)
		}
		records = append(records, map[string]string{"name": name, "state": state})
	}
	return records, nil
}

func receiptNames(raw any) []string {
	items, _ := raw.([]any)
	names := make([]string, 0, len(items))
	for _, item := range items {
		entry, _ := item.(map[string]any)
		if name, _ := entry["fqdn"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// releasedOriginRecords accepts only kombify.me's two terminal alias states;
// anything else means the origin DNS alias was not proven released.
func releasedOriginRecords(raw any) ([]map[string]string, error) {
	items, _ := raw.([]any)
	records := make([]map[string]string, 0, len(items))
	for _, item := range items {
		entry, _ := item.(map[string]any)
		name, _ := entry["name"].(string)
		state, _ := entry["state"].(string)
		if name == "" || (state != "absent" && state != "retained_in_use") {
			return nil, fmt.Errorf("kombify.me did not prove origin alias %q released (state %q)", name, state)
		}
		records = append(records, map[string]string{"name": name, "state": state})
	}
	return records, nil
}

func normalizedHTTPOrigin(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if port == "" {
		if strings.Contains(host, ":") {
			return parsed.Scheme + "://[" + host + "]"
		}
		return parsed.Scheme + "://" + host
	}
	return parsed.Scheme + "://" + net.JoinHostPort(host, port)
}

func responseBoundTo(response *kombifyme.UpstreamResponse, address kombifyme.ManagedAddress) bool {
	return responseString(response, "binding_owner_ref") == address.OwnerRef &&
		responseString(response, "binding_stack_ref") == address.StackRef
}

// withUpstreamErrorCode keeps kombify.me's structured denial code (for example
// binding_conflict) visible in lifecycle errors.
func withUpstreamErrorCode(err error) error {
	var upstream *kombifyme.UpstreamError
	if !errors.As(err, &upstream) {
		return err
	}
	body, _ := upstream.Body.(map[string]any)
	if code, _ := body["error_code"].(string); code != "" {
		return fmt.Errorf("%w (%s)", err, code)
	}
	return err
}

func managedAddressOrigin(target *ManagedRuntimeTarget) (string, error) {
	if target == nil {
		return "", fmt.Errorf("managed kombify.me address registration requires the exact runtime target")
	}
	host := strings.TrimSpace(firstNonEmpty(target.PublicIP, target.Host))
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return "", fmt.Errorf("managed kombify.me address registration requires a public runtime IP")
	}
	return "https://" + net.JoinHostPort(address.String(), "443"), nil
}

func responseString(response *kombifyme.UpstreamResponse, key string) string {
	if response == nil {
		return ""
	}
	body, _ := response.Body.(map[string]any)
	value, _ := body[key].(string)
	return strings.TrimSpace(value)
}

func responseBool(response *kombifyme.UpstreamResponse, key string) bool {
	if response == nil {
		return false
	}
	body, _ := response.Body.(map[string]any)
	value, _ := body[key].(bool)
	return value
}
