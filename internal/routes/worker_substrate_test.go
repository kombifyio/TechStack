package routes

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

// Provider-control boundary: an ordinary Node capability cannot enroll a
// hypervisor, and a hypervisor must not advertise StackKit execution authority.
func TestSubstrateEnrollmentRequiresMatchingOwnerCapability(t *testing.T) {
	for _, role := range []string{"worker", "substrate"} {
		t.Run(role, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			raw, hash, err := pairingtoken.Generate("tenant-1")
			if err != nil {
				t.Fatal(err)
			}
			expires := time.Now().Add(time.Hour)
			_, err = store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
				ID: "pairing", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "deployment-1",
				TokenHash: hash, Status: "active", ExpiresAt: &expires,
				Metadata: map[string]any{"server_node_role": role, "runtime_environment_class": "local"},
			})
			if err != nil {
				t.Fatal(err)
			}
			handler := workerRouteHandlers{wst: store, serverStore: store}
			event, response := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/register",
				`{"token":"`+raw+`","hostname":"hypervisor","os":"linux","arch":"amd64","type":"substrate","provider":"proxmox"}`)
			if err := handler.register(event); err != nil {
				t.Fatal(err)
			}
			if role == "worker" {
				if response.Code != http.StatusBadRequest {
					t.Fatalf("mismatched enrollment status=%d", response.Code)
				}
				token, err := store.GetPairingTokenByHash(t.Context(), "tenant-1", hash)
				if err != nil || token.UsedAt != nil {
					t.Fatal("mismatch consumed the owner's capability")
				}
				return
			}
			if response.Code != http.StatusOK {
				t.Fatalf("substrate enrollment status=%d", response.Code)
			}
			var result struct {
				Data struct {
					ServerID  string `json:"server_id"`
					GuardRole string `json:"guard_role"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Data.GuardRole != "substrate" {
				t.Fatal("hypervisor received an ordinary Guard role")
			}
			server, err := store.GetServerRuntime(t.Context(), "tenant-1", result.Data.ServerID)
			if err != nil {
				t.Fatal(err)
			}
			server.LifecycleState = "active"
			server.ConnectionState = "connected"
			if len(serverStackActions(*server)) != 0 {
				t.Fatal("hypervisor can receive StackKit actions")
			}
		})
	}
}
