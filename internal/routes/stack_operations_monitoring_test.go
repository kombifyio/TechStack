package routes

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

func TestBuildStackKPIsCountsOnlyOperationalResources(t *testing.T) {
	servers := []stackOperationServer{
		{Assignment: "stack", Health: stackServerHealth{State: "healthy"}},
		{Assignment: "stack", Health: stackServerHealth{State: "degraded"}},
		{Assignment: "unassigned", Health: stackServerHealth{State: "healthy"}},
	}
	services := []stackOperationService{
		{Status: "healthy"},
		{Status: "running"},
		{Status: "degraded"},
	}
	alerts := []stackOperationAlertState{{Name: "HighCPU"}, {Name: "DiskFull"}}

	got := buildStackKPIs(servers, services, alerts)
	if got.RegisteredServers != 3 || got.HealthyServers != 1 || got.RunningServices != 2 || got.ActiveAlerts != 2 {
		t.Fatalf("unexpected KPIs: %#v", got)
	}
}

func TestAlertBelongsToStackUsesFirstRecognizedScope(t *testing.T) {
	scope := stackAlertServerScope([]stackOperationServer{
		{ID: " server-a ", Hostname: " Node-A ", AgentID: " agent-a ", Assignment: "stack"},
		{ID: "server-b", Hostname: "node-b", AgentID: "agent-b", Assignment: "unassigned"},
	})

	tests := []struct {
		name       string
		labels     map[string]string
		wantBelong bool
		wantScoped bool
	}{
		{name: "no labels", labels: nil},
		{name: "unknown labels", labels: map[string]string{"service": "api"}},
		{name: "stack match", labels: map[string]string{"stack_id": "stack-1"}, wantBelong: true, wantScoped: true},
		{name: "stack mismatch wins over matching agent", labels: map[string]string{"stack_id": "stack-2", "agent_id": "agent-a"}, wantScoped: true},
		{name: "agent match", labels: map[string]string{"agent_id": "agent-a"}, wantBelong: true, wantScoped: true},
		{name: "first agent alias wins", labels: map[string]string{"agent_id": "agent-other", "agent": "agent-a"}, wantScoped: true},
		{name: "worker match", labels: map[string]string{"worker_id": "server-a"}, wantBelong: true, wantScoped: true},
		{name: "hostname match ignores case", labels: map[string]string{"hostname": "NODE-A"}, wantBelong: true, wantScoped: true},
		{name: "unassigned server is outside stack", labels: map[string]string{"agent_id": "agent-b"}, wantScoped: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotBelong, gotScoped := alertBelongsToStack(test.labels, "stack-1", scope)
			if gotBelong != test.wantBelong || gotScoped != test.wantScoped {
				t.Fatalf("alertBelongsToStack() = (%t, %t), want (%t, %t)", gotBelong, gotScoped, test.wantBelong, test.wantScoped)
			}
		})
	}
}

// Regression for kombify-Techstack-xjo.1: server details fetched the newest 20
// entries for the whole stack and only then filtered by server, so activity
// from busy sibling servers hid the selected server's own older records.
func TestServerLogsReturnNewestEntriesForTheSelectedServerOnly(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := t.Context()
	base := time.Now().UTC().Truncate(time.Millisecond)

	// The selected server's own entries are the oldest in the stack.
	for i := range serverLogLimit {
		appendTestActivity(t, store, ctx, fmt.Sprintf("own-%02d", i), "stack-1",
			map[string]any{"server_id": "server-a"}, base.Add(time.Duration(i)*time.Second))
	}
	// Twice the page size of newer noise from a sibling server.
	for i := range serverLogLimit * 2 {
		appendTestActivity(t, store, ctx, fmt.Sprintf("sibling-%02d", i), "stack-1",
			map[string]any{"server_id": "server-b"}, base.Add(time.Duration(1000+i)*time.Second))
	}

	handlers := stackOperationsRouteHandlers{activityStore: store}
	logs := handlers.serverLogs(ctx, "tenant-1", "stack-1",
		stackOperationServer{ID: "server-a", Hostname: "node-a"})

	if len(logs) != serverLogLimit {
		t.Fatalf("serverLogs returned %d entries, want %d", len(logs), serverLogLimit)
	}
	previous := ""
	for _, entry := range logs {
		id, _ := entry["id"].(string)
		if !strings.HasPrefix(id, "own-") {
			t.Fatalf("serverLogs leaked an entry from another server: %q", id)
		}
		if previous != "" && id >= previous {
			t.Fatalf("serverLogs returned %q after %q, want newest first", id, previous)
		}
		previous = id
	}
}

// A managed runtime server carries a synthetic lease:<id> server id, so its
// lease id is a separate identity the scope key may hold.
func TestServerLogsMatchAgentAndLeaseIdentities(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := t.Context()
	base := time.Now().UTC().Truncate(time.Millisecond)

	appendTestActivity(t, store, ctx, "by-agent", "stack-1",
		map[string]any{"agent_id": "agent-a"}, base)
	appendTestActivity(t, store, ctx, "by-lease", "stack-1",
		map[string]any{"lease_id": "lease-a"}, base.Add(time.Second))
	appendTestActivity(t, store, ctx, "other-server", "stack-1",
		map[string]any{"server_id": "server-z"}, base.Add(2*time.Second))

	handlers := stackOperationsRouteHandlers{activityStore: store}
	logs := handlers.serverLogs(ctx, "tenant-1", "stack-1", stackOperationServer{
		ID: "lease:lease-a", AgentID: "agent-a", LeaseID: "lease-a",
		Source: managedRuntimeInventorySource,
	})

	got := make(map[string]bool, len(logs))
	for _, entry := range logs {
		id, _ := entry["id"].(string)
		got[id] = true
	}
	if !got["by-agent"] || !got["by-lease"] {
		t.Fatalf("serverLogs dropped an identity alias: %v", got)
	}
	if got["other-server"] {
		t.Fatalf("serverLogs leaked another server's activity: %v", got)
	}
}

func appendTestActivity(t *testing.T, store *controlplane.MemoryStore, ctx context.Context,
	id, stackID string, details map[string]any, at time.Time) {
	t.Helper()
	if _, err := store.AppendActivity(ctx, controlplane.ActivityEvent{
		ID: id, TenantID: "tenant-1", StackID: stackID, Action: "update",
		Details: details, CreatedAt: at,
	}); err != nil {
		t.Fatalf("append activity %s: %v", id, err)
	}
}
