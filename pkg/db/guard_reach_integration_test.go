package db_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/pkg/controlplane"
	pgkdb "github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/guardreach"
)

// Reach state is tenant scoped, leased to one reconciler at a time, found by
// its Connect identity across tenants only through the definer function, and
// detaching a Guard in the control plane obliges the reconciler to revoke it.
func TestIntegrationGuardReachStateIsTenantScopedLeasedAndRevokedByDetach(t *testing.T) {
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
	reach, err := guardreach.NewPostgresStore(database.DB)
	if err != nil {
		t.Fatal(err)
	}
	identities := controlplane.NewPostgresStore(database.DB)

	suffix := uuid.NewString()
	tenantA, tenantB := "org_a"+suffix, "org_b"+suffix
	enroll := func(tenant, worker, connectTenant string) {
		t.Helper()
		if err := reach.Enroll(t.Context(), guardreach.Enrollment{
			TenantID: tenant, WorkerID: worker, ServerID: "server-" + worker, StackID: "stack-1", OwnerSubjectID: "auth0|owner",
			DisplayName: "Node", Placement: "byo", OwnerRoles: []string{"admin", "member"}, ConnectTenantID: connectTenant,
		}); err != nil {
			t.Fatal(err)
		}
	}
	enroll(tenantA, "worker-a"+suffix, "")
	enroll(tenantB, "worker-b"+suffix, "")

	rowA, err := reach.Get(t.Context(), tenantA, "worker-a"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if rowA.Desired != guardreach.DesiredActive || rowA.ConnectTenantID != tenantA || len(rowA.OwnerRoles) != 1 || rowA.OwnerRoles[0] != "admin" {
		t.Fatalf("enrolled row = %+v", rowA)
	}
	if _, err := reach.Get(t.Context(), tenantB, "worker-a"+suffix); !errors.Is(err, guardreach.ErrNotFound) {
		t.Fatalf("another tenant read the row: %v", err)
	}
	if listed, err := reach.ListByTenant(t.Context(), tenantA); err != nil || len(listed) != 1 || listed[0].WorkerID != "worker-a"+suffix {
		t.Fatalf("tenant list = %+v, %v", listed, err)
	}

	// The owner's own Gateway tenant, once observed, replaces a mapped one, and
	// a later mapped value never replaces an observed one.
	enroll(tenantA, "worker-a"+suffix, "usr:auth0|owner")
	enroll(tenantA, "worker-a"+suffix, "")
	if row, _ := reach.Get(t.Context(), tenantA, "worker-a"+suffix); row.ConnectTenantID != "usr:auth0|owner" {
		t.Fatalf("connect tenant = %q", row.ConnectTenantID)
	}

	// Every field the reconciler writes survives a round trip.
	retry := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	if _, err := reach.Update(t.Context(), tenantA, "worker-a"+suffix, func(r *guardreach.Row) bool {
		r.KeyThumbprint, r.CredentialEpoch, r.Generation = "thumb", 4, 7
		r.Me = guardreach.Target{State: guardreach.TargetRegistered, Generation: 7, Ref: "sh-x.kombify.me"}
		r.Connect = guardreach.Target{State: guardreach.TargetFailed, Generation: 7, ReasonCode: "connect_owner_conflict", RetryAt: &retry}
		r.Relay = guardreach.RelayReport{State: "connected", Generation: 7}
		r.Public = guardreach.PublicReach{Enabled: true, Service: "web", Scheme: "http", Port: 8080, EnabledBy: "auth0|owner"}
		r.NextAttemptAt = time.Now().Add(-time.Second)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	rowA, _ = reach.Get(t.Context(), tenantA, "worker-a"+suffix)
	if !rowA.Public.Enabled || rowA.Public.Service != "web" || rowA.Public.Port != 8080 || rowA.Public.EnabledBy != "auth0|owner" {
		t.Fatalf("public reach round trip = %+v", rowA.Public)
	}
	if rowB0, _ := reach.Get(t.Context(), tenantB, "worker-b"+suffix); rowB0.Public.Enabled {
		t.Fatalf("a freshly enrolled server is published: %+v", rowB0.Public)
	}
	if rowA.Generation != 7 || rowA.Me.Ref != "sh-x.kombify.me" || rowA.Connect.ReasonCode != "connect_owner_conflict" ||
		rowA.Connect.RetryAt == nil || !rowA.Connect.RetryAt.Equal(retry) || rowA.Relay.State != "connected" || len(rowA.OwnerRoles) != 1 {
		t.Fatalf("round trip = %+v", rowA)
	}

	// The definer listing sees due rows of every tenant; a lease hands a row to
	// exactly one reconciler.
	due, err := reach.ListDue(t.Context(), time.Now().UTC(), "", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, key := range due {
		seen[key.TenantID] = true
	}
	if !seen[tenantA] || !seen[tenantB] {
		t.Fatalf("due rows of both tenants expected, got %v", seen)
	}
	if _, claimed, err := reach.Claim(t.Context(), tenantA, "worker-a"+suffix, time.Now().UTC(), time.Minute); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	if _, claimed, _ := reach.Claim(t.Context(), tenantA, "worker-a"+suffix, time.Now().UTC(), time.Minute); claimed {
		t.Fatal("a leased row was claimed twice")
	}

	found, err := reach.FindByConnect(t.Context(), "usr:auth0|owner", "server-worker-a"+suffix)
	if err != nil || len(found) != 1 || found[0].TenantID != tenantA {
		t.Fatalf("find by connect = %+v, %v", found, err)
	}

	// The cursor only moves forward.
	name := "test-cursor-" + suffix
	if err := reach.AdvanceCursor(t.Context(), name, 10); err != nil {
		t.Fatal(err)
	}
	if err := reach.AdvanceCursor(t.Context(), name, 4); err != nil {
		t.Fatal(err)
	}
	if position, _ := reach.Cursor(t.Context(), name); position != 10 {
		t.Fatalf("cursor = %d, want 10", position)
	}

	// Detaching the Guard in the control plane marks the row revoked in the
	// same operation and makes it due at once.
	if err := identities.RevokeWorkerIdentity(t.Context(), tenantB, "worker-b"+suffix, time.Now()); err != nil {
		t.Fatal(err)
	}
	rowB, err := reach.Get(t.Context(), tenantB, "worker-b"+suffix)
	if err != nil || rowB.Desired != guardreach.DesiredRevoked || rowB.NextAttemptAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("row after detach = %+v, %v", rowB, err)
	}
}

// The cross-tenant definer readers are not executable by an arbitrary database
// role: a tenant-scoped NOBYPASSRLS connection that is not the owning
// application role is refused, while the owner keeps its access (F-18).
func TestIntegrationGuardReachDefinerReadersAreNotExecutableByOtherRoles(t *testing.T) {
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
	var currentUser, schema string
	if err := database.DB.QueryRowContext(t.Context(), `SELECT current_user, current_schema()`).Scan(&currentUser, &schema); err != nil {
		t.Fatal(err)
	}
	role := "guard_probe_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	for _, statement := range []string{
		`CREATE ROLE ` + role + ` NOLOGIN NOSUPERUSER NOBYPASSRLS`,
		`GRANT ` + role + ` TO "` + currentUser + `"`,
		`GRANT USAGE ON SCHEMA "` + schema + `" TO ` + role,
	} {
		if _, err := database.DB.ExecContext(t.Context(), statement); err != nil {
			t.Skipf("the integration role cannot create a probe role (%v)", err)
		}
	}
	t.Cleanup(func() {
		_, _ = database.DB.ExecContext(t.Context(), `REVOKE USAGE ON SCHEMA "`+schema+`" FROM `+role)
		_, _ = database.DB.ExecContext(t.Context(), `DROP ROLE `+role)
	})

	conn, err := database.DB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(t.Context(), `SET ROLE `+role); err != nil {
		t.Fatal(err)
	}
	_, listErr := conn.ExecContext(t.Context(), `SELECT tenant_id, worker_id FROM techstack_list_due_guard_reach(now(), '', '', 50)`)
	_, findErr := conn.ExecContext(t.Context(), `SELECT tenant_id, worker_id FROM techstack_find_guard_reach_by_connect('org', 'server')`)
	if listErr == nil || findErr == nil || !strings.Contains(listErr.Error(), "permission denied") || !strings.Contains(findErr.Error(), "permission denied") {
		t.Fatalf("a foreign role executed the definer readers: list=%v find=%v", listErr, findErr)
	}
	if _, err := conn.ExecContext(t.Context(), `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `SELECT tenant_id, worker_id FROM techstack_list_due_guard_reach(now(), '', '', 50)`); err != nil {
		t.Fatalf("the owning role lost access: %v", err)
	}
}
