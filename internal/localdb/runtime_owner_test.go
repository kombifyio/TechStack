package localdb

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/kombifyio/techstack/pkg/db"
)

// Regression (kombify-Techstack-8pzz): an existing local store whose schema was
// migrated by the bootstrap superuser must, after a restart, be migratable by
// the runtime DSN under the guard of migrations 153 to 155 (exact non-bypass
// owner of a FORCE RLS table and of the public schema).
func TestRestartedStoreRunsUnderNonBypassMigrationOwner(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(EnvEmbeddedPostgresDir, filepath.Join(dataDir, "postgres"))
	t.Setenv(EnvEmbeddedPostgresPort, "")
	t.Setenv(EnvEmbeddedPostgresBundleDir, "")

	first, err := StartEmbeddedPostgres(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, first.BootstrapDSN(), `CREATE TABLE public.legacy_stacks (tenant_id text NOT NULL);
		INSERT INTO public.legacy_stacks VALUES ('retained');
		CREATE FUNCTION public.legacy_wake() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}

	second, err := StartEmbeddedPostgres(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Stop(); err != nil {
			t.Error(err)
		}
	})
	exec(t, second.DSN(), `ALTER TABLE public.legacy_stacks ENABLE ROW LEVEL SECURITY;
		ALTER TABLE public.legacy_stacks FORCE ROW LEVEL SECURITY;
		DO $guard$
		DECLARE owner_oid oid := (SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user);
		BEGIN
		    IF NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE oid='public.legacy_stacks'::regclass
		        AND relowner=owner_oid AND relrowsecurity AND relforcerowsecurity)
		        OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_proc WHERE oid='public.legacy_wake()'::regprocedure AND proowner=owner_oid)
		        OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname='public' AND nspowner=owner_oid)
		        OR EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE oid=owner_oid AND (rolsuper OR rolbypassrls)) THEN
		        RAISE EXCEPTION 'requires the exact non-bypass migration owner and FORCE RLS' USING ERRCODE='42501';
		    END IF;
		END $guard$`)

	var visible int
	conn := connect(t, second.BootstrapDSN())
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.legacy_stacks").Scan(&visible); err != nil || visible != 1 {
		t.Fatalf("retained rows = %d, err = %v", visible, err)
	}
}

// Regression (kombify-Techstack-9d5s): the NOCREATEROLE runtime owner applies
// the whole migration chain, including 168 and 173, which adopt NOLOGIN system
// roles and grant pg_control_system(), on a fresh store and again after a
// restart re-prepares the store; the owner keeps its non-bypass posture.
func TestRuntimeOwnerAppliesTheFullMigrationChain(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(EnvEmbeddedPostgresDir, filepath.Join(dataDir, "postgres"))
	t.Setenv(EnvEmbeddedPostgresPort, "")
	t.Setenv(EnvEmbeddedPostgresBundleDir, "")
	// The native self-hosted boot: no hosted channel, edition or database pin.
	for _, name := range []string{db.EnvDatabaseMigrationMode, db.EnvReleaseChannel, db.EnvExpectedDatabaseIdentity,
		db.EnvAllowDBGenesis, "KOMBIFY_EDITION", "DEPLOYMENT_MODE"} {
		t.Setenv(name, "")
	}

	for start := 1; start <= 2; start++ {
		pg, err := StartEmbeddedPostgres(dataDir)
		if err != nil {
			t.Fatalf("start %d: %v", start, err)
		}
		store, err := db.Open(db.Config{Backend: db.StoreBackendPostgres, DSN: pg.DSN()})
		if err == nil {
			err = store.Migrate(t.Context())
			_ = store.Close()
		}
		if err == nil && start == 2 {
			err = verifyMigratedLocalPosture(t, pg.BootstrapDSN())
		}
		if stopErr := pg.Stop(); err == nil && stopErr != nil {
			err = fmt.Errorf("stop: %w", stopErr)
		}
		if err != nil {
			t.Fatalf("start %d: %v", start, err)
		}
	}
}

// verifyMigratedLocalPosture reads, as the bootstrap superuser, the ledger tail,
// the two adopted system roles (NOLOGIN, no membership) and the owner posture.
func verifyMigratedLocalPosture(t *testing.T, bootstrapDSN string) error {
	var ok bool
	err := connect(t, bootstrapDSN).QueryRow(t.Context(), `SELECT
	    (SELECT count(*) FROM public.schema_migrations
	        WHERE version IN ('168_household_projection_bindings.sql','173_platform_projection_system.sql')) = 2
	    AND (SELECT count(*) FROM pg_catalog.pg_roles r
	        WHERE r.rolname IN ('techstack_household_worker','techstack_platform_projection_system')
	          AND NOT (r.rolcanlogin OR r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolinherit)
	          AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members m WHERE r.oid IN (m.roleid, m.member))) = 2
	    AND EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = $1
	        AND NOT (rolsuper OR rolbypassrls OR rolcreaterole))`, embeddedRuntimeOwner).Scan(&ok)
	if err == nil && !ok {
		err = fmt.Errorf("migrated local store lacks 168/173, the exact system roles or the non-bypass owner posture")
	}
	return err
}

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func exec(t *testing.T, dsn, sql string) {
	t.Helper()
	if _, err := connect(t, dsn).Exec(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
}
