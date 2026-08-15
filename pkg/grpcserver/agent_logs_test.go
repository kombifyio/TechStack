package grpcserver

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
)

func TestAgentLogs_SubscribeAndGet(t *testing.T) {
	srv := &Server{
		agents: make(map[string]*ConnectedAgent),
	}
	srv.agents["agent-1"] = &ConnectedAgent{ID: "agent-1"}

	ch, cancel, err := srv.SubscribeAgentLogs("agent-1", 10)
	if err != nil {
		t.Fatalf("SubscribeAgentLogs returned error: %v", err)
	}
	defer cancel()

	now := time.Now().UTC()
	srv.addAgentLog("agent-1", AgentLogEntry{Timestamp: now, Level: "info", Message: "hello"})

	select {
	case entry := <-ch:
		if entry.Message != "hello" {
			t.Fatalf("expected message 'hello', got %q", entry.Message)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for log entry")
	}

	logs, ok := srv.GetAgentLogs("agent-1", 200)
	if !ok {
		t.Fatal("expected ok=true for existing agent")
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Message != "hello" {
		t.Fatalf("expected message 'hello', got %q", logs[0].Message)
	}
}

func TestAgentLogs_UnknownAgent(t *testing.T) {
	srv := &Server{agents: make(map[string]*ConnectedAgent)}

	if _, ok := srv.GetAgentLogs("missing", 10); ok {
		t.Fatal("expected ok=false for missing agent")
	}
	if _, _, err := srv.SubscribeAgentLogs("missing", 10); err == nil {
		t.Fatal("expected error for missing agent")
	}
}

func TestRuntimeLogs_PersistRedactAndQueryCorrelation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-logs.jsonl")
	log := logger.New("error", "text")
	now := time.Now().UTC().Truncate(time.Second)

	srv, err := New(Config{ListenAddr: ":0", RuntimeLogPath: path, RuntimeLogMaxEntries: 10}, log)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	registerErr := srv.RegisterAgent(&ConnectedAgent{ID: "agent-1", Hostname: "vm-1", Status: agentStatusConnected})
	if registerErr != nil {
		t.Fatalf("RegisterAgent() failed: %v", registerErr)
	}
	srv.addAgentLog("agent-1", AgentLogEntry{
		Timestamp: now,
		Level:     "error",
		Message:   "rollout failed password=super-secret-value",
		Fields: map[string]string{
			"stack_id":          "stack-1",
			"job_id":            "job-1",
			"lease_id":          "lease-1",
			"provider":          "ionos-managed",
			"runtime_action_id": "rollout-1",
			"service.name":      "stackkit-hub",
			"client_secret":     "client_secret=abcdef0123456789",
		},
	})

	logs := srv.GetRuntimeLogs(RuntimeLogQuery{StackID: "stack-1", Limit: 10})
	if len(logs) != 1 {
		t.Fatalf("GetRuntimeLogs() len = %d, want 1", len(logs))
	}
	entry := logs[0]
	if entry.AgentID != "agent-1" || entry.StackID != "stack-1" || entry.JobID != "job-1" || entry.LeaseID != "lease-1" {
		t.Fatalf("runtime log correlation missing: %+v", entry)
	}
	if entry.Provider != "ionos-managed" || entry.RuntimeActionID != "rollout-1" || entry.ServiceName != "stackkit-hub" {
		t.Fatalf("runtime log runtime metadata missing: %+v", entry)
	}
	if strings.Contains(entry.Message, "super-secret-value") || strings.Contains(entry.Fields["client_secret"], "abcdef0123456789") {
		t.Fatalf("runtime log was not redacted: %+v", entry)
	}
	stopErr := srv.Stop()
	if stopErr != nil {
		t.Fatalf("Stop() failed: %v", stopErr)
	}

	reopened, err := New(Config{ListenAddr: ":0", RuntimeLogPath: path, RuntimeLogMaxEntries: 10}, log)
	if err != nil {
		t.Fatalf("reopen New() failed: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Stop() })
	persisted := reopened.GetRuntimeLogs(RuntimeLogQuery{LeaseID: "lease-1", Limit: 10})
	if len(persisted) != 1 {
		t.Fatalf("reopened GetRuntimeLogs() len = %d, want 1", len(persisted))
	}
	if persisted[0].StackID != "stack-1" || persisted[0].Source != "agent-grpc" {
		t.Fatalf("persisted runtime log lost metadata: %+v", persisted[0])
	}
}

func TestRuntimeLogsAreTenantScopedAndCursorBound(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	buffer := newMemoryRuntimeLogBuffer(10)
	first := buffer.Append(AgentLogEntry{ID: "log-1", TenantID: "tenant-a", Timestamp: now, StackID: "stack-1", ServerID: "server-1", ServiceID: "service-1", Level: "info"})
	second := buffer.Append(AgentLogEntry{ID: "log-2", TenantID: "tenant-a", Timestamp: now.Add(time.Second), RuntimeTargetID: "worker-1", ServiceID: "service-2", Level: "info"})
	buffer.Append(AgentLogEntry{ID: "log-foreign", TenantID: "tenant-b", Timestamp: now.Add(2 * time.Second), ServerID: "server-foreign", Level: "info"})

	if first.RuntimeScopeKey != "server:server-1" || first.ServerScopeKey != "server-1" || first.ServiceScopeKey != "service-1" || first.ScopeStatus != "scoped" {
		t.Fatalf("unexpected server scopes: %#v", first)
	}
	if second.RuntimeScopeKey != "managed_target:worker-1" || second.ServerScopeKey != "" || second.ScopeStatus != "scoped" {
		t.Fatalf("unexpected managed target scopes: %#v", second)
	}

	logs := buffer.Query(RuntimeLogQuery{TenantID: "tenant-a", Cursor: "log-2", Limit: 10})
	if len(logs) != 1 || logs[0].ID != "log-1" {
		t.Fatalf("tenant/cursor query returned %#v, want only log-1", logs)
	}
	if foreign := buffer.Query(RuntimeLogQuery{TenantID: "tenant-b", ServerID: "server-1", Limit: 10}); len(foreign) != 0 {
		t.Fatalf("tenant filter leaked server log: %#v", foreign)
	}
}
