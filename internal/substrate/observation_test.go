package substrate

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Authenticated heartbeat endpoint evidence must come from the exact managed
// guest's native QGA, never from imported guests or caller-supplied addresses.
func TestOwnedGuestEndpointEvidencePreservesIdentity(t *testing.T) {
	identity := GuestIdentity{ID: 1100, Name: "kombify-home", OperationTag: "kombify-op-" + strings.Repeat("a", 32)}
	tags := "kombify-managed;" + identity.OperationTag
	guest := Guest{ID: 1100, Name: identity.Name, Status: "running", Tags: tags}
	foreign := Guest{ID: 1101, Name: "existing-home", Status: "running"}
	tampered := false
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("inventory attempted native mutation")
		}
		var data any
		switch r.URL.Path {
		case "/api2/json/access/permissions":
			data = map[string]any{"/vms/1100": map[string]int{"VM.Audit": 1}}
		case "/api2/json/nodes/pve/qemu":
			data = []Guest{guest, foreign}
		case "/api2/json/nodes/pve/qemu/1100/config":
			currentTags := tags
			if tampered {
				currentTags = "imported"
			}
			data = map[string]any{"name": identity.Name, "tags": currentTags, "description": "kombify-spec-sha256:" + strings.Repeat("b", 64), "scsi0": "local:vm-1100-disk-0"}
		case "/api2/json/nodes/pve/qemu/1100/agent/network-get-interfaces":
			data = map[string]any{"result": []any{map[string]any{"ip-addresses": []any{map[string]any{"ip-address": "192.168.4.12"}, map[string]any{"ip-address": "127.0.0.1"}}}}}
		default:
			t.Errorf("unowned endpoint queried: %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	inventory := Inventory{Guests: []Guest{guest, foreign}}
	evidence := client.ObserveOwnedGuests(t.Context(), inventory)
	bound, ok := evidence[1100]
	if !ok || bound.Identity != identity || bound.ObservedAt.IsZero() || !bound.Observation.Present {
		t.Fatalf("owned guest identity lost: %+v", evidence)
	}
	found := false
	for _, ip := range bound.Observation.Addresses {
		if ip == "192.168.4.12" {
			found = true
		}
		if ip == "127.0.0.1" {
			t.Fatal("loopback emitted as guest evidence")
		}
	}
	if !found {
		t.Fatal("native QGA guest address unavailable")
	}
	if _, ok := evidence[1101]; ok {
		t.Fatal("imported guest received endpoint ownership evidence")
	}
	tampered = true
	if _, ok := client.ObserveOwnedGuests(t.Context(), inventory)[1100]; ok {
		t.Fatal("stale list identity bypassed current guest ownership")
	}
}
