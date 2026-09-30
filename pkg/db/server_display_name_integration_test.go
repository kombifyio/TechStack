package db_test

import (
	"os"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	pgkdb "github.com/kombifyio/techstack/pkg/db"
)

// Runtime projections overwrite servers.name on every upsert; the owner's
// rename lives in servers.display_name and must survive them (migration 123).
func TestIntegrationRuntimeProjectionUpsertPreservesOwnerRename(t *testing.T) {
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
	const tenantID, ownerID, serverID = "tenant-rename", "owner-rename", "server-rename"
	if _, err := store.EnsureTenant(t.Context(), controlplane.Tenant{ID: tenantID, DisplayName: tenantID, Kind: "self_hosted", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	projection := controlplane.ServerRuntime{ID: serverID, TenantID: tenantID, OwnerSubjectID: ownerID, Name: "worker-a1b2"}
	if _, err := store.UpsertServerRuntime(t.Context(), projection); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenameInventoryServer(t.Context(), tenantID, ownerID, serverID, "Basement NAS"); err != nil {
		t.Fatal(err)
	}

	projection.Name = "worker-c3d4"
	if _, err := store.UpsertServerRuntime(t.Context(), projection); err != nil {
		t.Fatal(err)
	}
	server, err := store.GetServerRuntime(t.Context(), tenantID, serverID)
	if err != nil {
		t.Fatal(err)
	}
	if server.DisplayName != "Basement NAS" || server.Name != "worker-c3d4" {
		t.Fatalf("after projection upsert: display_name=%q name=%q, want the rename kept and the projection applied", server.DisplayName, server.Name)
	}
}
