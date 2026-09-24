package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

// The sensitive adoption boundary must preserve existing managed records and
// perform only authenticated reads against a previously unlinked HA instance.
func TestHomeAssistantImportPreservesExistingAndNeverWritesSource(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	_, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "ha-stack", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Home"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpsertNode(ctx, controlplane.Node{ID: "ha-node", TenantID: "tenant-1", StackID: "ha-stack", Name: "Home"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Error("source configuration mutation")
			w.WriteHeader(405)
			return
		}
		_, _ = w.Write([]byte(`{"version":"2026.9.1","components":[]}`))
	}))
	defer server.Close()
	body := map[string]any{"stack_id": "ha-stack", "server_id": "ha-node", "url": server.URL, "token": "not-for-storage", "installation_method": "container"}
	h := registryRouteHandlers{stackStore: store, registryStore: store, allowLocalHomeAssistant: true}
	for i := 0; i < 2; i++ {
		e, rr := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/home-assistant/import", "owner-1", "tenant-1", body)
		if err := h.importHomeAssistant(e); err != nil {
			t.Fatal(err)
		}
		if rr.Code != 200 {
			t.Fatalf("import status %d: %s", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "not-for-storage") {
			t.Fatal("token exposed")
		}
	}
	if calls != 1 {
		t.Fatal("reimport contacted an existing installation")
	}
	id := runtimeidentity.ServiceID("ha-stack", "ha-node", "home-assistant", "default")
	s, err := store.GetService(ctx, "tenant-1", id)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(s)
	if s.ManagementState != "observed" || strings.Contains(string(encoded), "not-for-storage") {
		t.Fatalf("unexpected adoption authority or secret: %s", encoded)
	}
	s.ManagementState = "managed"
	s.Source = "stackkits-inventory"
	s.Metadata = map[string]any{"user_customization": "preserve"}
	_, _ = store.UpsertService(ctx, *s)
	e, _ := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/home-assistant/import", "owner-1", "tenant-1", body)
	if err := h.importHomeAssistant(e); err != nil {
		t.Fatal(err)
	}
	s, _ = store.GetService(ctx, "tenant-1", id)
	if s.ManagementState != "managed" || s.Metadata["user_customization"] != "preserve" || calls != 1 {
		t.Fatal("reimport replaced existing user state")
	}
}
