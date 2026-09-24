package homeassistant_test

import (
	"context"
	"github.com/kombifyio/techstack/internal/homeassistant"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Native admin WebSocket authorization is separate from a successful REST read.
func TestNativeCoreBackupAndExplicitSupervisorBridge(t *testing.T) {
	generated := false
	upgrade := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/config" {
			_, _ = w.Write([]byte(`{"version":"2026.9.1","components":["backup"]}`))
			return
		}
		if r.URL.Path != "/api/websocket" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		conn, err := upgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
		var auth map[string]any
		if conn.ReadJSON(&auth) != nil || auth["access_token"] != "admin-token" {
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
		var command map[string]any
		if conn.ReadJSON(&command) != nil {
			return
		}
		var result any
		switch command["type"] {
		case "backup/agents/info":
			result = map[string]any{"agents": []any{map[string]any{"agent_id": "backup.local"}}}
		case "backup/generate":
			if command["password"] == "" || command["include_homeassistant"] != true || command["include_database"] != true {
				t.Error("incomplete or unencrypted native backup request")
			}
			generated = true
			result = map[string]any{"backup_job_id": "native-job"}
		case "supervisor/api":
			switch command["endpoint"] {
			case "/core/info":
				result = map[string]any{"version": "2026.9.1", "arch": "amd64"}
			case "/os/info":
				result = map[string]any{"version": "16.2"}
			default:
				t.Error("unexpected supervisor operation")
			}
		default:
			t.Errorf("unexpected native command %v", command["type"])
		}
		_ = conn.WriteJSON(map[string]any{"id": command["id"], "type": "result", "success": true, "result": result})
	}))
	defer server.Close()
	core, err := homeassistant.NewLocalClient(context.Background(), server.URL, "admin-token")
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	backup := &homeassistant.CoreBackup{Client: core, AgentID: "backup.local"}
	if _, err = backup.SubmitFullBackup(context.Background(), "operation", "password"); err == nil || generated {
		t.Fatal("ordinary Core read authority granted backup")
	}
	backup.BackupGranted = true
	if _, err = backup.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, err := backup.SubmitFullBackup(context.Background(), "operation", "password")
	if err != nil || out.JobID != "native-job" || !generated {
		t.Fatal("native backup submission not accepted")
	}
	supervisor, err := homeassistant.NewSupervisorViaCore(context.Background(), server.URL, "admin-token", homeassistant.Grants{})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	observed, err := supervisor.Observe(context.Background())
	if err != nil || observed.OSVersion != "16.2" || observed.Architecture != "amd64" {
		t.Fatalf("native Supervisor bridge observation failed: %v", err)
	}
}
