package db_test

import (
	"errors"
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
	if _, err := store.IssueWorkerIdentity(t.Context(), controlplane.WorkerIdentityIssue{
		TenantID: tenantID, WorkerID: agentID, AttemptID: "attempt-detach-0123456789", Thumbprint: "thumb-detach",
		PublicKeyJWK: `{"crv":"Ed25519","kty":"OKP","x":"detach"}`, IssuedAt: now,
	}); err != nil {
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
	if identity, err := store.GetWorkerIdentity(t.Context(), tenantID, agentID); err != nil || identity.State != controlplane.WorkerIdentityRevoked {
		t.Fatalf("detach left the Guard host key active: %+v, %v", identity, err)
	}
	replay, err := store.DetachSelfOwnedServer(t.Context(), controlplane.SelfOwnedServerDetachRequest{TenantID: tenantID, OwnerSubjectID: ownerID, ServerID: serverID, ConfirmServerID: serverID})
	if err != nil || !replay.Replay || replay.Revision != receipt.Revision {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

// An HTTPS Guard is bound by its worker row, credential and host key only: it
// never has a gRPC enrollment, and detach must still revoke it.
func TestIntegrationSelfOwnedServerDetachRevokesHostKeyGuardWithoutAgentEnrollment(t *testing.T) {
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
	const tenantID, ownerID, serverID, agentID = "tenant-detach-hk", "owner-detach-hk", "server-detach-hk", "agent-detach-hk"
	if _, err := store.EnsureTenant(t.Context(), controlplane.Tenant{ID: tenantID, DisplayName: tenantID, Kind: "self_hosted", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "stack-detach-hk", TenantID: tenantID, OwnerSubjectID: ownerID, Name: "Detach"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{ID: agentID, TenantID: tenantID, StackID: "stack-detach-hk", OwnerSubjectID: ownerID, Hostname: "byo", Status: "connected", Approved: true, ApprovedAt: &now, LastSeenAt: &now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyServerEvent(t.Context(), controlplane.ServerEvent{
		TenantID: tenantID, ServerID: serverID, Generation: 1,
		Authority: controlplane.ServerEventAuthorityControlPlane, Source: "owner-pairing", SourceID: ownerID, ObservedAt: now,
		Runtime: controlplane.ServerRuntime{
			StackID: "stack-detach-hk", OwnerSubjectID: ownerID, WorkerID: agentID, Name: "BYO",
			LifecycleState: string(serverregistry.LifecycleActive), DesiredState: string(serverregistry.DesiredRunning),
			ConnectionState: string(serverregistry.ConnectionPending), HealthState: string(serverregistry.HealthUnknown),
			RuntimeTarget: serverregistry.RuntimeTarget{
				EnvironmentClass: serverregistry.EnvironmentLocal, Offering: serverregistry.OfferingSelfOwnedDevice,
				AvailabilityOwner: serverregistry.AvailabilityCustomer, OperationsOwner: serverregistry.OperationsCustomer,
				EvidenceRef: "owner-pairing:" + agentID, ObservedAt: &now,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IssueWorkerIdentity(t.Context(), controlplane.WorkerIdentityIssue{
		TenantID: tenantID, WorkerID: agentID, AttemptID: "attempt-detach-hk-0123456789", Thumbprint: "thumb-detach-hk",
		PublicKeyJWK: `{"crv":"Ed25519","kty":"OKP","x":"detachhk"}`, IssuedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	enrollments := grpcserver.NewPostgresAgentEnrollmentStore(database)
	if _, err := enrollments.Get(t.Context(), tenantID, agentID); err == nil {
		t.Fatal("precondition: the host-key Guard must have no gRPC agent enrollment")
	}

	receipt, err := store.DetachSelfOwnedServer(t.Context(), controlplane.SelfOwnedServerDetachRequest{TenantID: tenantID, OwnerSubjectID: ownerID, ServerID: serverID, ConfirmServerID: serverID})
	if err != nil {
		t.Fatalf("detach of a host-key Guard without an agent enrollment: %v", err)
	}
	if receipt.Replay || receipt.AgentID != agentID {
		t.Fatalf("receipt = %+v", receipt)
	}
	// The Guard's next authenticated request is refused: the worker row no
	// longer authenticates and the host key can neither confirm nor re-pair.
	worker, err := store.GetWorker(t.Context(), tenantID, agentID)
	if err != nil || worker.Status != "revoked" {
		t.Fatalf("detach left the Guard worker authenticating: %+v, %v", worker, err)
	}
	if _, err := store.ConfirmWorkerIdentity(t.Context(), tenantID, agentID, "thumb-detach-hk", now); !errors.Is(err, controlplane.ErrWorkerIdentityRevoked) {
		t.Fatalf("revoked Guard host key was still accepted: %v", err)
	}
	server, err := store.GetServerRuntime(t.Context(), tenantID, serverID)
	if err != nil || server.LifecycleState != string(serverregistry.LifecycleDecommissioned) {
		t.Fatalf("server = %+v, %v", server, err)
	}
}
