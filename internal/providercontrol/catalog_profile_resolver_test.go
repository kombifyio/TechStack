package providercontrol

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestPostgresCatalogExecutionProfileResolverReturnsStableSecretFreeProfile(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()
	resolver, err := NewPostgresCatalogExecutionProfileResolver(db)
	if err != nil {
		t.Fatalf("NewPostgresCatalogExecutionProfileResolver: %v", err)
	}

	capabilities := []string{
		`{"regions":["de-fra"],"network":{"ipv4":true,"ipv6":false},"storage":{"snapshots":true,"volume_types":["ssd"]}}`,
		`{"storage":{"volume_types":["ssd"],"snapshots":true},"network":{"ipv6":false,"ipv4":true},"regions":["de-fra"]}`,
	}
	profiles := make([]ExecutionProfile, 0, len(capabilities))
	for _, capability := range capabilities {
		expectCatalogResolverBegin(mock)
		expectLeaseProfileSelection(mock, "ionos")
		expectActiveCatalog(mock, "catalog-2026-07-22")
		expectCatalogProfile(mock, capability)
		expectCredentialHandles(mock, credentialHandleRows().AddRow(credentialHandleTestValues(t,
			"handle-main", int64(4), "ionos", "managed", "org", "tenant-1",
			"grant-compute", "compute", "custody://tenant-1/ionos/main/v4",
			"provider-connection://tenant-1/ionos/main/v4",
		)...))
		mock.ExpectCommit()

		profile, resolveErr := resolver.ResolveExecutionProfile(t.Context(), catalogProfileRequest())
		if resolveErr != nil {
			t.Fatalf("ResolveExecutionProfile: %v", resolveErr)
		}
		profiles = append(profiles, profile)
	}

	got := profiles[0]
	if got.ProviderID != "ionos" || got.AdapterID != "ionos-v1" ||
		got.CredentialMode != CredentialModeManaged || got.RuntimeProfileID != "ionos-managed-pvm-monthly" ||
		got.OfferingID != "monthly-runtime-standard" || got.CatalogVersion != "catalog-2026-07-22" ||
		got.ProvisionDispatchMode != ProvisionDispatchNativeIdempotency {
		t.Fatalf("unexpected execution profile identity: %+v", got)
	}
	if got.CustodyRef != "custody://tenant-1/ionos/main/v4" ||
		got.ConnectionRef != "provider-connection://tenant-1/ionos/main/v4" {
		t.Fatalf("unexpected opaque references: %+v", got)
	}
	for name, value := range map[string]string{
		"capability": got.CapabilitySnapshotHash,
		"custody":    got.CustodyHash,
		"connection": got.ConnectionHash,
		"profile":    got.ExecutionProfileHash,
	} {
		if !executionProfileDigestPattern.MatchString(value) {
			t.Fatalf("%s hash = %q, want sha256 digest", name, value)
		}
	}
	if profiles[0] != profiles[1] {
		t.Fatalf("equivalent catalog JSON changed hashes:\nfirst:  %+v\nsecond: %+v", profiles[0], profiles[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestValidateActiveCatalogCoverageRequiresEveryProductOffering(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT catalog_version.*FROM provider_catalog_versions.*status = 'active'.*LIMIT 2`).
		WillReturnRows(sqlmock.NewRows([]string{"catalog_version"}).
			AddRow("managed-vps-2026-07-24.2"))
	for _, offeringID := range []string{
		"monthly-runtime-standard",
		"monthly-runtime-premium",
	} {
		mock.ExpectQuery(`(?s)SELECT count\(\*\).*FROM provider_catalog_profiles`).
			WithArgs("managed-vps-2026-07-24.2", "ionos", offeringID, "ionos-v1", digest("ionos-adapter"), ProvisionDispatchAtMostOnceManualReconcile).
			WillReturnRows(sqlmock.NewRows([]string{"count", "pinned_count"}).AddRow(1, 1))
	}
	mock.ExpectCommit()

	err = ValidateActiveCatalogCoverage(t.Context(), db, []CatalogCoverageRequirement{
		{ProviderID: "ionos", OfferingID: "monthly-runtime-standard", AdapterID: "ionos-v1", AdapterManifestHash: digest("ionos-adapter"), ProvisionDispatchMode: ProvisionDispatchAtMostOnceManualReconcile},
		{ProviderID: "ionos", OfferingID: "monthly-runtime-premium", AdapterID: "ionos-v1", AdapterManifestHash: digest("ionos-adapter"), ProvisionDispatchMode: ProvisionDispatchAtMostOnceManualReconcile},
	})
	if err != nil {
		t.Fatalf("ValidateActiveCatalogCoverage: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestValidateActiveCatalogCoverageFailsClosedOnMissingOffering(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT catalog_version.*FROM provider_catalog_versions.*status = 'active'.*LIMIT 2`).
		WillReturnRows(sqlmock.NewRows([]string{"catalog_version"}).
			AddRow("managed-vps-2026-07-24.1"))
	mock.ExpectQuery(`(?s)SELECT count\(\*\).*FROM provider_catalog_profiles`).
		WithArgs("managed-vps-2026-07-24.1", "ionos", "monthly-runtime-premium", "ionos-v1", digest("ionos-adapter"), ProvisionDispatchAtMostOnceManualReconcile).
		WillReturnRows(sqlmock.NewRows([]string{"count", "pinned_count"}).AddRow(0, 0))
	mock.ExpectRollback()

	err = ValidateActiveCatalogCoverage(t.Context(), db, []CatalogCoverageRequirement{
		{ProviderID: "ionos", OfferingID: "monthly-runtime-premium", AdapterID: "ionos-v1", AdapterManifestHash: digest("ionos-adapter"), ProvisionDispatchMode: ProvisionDispatchAtMostOnceManualReconcile},
	})
	if !errors.Is(err, ErrProfileUnavailable) ||
		!strings.Contains(err.Error(), "monthly-runtime-premium") {
		t.Fatalf("missing offering error = %v, want fail-closed profile coverage", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestValidateActiveCatalogCoverageFailsClosedOnStaleAdapterManifest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT catalog_version.*FROM provider_catalog_versions.*status = 'active'.*LIMIT 2`).
		WillReturnRows(sqlmock.NewRows([]string{"catalog_version"}).
			AddRow("managed-vps-2026-07-26.2"))
	mock.ExpectQuery(`(?s)SELECT.*count\(\*\) FILTER.*adapter_manifest_hash = \$5.*provision_dispatch_mode = \$6.*FROM provider_catalog_profiles`).
		WithArgs("managed-vps-2026-07-26.2", "centron", "monthly-runtime-standard", "centron-ccloud-v1", digest("current-centron-adapter"), ProvisionDispatchAtMostOnceManualReconcile).
		WillReturnRows(sqlmock.NewRows([]string{"count", "pinned_count"}).AddRow(1, 0))
	mock.ExpectRollback()

	err = ValidateActiveCatalogCoverage(t.Context(), db, []CatalogCoverageRequirement{{
		ProviderID: "centron", OfferingID: "monthly-runtime-standard", AdapterID: "centron-ccloud-v1",
		AdapterManifestHash:   digest("current-centron-adapter"),
		ProvisionDispatchMode: ProvisionDispatchAtMostOnceManualReconcile,
	}})
	if !errors.Is(err, ErrProfileUnavailable) || !strings.Contains(err.Error(), "adapter-pinned centron/monthly-runtime-standard") {
		t.Fatalf("stale adapter manifest error = %v, want fail-closed pinned coverage", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestPostgresCatalogExecutionProfileResolverUsesCallerTransaction(t *testing.T) {
	db, mock, resolver := newCatalogResolverTest(t)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config($1, $2, true)`)).
		WithArgs(tenantContextKey, "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectLeaseProfileSelection(mock, "ionos")
	expectActiveCatalog(mock, "catalog-2026-07-22")
	expectCatalogProfile(mock, `{}`)
	expectCredentialHandles(mock, credentialHandleRows().AddRow(credentialHandleTestValues(t,
		"handle-main", int64(4), "ionos", "managed", "org", "tenant-1",
		"grant-compute", "compute", "custody://tenant-1/ionos/main/v4",
		"provider-connection://tenant-1/ionos/main/v4",
	)...))
	mock.ExpectRollback()

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, execErr := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, "tenant-1"); execErr != nil {
		t.Fatalf("set tenant context: %v", execErr)
	}
	profile, err := resolver.ResolveExecutionProfileTx(t.Context(), tx, catalogProfileRequest())
	if err != nil {
		t.Fatalf("ResolveExecutionProfileTx: %v", err)
	}
	if profile.ProviderID != "ionos" {
		t.Fatalf("provider_id = %q, want ionos", profile.ProviderID)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPostgresCatalogExecutionProfileResolverRequiresExactlyOneActiveCatalog(t *testing.T) {
	db, mock, resolver := newCatalogResolverTest(t)
	defer func() { _ = db.Close() }()
	expectCatalogResolverBegin(mock)
	expectLeaseProfileSelection(mock, "ionos")
	mock.ExpectQuery("FROM provider_catalog_versions").WillReturnRows(
		sqlmock.NewRows([]string{"catalog_version"}).
			AddRow("catalog-a").
			AddRow("catalog-b"),
	)
	mock.ExpectRollback()

	_, err := resolver.ResolveExecutionProfile(t.Context(), catalogProfileRequest())
	if !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("ResolveExecutionProfile error = %v, want ErrProfileUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPostgresCatalogExecutionProfileResolverRejectsCompositeProviderID(t *testing.T) {
	db, mock, resolver := newCatalogResolverTest(t)
	defer func() { _ = db.Close() }()
	expectCatalogResolverBegin(mock)
	expectLeaseProfileSelection(mock, "ionos-managed")
	mock.ExpectRollback()

	_, err := resolver.ResolveExecutionProfile(t.Context(), catalogProfileRequest())
	if !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("ResolveExecutionProfile error = %v, want ErrProfileUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPostgresCatalogExecutionProfileResolverRejectsCredentialSubjectDrift(t *testing.T) {
	db, mock, resolver := newCatalogResolverTest(t)
	defer func() { _ = db.Close() }()
	expectCatalogResolverBegin(mock)
	mock.ExpectQuery("FROM techstack_vm_leases AS lease").
		WithArgs("tenant-1", "lease-1", int64(7), "server-1", testResourceGenerationID, runtimeOfferingMetadataKey).
		WillReturnRows(sqlmock.NewRows([]string{
			"provider_id", "owner_subject_id", "subject_kind", "subject_id", "offering_id",
		}).AddRow("ionos", "user-owner", "user", "different-user", "monthly-runtime-standard"))
	mock.ExpectRollback()

	_, err := resolver.ResolveExecutionProfile(t.Context(), catalogProfileRequest())
	if !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("ResolveExecutionProfile error = %v, want ErrProfileUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPostgresCatalogExecutionProfileResolverRejectsAmbiguousCredentialHandle(t *testing.T) {
	db, mock, resolver := newCatalogResolverTest(t)
	defer func() { _ = db.Close() }()
	expectCatalogResolverBegin(mock)
	expectLeaseProfileSelection(mock, "ionos")
	expectActiveCatalog(mock, "catalog-2026-07-22")
	expectCatalogProfile(mock, `{}`)
	expectCredentialHandles(mock, credentialHandleRows().
		AddRow(credentialHandleTestValues(t,
			"handle-a", int64(1), "ionos", "managed", "org", "tenant-1",
			"grant-a", "compute", "custody://tenant-1/ionos/a",
			"provider-connection://tenant-1/ionos/a",
		)...).
		AddRow(credentialHandleTestValues(t,
			"handle-b", int64(1), "ionos", "managed", "org", "tenant-1",
			"grant-b", "compute", "custody://tenant-1/ionos/b",
			"provider-connection://tenant-1/ionos/b",
		)...))
	mock.ExpectRollback()

	_, err := resolver.ResolveExecutionProfile(t.Context(), catalogProfileRequest())
	if !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("ResolveExecutionProfile error = %v, want ErrProfileUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPostgresCatalogExecutionProfileResolverTreatsRevokedHandleAsUnavailable(t *testing.T) {
	db, mock, resolver := newCatalogResolverTest(t)
	defer func() { _ = db.Close() }()
	expectCatalogResolverBegin(mock)
	expectLeaseProfileSelection(mock, "ionos")
	expectActiveCatalog(mock, "catalog-2026-07-22")
	expectCatalogProfile(mock, `{}`)
	expectCredentialHandles(mock, credentialHandleRows())
	mock.ExpectRollback()

	_, err := resolver.ResolveExecutionProfile(t.Context(), catalogProfileRequest())
	if !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("ResolveExecutionProfile error = %v, want ErrProfileUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPostgresCatalogExecutionProfileResolverRejectsPersistedCredentialHashDrift(t *testing.T) {
	db, mock, resolver := newCatalogResolverTest(t)
	defer func() { _ = db.Close() }()
	expectCatalogResolverBegin(mock)
	expectLeaseProfileSelection(mock, "ionos")
	expectActiveCatalog(mock, "catalog-2026-07-22")
	expectCatalogProfile(mock, `{}`)
	values := credentialHandleTestValues(t,
		"handle-main", int64(4), "ionos", "managed", "org", "tenant-1",
		"grant-compute", "compute", "custody://tenant-1/ionos/main/v4",
		"provider-connection://tenant-1/ionos/main/v4",
	)
	values[10] = digest("drifted-custody-hash")
	expectCredentialHandles(mock, credentialHandleRows().AddRow(values...))
	mock.ExpectRollback()

	_, err := resolver.ResolveExecutionProfile(t.Context(), catalogProfileRequest())
	if !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("ResolveExecutionProfile error = %v, want ErrProfileUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPostgresCatalogExecutionProfileResolverRejectsSecretCapabilityExtension(t *testing.T) {
	db, mock, resolver := newCatalogResolverTest(t)
	defer func() { _ = db.Close() }()
	expectCatalogResolverBegin(mock)
	expectLeaseProfileSelection(mock, "ionos")
	expectActiveCatalog(mock, "catalog-2026-07-22")
	expectCatalogProfile(mock, `{"token":"must-not-enter-profile"}`)
	mock.ExpectRollback()

	_, err := resolver.ResolveExecutionProfile(t.Context(), catalogProfileRequest())
	if !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("ResolveExecutionProfile error = %v, want ErrProfileUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestNewPostgresCatalogExecutionProfileResolverRequiresDatabase(t *testing.T) {
	if _, err := NewPostgresCatalogExecutionProfileResolver(nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("constructor error = %v, want ErrInvalidRequest", err)
	}
}

func newCatalogResolverTest(
	t *testing.T,
) (*sql.DB, sqlmock.Sqlmock, *PostgresCatalogExecutionProfileResolver) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	resolver, err := NewPostgresCatalogExecutionProfileResolver(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("NewPostgresCatalogExecutionProfileResolver: %v", err)
	}
	return db, mock, resolver
}

func catalogProfileRequest() ProfileRequest {
	return ProfileRequest{
		TenantID: "tenant-1", LeaseID: "lease-1", LeaseRevision: 7,
		RuntimeServerID: "server-1", ResourceGenerationID: testResourceGenerationID,
		Operation: providerexecutor.OperationProvision,
	}
}

func expectCatalogResolverBegin(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config($1, $2, true)`)).
		WithArgs(tenantContextKey, "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectLeaseProfileSelection(mock sqlmock.Sqlmock, providerID string) {
	mock.ExpectQuery("FROM techstack_vm_leases AS lease").
		WithArgs("tenant-1", "lease-1", int64(7), "server-1", testResourceGenerationID, runtimeOfferingMetadataKey).
		WillReturnRows(sqlmock.NewRows([]string{
			"provider_id", "owner_subject_id", "subject_kind", "subject_id", "offering_id",
		}).AddRow(providerID, "user-owner", "org", "tenant-1", "monthly-runtime-standard"))
}

func expectActiveCatalog(mock sqlmock.Sqlmock, catalogVersion string) {
	mock.ExpectQuery("FROM provider_catalog_versions").
		WillReturnRows(sqlmock.NewRows([]string{"catalog_version"}).AddRow(catalogVersion))
}

func expectCatalogProfile(mock sqlmock.Sqlmock, capabilitySnapshot string) {
	mock.ExpectQuery("FROM provider_catalog_profiles").
		WithArgs("catalog-2026-07-22", "ionos", "monthly-runtime-standard").
		WillReturnRows(sqlmock.NewRows([]string{
			"catalog_version", "provider_id", "adapter_id", "credential_mode", "runtime_profile_id",
			"offering_id", "can_pause", "stop_effect", "can_recreate", "adapter_manifest_hash",
			"provision_dispatch_mode", "capability_snapshot",
		}).AddRow(
			"catalog-2026-07-22", "ionos", "ionos-v1", "managed", "ionos-managed-pvm-monthly",
			"monthly-runtime-standard", true, "pause", true, digest("ionos-v1-manifest"),
			ProvisionDispatchNativeIdempotency, capabilitySnapshot,
		))
}

func expectCredentialHandles(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(`FROM provider_credential_handles[\s\S]+revoked_at IS NULL[\s\S]+valid_from <= clock_timestamp\(\)[\s\S]+valid_until > clock_timestamp\(\)`).
		WithArgs("tenant-1", "ionos", "managed", "org", "tenant-1").
		WillReturnRows(rows)
}

func credentialHandleRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"handle_id", "handle_version", "provider_id", "credential_mode", "subject_kind",
		"subject_id", "grant_id", "credential_scope", "custody_ref", "connection_ref",
		"custody_hash", "connection_hash", "valid_from", "valid_until",
	})
}

func credentialHandleTestValues(
	t *testing.T,
	handleID string,
	handleVersion int64,
	providerID string,
	credentialMode string,
	subjectKind string,
	subjectID string,
	grantID string,
	scope string,
	custodyRef string,
	connectionRef string,
) []driver.Value {
	t.Helper()
	row := credentialHandleRow{
		HandleID: handleID, HandleVersion: handleVersion, ProviderID: providerID,
		CredentialMode: CredentialMode(credentialMode), SubjectKind: subjectKind,
		SubjectID: subjectID, GrantID: grantID, Scope: scope,
		CustodyRef: custodyRef, ConnectionRef: connectionRef,
	}
	custodyHash, err := credentialCustodyHash("tenant-1", row)
	if err != nil {
		t.Fatalf("credentialCustodyHash: %v", err)
	}
	connectionHash, err := credentialConnectionHash("tenant-1", row)
	if err != nil {
		t.Fatalf("credentialConnectionHash: %v", err)
	}
	return []driver.Value{
		handleID, handleVersion, providerID, credentialMode, subjectKind, subjectID,
		grantID, scope, custodyRef, connectionRef, custodyHash, connectionHash,
		contractNow.Add(-time.Hour), contractNow.Add(time.Hour),
	}
}
