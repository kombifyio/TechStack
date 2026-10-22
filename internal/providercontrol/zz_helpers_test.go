package providercontrol

// Helpers of retired unit tests that the remaining boundary tests of this package still use.

import (
	"context"
	"path/filepath"
	"fmt"
	"encoding/hex"
	"encoding/json"
	"os"
	pgx "github.com/jackc/pgx/v5"
	providerexecutor "github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"crypto/sha256"
	"database/sql"
	"github.com/jackc/pgx/v5/stdlib"
	"strings"
	"sync"
	"testing"
	"time"
	"github.com/google/uuid"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

// AllowGate is the test-only activation gate for callers which need to prove
// behavior beyond mutation admission.
type AllowGate struct{}

// Require allows activation only inside tests.
func (AllowGate) Require(context.Context) error { return nil }

func newNativeAdmissionIntegrationServiceWithResolver(
	t *testing.T,
	database *sql.DB,
	resolver ExecutionProfileResolver,
	capacity CapacityPolicyResolver,
) (*NativeAdmission, *queueExecutor) {
	return newNativeAdmissionIntegrationServiceWithResolverAndProviderCreates(
		t, database, resolver, capacity, AllowProviderCreate{},
	)
}

func newNativeAdmissionIntegrationServiceWithResolverAndProviderCreates(
	t *testing.T,
	database *sql.DB,
	resolver ExecutionProfileResolver,
	capacity CapacityPolicyResolver,
	providerCreates ProviderCreateAuthorizer,
) (*NativeAdmission, *queueExecutor) {
	t.Helper()
	executor := &queueExecutor{}
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", executor); err != nil {
		t.Fatalf("register native integration adapter: %v", err)
	}
	ledger, err := NewPostgresLedger(database, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	gate := AllowGate{}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: resolver, Ledger: ledger, ActivationGate: gate,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	admission, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ActivationGate: gate,
		ProviderCreates: providerCreates,
		CapacityPolicy:  capacity,
	})
	if err != nil {
		t.Fatalf("NewNativeAdmission: %v", err)
	}
	return admission, executor
}

func seedNativeAdmissionCredentialHandle(t *testing.T, database *sql.DB) {
	seedNativeAdmissionCredentialHandleForProvider(t, database, "ionos")
}

func seedNativeAdmissionCredentialHandleForProvider(t *testing.T, database *sql.DB, providerID string) {
	t.Helper()
	handle := testCredentialHandle()
	handle.ProviderID = providerID
	custodyHash, err := credentialCustodyHash("tenant-1", handle)
	if err != nil {
		t.Fatalf("hash native admission credential custody: %v", err)
	}
	connectionHash, err := credentialConnectionHash("tenant-1", handle)
	if err != nil {
		t.Fatalf("hash native admission provider connection: %v", err)
	}
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin native admission credential seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, "tenant-1"); err != nil {
		t.Fatalf("set native admission credential tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
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
		t.Fatalf("seed native admission credential handle: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit native admission credential handle: %v", err)
	}
}

func openNativeAdmissionIntegrationDB(t *testing.T) *sql.DB {
	return openNativeAdmissionIntegrationDBWithDispatch(t, ProvisionDispatchNativeIdempotency)
}

func openNativeAdmissionIntegrationDBWithDispatch(
	t *testing.T,
	provisionDispatchMode ProvisionDispatchMode,
) *sql.DB {
	return openNativeAdmissionIntegrationDBWithProfile(
		t,
		provisionDispatchMode,
		"ionos",
		"ionos-v1",
		"ionos-managed-pvm-monthly",
	)
}

func openNativeAdmissionIntegrationDBWithProfile(
	t *testing.T,
	provisionDispatchMode ProvisionDispatchMode,
	providerID string,
	adapterID string,
	runtimeProfileID string,
) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping native admission PostgreSQL integration test")
	}

	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse native admission integration DSN: %v", err)
	}
	adminDatabase := stdlib.OpenDB(*adminConfig)
	if pingErr := adminDatabase.PingContext(t.Context()); pingErr != nil {
		_ = adminDatabase.Close()
		t.Fatalf("ping native admission integration PostgreSQL: %v", pingErr)
	}
	schema := fmt.Sprintf("native_admission_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, createSchemaErr := adminDatabase.ExecContext(t.Context(), "CREATE SCHEMA "+quotedSchema); createSchemaErr != nil {
		_ = adminDatabase.Close()
		t.Fatalf("create native admission integration schema: %v", createSchemaErr)
	}

	scopedConfig := adminConfig.Copy()
	scopedConfig.RuntimeParams = make(map[string]string, len(adminConfig.RuntimeParams)+1)
	for key, value := range adminConfig.RuntimeParams {
		scopedConfig.RuntimeParams[key] = value
	}
	scopedConfig.RuntimeParams["search_path"] = schema + ",public"
	database := stdlib.OpenDB(*scopedConfig)
	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)
	if pingErr := database.PingContext(t.Context()); pingErr != nil {
		_ = database.Close()
		_, _ = adminDatabase.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = adminDatabase.Close()
		t.Fatalf("open native admission integration database: %v", pingErr)
	}
	t.Cleanup(func() {
		_ = database.Close()
		_, _ = adminDatabase.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = adminDatabase.Close()
	})
	applyNativeAdmissionMigrations(t, database)
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin native admission tenant seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, "tenant-1"); err != nil {
		t.Fatalf("set native admission seed tenant context: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO techstack_tenants (id, display_name, kind, status)
		VALUES ('tenant-1', 'Native Admission Test', 'saas', 'active')
	`); err != nil {
		t.Fatalf("seed native admission tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO stacks (id, tenant_id, owner_subject_id, name, status)
		VALUES ('stack-native-1', 'tenant-1', 'owner-1', 'Native Admission Stack', 'draft')
	`); err != nil {
		t.Fatalf("seed native admission stack: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO provider_catalog_versions (catalog_version, status)
		VALUES ('catalog-2026-07-21', 'draft')
	`); err != nil {
		t.Fatalf("seed native admission provider catalog version: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO provider_catalog_profiles (
			catalog_version, provider_id, adapter_id, credential_mode,
			runtime_profile_id, offering_id, can_pause, stop_effect,
			can_recreate, capability_snapshot, adapter_manifest_hash,
			provision_dispatch_mode
		) VALUES (
			'catalog-2026-07-21', $1, $2, 'managed',
			$3, 'monthly-runtime-standard',
			true, 'pause', true, '{}'::jsonb, $4, $5
		)
	`, providerID, adapterID, runtimeProfileID, digest("test-adapter-manifest"), provisionDispatchMode); err != nil {
		t.Fatalf("seed native admission provider catalog profile: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		UPDATE provider_catalog_versions
		SET status = 'retired', retired_at = clock_timestamp()
		WHERE status = 'active';

		UPDATE provider_catalog_versions
		SET status = 'active', activated_at = clock_timestamp()
		WHERE catalog_version = 'catalog-2026-07-21'
	`); err != nil {
		t.Fatalf("activate native admission provider catalog: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit native admission tenant seed: %v", err)
	}
	return database
}

func openRestrictedNativeAdmissionRuntimeDB(t *testing.T, adminDatabase *sql.DB) *sql.DB {
	t.Helper()
	var schema string
	if err := adminDatabase.QueryRowContext(t.Context(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatalf("load native admission integration schema: %v", err)
	}
	var superuser, createRole bool
	if err := adminDatabase.QueryRowContext(t.Context(), `
		SELECT rolsuper, rolcreaterole
		FROM pg_roles
		WHERE rolname = current_user
	`).Scan(&superuser, &createRole); err != nil {
		t.Fatalf("inspect native admission integration role authority: %v", err)
	}
	if !superuser && !createRole {
		t.Skip("integration PostgreSQL role cannot create a restricted runtime role")
	}

	role := "native_admission_runtime_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := "native_admission_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedRole := pgx.Identifier{role}.Sanitize()
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDatabase.ExecContext(t.Context(), fmt.Sprintf(
		"CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS",
		quotedRole, password,
	)); err != nil {
		t.Fatalf("create restricted native admission role: %v", err)
	}

	var runtimeDatabase *sql.DB
	t.Cleanup(func() {
		if runtimeDatabase != nil {
			_ = runtimeDatabase.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = adminDatabase.ExecContext(cleanupCtx, "DROP OWNED BY "+quotedRole)
		_, _ = adminDatabase.ExecContext(cleanupCtx, "DROP ROLE IF EXISTS "+quotedRole)
	})
	// #nosec G202 -- both identifiers are locally generated and quoted with pgx.Identifier.
	if _, err := adminDatabase.ExecContext(t.Context(),
		"GRANT USAGE ON SCHEMA "+quotedSchema+" TO "+quotedRole); err != nil {
		t.Fatalf("grant restricted integration schema usage: %v", err)
	}
	grants := []struct {
		table      string
		privileges string
	}{
		{"provider_catalog_versions", "SELECT"},
		{"provider_catalog_profiles", "SELECT"},
		{"provider_credential_handles", "SELECT"},
		{"substrate_bindings", "SELECT"},
		{"substrate_guest_leases", "SELECT,INSERT"},
		{"servers", "SELECT,INSERT,UPDATE"},
		{"server_state_transitions", "SELECT,INSERT"},
		{"server_registry_outbox", "SELECT,INSERT"},
		{"techstack_vm_leases", "SELECT,INSERT"},
		{"runtime_lease_execution_authorities", "SELECT,INSERT"},
		{"runtime_lease_idempotency_records", "SELECT,INSERT"},
		{"managed_runtime_capacity_reservations", "SELECT,INSERT"},
		{"managed_runtime_server_slots", "SELECT,INSERT"},
		{"managed_runtime_server_slot_generations", "SELECT,INSERT"},
		{"managed_runtime_capacity_release_facts", "SELECT,INSERT"},
		{"managed_runtime_capacity_quarantine_retirements", "SELECT"},
		{"provider_desired_spec_revisions", "SELECT,INSERT,UPDATE"},
		{"provider_operations", "SELECT,INSERT,UPDATE"},
		{"provider_operation_receipts", "SELECT,INSERT"},
		{"provider_operation_resources", "SELECT,INSERT,UPDATE"},
		{"provider_operation_evidence", "SELECT,INSERT,UPDATE"},
		{"provider_operation_execution_claims", "SELECT,INSERT,UPDATE"},
		{"provider_provision_dispatch_guards", "SELECT,INSERT"},
		{"provider_provision_discovery_observations", "SELECT,INSERT"},
		{"provider_provision_resolution_decisions", "SELECT,INSERT"},
		{"server_provider_resource_bindings", "SELECT,INSERT"},
		{"provider_operation_resource_free_terminalizations", "SELECT,INSERT"},
	}
	for _, grant := range grants {
		// #nosec G202 -- table names are constants and the role identifier is locally generated and quoted.
		statement := "GRANT " + grant.privileges + " ON " +
			pgx.Identifier{schema, grant.table}.Sanitize() + " TO " + quotedRole
		if _, err := adminDatabase.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("grant restricted runtime access on %s: %v", grant.table, err)
		}
	}
	for _, sequence := range []string{"server_state_transitions_id_seq", "server_registry_outbox_id_seq"} {
		// #nosec G202 -- sequence names are constants and the role identifier is locally generated and quoted.
		statement := "GRANT USAGE ON SEQUENCE " +
			pgx.Identifier{schema, sequence}.Sanitize() + " TO " + quotedRole
		if _, err := adminDatabase.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("grant restricted runtime sequence %s: %v", sequence, err)
		}
	}
	for _, function := range []string{
		"substrate_guest_custody_valid(text,text,text,text)",
		"substrate_lock_binding(text,text,bigint)",
		"provider_control_lock_runtime_lease_projection(text)",
		"provider_control_count_unsettled_generation_dispatch_guards(text,uuid)",
	} {
		// #nosec G202 -- function and role identifiers are fixed or locally generated and quoted.
		if _, err := adminDatabase.ExecContext(
			t.Context(),
			"GRANT EXECUTE ON FUNCTION "+quotedSchema+"."+function+" TO "+quotedRole,
		); err != nil {
			t.Fatalf("grant restricted runtime function %s: %v", function, err)
		}
	}

	config, err := pgx.ParseConfig(strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL")))
	if err != nil {
		t.Fatalf("parse restricted native admission DSN: %v", err)
	}
	config.User = role
	config.Password = password
	runtimeParams := make(map[string]string, len(config.RuntimeParams)+1)
	for key, value := range config.RuntimeParams {
		runtimeParams[key] = value
	}
	config.RuntimeParams = runtimeParams
	config.RuntimeParams["search_path"] = schema + ",public"
	runtimeDatabase = stdlib.OpenDB(*config)
	runtimeDatabase.SetMaxOpenConns(4)
	runtimeDatabase.SetMaxIdleConns(4)
	if err := runtimeDatabase.PingContext(t.Context()); err != nil {
		t.Fatalf("ping restricted native admission role: %v", err)
	}
	return runtimeDatabase
}

func applyNativeAdmissionMigrations(t *testing.T, database *sql.DB) {
	t.Helper()
	migrationDir := filepath.Join("..", "..", "pkg", "db", "migrations")
	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		t.Fatalf("read native admission migrations: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(migrationDir, entry.Name()))
		if err != nil {
			t.Fatalf("read migration %s: %v", entry.Name(), err)
		}
		tx, err := database.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatalf("begin migration %s: %v", entry.Name(), err)
		}
		if _, err := tx.ExecContext(t.Context(), string(payload)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply migration %s: %v", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit migration %s: %v", entry.Name(), err)
		}
	}
}

type staticAdmissionCapacityPolicy struct {
	grant CapacityGrant
	err   error
}

func (p staticAdmissionCapacityPolicy) ResolveCapacity(context.Context, CapacityPolicyRequest) (CapacityGrant, error) {
	return p.grant, p.err
}

func ownerLimitedCapacityGrant(ownerID string, limit int) CapacityGrant {
	return CapacityGrant{
		ScopeKind: managedRuntimeCapacityScopeOwner, ScopeID: ownerID,
		Mode: CapacityModeLimited, Limit: limit,
		DecisionSource: CapacityDecisionSourceSignedRuntimeBudget,
	}
}

func nativeProvisionAdmissionFixture() NativeProvisionAdmissionRequest {
	return NativeProvisionAdmissionRequest{
		TenantID:        "tenant-1",
		RuntimeSlotKey:  "foundation",
		RuntimeSlotID:   DeriveManagedRuntimeSlotID("tenant-1", "stack-native-1", "foundation"),
		RuntimeServerID: "server-native-1",
		OwnerSubjectID:  "owner-1",
		IdempotencyKey:  "native-admission-1",
		Lease: vmlease.Lease{
			ID:      "lease-native-1",
			Subject: vmlease.Subject{Kind: vmlease.SubjectOrg, ID: "org-1", OrgID: "tenant-1"},
			Resource: vmlease.ResourceRef{
				ProviderID: " IONOS ",
			},
			DesiredState:   vmlease.DesiredStateRunning,
			BillingMode:    vmlease.BillingModeSubscription,
			LifecycleClass: vmlease.LifecycleClassSubscription,
			RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
			RecreatePolicy: vmlease.RecreatePolicyManual,
		},
		Server:         NativeAdmissionServer{StackID: "stack-native-1", Name: "native-runtime"},
		DesiredSpecRef: "desired-spec://techstack/leases/lease-native-1/revisions/1",
		DesiredSpec:    json.RawMessage(`{"region":"de","cpu":4,"ram":8}`),
	}
}

var contractNow = time.Date(2026, 7, 17, 16, 0, 0, 0, time.UTC)

func testCredentialHandle() credentialHandleRow {
	return credentialHandleRow{
		HandleID: "provider-access", HandleVersion: 4, ProviderID: "ionos",
		CredentialMode: CredentialModeManaged, SubjectKind: "org", SubjectID: "tenant-1",
		GrantID: "managed-compute", Scope: "compute",
		CustodyRef:    "custody://tenant-1/provider-access",
		ConnectionRef: "provider-connection://tenant-1/managed-compute",
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type queueExecutor struct {
	mu            sync.Mutex
	results       []providerexecutor.ExecutionResult
	calls         int
	readOnlyCalls int
	mutationCalls int
}

func (e *queueExecutor) execute(access ExecutionClaimAccess) providerexecutor.ExecutionResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if access == ExecutionClaimReadOnly {
		e.readOnlyCalls++
	} else {
		e.mutationCalls++
	}
	if len(e.results) == 0 {
		return providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusFailed,
			Phase:  providerexecutor.PhaseFailed,
			Reason: &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderTransient, Retryable: true},
		}
	}
	result := e.results[0]
	e.results = e.results[1:]
	return result
}

func (*queueExecutor) CrashRecoveryCapability() CrashRecoveryCapability {
	return CrashRecoveryCapability{
		AdapterManifestHash: digest("test-adapter-manifest"),
		Mode:                CrashRecoveryNativeIdempotency, PerHeadInvocationKey: true,
	}
}

func (e *queueExecutor) ExecuteCrashRecoverableReadOnly(context.Context, AdapterInvocation) providerexecutor.ExecutionResult {
	return e.execute(ExecutionClaimReadOnly)
}

func (e *queueExecutor) ExecuteCrashRecoverableMutation(context.Context, AdapterInvocation) providerexecutor.ExecutionResult {
	return e.execute(ExecutionClaimSideEffecting)
}

