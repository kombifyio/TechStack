package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// Sensitive migration boundary: the shared staging ledger must not be edited to
// manufacture partial-prefix, hole, duplicate or foreign-ledger failure cases.
func TestForwardBatchRejectsChangedLedger(t *testing.T) {
	batch, err := DeliveryForwardBatch()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]bool{}
	for _, artifact := range batch {
		pending[artifact.Version] = true
	}
	basis := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && !pending[entry.Name()] {
			basis = append(basis, entry.Name())
		}
	}
	check := func(applied []string) (int, error) {
		expected := map[string]bool{}
		for _, version := range basis {
			expected[version] = true
		}
		return compareForwardBatchLedger(expected, applied, batch)
	}
	for prefix := 0; prefix <= len(batch); prefix++ {
		applied := append([]string{}, basis...)
		for _, artifact := range batch[:prefix] {
			applied = append(applied, artifact.Version)
		}
		if observed, err := check(applied); err != nil || observed != prefix {
			t.Fatalf("committed prefix %d refused: %d %v", prefix, observed, err)
		}
	}
	for _, applied := range [][]string{append(append([]string{}, basis...), batch[1].Version),
		append(append([]string{}, basis...), batch[0].Version, batch[2].Version),
		append(append([]string{}, basis...), batch[0].Version, batch[0].Version),
		append(append([]string{}, basis...), "unexpected.sql"), basis[1:],
		append(append([]string{}, basis...), batch[0].Version, "999_newer.sql")} {
		if _, err := check(applied); !errors.Is(err, ErrForwardBatchLedger) {
			t.Fatalf("changed ledger admitted: %v", err)
		}
	}
	newer := append([]string{}, basis...)
	for _, artifact := range batch {
		newer = append(newer, artifact.Version)
	}
	if observed, err := check(append(newer, "999_newer.sql")); err != nil || observed != len(batch) {
		t.Fatalf("ledger of a newer deploy refused: %d %v", observed, err)
	}
	badHash := append([]ForwardMigrationArtifact{}, batch...)
	badHash[0].SHA256 = strings.Repeat("0", 64)
	for _, invalid := range [][]ForwardMigrationArtifact{nil, batch[:len(batch)-1],
		append(append([]ForwardMigrationArtifact{}, batch[:1]...), batch[2:]...), badHash,
		{{Version: "../164.sql", SHA256: strings.Repeat("0", 64)}}} {
		if _, err := forwardBatchBodies(invalid); err == nil {
			t.Fatal("invalid or incomplete manifest admitted")
		}
	}
}

// Sensitive migration boundary: the previous binary keeps serving after a
// database leg only when every newer ledger entry is a proven additive
// migration. Every embedded *.additive.sql must pass the allowlist.
func TestAdditiveMigrationsStayAdditive(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), AdditiveMigrationSuffix) {
			continue
		}
		if _, ok := migrationNumber(entry.Name()); !ok {
			t.Errorf("%s: additive migration needs a numbered lower-case name", entry.Name())
		}
		body, err := migrationsFS.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateAdditiveMigrationSQL(string(body)); err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
		}
	}

	admitted := []string{
		"SET LOCAL lock_timeout = '5s';\nCREATE TABLE guard_notes (id uuid PRIMARY KEY, note text NOT NULL);\nCREATE UNIQUE INDEX guard_notes_note ON guard_notes (note);\nALTER TABLE guard_notes ENABLE ROW LEVEL SECURITY;\nCREATE POLICY guard_notes_owner ON guard_notes USING (true);\nGRANT SELECT, INSERT ON guard_notes TO techstack_application;\nCOMMENT ON TABLE guard_notes IS 'a; b';",
		"-- nullable and constant defaults neither rewrite nor break old inserts\nALTER TABLE servers ADD COLUMN label text, ADD COLUMN IF NOT EXISTS retries integer NOT NULL DEFAULT 0;",
		"ALTER TABLE servers ADD COLUMN settings jsonb DEFAULT '{}'::jsonb;",
		"ALTER TABLE servers ADD COLUMN owner_id uuid REFERENCES users(id);",
	}
	for _, body := range admitted {
		if err := ValidateAdditiveMigrationSQL(body); err != nil {
			t.Errorf("additive migration refused: %v", err)
		}
	}
	refused := []string{
		"",
		"DROP TABLE servers;",
		"ALTER TABLE servers DROP COLUMN label;",
		"ALTER TABLE servers RENAME COLUMN label TO name;",
		"ALTER TABLE servers ALTER COLUMN label SET NOT NULL;",
		"ALTER TABLE servers ADD CONSTRAINT servers_label CHECK (label <> '');",
		"ALTER TABLE servers ADD COLUMN label text NOT NULL;",
		"ALTER TABLE servers ADD COLUMN created timestamptz DEFAULT now();",
		"ALTER TABLE servers ADD COLUMN id2 uuid NOT NULL DEFAULT gen_random_uuid();",
		"ALTER TABLE servers ADD COLUMN label text UNIQUE;",
		"ALTER TABLE servers ENABLE ROW LEVEL SECURITY;",
		"CREATE INDEX servers_label ON servers (label);",
		"CREATE UNIQUE INDEX servers_label ON servers (label);",
		"CREATE POLICY servers_all ON servers USING (true);",
		"CREATE TABLE IF NOT EXISTS servers (id uuid);",
		"CREATE TABLE servers_copy AS SELECT * FROM servers;",
		"CREATE OR REPLACE FUNCTION f() RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql;",
		"DO $$ BEGIN END $$;",
		"CREATE TRIGGER t BEFORE INSERT ON servers FOR EACH ROW EXECUTE FUNCTION f();",
		"INSERT INTO servers (id) VALUES ('x');",
		"UPDATE servers SET label = 'x';",
		"REVOKE SELECT ON servers FROM techstack_application;",
		"SET search_path = public;",
	}
	for _, body := range refused {
		if err := ValidateAdditiveMigrationSQL(body); err == nil {
			t.Errorf("non-additive migration admitted: %q", body)
		}
	}
}

// Sensitive migration boundary: boot admits a ledger that meets the schema
// floor, whatever is newer or not applied above it (D-2026-10-27-06).
func TestSchemaFloorAdmitsAnyOrder(t *testing.T) {
	expected := map[string]bool{"163_servers.sql": true, "164_routine.sql": true, "165_notes.sql": true}
	applied := func(versions ...string) map[string]bool {
		set := map[string]bool{}
		for _, version := range versions {
			set[version] = true
		}
		return set
	}
	for _, ledger := range []map[string]bool{
		applied("163_servers.sql", "164_routine.sql", "165_notes.sql"),
		applied("163_servers.sql", "164_routine.sql"),
		applied("163_servers.sql", "164_routine.sql", "166_drop.sql", "170_labels.sql"),
	} {
		if err := verifySchemaFloor(expected, ledger, 164); err != nil {
			t.Errorf("floor 164 refused %v: %v", ledger, err)
		}
	}
	for _, ledger := range []map[string]bool{
		applied("163_servers.sql", "165_notes.sql"),
		applied("164_routine.sql"),
	} {
		if err := verifySchemaFloor(expected, ledger, 164); !errors.Is(err, ErrSchemaFloorUnmet) {
			t.Errorf("floor 164 admitted %v: %v", ledger, err)
		}
	}
	if state := EmbeddedSchemaState(); state.Floor < 1 || state.Floor > state.EmbeddedMax {
		t.Errorf("SchemaFloor %d must name an embedded migration (newest %d)", state.Floor, state.EmbeddedMax)
	}
}

// Sensitive migration boundary (kombify-Techstack-qhrf): the forward batch
// names its locks from the pending bodies, comments excluded and string
// literals included, so a FOREACH list of tables is locked up front.
func TestForwardLockCandidatesNameTablesOutsideComments(t *testing.T) {
	body := []byte(`-- servers is only mentioned here
/* and services
   only here */
CREATE POLICY p ON "Odd Name" FOR SELECT USING (true);
ALTER TABLE household_identity_refs ADD COLUMN x int;
GRANT SELECT ON kombify_ctx.role_class TO someone;
DO $$ BEGIN FOREACH t IN ARRAY ARRAY['workers','stacks'] LOOP NULL; END LOOP; END $$;`)
	candidates := forwardLockCandidates([][]byte{body})
	has := func(name string) bool {
		for _, candidate := range candidates {
			if candidate == name {
				return true
			}
		}
		return false
	}
	for _, name := range []string{`"Odd Name"`, "household_identity_refs", "kombify_ctx.role_class", "workers", "stacks"} {
		if !has(name) {
			t.Fatalf("candidate %s missing from %v", name, candidates)
		}
	}
	for _, name := range []string{"servers", "services"} {
		if has(name) {
			t.Fatalf("commented name %s became a candidate", name)
		}
	}
	if forwardLockName("Servers") != "servers" || forwardLockName(`"Odd ""Name"""`) != `Odd "Name"` {
		t.Fatal("candidates fold like the parser: unquoted to lower case, quoted exactly")
	}
	order := forwardLockOrder([]string{"public.a", "public.b", "public.c"}, []string{"public.c", "public.x", "public.c"})
	if strings.Join(order, ",") != "public.c,public.a,public.b" {
		t.Fatalf("refused tables must lead, the rest stay sorted: %v", order)
	}
	if !retryableForwardLock(fmt.Errorf("w: %w", &pgconn.PgError{Code: "55P03"})) ||
		!retryableForwardLock(&pgconn.PgError{Code: "40P01"}) || !retryableForwardLock(errForwardPreLockLimit) {
		t.Fatal("lock timeout, deadlock and the attempt limit must retry")
	}
	if retryableForwardLock(forwardCommitError{&pgconn.PgError{Code: "40P01"}}) ||
		retryableForwardLock(&pgconn.PgError{Code: "42P01"}) || retryableForwardLock(errors.New("x")) {
		t.Fatal("a commit failure or any other error must never retry")
	}
}

// Sensitive migration boundary (kombify-Techstack-qhrf): the real pending
// tail (165-173) against a synthetic load that holds short locks on the same
// tables. The unchanged single attempt fails as production did (55P03 on an
// application wait cycle, 40P01 as the deadlock victim); the batch with
// up-front locks and bounded retry commits, with its ledger complete.
func TestIntegrationForwardBatchSurvivesConcurrentShortLocks(t *testing.T) {
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	batch, err := DeliveryForwardBatch()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, refusal string
		load          func(context.Context, *sql.DB) error
	}{
		// A transaction holds servers and waits in the application for another
		// connection that reads jobs: Postgres cannot see this cycle.
		{name: "application wait cycle", refusal: "55P03", load: func(ctx context.Context, db *sql.DB) error {
			holder, err := db.Conn(ctx)
			if err != nil {
				return err
			}
			defer holder.Close()
			reader, err := db.Conn(ctx)
			if err != nil {
				return err
			}
			defer reader.Close()
			tx, err := holder.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, "LOCK TABLE servers IN ROW EXCLUSIVE MODE"); err != nil {
				return err
			}
			readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if _, err := reader.ExecContext(readCtx, "SELECT count(*) FROM jobs"); err != nil {
				return err
			}
			return tx.Commit()
		}},
		// The same transaction waits on jobs itself, a cycle Postgres detects;
		// its long deadlock_timeout makes the migration the victim.
		{name: "deadlock", refusal: "40P01", load: func(ctx context.Context, db *sql.DB) error {
			conn, err := db.Conn(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, "SET deadlock_timeout = '60s'"); err != nil {
				return err
			}
			tx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, "LOCK TABLE servers IN ROW EXCLUSIVE MODE"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "SELECT count(*) FROM jobs"); err != nil {
				return err
			}
			return tx.Commit()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
			defer cancel()
			target, load, name := newForwardBatchDatabase(t, ctx, dsn)
			applyForwardBatchBasis(t, ctx, target, batch)
			var system string
			if err := target.QueryRowContext(ctx, "SELECT system_identifier::text FROM pg_catalog.pg_control_system()").Scan(&system); err != nil {
				t.Fatal(err)
			}
			t.Setenv(EnvDatabaseMigrationMode, "apply")
			t.Setenv(EnvAllowDBGenesis, "false")
			t.Setenv(EnvExpectedDatabaseIdentity, name+"@"+system)
			verify := func(context.Context, *sql.Tx) error { return nil }
			for _, statement := range []string{
				"CREATE SCHEMA forward_private", "CREATE TABLE forward_private.hidden (id int)",
				"CREATE TABLE public.forward_readonly (id int)", "GRANT SELECT ON public.forward_readonly TO PUBLIC",
			} {
				if _, err := load.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			if tx, err := target.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}); err == nil {
				tables, err := forwardLockSet(ctx, tx, [][]byte{[]byte("SELECT * FROM forward_private.hidden, forward_readonly, servers")})
				_ = tx.Rollback()
				if err != nil || strings.Join(tables, ",") != "public.servers" {
					t.Fatalf("only tables the owner may lock become locks, without raising: %v %v", tables, err)
				}
			}
			if tx, err := target.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}); err == nil {
				bodies, _ := forwardBatchBodies(batch)
				prefix, err := forwardBatchPrefix(ctx, tx, batch)
				if err != nil {
					t.Fatal(err)
				}
				tables, err := forwardLockSet(ctx, tx, bodies[prefix:])
				_ = tx.Rollback()
				if err != nil || len(tables) == 0 {
					t.Fatalf("the pending tail must name its existing tables: %v %v", tables, err)
				}
				t.Logf("pending tail locks %d tables up front: %s", len(tables), strings.Join(tables, ", "))
			}
			preLock, attempts, retryLog := forwardPreLock, forwardMaxAttempts, forwardRetryLog
			t.Cleanup(func() { forwardPreLock, forwardMaxAttempts, forwardRetryLog = preLock, attempts, retryLog })

			// The unchanged runner: one attempt, no up-front locks, under the
			// case's cycle alone, so its refusal is the production one.
			stopCycle, cycleRounds := startForwardBatchLoad(ctx, load, tc.load, 0)
			defer stopCycle()
			time.Sleep(300 * time.Millisecond)
			forwardPreLock, forwardMaxAttempts = false, 1
			err := target.MigrateForwardBatch(ctx, batch, verify)
			var refusal *pgconn.PgError
			if !errors.As(err, &refusal) || refusal.Code != tc.refusal {
				t.Fatalf("the unchanged single attempt must fail with %s under this load, got %v", tc.refusal, err)
			}
			if state, err := target.InspectForwardBatch(ctx, batch); err != nil || len(state.Pending) != len(batch)-state.AppliedPrefix || len(state.Pending) == 0 {
				t.Fatalf("the refused attempt must leave its whole tail pending: %+v %v", state, err)
			}

			// The runner as shipped, under the same cycle plus four workers that
			// hold short row locks on the policy tables in random order.
			stopNoise, noiseRounds := startForwardBatchLoad(ctx, load, nil, 4)
			defer stopNoise()
			var log strings.Builder
			forwardPreLock, forwardMaxAttempts, forwardRetryLog = true, attempts, &log
			started := time.Now()
			if err := target.MigrateForwardBatch(ctx, batch, verify); err != nil {
				t.Fatalf("the batch must survive the load: %v\n%s", err, log.String())
			}
			t.Logf("committed after %s with %d cycle and %d noise rounds; retries:\n%s",
				time.Since(started).Round(time.Millisecond), cycleRounds.Load(), noiseRounds.Load(), log.String())
			state, err := target.InspectForwardBatch(ctx, batch)
			if err != nil || len(state.Pending) != 0 || state.AppliedPrefix != len(batch) {
				t.Fatalf("the ledger must hold the whole batch: %+v %v", state, err)
			}
			before := cycleRounds.Load() + noiseRounds.Load()
			time.Sleep(300 * time.Millisecond)
			if cycleRounds.Load()+noiseRounds.Load() == before {
				t.Fatal("the application load must keep running after the commit")
			}
		})
	}
}

// newForwardBatchDatabase creates an empty database per case, owned by a
// non-bypass migration owner as production's is (several migrations refuse a
// superuser owner, and name public explicitly, so a schema per test cannot
// hold them). The superuser prepares only what production's prerequisites
// prepare: the two NOLOGIN roles the tail would otherwise create, the grant
// option on pg_control_system and the public schema owner.
func newForwardBatchDatabase(t *testing.T, ctx context.Context, dsn string) (*DB, *sql.DB, string) {
	t.Helper()
	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*adminConfig)
	t.Cleanup(func() { _ = admin.Close() })
	var superuser bool
	if err := admin.QueryRowContext(ctx, "SELECT rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&superuser); err != nil || !superuser {
		t.Skip("the forward batch case prepares a migration owner and needs a superuser test cluster")
	}
	id := strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	name, owner, password := "techstack_forward_"+id, "techstack_forward_owner_"+id, uuid.NewString()
	statements := []string{"CREATE ROLE " + owner + " LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE PASSWORD '" + password + "'"}
	created := []string{}
	for _, role := range []string{"techstack_household_worker", "techstack_platform_projection_system"} {
		var exists bool
		if err := admin.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			statements = append(statements, "CREATE ROLE "+role+" NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION")
			created = append(created, role)
		}
	}
	statements = append(statements,
		"GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO "+owner+" WITH GRANT OPTION",
		"CREATE DATABASE "+name+" OWNER "+owner)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = admin.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_, _ = admin.ExecContext(cleanupCtx, "REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM "+owner+" CASCADE")
		for _, role := range append(created, owner) {
			_, _ = admin.ExecContext(cleanupCtx, "DROP ROLE IF EXISTS "+role)
		}
	})
	for _, statement := range statements {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare the forward batch database: %v", err)
		}
	}
	config := adminConfig.Copy()
	config.Database, config.User, config.Password = name, owner, password
	sqlDB := stdlib.OpenDB(*config)
	sqlDB.SetMaxOpenConns(4)
	// The synthetic application load holds only locks; it connects as the
	// cluster superuser so the deadlock case may set its deadlock_timeout.
	loadConfig := adminConfig.Copy()
	loadConfig.Database = name
	loadDB := stdlib.OpenDB(*loadConfig)
	loadDB.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = loadDB.Close(); _ = sqlDB.Close() })
	// Production's public schema belongs to the migration owner.
	if _, err := loadDB.ExecContext(ctx, "ALTER SCHEMA public OWNER TO "+owner); err != nil {
		t.Fatalf("prepare the forward batch schema owner: %v", err)
	}
	return &DB{DB: sqlDB, backend: StoreBackendPostgres}, loadDB, name
}

// applyForwardBatchBasis applies every embedded migration before the tail
// production still has pending (165), as the normal runner does.
func applyForwardBatchBasis(t *testing.T, ctx context.Context, target *DB, batch []ForwardMigrationArtifact) {
	t.Helper()
	if _, err := target.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	pendingFrom := "165_"
	if !strings.HasPrefix(batch[len(batch)-1].Version, "174_") {
		t.Fatalf("the case is calibrated on the 165-174 tail, the manifest ends at %s", batch[len(batch)-1].Version)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= pendingFrom {
			continue
		}
		body, err := migrationsFS.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := executePendingMigration(ctx, tx, entry.Name(), body); err != nil {
			_ = tx.Rollback()
			t.Fatalf("basis migration %s: %v", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", entry.Name()); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

// startForwardBatchLoad runs the cycle worker, when given, without pause and
// noise workers that hold short row locks on the policy tables in random order.
func startForwardBatchLoad(ctx context.Context, db *sql.DB, cycle func(context.Context, *sql.DB) error, noise int) (func(), *atomic.Int64) {
	loadCtx, cancel := context.WithCancel(ctx)
	rounds := &atomic.Int64{}
	var wg sync.WaitGroup
	run := func(round func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for loadCtx.Err() == nil {
				if round() == nil {
					rounds.Add(1)
				}
			}
		}()
	}
	if cycle != nil {
		run(func() error { return cycle(loadCtx, db) })
	}
	tables := []string{"servers", "services", "stacks", "workers", "homelabs", "typed_agent_commands"}
	for worker := 0; worker < noise; worker++ {
		run(func() error {
			tx, err := db.BeginTx(loadCtx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			for _, index := range mathrand.Perm(len(tables))[:3] {
				if _, err := tx.ExecContext(loadCtx, "LOCK TABLE "+tables[index]+" IN ROW EXCLUSIVE MODE"); err != nil {
					return err
				}
				time.Sleep(time.Duration(20+mathrand.IntN(180)) * time.Millisecond)
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			time.Sleep(time.Duration(mathrand.IntN(150)) * time.Millisecond)
			return nil
		})
	}
	return func() { cancel(); wg.Wait() }, rounds
}
