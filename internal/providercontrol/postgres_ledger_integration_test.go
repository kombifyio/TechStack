package providercontrol

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/localdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const envProviderControlEmbeddedPostgres = "TECHSTACK_PROVIDERCONTROL_EMBEDDED_POSTGRES"

// isolatedLedgerRuntimeProjector keeps the low-level ledger fixture focused on
// receipt/claim SQL. Runtime projection is exercised by the full-migration
// native-admission fixture, which has the authoritative server registry,
// resource-binding, and capacity schemas.
type isolatedLedgerRuntimeProjector struct{}

func (isolatedLedgerRuntimeProjector) ValidateMutationStartTx(
	context.Context,
	*sql.Tx,
	providerexecutor.Command,
	int64,
) error {
	return nil
}

func (isolatedLedgerRuntimeProjector) PrepareTx(
	context.Context,
	*sql.Tx,
	OperationRecord,
	providerexecutor.Receipt,
	providerexecutor.Receipt,
) (preparedProviderReceiptRuntimeProjection, error) {
	return preparedProviderReceiptRuntimeProjection{}, nil
}

func (isolatedLedgerRuntimeProjector) ApplyTx(
	context.Context,
	*sql.Tx,
	preparedProviderReceiptRuntimeProjection,
) error {
	return nil
}

func newIsolatedPostgresLedger(
	db *sql.DB,
	verifier providerexecutor.EvidenceVerifier,
) (*PostgresLedger, error) {
	ledger, err := NewPostgresLedger(db, verifier)
	if err == nil {
		ledger.runtimeProjection = isolatedLedgerRuntimeProjector{}
	}
	return ledger, err
}

func TestMain(m *testing.M) {
	var embedded *localdb.EmbeddedPostgres
	var tempDir string
	if strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL")) == "" &&
		strings.TrimSpace(os.Getenv(envProviderControlEmbeddedPostgres)) == "1" {
		var err error
		tempDir, err = os.MkdirTemp("", "techstack-providercontrol-postgres-")
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "create embedded PostgreSQL test dir: %v\n", err)
			os.Exit(1)
		}
		if setEnvErr := os.Setenv(localdb.EnvEmbeddedPostgresDir, filepath.Join(tempDir, "postgres")); setEnvErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "configure embedded PostgreSQL test dir: %v\n", setEnvErr)
			_ = os.RemoveAll(tempDir)
			os.Exit(1)
		}
		embedded, err = localdb.StartEmbeddedPostgres(tempDir)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "start embedded PostgreSQL: %v\n", err)
			_ = os.RemoveAll(tempDir)
			os.Exit(1)
		}
		if setEnvErr := os.Setenv("TECHSTACK_TEST_POSTGRES_URL", embedded.DSN()); setEnvErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "configure PostgreSQL integration DSN: %v\n", setEnvErr)
			_ = embedded.Stop()
			_ = os.RemoveAll(tempDir)
			os.Exit(1)
		}
	}

	code := m.Run()
	if embedded != nil {
		if err := embedded.Stop(); err != nil && code == 0 {
			_, _ = fmt.Fprintf(os.Stderr, "stop embedded PostgreSQL: %v\n", err)
			code = 1
		}
	}
	if tempDir != "" {
		_ = os.RemoveAll(tempDir)
	}
	os.Exit(code)
}

// These tests intentionally use a dedicated opt-in DSN and a unique schema.
// They never reuse DATABASE_URL, never touch an existing schema, and remove
// only the generated providercontrol_test_* schema during cleanup.
func openProviderControlIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping provider execution PostgreSQL integration tests")
	}

	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse integration DSN: %v", err)
	}
	adminDB := stdlib.OpenDB(*adminConfig)
	if err := adminDB.PingContext(t.Context()); err != nil {
		_ = adminDB.Close()
		t.Fatalf("ping integration PostgreSQL: %v", err)
	}

	schema := fmt.Sprintf("providercontrol_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	temporaryRole := ""
	if _, err := adminDB.ExecContext(t.Context(), "CREATE SCHEMA "+quotedSchema); err != nil {
		_ = adminDB.Close()
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		if temporaryRole != "" {
			_, _ = adminDB.ExecContext(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{temporaryRole}.Sanitize())
		}
		_ = adminDB.Close()
	})

	scopedConfig := adminConfig.Copy()
	scopedConfig.RuntimeParams = make(map[string]string, len(adminConfig.RuntimeParams)+1)
	for key, value := range adminConfig.RuntimeParams {
		scopedConfig.RuntimeParams[key] = value
	}
	scopedConfig.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*scopedConfig)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE techstack_tenants (id text PRIMARY KEY);
		INSERT INTO techstack_tenants (id) VALUES ('tenant-1'), ('tenant-2');
	`); err != nil {
		t.Fatalf("seed isolated tenant authority: %v", err)
	}
	for _, migration := range []string{
		"013_provider_execution_ledger.sql",
		"014_provider_execution_claims.sql",
		"027_provider_catalog_authority.sql",
	} {
		payload, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "migrations", migration))
		if err != nil {
			t.Fatalf("read %s: %v", migration, err)
		}
		if _, err := db.ExecContext(t.Context(), string(payload)); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
	}
	// Migrations 019, 025, and 026 depend on the full lease/outbox/registry
	// schema. This isolated ledger fixture creates only their authority and
	// canonical lease-fence boundaries and seeds the exact native lane used by
	// provider control.
	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE servers (
			tenant_id text NOT NULL,
			id text NOT NULL,
			lease_id text,
			provider_ref text,
			revision bigint NOT NULL DEFAULT 1,
			generation bigint NOT NULL DEFAULT 1,
			desired_state text NOT NULL,
			lifecycle_state text NOT NULL,
			decommissioned_at timestamptz,
			PRIMARY KEY (tenant_id, id),
			FOREIGN KEY (tenant_id) REFERENCES techstack_tenants (id) ON DELETE RESTRICT
		);
		INSERT INTO servers (
			tenant_id, id, lease_id, provider_ref, desired_state, lifecycle_state
		) VALUES ('tenant-1', 'server-1', 'lease-1', 'ionos', 'running', 'active');
		ALTER TABLE servers ENABLE ROW LEVEL SECURITY;
		ALTER TABLE servers FORCE ROW LEVEL SECURITY;
		CREATE POLICY tenant_isolation ON servers
			USING (tenant_id = current_setting('app.tenant_id', true))
			WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

		CREATE TABLE techstack_vm_leases (
			tenant_id text NOT NULL,
			id text NOT NULL,
			lease_revision bigint NOT NULL,
			server_id text NOT NULL,
			resource_generation_id uuid NOT NULL,
			desired_state text NOT NULL,
			cancelled_at timestamptz,
			PRIMARY KEY (tenant_id, id),
			UNIQUE (tenant_id, id, lease_revision, server_id, resource_generation_id),
			FOREIGN KEY (tenant_id) REFERENCES techstack_tenants (id) ON DELETE RESTRICT,
			FOREIGN KEY (tenant_id, server_id) REFERENCES servers (tenant_id, id) ON DELETE RESTRICT
		);
		INSERT INTO techstack_vm_leases (
			tenant_id, id, lease_revision, server_id, resource_generation_id, desired_state
		) VALUES (
			'tenant-1', 'lease-1', 7, 'server-1',
			'11111111-1111-4111-8111-111111111111', 'running'
		);
		ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY;
		ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY;
		CREATE POLICY tenant_isolation ON techstack_vm_leases
			USING (tenant_id = current_setting('app.tenant_id', true))
			WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
		CREATE FUNCTION provider_control_lock_runtime_lease_projection(
			requested_lease_id text
		)
		RETURNS TABLE (
			desired_state text,
			cancelled_at timestamptz,
			server_id text,
			resource_generation_id text
		)
		LANGUAGE sql
		SECURITY DEFINER
		SET search_path FROM CURRENT
		AS $$
			SELECT lease.desired_state, lease.cancelled_at, lease.server_id,
			       lease.resource_generation_id::text
			FROM techstack_vm_leases AS lease
			WHERE lease.tenant_id = current_setting('app.tenant_id', true)
			  AND lease.id = requested_lease_id
			FOR SHARE OF lease
		$$;

		CREATE TABLE runtime_lease_execution_authorities (
			tenant_id text NOT NULL,
			lease_id text NOT NULL,
			execution_authority text NOT NULL,
			bound_at timestamptz NOT NULL,
			PRIMARY KEY (tenant_id, lease_id),
			FOREIGN KEY (tenant_id, lease_id)
				REFERENCES techstack_vm_leases (tenant_id, id) ON DELETE RESTRICT,
			CHECK (execution_authority IN ('legacy_simulate', 'techstack_provider_control'))
		);
		INSERT INTO runtime_lease_execution_authorities (
			tenant_id, lease_id, execution_authority, bound_at
		) VALUES ('tenant-1', 'lease-1', 'techstack_provider_control', clock_timestamp());
		ALTER TABLE runtime_lease_execution_authorities ENABLE ROW LEVEL SECURITY;
		ALTER TABLE runtime_lease_execution_authorities FORCE ROW LEVEL SECURITY;
		CREATE POLICY tenant_isolation ON runtime_lease_execution_authorities
			USING (tenant_id = current_setting('app.tenant_id', true))
			WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

		CREATE UNIQUE INDEX uq_provider_operations_tenant_operation_lease
			ON provider_operations (tenant_id, operation_id, lease_id);
	`); err != nil {
		t.Fatalf("seed isolated provider-control authority: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "migrations", "029_provider_provision_dispatch_guards.sql"))
	if err != nil {
		t.Fatalf("read provider dispatch guard migration: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), string(payload)); err != nil {
		t.Fatalf("apply provider dispatch guard migration: %v", err)
	}
	payload, err = os.ReadFile(filepath.Join("..", "..", "pkg", "db", "migrations", "031_provider_provision_operator_resolution.sql"))
	if err != nil {
		t.Fatalf("read provider operator resolution migration: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), string(payload)); err != nil {
		t.Fatalf("apply provider operator resolution migration: %v", err)
	}
	payload, err = os.ReadFile(filepath.Join("..", "..", "pkg", "db", "migrations", "032_provider_claim_credential_authority.sql"))
	if err != nil {
		t.Fatalf("read provider claim credential migration: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), string(payload)); err != nil {
		t.Fatalf("apply provider claim credential migration: %v", err)
	}
	payload, err = os.ReadFile(filepath.Join("..", "..", "pkg", "db", "migrations", "034_provider_control_runtime_authority.sql"))
	if err != nil {
		t.Fatalf("read provider runtime authority migration: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), string(payload)); err != nil {
		t.Fatalf("apply provider runtime authority migration: %v", err)
	}
	payload, err = os.ReadFile(filepath.Join("..", "..", "pkg", "db", "migrations", "079_provider_provision_successor_resolution.sql"))
	if err != nil {
		t.Fatalf("read provider successor resolution migration: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), string(payload)); err != nil {
		t.Fatalf("apply provider successor resolution migration: %v", err)
	}
	// This isolated legacy-ledger fixture intentionally does not apply the
	// full RuntimeServer projection migration. Keep its read projection
	// compatible with Migration 035 without weakening production constraints;
	// resource-free terminalization behavior is covered by the native
	// admission integration database, which applies every migration.
	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE provider_operation_resource_free_terminalizations (
			tenant_id text NOT NULL,
			operation_id text NOT NULL,
			lease_id text NOT NULL,
			lease_revision bigint NOT NULL,
			server_id text NOT NULL,
			server_generation bigint NOT NULL,
			resource_generation_id uuid NOT NULL,
			head_sequence bigint NOT NULL,
			head_receipt_digest text NOT NULL,
			authority text NOT NULL,
			resolution_revision bigint,
			decision_digest text,
			terminalized_at timestamptz NOT NULL,
			PRIMARY KEY (tenant_id, operation_id)
		);
		ALTER TABLE provider_operation_resource_free_terminalizations
			ENABLE ROW LEVEL SECURITY;
		ALTER TABLE provider_operation_resource_free_terminalizations
			FORCE ROW LEVEL SECURITY;
		CREATE POLICY tenant_isolation
			ON provider_operation_resource_free_terminalizations
			USING (tenant_id = current_setting('app.tenant_id', true))
			WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
	`); err != nil {
		t.Fatalf("create resource-free terminalization projection fixture: %v", err)
	}
	handle := testCredentialHandle()
	custodyHash, err := credentialCustodyHash("tenant-1", handle)
	if err != nil {
		t.Fatalf("hash integration credential custody: %v", err)
	}
	connectionHash, err := credentialConnectionHash("tenant-1", handle)
	if err != nil {
		t.Fatalf("hash integration provider connection: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO provider_credential_handles (
			tenant_id, handle_id, handle_version, provider_id, credential_mode,
			subject_kind, subject_id, grant_id, credential_scope,
			custody_ref, connection_ref, custody_hash, connection_hash,
			valid_from, valid_until
		) VALUES (
			'tenant-1', $1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, clock_timestamp() - interval '1 hour',
			clock_timestamp() + interval '24 hours'
		)
	`, handle.HandleID, handle.HandleVersion, handle.ProviderID, handle.CredentialMode,
		handle.SubjectKind, handle.SubjectID, handle.GrantID, handle.Scope,
		handle.CustodyRef, handle.ConnectionRef, custodyHash, connectionHash); err != nil {
		t.Fatalf("seed isolated provider credential handle: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO provider_catalog_versions (catalog_version, status)
		VALUES ('catalog-2026-07-21', 'draft')
	`); err != nil {
		t.Fatalf("seed isolated provider catalog version: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO provider_catalog_profiles (
			catalog_version, provider_id, adapter_id, credential_mode,
			runtime_profile_id, offering_id, can_pause, stop_effect,
			can_recreate, capability_snapshot, adapter_manifest_hash,
			provision_dispatch_mode
		) VALUES
			('catalog-2026-07-21', 'ionos', 'managed-compute', 'managed',
			 'ionos-managed-pvm-monthly', 'monthly-runtime-standard', true, 'pause', true, '{}', $1, 'native_idempotency'),
			('catalog-2026-07-21', 'ionos', 'ionos-amo', 'managed',
			 'ionos-amo-pvm-monthly', 'monthly-runtime-standard', false, 'destroy', true, '{}', $2,
			 'at_most_once_dispatch_manual_reconcile')
	`, digest("test-adapter-manifest"), digest("test-adapter-manifest")); err != nil {
		t.Fatalf("seed isolated provider catalog profiles: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `
		UPDATE provider_catalog_versions
		SET status = 'active', activated_at = clock_timestamp()
		WHERE catalog_version = 'catalog-2026-07-21'
	`); err != nil {
		t.Fatalf("activate isolated provider catalog: %v", err)
	}

	var bypassRLS bool
	if err := db.QueryRowContext(t.Context(), `
		SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user
	`).Scan(&bypassRLS); err != nil {
		t.Fatalf("inspect integration role: %v", err)
	}
	if !bypassRLS {
		return db
	}

	// Embedded PostgreSQL starts with a superuser. Create a temporary app role
	// so FORCE ROW LEVEL SECURITY is exercised rather than silently bypassed.
	temporaryRole = fmt.Sprintf("providercontrol_rls_%d", time.Now().UnixNano())
	quotedRole := pgx.Identifier{temporaryRole}.Sanitize()
	password := fmt.Sprintf("providercontrol_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(t.Context(), fmt.Sprintf(
		"CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS",
		quotedRole, password,
	)); err != nil {
		t.Logf("temporary non-bypass role unavailable; RLS assertion will report the role limitation: %v", err)
		temporaryRole = ""
		return db
	}
	// #nosec G202 -- both identifiers are generated locally and quoted with pgx.Identifier.
	if _, err := db.ExecContext(t.Context(), "GRANT USAGE ON SCHEMA "+quotedSchema+" TO "+quotedRole); err != nil {
		t.Fatalf("grant integration schema usage: %v", err)
	}
	// #nosec G202 -- both identifiers are generated locally and quoted with pgx.Identifier.
	if _, err := db.ExecContext(t.Context(), "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA "+quotedSchema+" TO "+quotedRole); err != nil {
		t.Fatalf("grant integration table access: %v", err)
	}
	// Claim tokens are bearer capabilities. Only their non-reversible digest is
	// persisted, so the runtime role may inspect the tenant-scoped claim row.
	// #nosec G202 -- the role identifier is generated locally and quoted with pgx.Identifier.
	if _, err := db.ExecContext(t.Context(), "REVOKE SELECT, DELETE ON provider_operation_execution_claims FROM "+quotedRole); err != nil {
		t.Fatalf("restrict integration claim access: %v", err)
	}
	// #nosec G202 -- the role identifier is generated locally and quoted with pgx.Identifier.
	if _, err := db.ExecContext(t.Context(), `GRANT SELECT (
		tenant_id, operation_id, head_sequence, head_receipt_digest,
		claim_token_digest, claim_owner, claim_access, state, claimed_at, lease_expires_at, released_at
	) ON provider_operation_execution_claims TO `+quotedRole); err != nil {
		t.Fatalf("grant non-secret integration claim reads: %v", err)
	}
	// #nosec G202 -- the schema and role identifiers are generated locally and quoted.
	if _, err := db.ExecContext(t.Context(), "GRANT EXECUTE ON FUNCTION "+quotedSchema+".provider_control_list_runnable_tenants(text,integer) TO "+quotedRole); err != nil {
		t.Fatalf("grant bounded integration tenant discovery: %v", err)
	}

	appConfig := scopedConfig.Copy()
	appConfig.User = temporaryRole
	appConfig.Password = password
	appDB := stdlib.OpenDB(*appConfig)
	if err := appDB.PingContext(t.Context()); err != nil {
		_ = appDB.Close()
		t.Fatalf("ping temporary RLS role: %v", err)
	}
	t.Cleanup(func() { _ = appDB.Close() })
	return appDB
}

func TestPostgresLedgerSerializesConcurrentCoordinators(t *testing.T) {
	db := openProviderControlIntegrationDB(t)
	ledger, err := newIsolatedPostgresLedger(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	executor := &blockingExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	registry := NewRegistry()
	if registerErr := registry.Register("managed-compute", executor); registerErr != nil {
		t.Fatalf("Register: %v", registerErr)
	}
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
		Ledger:   ledger, ClaimOwner: "integration-worker", ClaimTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	record, _, err := coordinator.Start(t.Context(), planRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("Advance accepted: %v", err)
	}

	type advanceResult struct {
		record   OperationRecord
		advanced bool
		err      error
	}
	completed := make(chan advanceResult, 1)
	go func() {
		next, advanced, advanceErr := coordinator.Advance(context.Background(), record.Command.TenantID, record.Command.OperationID)
		completed <- advanceResult{record: next, advanced: advanced, err: advanceErr}
	}()
	select {
	case <-executor.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first adapter invocation did not start")
	}

	second, advanced, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("concurrent Advance: %v", err)
	}
	if advanced || second.Head.ReceiptDigest != record.Head.ReceiptDigest {
		t.Fatalf("concurrent coordinator advanced claimed head: advanced=%v head=%+v", advanced, second.Head)
	}
	if calls := executor.CallCount(); calls != 1 {
		t.Fatalf("adapter calls while claim is live = %d, want 1", calls)
	}
	close(executor.release)
	first := <-completed
	if first.err != nil || !first.advanced || first.record.Head.Phase != providerexecutor.PhasePlanned {
		t.Fatalf("claimed Advance = %+v, advanced=%v, err=%v", first.record.Head, first.advanced, first.err)
	}
	if calls := executor.CallCount(); calls != 1 {
		t.Fatalf("adapter calls after claimed append = %d, want 1", calls)
	}

	var state string
	withIntegrationTenant(t, db, record.Command.TenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), `
			SELECT state FROM provider_operation_execution_claims
			WHERE tenant_id = $1 AND operation_id = $2 AND head_receipt_digest = $3
		`, record.Command.TenantID, record.Command.OperationID, record.Head.ReceiptDigest).Scan(&state)
	})
	if state != "consumed" {
		t.Fatalf("claim state after atomic append = %q, want consumed", state)
	}
}

func TestPostgresLedgerUsesDBTimeAndRejectsClaimBypasses(t *testing.T) {
	db := openProviderControlIntegrationDB(t)
	ledger, err := newIsolatedPostgresLedger(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	record := startAcceptedIntegrationPlan(t, ledger)
	request := providerexecutor.ExecutionRequest{Command: record.Command, Previous: record.Head}
	next, err := providerexecutor.AssembleReceipt(t.Context(), request, providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusSucceeded,
		Phase:  providerexecutor.PhasePlanned,
	}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("AssembleReceipt: %v", err)
	}

	assertRawHeadShiftRejected(t, db, record, next)
	if appendErr := ledger.AppendReceipt(t.Context(), record.Command, record.Head, next); !errors.Is(appendErr, ErrExecutionClaimNeeded) {
		t.Fatalf("claim-free adapter append error = %v, want ErrExecutionClaimNeeded", appendErr)
	}
	assertRawOversizedClaimInsertRejected(t, db, record)

	dbBefore := integrationDBTime(t, db)
	claim, err := ledger.AcquireExecutionClaim(t.Context(), record.Command, record.Head, ExecutionClaimReadOnly, "worker-a", "token-a", time.Second)
	if err != nil {
		t.Fatalf("AcquireExecutionClaim: %v", err)
	}
	dbAfter := integrationDBTime(t, db)
	if claim.ExpiresAt.Before(dbBefore.Add(time.Second)) || claim.ExpiresAt.After(dbAfter.Add(time.Second+250*time.Millisecond)) {
		t.Fatalf("claim expiry %s is not derived from DB time window [%s,%s]", claim.ExpiresAt, dbBefore, dbAfter)
	}
	if _, acquireErr := ledger.AcquireExecutionClaim(t.Context(), record.Command, record.Head, ExecutionClaimReadOnly, "worker-b", "token-b", time.Second); !errors.Is(acquireErr, ErrExecutionClaimHeld) {
		t.Fatalf("second live acquisition error = %v, want ErrExecutionClaimHeld", acquireErr)
	}
	assertPlaintextClaimTokensAbsent(t, db, record)
	assertRawClaimMutationRejected(t, db, record)
	assertRawOversizedHeartbeatRejected(t, db, record, claim)
	wrong := claim
	wrong.Token = "wrong-token"
	if appendErr := ledger.AppendClaimedReceipt(t.Context(), record.Command, record.Head, next, wrong); !errors.Is(appendErr, ErrExecutionClaimLost) {
		t.Fatalf("wrong-token append error = %v, want ErrExecutionClaimLost", appendErr)
	}

	time.Sleep(1100 * time.Millisecond)
	assertRawExpiredHeartbeatRejected(t, db, record, claim)
	takeover, err := ledger.AcquireExecutionClaim(t.Context(), record.Command, record.Head, ExecutionClaimReadOnly, "worker-b", "token-b", time.Second)
	if err != nil {
		t.Fatalf("expired claim takeover: %v", err)
	}
	renewed, err := ledger.RenewExecutionClaim(t.Context(), takeover, 2*time.Second)
	if err != nil {
		t.Fatalf("RenewExecutionClaim: %v", err)
	}
	if !renewed.ExpiresAt.After(takeover.ExpiresAt) {
		t.Fatalf("renewed expiry %s did not advance beyond %s", renewed.ExpiresAt, takeover.ExpiresAt)
	}
	if appendErr := ledger.AppendClaimedReceipt(t.Context(), record.Command, record.Head, next, renewed); appendErr != nil {
		t.Fatalf("AppendClaimedReceipt: %v", appendErr)
	}
	loaded, err := ledger.LoadOperation(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("LoadOperation: %v", err)
	}
	if loaded.Head.ReceiptDigest != next.ReceiptDigest || loaded.Head.Phase != providerexecutor.PhasePlanned {
		t.Fatalf("stored claimed head = %+v, want %+v", loaded.Head, next)
	}
}

func TestPostgresLedgerCredentialRevocationBlocksNewMutationButNotInflightCustody(t *testing.T) {
	db := openProviderControlIntegrationDB(t)
	assertCredentialValidityMutationRejected(t, db, "tenant-1")
	ledger, err := newIsolatedPostgresLedger(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	inflight := startAcceptedIntegrationRequest(t, ledger, sideEffectingRequest(providerexecutor.OperationReconcile, "inflight"))
	expiring := startAcceptedIntegrationRequest(t, ledger, sideEffectingRequest(providerexecutor.OperationReconcile, "expiring"))
	unclaimed := startAcceptedIntegrationRequest(t, ledger, sideEffectingRequest(providerexecutor.OperationReconcile, "unclaimed"))

	inflightClaim, err := ledger.AcquireExecutionClaim(
		t.Context(), inflight.Command, inflight.Head, ExecutionClaimSideEffecting,
		"inflight-worker", "inflight-token", 5*time.Second,
	)
	if err != nil {
		t.Fatalf("AcquireExecutionClaim inflight: %v", err)
	}
	expiringClaim, err := ledger.AcquireExecutionClaim(
		t.Context(), expiring.Command, expiring.Head, ExecutionClaimSideEffecting,
		"expiring-worker", "expiring-token", time.Second,
	)
	if err != nil {
		t.Fatalf("AcquireExecutionClaim expiring: %v", err)
	}

	withIntegrationTenant(t, db, "tenant-1", func(tx *sql.Tx) error {
		_, updateErr := tx.ExecContext(t.Context(), `
			UPDATE provider_credential_handles
			SET revoked_at = clock_timestamp()
			WHERE tenant_id = $1 AND custody_ref = $2
		`, inflight.Command.TenantID, inflight.Command.CustodyRef)
		return updateErr
	})

	if _, err := ledger.RenewExecutionClaim(t.Context(), inflightClaim, 5*time.Second); err != nil {
		t.Fatalf("same-token heartbeat after revocation: %v", err)
	}
	if err := ledger.AppendClaimedReceipt(
		t.Context(), inflight.Command, inflight.Head, failedIntegrationReceipt(t, inflight), inflightClaim,
	); err != nil {
		t.Fatalf("append in-flight result after revocation: %v", err)
	}
	if _, err := ledger.AcquireExecutionClaim(
		t.Context(), unclaimed.Command, unclaimed.Head, ExecutionClaimSideEffecting,
		"late-worker", "late-token", time.Minute,
	); !errors.Is(err, ErrClaimCredentialDenied) {
		t.Fatalf("new side-effecting claim after revocation error = %v, want ErrClaimCredentialDenied", err)
	}

	time.Sleep(1100 * time.Millisecond)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin raw reacquire transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `
		SELECT
			set_config('app.tenant_id', $1, true),
			set_config('app.provider_execution_claim_token', $2, true),
			set_config('app.provider_execution_claim_owner', $3, true)
	`, expiring.Command.TenantID, expiringClaim.Token, expiringClaim.Owner); err != nil {
		t.Fatalf("bind raw reacquire context: %v", err)
	}
	_, rawErr := tx.ExecContext(t.Context(), `
		WITH db_time AS (SELECT clock_timestamp() AS at)
		INSERT INTO provider_operation_execution_claims (
			tenant_id, operation_id, head_sequence, head_receipt_digest,
			claim_token_digest, claim_owner, claim_access, state, claimed_at, lease_expires_at
		)
		SELECT $1,$2,$3,$4,$5,$6,'side_effecting','active',db_time.at,db_time.at + interval '1 minute'
		FROM db_time
		ON CONFLICT (tenant_id, operation_id, head_receipt_digest) DO UPDATE
		SET claim_token_digest = EXCLUDED.claim_token_digest,
			claim_owner = EXCLUDED.claim_owner,
			claim_access = EXCLUDED.claim_access,
			state = 'active',
			claimed_at = EXCLUDED.claimed_at,
			lease_expires_at = EXCLUDED.lease_expires_at,
			released_at = NULL
	`, expiring.Command.TenantID, expiring.Command.OperationID,
		int64(expiring.Head.Sequence), expiring.Head.ReceiptDigest,
		executionClaimTokenDigest(expiringClaim.Token), expiringClaim.Owner)
	if rawErr == nil || !strings.Contains(rawErr.Error(), "credential is not authorized") {
		t.Fatalf("raw same-token reacquire after revocation error = %v", rawErr)
	}
}

func assertCredentialValidityMutationRejected(t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin credential validity mutation: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatalf("set credential validity tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		UPDATE provider_credential_handles
		SET valid_until = valid_until + interval '1 hour'
		WHERE tenant_id = $1
	`, tenantID); err == nil || !strings.Contains(err.Error(), "credential handle identity is immutable") {
		t.Fatalf("credential validity mutation error = %v", err)
	}
}

func TestPostgresLedgerAMODispatchGuardGrantsExactlyOneNonReplayablePermit(t *testing.T) {
	db := openProviderControlIntegrationDB(t)
	ledger, err := newIsolatedPostgresLedger(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	record := startAcceptedIntegrationAMOProvision(t, ledger)
	binding := preparedBindingForCommandHead(t, record.Command, record.Head)
	beforeGuard, err := ledger.ListRunnableTenants(t.Context(), "", 10)
	if err != nil {
		t.Fatalf("ListRunnableTenants before AMO guard: %v", err)
	}
	if len(beforeGuard.TenantIDs) != 1 || beforeGuard.TenantIDs[0] != record.Command.TenantID {
		t.Fatalf("runnable tenants before AMO guard = %#v", beforeGuard)
	}
	if _, err := ledger.AcquireExecutionClaim(
		t.Context(), record.Command, record.Head, ExecutionClaimSideEffecting, "generic-before-guard", "generic-before-token", 5*time.Second,
	); !errors.Is(err, ErrDispatchPermitNeeded) {
		t.Fatalf("generic claim before guard error = %v, want ErrDispatchPermitNeeded", err)
	}

	type acquireResult struct {
		grant ProvisionDispatchGrant
		err   error
	}
	results := make(chan acquireResult, 2)
	for index := range 2 {
		go func(worker int) {
			grant, acquireErr := ledger.AcquireProvisionDispatchClaim(
				t.Context(), record.Command, record.Head, binding,
				fmt.Sprintf("amo-worker-%d", worker), fmt.Sprintf("amo-token-%d", worker), 5*time.Second,
			)
			results <- acquireResult{grant: grant, err: acquireErr}
		}(index)
	}
	var winner ProvisionDispatchGrant
	succeeded, manual := 0, 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			succeeded++
			winner = result.grant
		case errors.Is(result.err, ErrProvisionManualReconcile):
			manual++
		default:
			t.Fatalf("AcquireProvisionDispatchClaim error = %v", result.err)
		}
	}
	if succeeded != 1 || manual != 1 || !validExecutePermit(winner.Permit, winner.Claim, binding) {
		t.Fatalf("guard acquisition succeeded=%d manual=%d winner=%+v", succeeded, manual, winner.Claim)
	}
	afterGuard, err := ledger.ListRunnableTenants(t.Context(), "", 10)
	if err != nil {
		t.Fatalf("ListRunnableTenants after AMO guard: %v", err)
	}
	if len(afterGuard.TenantIDs) != 0 || afterGuard.NextCursor != "" {
		t.Fatalf("runnable tenants after AMO guard = %#v, want empty", afterGuard)
	}

	loaded, err := ledger.LoadOperation(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("LoadOperation: %v", err)
	}
	if loaded.AutomationState != OperationAutomationManualReconcileRequired {
		t.Fatalf("automation_state = %q, want manual_reconcile_required", loaded.AutomationState)
	}
	if loaded.AutomationReasonCode != providerexecutor.ReasonCodeProviderPartialCreate {
		t.Fatalf("automation_reason_code = %q, want provider.partial_create", loaded.AutomationReasonCode)
	}
	if err := ledger.ReleaseExecutionClaim(t.Context(), winner.Claim); err != nil {
		t.Fatalf("ReleaseExecutionClaim: %v", err)
	}
	if _, err := ledger.AcquireProvisionDispatchClaim(
		t.Context(), record.Command, record.Head, binding, "amo-takeover", "amo-takeover-token", 5*time.Second,
	); !errors.Is(err, ErrProvisionManualReconcile) {
		t.Fatalf("guarded takeover error = %v, want ErrProvisionManualReconcile", err)
	}
	if _, err := ledger.AcquireExecutionClaim(
		t.Context(), record.Command, record.Head, ExecutionClaimSideEffecting, "generic-bypass", "generic-bypass-token", 5*time.Second,
	); !errors.Is(err, ErrProvisionManualReconcile) {
		t.Fatalf("generic claim after guard error = %v, want ErrProvisionManualReconcile", err)
	}

	assertDispatchGuardMutationRejected(t, db, record)
}

func TestPostgresLedgerAMOFirstClaimAloneCanBindResources(t *testing.T) {
	db := openProviderControlIntegrationDB(t)
	ledger, err := newIsolatedPostgresLedger(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	record := startAcceptedIntegrationAMOProvision(t, ledger)
	binding := preparedBindingForCommandHead(t, record.Command, record.Head)
	grant, err := ledger.AcquireProvisionDispatchClaim(
		t.Context(), record.Command, record.Head, binding, "amo-worker", "amo-token", 5*time.Second,
	)
	if err != nil {
		t.Fatalf("AcquireProvisionDispatchClaim: %v", err)
	}
	resource := providerexecutor.ResourceBinding{
		BindingID: "server", Kind: "compute", NativeRef: "provider-server-1",
		OwnershipHash: digest("owner-server"), Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
	}
	next, err := providerexecutor.AssembleReceipt(t.Context(), providerexecutor.ExecutionRequest{
		Command: record.Command, Previous: record.Head,
	}, providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
		Resources: []providerexecutor.ResourceBinding{resource},
	}, record.Head.IssuedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatalf("AssembleReceipt: %v", err)
	}
	if err := ledger.AppendClaimedReceipt(t.Context(), record.Command, record.Head, next, grant.Claim); err != nil {
		t.Fatalf("AppendClaimedReceipt: %v", err)
	}
	loaded, err := ledger.LoadOperation(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("LoadOperation: %v", err)
	}
	if loaded.Head.Phase != providerexecutor.PhaseResourcesBound || loaded.AutomationState != OperationAutomationRunnable {
		t.Fatalf("loaded head=%q automation=%q", loaded.Head.Phase, loaded.AutomationState)
	}
	withIntegrationTenant(t, db, loaded.Command.TenantID, func(tx *sql.Tx) error {
		_, updateErr := tx.ExecContext(t.Context(), `
			UPDATE provider_credential_handles
			SET revoked_at = clock_timestamp()
			WHERE tenant_id = $1 AND custody_ref = $2
		`, loaded.Command.TenantID, loaded.Command.CustodyRef)
		return updateErr
	})
	pollClaim, err := ledger.AcquireExecutionClaim(
		t.Context(), loaded.Command, loaded.Head, ExecutionClaimReadOnly, "poll-worker", "poll-token", 5*time.Second,
	)
	if err != nil {
		t.Fatalf("AcquireExecutionClaim for read-only poll: %v", err)
	}
	if err := ledger.ReleaseExecutionClaim(t.Context(), pollClaim); err != nil {
		t.Fatalf("ReleaseExecutionClaim for read-only poll: %v", err)
	}
}

func TestPostgresLedgerForcesTenantRLS(t *testing.T) {
	db := openProviderControlIntegrationDB(t)
	ledger, err := newIsolatedPostgresLedger(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	record := startAcceptedIntegrationPlan(t, ledger)
	claim, err := ledger.AcquireExecutionClaim(t.Context(), record.Command, record.Head, ExecutionClaimReadOnly, "worker-a", "tenant-claim-token", time.Minute)
	if err != nil {
		t.Fatalf("AcquireExecutionClaim: %v", err)
	}
	t.Cleanup(func() { _ = ledger.ReleaseExecutionClaim(context.Background(), claim) })

	var bypass bool
	if err := db.QueryRowContext(t.Context(), `
		SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user
	`).Scan(&bypass); err != nil {
		t.Fatalf("inspect integration role: %v", err)
	}
	if bypass {
		t.Skip("integration role bypasses RLS; trigger and transaction tests still run in the other integration cases")
	}

	var count int
	withIntegrationTenant(t, db, "tenant-2", func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM provider_operations").Scan(&count)
	})
	if count != 0 {
		t.Fatalf("tenant-2 can see %d tenant-1 provider operations", count)
	}
	withIntegrationTenant(t, db, "tenant-2", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM provider_operation_execution_claims").Scan(&count); err != nil {
			return err
		}
		result, err := tx.ExecContext(t.Context(), `
			UPDATE provider_operation_execution_claims SET state = 'released', released_at = clock_timestamp()
		`)
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if updated != 0 {
			return fmt.Errorf("tenant-2 updated %d tenant-1 claims", updated)
		}
		return nil
	})
	if count != 0 {
		t.Fatalf("tenant-2 can see %d tenant-1 execution claims", count)
	}
	withIntegrationTenant(t, db, "tenant-1", func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM provider_operations").Scan(&count)
	})
	if count != 1 {
		t.Fatalf("tenant-1 operation count = %d, want 1", count)
	}
}

func TestPostgresLedgerRejectsNonNativeLeaseAuthority(t *testing.T) {
	for _, test := range []struct {
		name      string
		statement string
	}{
		{name: "unbound", statement: `DELETE FROM runtime_lease_execution_authorities WHERE tenant_id = 'tenant-1' AND lease_id = 'lease-1'`},
		{name: "legacy quarantine", statement: `UPDATE runtime_lease_execution_authorities SET execution_authority = 'legacy_simulate' WHERE tenant_id = 'tenant-1' AND lease_id = 'lease-1'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openProviderControlIntegrationDB(t)
			withIntegrationTenant(t, db, "tenant-1", func(tx *sql.Tx) error {
				_, err := tx.ExecContext(t.Context(), test.statement)
				return err
			})
			ledger, err := newIsolatedPostgresLedger(db, nil)
			if err != nil {
				t.Fatalf("NewPostgresLedger: %v", err)
			}
			registry := NewRegistry()
			if registerErr := registry.Register("managed-compute", &queueExecutor{}); registerErr != nil {
				t.Fatalf("Register: %v", registerErr)
			}
			coordinator, err := newTestCoordinator(CoordinatorConfig{
				Registry: registry,
				Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
				Ledger:   ledger,
			})
			if err != nil {
				t.Fatalf("NewCoordinator: %v", err)
			}
			if _, _, err := coordinator.Start(t.Context(), planRequest()); !errors.Is(err, ErrExecutionAuthority) {
				t.Fatalf("Start error = %v, want ErrExecutionAuthority", err)
			}
			var operations int
			withIntegrationTenant(t, db, "tenant-1", func(tx *sql.Tx) error {
				return tx.QueryRowContext(t.Context(), `SELECT count(*) FROM provider_operations`).Scan(&operations)
			})
			if operations != 0 {
				t.Fatalf("non-native authority persisted %d operations", operations)
			}
		})
	}
}

func TestPostgresLedgerRejectsMismatchedRuntimeLeaseFence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*StartRequest)
	}{
		{
			name: "lease revision",
			mutate: func(request *StartRequest) {
				request.LeaseRevision++
			},
		},
		{
			name: "runtime server",
			mutate: func(request *StartRequest) {
				request.RuntimeServerID = "server-2"
			},
		},
		{
			name: "resource generation",
			mutate: func(request *StartRequest) {
				request.ResourceGenerationID = "22222222-2222-4222-8222-222222222222"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openProviderControlIntegrationDB(t)
			ledger, err := newIsolatedPostgresLedger(db, nil)
			if err != nil {
				t.Fatalf("NewPostgresLedger: %v", err)
			}
			registry := NewRegistry()
			if registerErr := registry.Register("managed-compute", &queueExecutor{}); registerErr != nil {
				t.Fatalf("Register: %v", registerErr)
			}
			coordinator, err := newTestCoordinator(CoordinatorConfig{
				Registry: registry,
				Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
				Ledger:   ledger,
			})
			if err != nil {
				t.Fatalf("NewCoordinator: %v", err)
			}
			request := planRequest()
			test.mutate(&request)
			if _, _, err := coordinator.Start(t.Context(), request); !errors.Is(err, ErrLeaseFence) {
				t.Fatalf("Start error = %v, want ErrLeaseFence", err)
			}
			var operations int
			withIntegrationTenant(t, db, "tenant-1", func(tx *sql.Tx) error {
				return tx.QueryRowContext(t.Context(), `SELECT count(*) FROM provider_operations`).Scan(&operations)
			})
			if operations != 0 {
				t.Fatalf("mismatched lease fence persisted %d operations", operations)
			}
		})
	}
}

func TestPostgresLedgerRejectsSupersededRuntimeLeaseFenceOnClaim(t *testing.T) {
	db := openProviderControlIntegrationDB(t)
	ledger, err := newIsolatedPostgresLedger(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	record := startAcceptedIntegrationPlan(t, ledger)
	withIntegrationTenant(t, db, record.Command.TenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `
			UPDATE techstack_vm_leases
			SET lease_revision = lease_revision + 1
			WHERE tenant_id = $1 AND id = $2
		`, record.Command.TenantID, record.Command.LeaseID)
		return err
	})
	assertRawActiveClaimInsert(t, db, record, ExecutionClaimReadOnly, "stale against the live typed runtime lease")

	if loaded, err := ledger.LoadOperation(t.Context(), record.Command.TenantID, record.Command.OperationID); err != nil {
		t.Fatalf("LoadOperation read projection after supersession: %v", err)
	} else if loaded.Head.ReceiptDigest != record.Head.ReceiptDigest {
		t.Fatalf("LoadOperation read projection head = %q, want %q", loaded.Head.ReceiptDigest, record.Head.ReceiptDigest)
	}
	if _, err := ledger.AcquireExecutionClaim(
		t.Context(), record.Command, record.Head, ExecutionClaimReadOnly, "worker-a", "claim-token", time.Minute,
	); !errors.Is(err, ErrLeaseFence) {
		t.Fatalf("AcquireExecutionClaim error = %v, want ErrLeaseFence", err)
	}
	var claims int
	withIntegrationTenant(t, db, record.Command.TenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), `
			SELECT count(*) FROM provider_operation_execution_claims
			WHERE tenant_id = $1 AND operation_id = $2
		`, record.Command.TenantID, record.Command.OperationID).Scan(&claims)
	})
	if claims != 0 {
		t.Fatalf("superseded lease fence persisted %d execution claims", claims)
	}
}

func TestPreCutoverLedgerFixtureFencesSideEffectWorkAfterTeardown(t *testing.T) {
	mutations := []struct {
		name      string
		statement string
	}{
		{
			name: "lease cancelled",
			statement: `
				UPDATE techstack_vm_leases
				SET cancelled_at = clock_timestamp()
				WHERE tenant_id = $1 AND id = $2
			`,
		},
		{
			name: "lease absent",
			statement: `
				UPDATE techstack_vm_leases
				SET desired_state = 'absent'
				WHERE tenant_id = $1 AND id = $2
			`,
		},
		{
			name: "server decommission tombstone",
			statement: `
				UPDATE servers
				SET desired_state = 'absent',
					lifecycle_state = 'decommissioned',
					decommissioned_at = clock_timestamp()
				WHERE tenant_id = $1 AND lease_id = $2 AND id = $3
			`,
		},
	}

	for _, operation := range []providerexecutor.Operation{
		providerexecutor.OperationProvision,
		providerexecutor.OperationReconcile,
	} {
		for _, mutation := range mutations {
			t.Run(string(operation)+"/"+mutation.name, func(t *testing.T) {
				db := openProviderControlIntegrationDB(t)
				ledger, err := newIsolatedPostgresLedger(db, nil)
				if err != nil {
					t.Fatalf("NewPostgresLedger: %v", err)
				}

				unclaimed := startAcceptedIntegrationRequest(t, ledger, sideEffectingRequest(operation, "unclaimed"))
				claimed := startAcceptedIntegrationRequest(t, ledger, sideEffectingRequest(operation, "claimed"))
				claim, err := ledger.AcquireExecutionClaim(
					t.Context(), claimed.Command, claimed.Head, ExecutionClaimSideEffecting, "worker-a", "teardown-token", time.Minute,
				)
				if err != nil {
					t.Fatalf("AcquireExecutionClaim before teardown: %v", err)
				}
				next := failedIntegrationReceipt(t, claimed)

				withIntegrationTenant(t, db, claimed.Command.TenantID, func(tx *sql.Tx) error {
					mutationArgs := []any{claimed.Command.TenantID, claimed.Command.LeaseID}
					if strings.Contains(mutation.statement, "$3") {
						mutationArgs = append(mutationArgs, claimed.Command.RuntimeServerID)
					}
					_, mutationErr := tx.ExecContext(
						t.Context(), mutation.statement, mutationArgs...,
					)
					return mutationErr
				})
				wantRawFence := "provider execution claim is fenced by cancellation or teardown intent"
				if mutation.name == "server decommission tombstone" {
					wantRawFence = "terminal server decommission tombstone"
				}
				assertRawActiveClaimInsert(t, db, unclaimed, ExecutionClaimSideEffecting, wantRawFence)

				if _, err := ledger.AcquireExecutionClaim(
					t.Context(), unclaimed.Command, unclaimed.Head, ExecutionClaimSideEffecting, "worker-b", "late-token", time.Minute,
				); !errors.Is(err, ErrLeaseFence) {
					t.Fatalf("AcquireExecutionClaim after teardown error = %v, want ErrLeaseFence", err)
				}
				assertRawActiveClaimHeartbeat(t, db, claimed, claim, wantRawFence)
				if _, err := ledger.RenewExecutionClaim(t.Context(), claim, time.Minute); !errors.Is(err, ErrLeaseFence) {
					t.Fatalf("RenewExecutionClaim in pre-cutover fixture error = %v, want ErrLeaseFence", err)
				}
				if err := ledger.AppendClaimedReceipt(
					t.Context(), claimed.Command, claimed.Head, next, claim,
				); !errors.Is(err, ErrLeaseFence) {
					t.Fatalf("AppendClaimedReceipt in pre-cutover fixture error = %v, want ErrLeaseFence", err)
				}

				wantHeadDigest := claimed.Head.ReceiptDigest
				wantClaimState := "active"
				var headDigest, claimState string
				var lateClaims int
				withIntegrationTenant(t, db, claimed.Command.TenantID, func(tx *sql.Tx) error {
					if err := tx.QueryRowContext(t.Context(), `
						SELECT head_receipt_digest
						FROM provider_operations
						WHERE tenant_id = $1 AND operation_id = $2
					`, claimed.Command.TenantID, claimed.Command.OperationID).Scan(&headDigest); err != nil {
						return err
					}
					if err := tx.QueryRowContext(t.Context(), `
						SELECT state
						FROM provider_operation_execution_claims
						WHERE tenant_id = $1 AND operation_id = $2 AND head_receipt_digest = $3
					`, claimed.Command.TenantID, claimed.Command.OperationID, claimed.Head.ReceiptDigest).Scan(&claimState); err != nil {
						return err
					}
					return tx.QueryRowContext(t.Context(), `
						SELECT count(*)
						FROM provider_operation_execution_claims
						WHERE tenant_id = $1 AND operation_id = $2
					`, unclaimed.Command.TenantID, unclaimed.Command.OperationID).Scan(&lateClaims)
				})
				if headDigest != wantHeadDigest || claimState != wantClaimState || lateClaims != 0 {
					t.Fatalf(
						"teardown custody = head=%q want=%q claim=%q want=%q late_claims=%d",
						headDigest, wantHeadDigest, claimState, wantClaimState, lateClaims,
					)
				}
			})
		}
	}
}

func TestPostgresLedgerAdmitsOnlyExactDecommissionDuringTeardown(t *testing.T) {
	db := openProviderControlIntegrationDB(t)
	ledger, err := newIsolatedPostgresLedger(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	withIntegrationTenant(t, db, "tenant-1", func(tx *sql.Tx) error {
		if _, updateLeaseErr := tx.ExecContext(t.Context(), `
			UPDATE techstack_vm_leases
			SET desired_state = 'absent', cancelled_at = clock_timestamp()
			WHERE tenant_id = 'tenant-1' AND id = 'lease-1'
		`); updateLeaseErr != nil {
			return updateLeaseErr
		}
		_, updateServerErr := tx.ExecContext(t.Context(), `
			UPDATE servers
			SET desired_state = 'absent', lifecycle_state = 'decommissioning'
			WHERE tenant_id = 'tenant-1' AND id = 'server-1'
		`)
		return updateServerErr
	})

	record := startAcceptedIntegrationRequest(t, ledger, decommissionRequest("exact"))
	unclaimed := startAcceptedIntegrationRequest(t, ledger, decommissionRequest("unclaimed"))
	assertRawActiveClaimInsert(t, db, unclaimed, ExecutionClaimSideEffecting, "")
	claim, err := ledger.AcquireExecutionClaim(
		t.Context(), record.Command, record.Head, ExecutionClaimSideEffecting, "cleanup-worker", "cleanup-token", time.Minute,
	)
	if err != nil {
		t.Fatalf("AcquireExecutionClaim for exact decommission: %v", err)
	}
	t.Cleanup(func() { _ = ledger.ReleaseExecutionClaim(context.Background(), claim) })

	wrong := decommissionRequest("wrong-generation")
	wrong.ResourceGenerationID = "22222222-2222-4222-8222-222222222222"
	coordinator := integrationCoordinator(t, ledger)
	if _, _, err := coordinator.Start(t.Context(), wrong); !errors.Is(err, ErrLeaseFence) {
		t.Fatalf("mismatched-generation decommission error = %v, want ErrLeaseFence", err)
	}

	withIntegrationTenant(t, db, "tenant-1", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `
			UPDATE servers
			SET lifecycle_state = 'decommissioned', decommissioned_at = clock_timestamp()
			WHERE tenant_id = 'tenant-1' AND id = 'server-1'
		`)
		return err
	})
	assertRawActiveClaimInsert(t, db, unclaimed, ExecutionClaimSideEffecting, "terminal server decommission tombstone")
	assertRawActiveClaimHeartbeat(t, db, record, claim, "terminal server decommission tombstone")
	if _, err := ledger.AcquireExecutionClaim(
		t.Context(), unclaimed.Command, unclaimed.Head, ExecutionClaimSideEffecting, "cleanup-worker-2", "late-cleanup-token", time.Minute,
	); !errors.Is(err, ErrLeaseFence) {
		t.Fatalf("decommission claim after terminal tombstone error = %v, want ErrLeaseFence", err)
	}
	if _, err := ledger.RenewExecutionClaim(t.Context(), claim, time.Minute); !errors.Is(err, ErrLeaseFence) {
		t.Fatalf("decommission renewal after terminal tombstone error = %v, want ErrLeaseFence", err)
	}
	if err := ledger.AppendClaimedReceipt(
		t.Context(), record.Command, record.Head, failedIntegrationReceipt(t, record), claim,
	); !errors.Is(err, ErrLeaseFence) {
		t.Fatalf("decommission append after terminal tombstone error = %v, want ErrLeaseFence", err)
	}
}

func assertPlaintextClaimTokensAbsent(t *testing.T, db *sql.DB, record OperationRecord) {
	t.Helper()
	var plaintextColumns int
	if err := db.QueryRowContext(t.Context(), `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'provider_operation_execution_claims'
		  AND column_name = 'claim_token'
	`).Scan(&plaintextColumns); err != nil {
		t.Fatalf("inspect execution-claim columns: %v", err)
	}
	if plaintextColumns != 0 {
		t.Fatalf("provider execution claims expose %d plaintext token columns", plaintextColumns)
	}

	var digest string
	withIntegrationTenant(t, db, record.Command.TenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), `
			SELECT claim_token_digest FROM provider_operation_execution_claims
			WHERE tenant_id = $1 AND operation_id = $2
		`, record.Command.TenantID, record.Command.OperationID).Scan(&digest)
	})
	if len(digest) != sha256.Size*2 {
		t.Fatalf("persisted claim-token digest length = %d, want %d", len(digest), sha256.Size*2)
	}
}

func assertRawClaimMutationRejected(t *testing.T, db *sql.DB, record OperationRecord) {
	t.Helper()
	for name, statement := range map[string]string{
		"future takeover": `
			UPDATE provider_operation_execution_claims
			SET claim_token_digest = repeat('0', 64), claim_owner = 'raw-owner',
				claimed_at = clock_timestamp() + interval '1 day',
				lease_expires_at = clock_timestamp() + interval '1 day 1 minute'
			WHERE tenant_id = $1 AND operation_id = $2`,
		"consume without capability": `
			UPDATE provider_operation_execution_claims
			SET state = 'consumed', released_at = clock_timestamp()
			WHERE tenant_id = $1 AND operation_id = $2`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatalf("begin raw claim mutation: %v", err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, record.Command.TenantID); err != nil {
				t.Fatalf("set raw claim tenant: %v", err)
			}
			if _, err := tx.ExecContext(t.Context(), statement, record.Command.TenantID, record.Command.OperationID); err == nil {
				t.Fatal("raw claim mutation succeeded without the bound capability")
			}
		})
	}
}

func assertRawOversizedClaimInsertRejected(t *testing.T, db *sql.DB, record OperationRecord) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin oversized claim insert transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, record.Command.TenantID); err != nil {
		t.Fatalf("set oversized claim insert tenant: %v", err)
	}
	_, err = tx.ExecContext(t.Context(), `
		INSERT INTO provider_operation_execution_claims (
			tenant_id, operation_id, head_sequence, head_receipt_digest,
			claim_token_digest, claim_owner, claim_access, state, claimed_at, lease_expires_at
		) VALUES (
			$1, $2, $3, $4, repeat('0', 64), 'raw-owner', 'read_only', 'active',
			clock_timestamp(), clock_timestamp() + interval '1 day'
		)
	`, record.Command.TenantID, record.Command.OperationID, record.Head.Sequence, record.Head.ReceiptDigest)
	if err == nil || !strings.Contains(err.Error(), "provider execution claim acquisition lease is outside the allowed range") {
		t.Fatalf("raw oversized claim insert error = %v", err)
	}
}

func assertRawActiveClaimInsert(
	t *testing.T,
	db *sql.DB,
	record OperationRecord,
	access ExecutionClaimAccess,
	wantError string,
) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin raw active claim insert transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	token := "raw-active-claim-token"
	owner := "raw-active-claim-owner"
	if _, err := tx.ExecContext(t.Context(), `
		SELECT
			set_config('app.tenant_id', $1, true),
			set_config('app.provider_execution_claim_token', $2, true),
			set_config('app.provider_execution_claim_owner', $3, true)
	`, record.Command.TenantID, token, owner); err != nil {
		t.Fatalf("bind raw active claim capability: %v", err)
	}
	_, err = tx.ExecContext(t.Context(), `
		INSERT INTO provider_operation_execution_claims (
			tenant_id, operation_id, head_sequence, head_receipt_digest,
			claim_token_digest, claim_owner, claim_access, state, claimed_at, lease_expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', clock_timestamp(), clock_timestamp() + interval '1 minute')
	`, record.Command.TenantID, record.Command.OperationID, record.Head.Sequence, record.Head.ReceiptDigest,
		executionClaimTokenDigest(token), owner, access)
	if wantError == "" {
		if err != nil {
			t.Fatalf("raw exact active claim insert error = %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("raw active claim insert error = %v, want %q", err, wantError)
	}
}

func assertRawActiveClaimHeartbeat(
	t *testing.T,
	db *sql.DB,
	record OperationRecord,
	claim ExecutionClaim,
	wantError string,
) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin raw active claim heartbeat transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `
		SELECT
			set_config('app.tenant_id', $1, true),
			set_config('app.provider_execution_claim_token', $2, true),
			set_config('app.provider_execution_claim_owner', $3, true)
	`, record.Command.TenantID, claim.Token, claim.Owner); err != nil {
		t.Fatalf("bind raw active claim heartbeat capability: %v", err)
	}
	_, err = tx.ExecContext(t.Context(), `
		UPDATE provider_operation_execution_claims
		SET lease_expires_at = clock_timestamp() + interval '1 minute'
		WHERE tenant_id = $1 AND operation_id = $2 AND head_receipt_digest = $3
	`, record.Command.TenantID, record.Command.OperationID, record.Head.ReceiptDigest)
	if err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("raw active claim heartbeat error = %v, want %q", err, wantError)
	}
}

func assertRawExpiredHeartbeatRejected(t *testing.T, db *sql.DB, record OperationRecord, claim ExecutionClaim) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin expired heartbeat transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `
		SELECT
			set_config('app.tenant_id', $1, true),
			set_config('app.provider_execution_claim_token', $2, true),
			set_config('app.provider_execution_claim_owner', $3, true)
	`, record.Command.TenantID, claim.Token, claim.Owner); err != nil {
		t.Fatalf("bind expired heartbeat capability: %v", err)
	}
	_, err = tx.ExecContext(t.Context(), `
		UPDATE provider_operation_execution_claims
		SET lease_expires_at = clock_timestamp() + interval '1 minute'
		WHERE tenant_id = $1 AND operation_id = $2
	`, record.Command.TenantID, record.Command.OperationID)
	if err == nil || !strings.Contains(err.Error(), "expired provider execution claim cannot be heartbeat-renewed") {
		t.Fatalf("raw expired heartbeat error = %v", err)
	}
}

func assertRawOversizedHeartbeatRejected(t *testing.T, db *sql.DB, record OperationRecord, claim ExecutionClaim) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin oversized heartbeat transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `
		SELECT
			set_config('app.tenant_id', $1, true),
			set_config('app.provider_execution_claim_token', $2, true),
			set_config('app.provider_execution_claim_owner', $3, true)
	`, record.Command.TenantID, claim.Token, claim.Owner); err != nil {
		t.Fatalf("bind oversized heartbeat capability: %v", err)
	}
	_, err = tx.ExecContext(t.Context(), `
		UPDATE provider_operation_execution_claims
		SET lease_expires_at = clock_timestamp() + interval '1 day'
		WHERE tenant_id = $1 AND operation_id = $2
	`, record.Command.TenantID, record.Command.OperationID)
	if err == nil || !strings.Contains(err.Error(), "provider execution claim heartbeat lease is outside the allowed range") {
		t.Fatalf("raw oversized heartbeat error = %v", err)
	}
}

func assertDispatchGuardMutationRejected(t *testing.T, db *sql.DB, record OperationRecord) {
	t.Helper()
	for name, statement := range map[string]string{
		"update": `
			UPDATE provider_provision_dispatch_guards SET first_claim_owner = 'rewritten'
			WHERE tenant_id = $1 AND operation_id = $2`,
		"delete": `
			DELETE FROM provider_provision_dispatch_guards
			WHERE tenant_id = $1 AND operation_id = $2`,
	} {
		t.Run("guard-"+name, func(t *testing.T) {
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatalf("begin guard mutation: %v", err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, record.Command.TenantID); err != nil {
				t.Fatalf("set guard tenant: %v", err)
			}
			if _, err := tx.ExecContext(t.Context(), statement, record.Command.TenantID, record.Command.OperationID); err == nil {
				t.Fatalf("dispatch guard %s succeeded", name)
			}
		})
	}
}

func startAcceptedIntegrationPlan(t *testing.T, ledger *PostgresLedger) OperationRecord {
	t.Helper()
	return startAcceptedIntegrationRequest(t, ledger, planRequest())
}

func startAcceptedIntegrationAMOProvision(t *testing.T, ledger *PostgresLedger) OperationRecord {
	t.Helper()
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-amo", &atMostOnceExecutor{}); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-amo")
	profile.RuntimeProfileID = "ionos-amo-pvm-monthly"
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile},
		Ledger: ledger, ClaimOwner: "amo-integration-worker", ClaimTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), provisionRequest())
	if err != nil {
		t.Fatalf("Start AMO provision: %v", err)
	}
	record, advanced, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil || !advanced || record.Head.Phase != providerexecutor.PhaseAccepted || len(record.Head.Resources) != 0 {
		t.Fatalf("Advance AMO accepted = %+v, advanced=%v, err=%v", record.Head, advanced, err)
	}
	return record
}

func startAcceptedIntegrationRequest(t *testing.T, ledger *PostgresLedger, request StartRequest) OperationRecord {
	t.Helper()
	coordinator := integrationCoordinator(t, ledger)
	record, _, err := coordinator.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, advanced, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil || !advanced || record.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("Advance accepted = %+v, advanced=%v, err=%v", record.Head, advanced, err)
	}
	return record
}

func integrationCoordinator(t *testing.T, ledger *PostgresLedger) *Coordinator {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Register("managed-compute", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
		Ledger:   ledger, ClaimOwner: "integration-worker", ClaimTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return coordinator
}

func sideEffectingRequest(operation providerexecutor.Operation, suffix string) StartRequest {
	request := planRequest()
	request.Operation = operation
	request.IdempotencyKey = string(operation) + "-" + suffix
	request.LedgerRevision = 4
	if operation == providerexecutor.OperationReconcile {
		request.Targets = []providerexecutor.ResourceTarget{integrationResourceTarget()}
	}
	return request
}

func decommissionRequest(suffix string) StartRequest {
	return StartRequest{
		TenantID: "tenant-1", LeaseID: "lease-1", LeaseRevision: 7,
		RuntimeServerID: "server-1", ResourceGenerationID: testResourceGenerationID,
		Operation: providerexecutor.OperationDecommission, IdempotencyKey: "decommission-" + suffix,
		LedgerRevision: 5, Targets: []providerexecutor.ResourceTarget{integrationResourceTarget()},
		RequestedAt: contractNow,
	}
}

func integrationResourceTarget() providerexecutor.ResourceTarget {
	return providerexecutor.ResourceTarget{
		BindingID: "server", Kind: "compute", NativeRef: "provider-server-1",
		OwnershipHash: digest("owner"), Disposition: providerexecutor.DispositionDelete,
	}
}

func failedIntegrationReceipt(t *testing.T, record OperationRecord) providerexecutor.Receipt {
	t.Helper()
	next, err := providerexecutor.AssembleReceipt(t.Context(), providerexecutor.ExecutionRequest{
		Command: record.Command, Previous: record.Head,
	}, providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusFailed,
		Phase:  providerexecutor.PhaseFailed,
		Reason: &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderTransient, Retryable: true},
	}, record.Head.IssuedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatalf("AssembleReceipt failed result: %v", err)
	}
	return next
}

func assertRawHeadShiftRejected(t *testing.T, db *sql.DB, record OperationRecord, next providerexecutor.Receipt) {
	t.Helper()
	payload, err := json.Marshal(next)
	if err != nil {
		t.Fatalf("marshal next receipt: %v", err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin raw bypass transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, setTenantErr := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, record.Command.TenantID); setTenantErr != nil {
		t.Fatalf("set raw bypass tenant: %v", setTenantErr)
	}
	if _, insertErr := tx.ExecContext(t.Context(), `
		INSERT INTO provider_operation_receipts (
			tenant_id, operation_id, sequence, previous_receipt_digest,
			receipt_digest, status, phase, receipt_json, issued_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)
	`, record.Command.TenantID, record.Command.OperationID, next.Sequence,
		next.PreviousReceiptDigest, next.ReceiptDigest, next.Status, next.Phase, payload, next.IssuedAt); insertErr != nil {
		t.Fatalf("insert raw bypass receipt: %v", insertErr)
	}
	_, err = tx.ExecContext(t.Context(), `
		UPDATE provider_operations
		SET status=$3, phase=$4, head_sequence=$5, head_receipt_digest=$6, updated_at=$7
		WHERE tenant_id=$1 AND operation_id=$2
	`, record.Command.TenantID, record.Command.OperationID, next.Status, next.Phase,
		next.Sequence, next.ReceiptDigest, next.IssuedAt)
	if err == nil || !strings.Contains(err.Error(), "requires a consumed execution claim") {
		t.Fatalf("raw head shift error = %v, want consumed-claim trigger rejection", err)
	}
}

func integrationDBTime(t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := db.QueryRowContext(t.Context(), "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatalf("read integration DB time: %v", err)
	}
	return now
}

func withIntegrationTenant(t *testing.T, db *sql.DB, tenantID string, fn func(*sql.Tx) error) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin tenant transaction: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}
	if err := fn(tx); err != nil {
		t.Fatalf("tenant transaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tenant transaction: %v", err)
	}
	committed = true
}
