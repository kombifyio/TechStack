package routes

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRILCommandRequestJSON(t *testing.T) {
	input := `{"command":"systemctl restart nginx","description":"Restart web server","timeout":30}`
	var req RILCommandRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Command != "systemctl restart nginx" {
		t.Errorf("command = %q, want systemctl restart nginx", req.Command)
	}
	if req.Description != "Restart web server" {
		t.Errorf("description = %q", req.Description)
	}
	if req.Timeout != 30 {
		t.Errorf("timeout = %d, want 30", req.Timeout)
	}
}

func TestRILCommandResponseJSON(t *testing.T) {
	now := time.Now().UTC()
	exitCode := 0
	completed := now.Add(2 * time.Second)

	resp := RILCommandResponse{
		CommandID:    "cmd-123",
		ServerID:     "srv-1",
		Command:      "uptime",
		Status:       "completed",
		Output:       "up 42 days",
		ExitCode:     &exitCode,
		DispatchedAt: now,
		CompletedAt:  &completed,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out["command_id"] != "cmd-123" {
		t.Errorf("command_id = %v", out["command_id"])
	}
	if out["status"] != "completed" {
		t.Errorf("status = %v", out["status"])
	}
	if out["output"] != "up 42 days" {
		t.Errorf("output = %v", out["output"])
	}
	if out["exit_code"].(float64) != 0 {
		t.Errorf("exit_code = %v", out["exit_code"])
	}
}

func TestRILCommandResponseOmitsEmpty(t *testing.T) {
	resp := RILCommandResponse{
		CommandID:    "cmd-456",
		ServerID:     "srv-2",
		Command:      "ls",
		Status:       "queued",
		DispatchedAt: time.Now().UTC(),
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out map[string]any
	json.Unmarshal(data, &out)

	if _, ok := out["output"]; ok {
		if out["output"] != "" {
			t.Error("output should be empty or omitted")
		}
	}
	if _, ok := out["exit_code"]; ok {
		t.Error("exit_code should be omitted when nil")
	}
	if _, ok := out["completed_at"]; ok {
		t.Error("completed_at should be omitted when nil")
	}
}

func TestRILSearchRequestJSON(t *testing.T) {
	input := `{"query":"redis","scope":"services","servers":["srv-1","srv-2"]}`
	var req RILSearchRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Query != "redis" {
		t.Errorf("query = %q", req.Query)
	}
	if req.Scope != "services" {
		t.Errorf("scope = %q", req.Scope)
	}
	if len(req.Servers) != 2 {
		t.Errorf("servers len = %d, want 2", len(req.Servers))
	}
}

func TestRILSearchResultJSON(t *testing.T) {
	result := RILSearchResult{
		ServerID: "srv-1",
		Source:   "systemd",
		Line:     "redis-server.service - Redis",
		Context:  "active (running)",
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out map[string]any
	json.Unmarshal(data, &out)

	if out["server_id"] != "srv-1" {
		t.Errorf("server_id = %v", out["server_id"])
	}
	if out["source"] != "systemd" {
		t.Errorf("source = %v", out["source"])
	}
}

func TestRILCommandStatuses(t *testing.T) {
	validStatuses := []string{
		"dispatched", "queued", "executing", "completed",
		"failed", "agent_offline", "queue_error",
	}

	for _, s := range validStatuses {
		resp := RILCommandResponse{Status: s}
		data, _ := json.Marshal(resp)
		var out map[string]any
		json.Unmarshal(data, &out)
		if out["status"] != s {
			t.Errorf("status roundtrip failed for %q", s)
		}
	}
}
