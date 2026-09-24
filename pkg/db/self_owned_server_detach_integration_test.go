package db_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	pgkdb "github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func TestIntegrationSelfOwnedServerDetachRevokesAndTombstonesAtomically(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	database, err := pgkdb.Open(pgkdb.Config{Backend: pgkdb.StoreBackendPostgres, DSN: dsn, DriverName: pgkdb.PostgresDriverName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := controlplane.NewPostgresStore(database.DB)
	const tenantID, ownerID, serverID, agentID = "tenant-detach", "owner-detach", "server-detach", "agent-detach"
	if _, err := store.EnsureTenant(t.Context(), controlplane.Tenant{ID: tenantID, DisplayName: tenantID, Kind: "self_hosted", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "stack-detach", TenantID: tenantID, OwnerSubjectID: ownerID, Name: "Detach"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{ID: agentID, TenantID: tenantID, StackID: "stack-detach", OwnerSubjectID: ownerID, Hostname: "byo", Status: "connected", Approved: true, ApprovedAt: &now, LastSeenAt: &now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyServerEvent(t.Context(), controlplane.ServerEvent{
		TenantID: tenantID, ServerID: serverID, Generation: 1,
		Authority: controlplane.ServerEventAuthorityControlPlane, Source: "owner-pairing", SourceID: ownerID, ObservedAt: now,
		Runtime: controlplane.ServerRuntime{
			StackID: "stack-detach", OwnerSubjectID: ownerID, WorkerID: agentID, Name: "BYO",
			LifecycleState: string(serverregistry.LifecycleActive), DesiredState: string(serverregistry.DesiredRunning),
			ConnectionState: string(serverregistry.ConnectionPending), HealthState: string(serverregistry.HealthUnknown),
			RuntimeTarget: serverregistry.RuntimeTarget{
				EnvironmentClass: serverregistry.EnvironmentLocal, Offering: serverregistry.OfferingSelfOwnedDevice,
				AvailabilityOwner: serverregistry.AvailabilityCustomer, OperationsOwner: serverregistry.OperationsCustomer,
				EvidenceRef: "owner-pairing:agent-detach", ObservedAt: &now,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	enrollments := grpcserver.NewPostgresAgentEnrollmentStore(database)
	if err := enrollments.Upsert(t.Context(), grpcserver.AgentEnrollment{AgentID: agentID, TenantID: tenantID, CertSerial: "serial-detach", CertFingerprint: "fingerprint-detach", EnrolledAt: now, EnrolledBy: ownerID}); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.DetachSelfOwnedServer(t.Context(), controlplane.SelfOwnedServerDetachRequest{TenantID: tenantID, OwnerSubjectID: ownerID, ServerID: serverID, ConfirmServerID: serverID})
	if err != nil {
		t.Fatal(err)
	}
	server, err := store.GetServerRuntime(t.Context(), tenantID, serverID)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := enrollments.Get(t.Context(), tenantID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.GetWorker(t.Context(), tenantID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Replay || receipt.AgentID != agentID || server.LifecycleState != string(serverregistry.LifecycleDecommissioned) || server.DesiredState != string(serverregistry.DesiredAbsent) || server.DecommissionedAt == nil || enrollment.RevokedAt == nil || worker.Status != "revoked" {
		t.Fatalf("receipt=%+v server=%+v enrollment=%+v worker=%+v", receipt, server, enrollment, worker)
	}
	replay, err := store.DetachSelfOwnedServer(t.Context(), controlplane.SelfOwnedServerDetachRequest{TenantID: tenantID, OwnerSubjectID: ownerID, ServerID: serverID, ConfirmServerID: serverID})
	if err != nil || !replay.Replay || replay.Revision != receipt.Revision {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}
