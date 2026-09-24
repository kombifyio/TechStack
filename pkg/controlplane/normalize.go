package controlplane

import (
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/outcome"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func serverRuntimeDefaults(server *ServerRuntime, now time.Time) {
	server.LifecycleState = firstNonEmpty(server.LifecycleState, "planned")
	server.DesiredState = firstNonEmpty(server.DesiredState, string(serverregistry.DesiredRunning))
	server.ConnectionState = firstNonEmpty(server.ConnectionState, "pending")
	server.HealthState = firstNonEmpty(server.HealthState, "unknown")
	if server.Revision <= 0 {
		server.Revision = 1
	}
	if server.Generation <= 0 {
		server.Generation = 1
	}
	if server.ConnectionChangedAt.IsZero() {
		server.ConnectionChangedAt = now
	}
	if server.LifecycleChangedAt.IsZero() {
		server.LifecycleChangedAt = now
	}
	if server.DesiredChangedAt.IsZero() {
		server.DesiredChangedAt = now
	}
	if server.HealthChangedAt.IsZero() {
		server.HealthChangedAt = now
	}
	server.LastHeartbeatAt = cloneTime(server.LastHeartbeatAt)
	server.SourceObservedAt = cloneTime(server.SourceObservedAt)
	server.OutcomeChangedAt = cloneTime(server.OutcomeChangedAt)
	server.DecommissionedAt = cloneTime(server.DecommissionedAt)
	if !serverregistry.RuntimeTargetIntentPresent(server.RuntimeTarget) {
		if target, ok := serverregistry.HostingerExternalVPSTarget(server.ProviderRef, now); ok {
			server.RuntimeTarget = target
		} else {
			server.RuntimeTarget = serverregistry.UnknownRuntimeTarget()
		}
	} else {
		server.RuntimeTarget = serverregistry.NormalizeRuntimeTarget(server.RuntimeTarget)
	}
	server.Metadata = cloneMap(server.Metadata)
	server.Channels = cloneServerChannels(server.Channels)
	server.LastOutcome = outcome.Clone(server.LastOutcome)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
