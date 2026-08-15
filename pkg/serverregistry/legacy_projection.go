package serverregistry

import (
	"strings"

	"github.com/kombifyio/techstack/pkg/runtimehealth"
)

// LegacyServerState projects the canonical orthogonal connection and health
// dimensions back into the single-valued legacy runtime vocabulary that the
// pre-aggregate read routes (/api/v1/registry/servers, /api/v1/monitor/cockpit)
// still publish as `status` / `health_state`.
//
// It is the exact inverse of DeriveObservedState: for every heartbeat input,
// LegacyServerState(DeriveObservedState(...)) equals
// runtimehealth.DeriveServerState(...) for the same input. That property is
// what lets the legacy routes read persisted canonical state instead of
// recomputing freshness at read time, without changing their wire contract.
//
// Mapping (canonical -> legacy), including the two states the legacy
// vocabulary cannot express:
//
//	connected + healthy/unknown -> healthy
//	connected + degraded/unhealthy -> degraded   (legacy has no "unhealthy")
//	degraded                    -> degraded
//	stale                       -> stale
//	offline                     -> offline
//	revoked                     -> offline       (legacy has no "revoked";
//	                                              the agent is unreachable, so
//	                                              offline is the honest legacy
//	                                              projection)
//	pending / connecting / ""   -> provisioned
//
// This helper exists only to keep the legacy projections truthful until the
// Wave 2 UI cutover retires them; it is not a new read model.
func LegacyServerState(connection, health string) runtimehealth.ServerState {
	switch ConnectionState(strings.ToLower(strings.TrimSpace(connection))) {
	case ConnectionConnected:
		switch HealthState(strings.ToLower(strings.TrimSpace(health))) {
		case HealthDegraded, HealthUnhealthy:
			return runtimehealth.ServerDegraded
		default:
			return runtimehealth.ServerHealthy
		}
	case ConnectionDegraded:
		return runtimehealth.ServerDegraded
	case ConnectionStale:
		return runtimehealth.ServerStale
	case ConnectionOffline, ConnectionRevoked:
		return runtimehealth.ServerOffline
	default:
		return runtimehealth.ServerProvisioned
	}
}

// LegacySatelliteState is the legacy projection for a row that exists only in
// a satellite table (`nodes`, `workers`) and has no canonical server aggregate
// behind it.
//
// Such a row carries no persisted connection or health dimension: the registry
// sweeper never wrote one, because there is nothing to write it to. The legacy
// read routes used to close that gap by recomputing freshness from
// `workers.last_seen_at` at request time (runtimehealth.DeriveServerState),
// which is the same read-time fabrication that #577 deleted for `ril_servers`.
//
// The honest projection is `provisioned`: "this row exists, and no connection
// evidence has been persisted for it". It is never healthy, never stale, and
// never rollout-ready — a satellite row cannot certify an agent it does not
// own. Wave 2 (kombify-Techstack-nzy1.7) makes the satellites read-only, so
// their state must come from the aggregate or not at all.
func LegacySatelliteState() runtimehealth.ServerState {
	return runtimehealth.ServerProvisioned
}

// LegacyRolloutReady reports whether the legacy read routes may advertise a
// server as rollout-ready. It deliberately stays stricter than
// MutationsAllowed, which also permits mutations on a degraded connection:
// the legacy `rollout_ready` flag has always meant "healthy", and widening it
// here would loosen an existing safety boundary as a side effect of the read
// model change.
func LegacyRolloutReady(lifecycle, connection, health string) bool {
	return LegacyServerState(connection, health) == runtimehealth.ServerHealthy &&
		LifecycleState(strings.TrimSpace(lifecycle)) == LifecycleActive
}
