package controlplane

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreUpsertTenantUsesTenantIsolation(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	tenant := Tenant{
		ID:            "tenant-1",
		ExternalOrgID: "org_123",
		DisplayName:   "Tenant One",
		Kind:          "saas",
		Status:        "active",
		Metadata:      map[string]any{"plan": "pro"},
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO techstack_tenants")).
		WithArgs("tenant-1", "org_123", "Tenant One", "saas", "active", sqlmock.AnyArg()).
		WillReturnRows(tenantRows().AddRow(
			"tenant-1", "org_123", "Tenant One", "saas", "active", `{"plan":"pro"}`, now, now,
		))
	mock.ExpectCommit()

	got, err := store.UpsertTenant(context.Background(), tenant)
	if err != nil {
		t.Fatalf("UpsertTenant: %v", err)
	}
	if got.ID != "tenant-1" || got.ExternalOrgID != "org_123" || got.Metadata["plan"] != "pro" {
		t.Fatalf("unexpected tenant: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreEnsureTenantPreservesExistingTenant(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "default")
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO techstack_tenants")).
		WithArgs("default", "", "default", "self_hosted", "active", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, external_org_id, display_name, kind, status")).
		WithArgs("default").
		WillReturnRows(tenantRows().AddRow(
			"default", "org-existing", "Existing tenant", "embedded", "active", `{"keep":true}`, now, now,
		))
	mock.ExpectCommit()

	got, err := store.EnsureTenant(context.Background(), Tenant{ID: "default"})
	if err != nil {
		t.Fatalf("EnsureTenant: %v", err)
	}
	if got.DisplayName != "Existing tenant" || got.Kind != "embedded" || got.Metadata["keep"] != true {
		t.Fatalf("existing tenant was not preserved: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreUpsertUserAndMembershipDecodeMetadata(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 15, 30, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO techstack_users")).
		WithArgs("user-1", "owner@example.test", "Owner", "active", sqlmock.AnyArg()).
		WillReturnRows(userRows().AddRow(
			"user-1", "owner@example.test", "Owner", "active", `{"source":"auth0"}`, now, now,
		))
	mock.ExpectCommit()

	user, err := store.UpsertUser(context.Background(), User{
		ID:           "user-1",
		PrimaryEmail: "owner@example.test",
		DisplayName:  "Owner",
		Status:       "active",
		Metadata:     map[string]any{"source": "auth0"},
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if user.ID != "user-1" || user.Metadata["source"] != "auth0" {
		t.Fatalf("unexpected user: %#v", user)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO techstack_memberships")).
		WithArgs("membership-1", "tenant-1", "user-1", "owner", "auth0", "auth0|user-1", "active", sqlmock.AnyArg()).
		WillReturnRows(membershipRows().AddRow(
			"membership-1", "tenant-1", "user-1", "owner", "auth0", "auth0|user-1", "active", `{"org":"org_123"}`, now, now,
		))
	mock.ExpectCommit()

	membership, err := store.UpsertMembership(context.Background(), Membership{
		ID:          "membership-1",
		TenantID:    "tenant-1",
		UserID:      "user-1",
		RoleKey:     "owner",
		ProviderKey: "auth0",
		SubjectID:   "auth0|user-1",
		Status:      "active",
		Metadata:    map[string]any{"org": "org_123"},
	})
	if err != nil {
		t.Fatalf("UpsertMembership: %v", err)
	}
	if membership.ID != "membership-1" || membership.Metadata["org"] != "org_123" {
		t.Fatalf("unexpected membership: %#v", membership)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreGetMembershipUsesTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, tenant_id, user_id, role_key, provider_key, subject_id")).
		WithArgs("tenant-1", "auth0|runtime-user").
		WillReturnRows(membershipRows().AddRow(
			"tenant-1:auth0|runtime-user", "tenant-1", "auth0|runtime-user", "global_admin", "cloud", "auth0|runtime-user", "active", `{"source":"auth0"}`, now, now,
		))
	mock.ExpectCommit()

	membership, err := store.GetMembership(context.Background(), "tenant-1", "auth0|runtime-user")
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if membership.RoleKey != "global_admin" || membership.Metadata["source"] != "auth0" {
		t.Fatalf("unexpected membership: %#v", membership)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreAuthConfigAndBreakglassUseTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 16, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO auth_config")).
		WithArgs("auth-1", "tenant-1", "instance-1", "cloud", sqlmock.AnyArg()).
		WillReturnRows(authConfigRows().AddRow(
			"auth-1", "tenant-1", "instance-1", "cloud", `{"issuer":"https://auth.example.test/"}`, now, now,
		))
	mock.ExpectCommit()

	config, err := store.UpsertAuthConfig(context.Background(), AuthConfig{
		ID:         "auth-1",
		TenantID:   "tenant-1",
		InstanceID: "instance-1",
		Mode:       "cloud",
		Config:     map[string]any{"issuer": "https://auth.example.test/"},
	})
	if err != nil {
		t.Fatalf("UpsertAuthConfig: %v", err)
	}
	if config.Mode != "cloud" || config.Config["issuer"] != "https://auth.example.test/" {
		t.Fatalf("unexpected auth config: %#v", config)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO breakglass_admin")).
		WithArgs("breakglass-1", "tenant-1", "user-1", "owner@example.test", "$argon2id$hash", false, sqlmock.AnyArg()).
		WillReturnRows(breakglassRows().AddRow(
			"breakglass-1", "tenant-1", "user-1", "owner@example.test", "$argon2id$hash", false, nil, `{"created_by":"test"}`, now, now,
		))
	mock.ExpectCommit()

	admin, err := store.UpsertBreakglassAdmin(context.Background(), BreakglassAdmin{
		ID:           "breakglass-1",
		TenantID:     "tenant-1",
		UserID:       "user-1",
		Email:        "owner@example.test",
		PasswordHash: "$argon2id$hash",
		Metadata:     map[string]any{"created_by": "test"},
	})
	if err != nil {
		t.Fatalf("UpsertBreakglassAdmin: %v", err)
	}
	if admin.Email != "owner@example.test" || admin.Metadata["created_by"] != "test" {
		t.Fatalf("unexpected breakglass admin: %#v", admin)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, tenant_id")).
		WithArgs("tenant-1").
		WillReturnRows(breakglassRows().AddRow(
			"breakglass-1", "tenant-1", "user-1", "owner@example.test", "$argon2id$hash", false, nil, `{"created_by":"test"}`, now, now,
		))
	mock.ExpectCommit()

	admin, err = store.GetBreakglassAdmin(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("GetBreakglassAdmin: %v", err)
	}
	if admin.ID != "breakglass-1" {
		t.Fatalf("unexpected breakglass admin: %#v", admin)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func tenantRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "external_org_id", "display_name", "kind", "status", "metadata_json", "created_at", "updated_at",
	})
}

func userRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "primary_email", "display_name", "status", "metadata_json", "created_at", "updated_at",
	})
}

func membershipRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "user_id", "role_key", "provider_key", "subject_id", "status", "metadata_json", "created_at", "updated_at",
	})
}

func authConfigRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "mode", "config_json", "created_at", "updated_at",
	})
}

func breakglassRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "user_id", "email", "password_hash", "locked", "last_used_at", "metadata_json", "created_at", "updated_at",
	})
}

// A metadata-less upsert must not erase the entitlements a previous caller
// stored: the embedded portal open writes a Membership with no Metadata, and an
// unconditional EXCLUDED there revoked the account's own inventory access
// (owner report 2026-09-18).
func TestUpsertMembershipKeepsExistingMetadataWhenCallerHasNone(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	store := &PostgresStore{db: db}
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec("set_config").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("INSERT INTO techstack_memberships").
		WithArgs("tenant-1:user-1", "tenant-1", "user-1", "member", "portal", "user-1", "active", []byte("{}")).
		WillReturnRows(membershipRows().AddRow(
			"tenant-1:user-1", "tenant-1", "user-1", "member", "portal", "user-1", "active",
			`{"entitlements":["techstack.inventory.read"]}`, now, now,
		))
	mock.ExpectCommit()

	membership, err := store.UpsertMembership(context.Background(), Membership{
		ID: "tenant-1:user-1", TenantID: "tenant-1", UserID: "user-1",
		RoleKey: "member", ProviderKey: "portal", SubjectID: "user-1",
	})
	if err != nil {
		t.Fatalf("UpsertMembership: %v", err)
	}
	// The row that comes back still carries what the standalone sign-in wrote.
	if got := membership.Metadata["entitlements"]; got == nil {
		t.Fatalf("entitlements were dropped: %#v", membership.Metadata)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
