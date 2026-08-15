package runtimehealth

import (
	"testing"
	"time"
)

func TestDeriveServerStateUsesHeartbeatFreshness(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		at   *time.Time
		obs  string
		want ServerState
	}{
		{name: "no heartbeat stays provisioned", want: ServerProvisioned},
		{name: "fresh heartbeat is healthy", at: timePointer(now.Add(-FreshHeartbeatWindow)), want: ServerHealthy},
		{name: "fresh degraded observation is degraded", at: timePointer(now.Add(-time.Second)), obs: "degraded", want: ServerDegraded},
		{name: "old heartbeat is stale", at: timePointer(now.Add(-FreshHeartbeatWindow - time.Second)), want: ServerStale},
		{name: "expired heartbeat is offline", at: timePointer(now.Add(-StaleHeartbeatWindow - time.Second)), want: ServerOffline},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveServerState(ServerInput{Now: now, HeartbeatAt: tc.at, ObservedState: tc.obs}); got != tc.want {
				t.Fatalf("DeriveServerState() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeriveServiceStateRequiresCurrentObservation(t *testing.T) {
	tests := []struct {
		name      string
		server    ServerState
		reported  string
		health    map[string]any
		endpoints []string
		want      ServiceState
	}{
		{name: "stale server makes prior healthy unknown", server: ServerStale, reported: "healthy", want: ServiceUnknown},
		{name: "endpoint health proves healthy", server: ServerHealthy, reported: "running", endpoints: []string{"ok"}, want: ServiceHealthy},
		{name: "auth gateway is reachable but not healthy", server: ServerHealthy, reported: "reachable", health: map[string]any{"status": "reachable", "auth_or_redirect_required": true}, endpoints: []string{"reachable"}, want: ServiceReachable},
		{name: "docker health proves unhealthy", server: ServerHealthy, health: map[string]any{"docker_health": "unhealthy"}, want: ServiceUnhealthy},
		{name: "starting remains starting", server: ServerHealthy, reported: "restarting", want: ServiceStarting},
		{name: "running without probe is not healthy", server: ServerHealthy, reported: "running", want: ServiceStarting},
		{name: "no observation is unknown", server: ServerHealthy, want: ServiceUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveServiceState(tc.server, tc.reported, tc.health, tc.endpoints); got != tc.want {
				t.Fatalf("DeriveServiceState() = %q, want %q", got, tc.want)
			}
		})
	}
}

func timePointer(value time.Time) *time.Time { return &value }
