package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
)

func TestNormalizeAgentCommandRequest(t *testing.T) {
	tests := []struct {
		name        string
		commandType string
		commandName string
		wantType    string
		wantCommand string
		wantErr     string
	}{
		{name: "health check", commandType: "health_check", commandName: "health_check", wantType: "health_check", wantCommand: "health_check"},
		{name: "trim and lower", commandType: " GET_LOGS ", commandName: " get_logs ", wantType: "get_logs", wantCommand: "get_logs"},
		{name: "mismatched type", commandType: "health_check", commandName: "get_logs", wantErr: "must match"},
		{name: "execute rejected", commandType: "execute", commandName: "execute", wantErr: "not allowed"},
		{name: "shell rejected", commandType: "execute", commandName: "/bin/sh", wantErr: "must match"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotCommand, err := normalizeAgentCommandRequest(tt.commandType, tt.commandName)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotType != tt.wantType || gotCommand != tt.wantCommand {
				t.Fatalf("got (%q, %q), want (%q, %q)", gotType, gotCommand, tt.wantType, tt.wantCommand)
			}
		})
	}
}

func TestAgentLogResponseIncludesCorrelationFields(t *testing.T) {
	got := agentLogResponse(grpcserver.AgentLogEntry{
		Timestamp:       time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
		AgentID:         "agent-1",
		Source:          "otlp-logs",
		Level:           "warning",
		Message:         "compose failed",
		StackID:         "stack-1",
		JobID:           "job-1",
		LeaseID:         "lease-1",
		Provider:        "ionos-managed",
		RuntimeActionID: "rollout-1",
		TenantID:        "tenant-1",
		ServerID:        "server-1",
		ServiceID:       "service-1",
		RuntimeScopeKey: "server:server-1",
		ServerScopeKey:  "server-1",
		ServiceScopeKey: "service-1",
		ScopeStatus:     "scoped",
		ServiceName:     "stackkit-hub",
		TraceID:         "trace-1",
		Fields: map[string]string{
			"container.id": "container-1",
		},
	})

	for _, key := range []string{"tenant_id", "agent_id", "source", "stack_id", "job_id", "lease_id", "provider", "runtime_action_id", "server_id", "service_id", "runtime_scope_key", "server_scope_key", "service_scope_key", "scope_status", "service_name", "trace_id", "fields"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("agentLogResponse() missing %q in %+v", key, got)
		}
	}
	if got["level"] != "warn" {
		t.Fatalf("level = %v, want warn", got["level"])
	}
}

func TestGetRuntimeLogsReturnsEmptyListWhenGRPCUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/logs?limit=5", nil)
	event := &httpx.Event{
		Request:  req,
		Response: rec,
		Auth:     &httpx.Principal{Id: "user-1"},
	}

	if err := (agentRouteHandler{}).getRuntimeLogs(event); err != nil {
		t.Fatalf("getRuntimeLogs() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if payload.Data == nil {
		t.Fatalf("data is nil; want empty list in body=%s", rec.Body.String())
	}
	if len(payload.Data) != 0 {
		t.Fatalf("data len = %d, want 0", len(payload.Data))
	}
}

func TestRuntimeLogQueryUsesTrustedTenantAndCanonicalScopes(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/logs?tenant_id=attacker&server_id=server-1&service_id=service-1&runtime_target_id=target-1&cursor=log-9", nil)
	query := runtimeLogQueryFromRequest(req, "tenant-trusted")
	if query.TenantID != "tenant-trusted" || query.ServerID != "server-1" || query.ServiceID != "service-1" || query.RuntimeTargetID != "target-1" || query.Cursor != "log-9" {
		t.Fatalf("unexpected scoped runtime log query: %#v", query)
	}
}
