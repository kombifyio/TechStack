// Package runtimehealth derives the public runtime health vocabulary from
// measured agent observations. It deliberately does not inspect job or lease
// completion state: those signals describe intent and provisioning progress,
// not the current health of a server or service.
package runtimehealth

import (
	"strings"
	"time"
)

const (
	// FreshHeartbeatWindow is the interval in which an authenticated agent
	// heartbeat is considered current.
	FreshHeartbeatWindow = 90 * time.Second
	// StaleHeartbeatWindow is the maximum age of the last heartbeat before the
	// agent is considered offline.
	StaleHeartbeatWindow = 5 * time.Minute
)

type ServerState string

const (
	ServerProvisioned ServerState = "provisioned"
	ServerHealthy     ServerState = "healthy"
	ServerDegraded    ServerState = "degraded"
	ServerStale       ServerState = "stale"
	ServerOffline     ServerState = "offline"
)

type ServiceState string

const (
	ServiceStarting  ServiceState = "starting"
	ServiceHealthy   ServiceState = "healthy"
	ServiceReachable ServiceState = "reachable"
	ServiceUnhealthy ServiceState = "unhealthy"
	ServiceUnknown   ServiceState = "unknown"
)

// ServerInput is the minimum measured context needed for a server status.
// ObservedState is the agent's last observed host state; it is only used while
// the heartbeat is current. A persisted node record without a heartbeat must
// therefore never stay connected indefinitely.
type ServerInput struct {
	Now           time.Time
	HeartbeatAt   *time.Time
	ObservedState string
}

// DeriveServerState returns the canonical public server health state. A node
// that is provisioned but has not emitted an authenticated heartbeat remains
// provisioned; a stale or offline heartbeat always overrides old inventory.
func DeriveServerState(in ServerInput) ServerState {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if in.HeartbeatAt == nil || in.HeartbeatAt.IsZero() {
		return ServerProvisioned
	}
	age := now.Sub(in.HeartbeatAt.UTC())
	switch {
	case age > StaleHeartbeatWindow:
		return ServerOffline
	case age > FreshHeartbeatWindow:
		return ServerStale
	case isDegradedHostState(in.ObservedState):
		return ServerDegraded
	default:
		return ServerHealthy
	}
}

// DeriveServiceState translates a live runtime observation into the public
// service vocabulary. A service cannot be healthy after its server heartbeat
// has expired, even if a previous inventory reported it as healthy.
func DeriveServiceState(server ServerState, reportedStatus string, health map[string]any, endpointHealths []string) ServiceState {
	if server == ServerStale || server == ServerOffline || server == ServerProvisioned {
		return ServiceUnknown
	}

	values := make([]string, 0, len(endpointHealths)+3)
	values = append(values, reportedStatus)
	values = append(values, healthStrings(health)...)
	values = append(values, endpointHealths...)

	for _, value := range values {
		if isUnhealthyServiceState(value) {
			return ServiceUnhealthy
		}
	}
	for _, value := range values {
		if isHealthyServiceState(value) {
			return ServiceHealthy
		}
	}
	for _, value := range values {
		if isReachableServiceState(value) {
			return ServiceReachable
		}
	}
	for _, value := range values {
		if isStartingServiceState(value) {
			return ServiceStarting
		}
	}
	// A container merely being "running" is not a service-health proof. Keep it
	// in a non-green transition state until Docker health or an endpoint probe
	// reports a result.
	if strings.EqualFold(strings.TrimSpace(reportedStatus), "running") {
		return ServiceStarting
	}
	return ServiceUnknown
}

func isReachableServiceState(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), string(ServiceReachable))
}

func isDegradedHostState(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "degraded", "unhealthy", "error", "failed":
		return true
	default:
		return false
	}
}

func healthStrings(health map[string]any) []string {
	if len(health) == 0 {
		return nil
	}
	values := make([]string, 0, 3)
	for _, key := range []string{"status", "state", "docker_health", "probe", "result"} {
		if value, ok := health[key]; ok {
			if text, ok := value.(string); ok {
				values = append(values, text)
			}
		}
	}
	if value, ok := health["healthy"].(bool); ok {
		if value {
			values = append(values, "healthy")
		} else {
			values = append(values, "unhealthy")
		}
	}
	return values
}

func isHealthyServiceState(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "healthy", "ok", "ready", "pass", "passing", "up":
		return true
	default:
		return false
	}
}

func isUnhealthyServiceState(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "unhealthy", "error", "failed", "failure", "exited", "dead", "offline", "down", "fail", "failing":
		return true
	default:
		return false
	}
}

func isStartingServiceState(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "starting", "created", "restarting", "pending", "deploying", "initializing":
		return true
	default:
		return false
	}
}
