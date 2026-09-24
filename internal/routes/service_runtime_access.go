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
	serviceActionStart         = "start"
	serviceActionStop          = "stop"
	serviceActionLogs          = "logs"
	serviceActionFreeze        = "freeze"
	serviceActionUnfreeze      = "unfreeze"
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

// serviceActionVocabulary is the closed request vocabulary of the service
// action endpoint. Whether a given service accepts a given action is decided
// against serviceRuntimeAllowedActions, not here.
var serviceActionVocabulary = map[string]bool{
	serviceActionStart: true, serviceActionStop: true, serviceActionRestart: true,
	serviceActionLogs: true, serviceActionFreeze: true, serviceActionUnfreeze: true,
}

// serviceMutatingActions is the closed set of agent-executed operations the
// owner guardrail suppresses. `logs` is deliberately absent: reading a locked
// service is not a mutation.
var serviceMutatingActions = map[string]bool{
	serviceActionStart: true, serviceActionStop: true, serviceActionRestart: true,
}

// serviceRuntimeAllowedActions is exactly what the action endpoint accepts for
// this service right now, so the advertised set and the enforced set can never
// drift apart. A locked service keeps its read path and offers the unlock, and
// the unlock is offered unconditionally so a lock can never become a trap on a
// service whose placement later stops supporting mutations.
//
// The card family renders the suppressed mutations as disabled-with-reason from
// the separate `mutation_lock` field; their absence here is the API contract,
// not a UI instruction.
func serviceRuntimeAllowedActions(capabilities []string, locked bool) []string {
	actions := canonicalServiceActions(capabilities)
	if !locked {
		if len(actions) == 0 {
			return actions
		}
		actions = append(actions, serviceActionFreeze)
		sort.Strings(actions)
		return actions
	}
	kept := make([]string, 0, len(actions)+1)
	for _, action := range actions {
		if !serviceMutatingActions[action] {
			kept = append(kept, action)
		}
	}
	kept = append(kept, serviceActionUnfreeze)
	sort.Strings(kept)
	return kept
}

func canonicalServiceActions(actions []string) []string {
	allowed := map[string]bool{serviceActionStart: true, serviceActionStop: true, serviceActionRestart: true, serviceActionLogs: true}
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
	if host == "home" || strings.HasSuffix(host, ".home") {
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
	if host == "home" || strings.HasSuffix(host, ".home") || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
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
