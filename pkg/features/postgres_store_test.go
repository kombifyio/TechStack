package features

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/kombifyio/techstack/pkg/identity"
)

func TestPostgresStoreFeaturePreferencesUseTenantIdentity(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	ctx := identity.NewContext(context.Background(), &identity.Identity{UserID: "user-1", OrgID: "tenant-1"})

	mock.ExpectBegin()
	expectFeatureTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT enabled")).
		WithArgs("tenant-1", "user-1", "monthly_runtime").
		WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(true))
	mock.ExpectCommit()

	enabled, err := store.GetUserFlag(ctx, "user-1", "monthly_runtime")
	if err != nil {
		t.Fatalf("GetUserFlag: %v", err)
	}
	if enabled == nil || !*enabled {
		t.Fatalf("enabled = %#v, want true", enabled)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreConsentReadShapes(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	ctx := identity.NewContext(context.Background(), &identity.Identity{UserID: "user-1", OrgID: "tenant-1"})

	mock.ExpectBegin()
	expectFeatureTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS")).
		WithArgs("tenant-1", "user-1", "raw_commands").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectCommit()

	hasConsent, err := store.HasUserConsent(ctx, "user-1", "raw_commands")
	if err != nil {
		t.Fatalf("HasUserConsent: %v", err)
	}
	if !hasConsent {
		t.Fatal("hasConsent = false, want true")
	}

	mock.ExpectBegin()
	expectFeatureTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT feature_key")).
		WithArgs("tenant-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"feature_key"}).AddRow("raw_commands"))
	mock.ExpectCommit()

	consents, err := store.GetUserConsentsMap(ctx, "user-1", []string{"raw_commands", "network_discovery"})
	if err != nil {
		t.Fatalf("GetUserConsentsMap: %v", err)
	}
	if !consents["raw_commands"] || consents["network_discovery"] {
		t.Fatalf("consents = %#v, want only raw_commands", consents)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectFeatureTenantGUC(mock sqlmock.Sqlmock, tenantID string) {
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs("app.tenant_id", tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}
