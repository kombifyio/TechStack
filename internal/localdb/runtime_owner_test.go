package localdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
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
