package homeassistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

// Native API boundary: existing/imported do not write, and a user change after
// first application permanently relinquishes that field across process restart.
func TestBaselinePreservesUserChangesAndImportedInstances(t *testing.T) {
	config := map[string]any{"version": "2026.9.1", "time_zone": "UTC", "language": "en"}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			_ = json.NewEncoder(w).Encode(config)
		case "/core/info":
			_, _ = w.Write([]byte(`{"result":"ok","data":{"version":"2026.9.1","arch":"amd64"}}`))
		case "/os/info":
			_, _ = w.Write([]byte(`{"result":"ok","data":{"version":"16.2"}}`))
		case "/api/websocket":
			upgrader := websocket.Upgrader{}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(map[string]any{"type": "auth_required"})
			var msg map[string]any
			if conn.ReadJSON(&msg) != nil {
				return
			}
			_ = conn.WriteJSON(map[string]any{"type": "auth_ok"})
			if conn.ReadJSON(&msg) != nil {
				return
			}
			if msg["type"] != "config/core/update" {
				t.Error("unexpected mutation")
				return
			}
			for _, field := range []string{"language", "time_zone", "internal_url", "external_url"} {
				if v, ok := msg[field]; ok {
					config[field] = v
					writes++
				}
			}
			_ = conn.WriteJSON(map[string]any{"id": msg["id"], "type": "result", "success": true, "result": nil})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	core, err := NewLocalClient(ctx, server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	supervisor, err := NewLocalSupervisor(ctx, server.URL, "token", Grants{})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	custody := &memoryCustody{receipts: map[string]map[string]any{}, keys: map[string]string{}}
	makeBinding := func(origin, scope string) *InstanceBinding {
		return &InstanceBinding{BindingRef: "exact-instance", Origin: origin, ManagementScope: scope, Core: core, Supervisor: supervisor, Custody: custody, ConfigureGranted: true, Settings: BaselineSettings{Language: "de", TimeZone: "Europe/Berlin"}}
	}
	for _, origin := range []string{"existing", "imported"} {
		if _, err = makeBinding(origin, "managed").ReconcileBaseline(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 0 {
		t.Fatal("import mutated native source")
	}
	if _, err = makeBinding("new", "managed").ReconcileBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	if config["language"] != "de" || config["time_zone"] != "Europe/Berlin" {
		t.Fatal("native settings were not applied")
	}
	config["language"] = "fr"
	before := writes
	restarted := makeBinding("new", "managed")
	restarted.Settings.Language = "it"
	if _, err = restarted.ReconcileBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	if config["language"] != "fr" || writes != before {
		t.Fatal("reconciliation overwrote user configuration")
	}
	denied := makeBinding("new", "managed")
	denied.ConfigureGranted = false
	if _, err = denied.ReconcileBaseline(ctx); err == nil || writes != before {
		t.Fatal("read token conferred mutation authority")
	}
}
