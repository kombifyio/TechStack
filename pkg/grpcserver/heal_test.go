package grpcserver

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/logger"
)

// Regression: the public ReportHealEvent RPC previously inherited the
// generated unimplemented handler while the HTTP audit read PocketBase.
func TestReportHealEventPersistsCanonicalTenantEvent(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertRILServer(t.Context(), controlplane.RILServer{ID: "server-1", TenantID: "tenant-1", NodeID: "agent-1"}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{ListenAddr: ":0"}, logger.Get())
	if err != nil {
		t.Fatal(err)
	}
	srv.SetRILHealStore(store)
	if err := srv.RegisterAgent(&ConnectedAgent{ID: "agent-1", Tenant: "tenant-1"}); err != nil {
		t.Fatal(err)
	}
	ack, err := srv.ReportHealEvent(context.Background(), &agentpb.HealEventReport{AgentId: "agent-1", RecipeName: "service-restart", Success: true})
	events, listErr := store.ListHealEvents(t.Context(), "tenant-1", "server-1")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if err != nil || !ack.GetReceived() || len(events) != 1 || events[0].Status != "succeeded" {
		t.Fatalf("canonical heal event not persisted: ack=%v events=%v err=%v", ack, events, err)
	}
}
