package serverregistry

import (
	"testing"
	"time"
)

func TestDeriveObservedStateUsesHeartbeatFreshness(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		heartbeat  *time.Time
		host       string
		connection ConnectionState
		health     HealthState
	}{
		{name: "never observed", connection: ConnectionPending, health: HealthUnknown},
		{name: "fresh", heartbeat: timePtr(now.Add(-30 * time.Second)), connection: ConnectionConnected, health: HealthHealthy},
		{name: "fresh degraded", heartbeat: timePtr(now.Add(-30 * time.Second)), host: "degraded", connection: ConnectionDegraded, health: HealthDegraded},
		{name: "stale", heartbeat: timePtr(now.Add(-2 * time.Minute)), connection: ConnectionStale, health: HealthUnknown},
		{name: "offline", heartbeat: timePtr(now.Add(-6 * time.Minute)), connection: ConnectionOffline, health: HealthUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			connection, health := DeriveObservedState(now, tc.heartbeat, tc.host)
			if connection != tc.connection || health != tc.health {
				t.Fatalf("got (%s,%s), want (%s,%s)", connection, health, tc.connection, tc.health)
			}
		})
	}
}

func TestLifecycleTransitionsFailClosed(t *testing.T) {
	if err := ValidateLifecycleTransition("active", "planned"); err == nil {
		t.Fatal("active -> planned must fail")
	}
	if err := ValidateLifecycleTransition("failed", "provisioning"); err != nil {
		t.Fatalf("failed -> provisioning should allow retry: %v", err)
	}
	if MutationsAllowed("stale") || MutationsAllowed("offline") || !MutationsAllowed("connected") {
		t.Fatal("mutation connection policy is incorrect")
	}
}

func TestHasDecommissionIntentIncludesTerminalTombstone(t *testing.T) {
	tests := []struct {
		state LifecycleState
		want  bool
	}{
		{state: LifecycleActive, want: false},
		{state: LifecycleFailed, want: false},
		{state: LifecycleDecommissioning, want: true},
		{state: LifecycleDecommissioned, want: true},
	}
	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			if got := HasDecommissionIntent(tc.state); got != tc.want {
				t.Fatalf("HasDecommissionIntent(%q) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

func timePtr(value time.Time) *time.Time { return &value }
