package routes

import (
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const (
	serviceAccessDirect        = "direct"
	serviceAccessRelay         = "relay"
	serviceAccessUnavailable   = "unavailable"
	serviceAccessModeKey       = "mode"
	serviceAccessURLKey        = "url"
	serviceAccessObservedKey   = "observed"
	serviceAccessProfileRefKey = "access_profile_ref"
	serviceAccessSourceKey     = "provenance"
	serviceActionRestart       = "restart"
	serviceHealthHealthy       = "healthy"
	serviceHealthReachable     = string(runtimehealth.ServiceReachable)
	serviceHTTP                = "http"
	serviceHTTPS               = "https"
	serviceInventoryIDKey      = "service_id"
	serviceInventoryKindKey    = "kind"
	serviceHealthUnhealthy     = "unhealthy"
	serviceObservedReady       = "ready"
	serviceObservedStarting    = "starting"
	serviceRuntimeAuthority    = "techstack"
	serviceStackKit            = "stackkit"
	serviceStackKits           = "stackkits"
	serviceStackKitManifest    = "stackkit-access-manifest"
)

func canonicalServiceActions(actions []string) []string {
	allowed := map[string]bool{"start": true, "stop": true, serviceActionRestart: true, "logs": true}
	seen := map[string]bool{}
	result := make([]string, 0, len(actions))
	for _, action := range actions {
		action = strings.ToLower(strings.TrimSpace(action))
		if !allowed[action] || seen[action] {
			continue
		}
		seen[action] = true
		result = append(result, action)
	}
	sort.Strings(result)
	return result
}

// canonicalServiceObservedState projects an agent-reported runtime status onto
// the StackKits service status vocabulary the registry stores and the database
// CHECK constraints enforce.
func canonicalServiceObservedState(value string) string {
	return string(serviceregistry.CanonicalObservedState(value))
}

func deriveInventoryServiceAccess(service workerInventoryService) map[string]any {
	for _, endpoint := range service.Endpoints {
		if !strings.EqualFold(endpoint.Visibility, "public") {
			continue
		}
		if publicURL := safePublicServiceURL(endpoint.URL); publicURL != "" {
			return serviceAccess(serviceAccessDirect, publicURL, endpoint.AccessProfileRef, "", "")
		}
	}
	for _, endpoint := range service.Endpoints {
		if !trustedObservedDirectEndpoint(endpoint) {
			continue
		}
		if observedURL := safeObservedDirectServiceURL(endpoint.URL); observedURL != "" {
			access := serviceAccess(serviceAccessDirect, observedURL, endpoint.AccessProfileRef, "", "")
			access[serviceAccessObservedKey] = true
			access[serviceAccessSourceKey] = serviceStackKitManifest
			return access
		}
	}
	for _, endpoint := range service.Endpoints {
		if !strings.EqualFold(endpoint.TargetType, "tunnel") || strings.TrimSpace(endpoint.RouteID) == "" || !stackKitEndpointProvenance(endpoint.Provenance) {
			continue
		}
		if relayURL := safeKombifyRelayURL(endpoint.URL); relayURL != "" {
			return serviceAccess(serviceAccessRelay, relayURL, endpoint.AccessProfileRef, "", endpoint.RouteID)
		}
	}
	if publicURL := safePublicServiceURL(service.URL); publicURL != "" {
		return serviceAccess(serviceAccessDirect, publicURL, "", "", "")
	}
	return serviceAccess(serviceAccessUnavailable, "", "", "no_registered_access", "")
}

func trustedObservedDirectEndpoint(endpoint workerInventoryEndpoint) bool {
	if !strings.EqualFold(strings.TrimSpace(endpoint.TargetType), serviceAccessDirect) ||
		!strings.EqualFold(strings.TrimSpace(endpoint.Provenance), serviceStackKitManifest) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(endpoint.Health)) {
	case serviceHealthHealthy, "ok", serviceHealthReachable:
		return true
	default:
		return false
	}
}

func safeObservedDirectServiceURL(raw string) string {
	parsed, ok := parseServiceURL(raw)
	if !ok {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(parsed.Hostname()), "."))
	if kombifyRelayHost(host) {
		if parsed.Scheme != serviceHTTPS {
			return ""
		}
		return parsed.String()
	}
	if host == "home.localhost" || strings.HasSuffix(host, ".home.localhost") {
		return parsed.String()
	}
	if privateServiceHost(host) {
		return ""
	}
	return parsed.String()
}

func serviceAccess(mode, address, accessProfileRef, reasonCode, routeID string) map[string]any {
	result := map[string]any{serviceAccessModeKey: mode}
	if address != "" {
		result[serviceAccessURLKey] = address
	}
	if accessProfileRef != "" {
		result[serviceAccessProfileRefKey] = accessProfileRef
	}
	if reasonCode != "" {
		result["reason_code"] = reasonCode
	}
	if routeID != "" {
		result["route_id"] = routeID
	}
	return result
}

func safePublicServiceURL(raw string) string {
	parsed, ok := parseServiceURL(raw)
	if !ok || privateServiceHost(parsed.Hostname()) || kombifyRelayHost(parsed.Hostname()) {
		return ""
	}
	return parsed.String()
}

func safeKombifyRelayURL(raw string) string {
	parsed, ok := parseServiceURL(raw)
	if !ok || parsed.Scheme != serviceHTTPS {
		return ""
	}
	if !kombifyRelayHost(parsed.Hostname()) {
		return ""
	}
	return parsed.String()
}

func kombifyRelayHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "kombify.me" || strings.HasSuffix(host, ".kombify.me")
}

func parseServiceURL(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != serviceHTTPS && parsed.Scheme != serviceHTTP) {
		return nil, false
	}
	return parsed, true
}

func privateServiceHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified())
}

func stackKitEndpointProvenance(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case serviceStackKit, serviceStackKits, "stackkit-runtime":
		return true
	default:
		return false
	}
}
