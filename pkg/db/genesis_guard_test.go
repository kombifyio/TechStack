package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const (
	currentDatabaseQueryPattern = `SELECT current_database\(\)`
	controlSystemQueryPattern   = `SELECT system_identifier::text FROM pg_catalog\.pg_control_system\(\)`
	ledgerPresenceQueryPattern  = `SELECT pg_catalog\.to_regclass\(\s+pg_catalog\.format\('%I\.schema_migrations', pg_catalog\.current_schema\(\)\)\s+\) IS NOT NULL`
	ledgerCountQueryPattern     = `SELECT COUNT\(\*\) FROM schema_migrations`
)

func clearGuardEnv(t *testing.T) {
	t.Helper()
	t.Setenv("KOMBIFY_EDITION", "")
	t.Setenv("DEPLOYMENT_MODE", "")
	t.Setenv(EnvExpectedDatabaseIdentity, "")
	t.Setenv(EnvAllowDBGenesis, "")
}

func TestExpectedDatabaseIdentityFromEnvParsesPinFormats(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		wantPinned bool
		wantPin    ExpectedDatabaseIdentity
		wantErr    bool
	}{
		{name: "unset", value: "", wantPinned: false},
		{name: "blank", value: "   ", wantPinned: false},
		{name: "name only", value: "kombify_runtime", wantPinned: true, wantPin: ExpectedDatabaseIdentity{DatabaseName: "kombify_runtime"}},
		{name: "name and system identifier", value: "kombify_runtime@7311945388571659001", wantPinned: true, wantPin: ExpectedDatabaseIdentity{DatabaseName: "kombify_runtime", SystemIdentifier: "7311945388571659001"}},
		{name: "trims whitespace", value: "  kombify_runtime @ 7311945388571659001 ", wantPinned: true, wantPin: ExpectedDatabaseIdentity{DatabaseName: "kombify_runtime", SystemIdentifier: "7311945388571659001"}},
		{name: "missing name", value: "@7311945388571659001", wantErr: true},
		{name: "missing system identifier", value: "kombify_runtime@", wantErr: true},
		{name: "separator only", value: "@", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearGuardEnv(t)
			t.Setenv(EnvExpectedDatabaseIdentity, test.value)

			pin, pinned, err := ExpectedDatabaseIdentityFromEnv()
			if test.wantErr {
				if err == nil {
					t.Fatalf("ExpectedDatabaseIdentityFromEnv() error = nil, want malformed-pin error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpectedDatabaseIdentityFromEnv() error = %v", err)
			}
			if pinned != test.wantPinned {
				t.Fatalf("pinned = %v, want %v", pinned, test.wantPinned)
			}
			if pin != test.wantPin {
				t.Fatalf("pin = %+v, want %+v", pin, test.wantPin)
			}
		})
	}
}

func TestExpectedDatabaseIdentityVerifyDistinguishesMismatchReasons(t *testing.T) {
	pin := ExpectedDatabaseIdentity{DatabaseName: "kombify_runtime", SystemIdentifier: "7311945388571659001"}

	if err := pin.Verify("kombify_runtime", "7311945388571659001"); err != nil {
		t.Fatalf("Verify(match) error = %v", err)
	}

	err := pin.Verify("kombify_platform", "7311945388571659001")
	if !errors.Is(err, ErrDatabaseIdentityMismatch) || !strings.Contains(err.Error(), "env contract stale") {
		t.Fatalf("Verify(name mismatch) error = %v, want env-contract-stale identity mismatch", err)
	}

	err = pin.Verify("kombify_runtime", "9999999999999999999")
	if !errors.Is(err, ErrDatabaseIdentityMismatch) || !strings.Contains(err.Error(), "database moved") {
		t.Fatalf("Verify(system identifier mismatch) error = %v, want database-moved identity mismatch", err)
	}

	nameOnlyPin := ExpectedDatabaseIdentity{DatabaseName: "kombify_runtime"}
	if err := nameOnlyPin.Verify("kombify_runtime", ""); err != nil {
		t.Fatalf("Verify(name-only pin) error = %v", err)
	}
}

func TestVerifyExpectedDatabaseIdentityFailsClosedWhenSystemIdentifierIsUnreadable(t *testing.T) {
	clearGuardEnv(t)
	t.Setenv(EnvExpectedDatabaseIdentity, "kombify_runtime@7311945388571659001")

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	mock.ExpectQuery(currentDatabaseQueryPattern).
		WillReturnRows(sqlmock.NewRows([]string{"current_database"}).AddRow("kombify_runtime"))
	controlErr := errors.New("permission denied for function pg_control_system")
	mock.ExpectQuery(controlSystemQueryPattern).WillReturnError(controlErr)

	err = VerifyExpectedDatabaseIdentity(context.Background(), sqlDB)
	if !errors.Is(err, controlErr) || !strings.Contains(err.Error(), "refusing to migrate an unverified database") {
		t.Fatalf("VerifyExpectedDatabaseIdentity error = %v, want fail-closed unreadable-system-identifier error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestVerifyGenesisAllowedGuardMatrix(t *testing.T) {
	tests := []struct {
		name         string
		edition      string
		ledgerExists bool
		appliedCount int
		optIn        string
		wantErr      bool
	}{
		{name: "saas absent ledger without opt-in refuses", edition: "saas-standalone", ledgerExists: false, wantErr: true},
		{name: "saas empty ledger without opt-in refuses", edition: "saas-standalone", ledgerExists: true, appliedCount: 0, wantErr: true},
		{name: "saas absent ledger with opt-in provisions", edition: "saas-standalone", ledgerExists: false, optIn: "true"},
		{name: "saas empty ledger with opt-in provisions", edition: "saas-standalone", ledgerExists: true, appliedCount: 0, optIn: "true"},
		{name: "saas populated ledger passes without opt-in", edition: "saas-standalone", ledgerExists: true, appliedCount: 41},
		{name: "preview lane is saas and refuses", edition: "preview", ledgerExists: false, wantErr: true},
		{name: "saas refuses non-true opt-in values", edition: "saas-standalone", ledgerExists: false, optIn: "1", wantErr: true},
		{name: "self-host absent ledger stays permissive", edition: "selfhost-oss", ledgerExists: false},
		{name: "default edition stays permissive", edition: "", ledgerExists: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearGuardEnv(t)
			t.Setenv("KOMBIFY_EDITION", test.edition)
			t.Setenv(EnvAllowDBGenesis, test.optIn)

			sqlDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })

			saasMode := test.edition != "" && test.edition != "selfhost-oss"
			if saasMode {
				mock.ExpectQuery(ledgerPresenceQueryPattern).
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(test.ledgerExists))
				if test.ledgerExists {
					mock.ExpectQuery(ledgerCountQueryPattern).
						WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(test.appliedCount))
				}
			}

			err = verifyGenesisAllowed(context.Background(), sqlDB)
			if test.wantErr {
				if !errors.Is(err, ErrImplicitGenesisRefused) {
					t.Fatalf("verifyGenesisAllowed error = %v, want ErrImplicitGenesisRefused", err)
				}
				if !strings.Contains(err.Error(), "refusing implicit genesis") ||
					!strings.Contains(err.Error(), EnvAllowDBGenesis+"=true only for deliberate first-boot provisioning") {
					t.Fatalf("verifyGenesisAllowed error = %q, want operator-actionable genesis refusal", err)
				}
			} else if err != nil {
				t.Fatalf("verifyGenesisAllowed error = %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

func TestMigrateRefusesImplicitSaaSGenesisBeforeAnyDDL(t *testing.T) {
	clearGuardEnv(t)
	t.Setenv("KOMBIFY_EDITION", "saas-standalone")

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	mock.ExpectExec(migrationLockQueryPattern).
		WithArgs(migrationAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(migrationTimeoutQueryPattern).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(ledgerPresenceQueryPattern).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(migrationResetQueryPattern).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(migrationUnlockQueryPattern).
		WithArgs(migrationAdvisoryLockKey).
		WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))

	err = (&DB{DB: sqlDB}).Migrate(context.Background())
	if !errors.Is(err, ErrImplicitGenesisRefused) {
		t.Fatalf("Migrate error = %v, want ErrImplicitGenesisRefused", err)
	}
	// ExpectationsWereMet proves the run stopped at the guard: no self-heal or
	// tracker DDL statements were ever issued.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMigrateRefusesMismatchedExpectedIdentityBeforeAnyDDL(t *testing.T) {
	clearGuardEnv(t)
	t.Setenv(EnvExpectedDatabaseIdentity, "kombify_runtime")

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	mock.ExpectExec(migrationLockQueryPattern).
		WithArgs(migrationAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(migrationTimeoutQueryPattern).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(currentDatabaseQueryPattern).
		WillReturnRows(sqlmock.NewRows([]string{"current_database"}).AddRow("kombify_platform"))
	mock.ExpectExec(migrationResetQueryPattern).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(migrationUnlockQueryPattern).
		WithArgs(migrationAdvisoryLockKey).
		WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))

	err = (&DB{DB: sqlDB}).Migrate(context.Background())
	if !errors.Is(err, ErrDatabaseIdentityMismatch) || !strings.Contains(err.Error(), "env contract stale") {
		t.Fatalf("Migrate error = %v, want env-contract-stale identity mismatch", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
