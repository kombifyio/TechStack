package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db/tenantcontext"
	"github.com/jackc/pgx/v5/pgconn"
)

// integrationDSN returns the DSN for an integration Postgres instance, or ""
// when the integration suite should be skipped. The variable is named
// distinctly from DATABASE_URL so it does not accidentally point at a
// production database.
func integrationDSN() string {
	return strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	cfg := Config{
		Backend:    StoreBackendPostgres,
		DSN:        dsn,
		DriverName: PostgresDriverName,
	}
	d, err := Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestIntegrationWithTenantSetsGUC(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	const tenantID = "tenant-integration-test-1"
	var got string
	err := d.WithTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, "SELECT current_setting($1, true)", TenantGUC)
		return row.Scan(&got)
	})
	if err != nil {
		t.Fatalf("WithTenant: %v", err)
	}
	if got != tenantID {
		t.Fatalf("GUC value: got %q, want %q", got, tenantID)
	}

	// After the transaction commits, the GUC must not leak into a follow-up
	// session-level query (set_config(..., true) is local to the tx).
	row := d.QueryRowContext(ctx, "SELECT current_setting($1, true)", TenantGUC)
	var leaked string
	if err := row.Scan(&leaked); err != nil {
		t.Fatalf("post-tx query: %v", err)
	}
	if leaked == tenantID {
		t.Fatalf("GUC leaked into next session: %q", leaked)
	}
}

func TestIntegrationMigrateIsIdempotent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

// Run after the real A/B onboarding journey has persisted both canonical users.
// No signer, migration login, schema write or replacement DB enters this probe.
// The hostile GUC cases are internal runtime failures, not public API inputs.
func TestStageOnboardingRejectsRuntimeChosenGUC(t *testing.T) {
	if os.Getenv("TECHSTACK_TEST_ONBOARDING_CONTEXT") != "1" {
		t.Skip("managed Stage onboarding context probe not selected")
	}
	if integrationDSN() == "" {
		t.Fatal("the managed Stage application DSN is required")
	}
	first, second := os.Getenv("TEST_RIL_PRO_USER_ID"), os.Getenv("TEST_ALL_YOU_NEED_USER_ID")
	if !strings.HasPrefix(first, "auth0|") || !strings.HasPrefix(second, "auth0|") || first == second {
		t.Fatal("two distinct canonical shared Stage subjects are required")
	}
	database := openTestDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := VerifyStagingApplicationBinding(ctx, database.DB); err != nil {
		t.Fatal("the application pool is not the governed managed Stage database")
	}
	if err := VerifyRuntimeRolePosture(ctx, database.DB, RuntimeRoleHosted); err != nil {
		t.Fatal("the Stage login is not the bounded hosted application role")
	}
	var active bool
	if err := database.QueryRowContext(ctx, `SELECT row_security_active('user_onboarding_state'::regclass)`).Scan(&active); err != nil || !active {
		t.Fatal("onboarding row security is not active for the Stage application login")
	}
	for _, token := range []string{"", "v1.untrusted.e30.forged"} {
		for _, subject := range []string{first, second} {
			tx, err := database.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal("Stage probe transaction unavailable")
			}
			func() {
				defer tx.Rollback()
				_, err = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true),
					set_config('app.user_id',$2,true),set_config('kombify.ctx_token',$3,true)`, "usr:"+subject, subject, token)
				if err != nil {
					t.Fatal("hostile runtime context setup failed")
				}
				var rows int
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_onboarding_state
					WHERE tenant_id=$1 AND owner_subject_id=$2`, "usr:"+subject, subject).Scan(&rows); err != nil || rows != 0 {
					t.Fatal("unproven runtime context exposed onboarding rows")
				}
				for _, statement := range []string{
					`UPDATE user_onboarding_state SET revision=revision WHERE tenant_id=$1 AND owner_subject_id=$2`,
					`DELETE FROM user_onboarding_state WHERE tenant_id=$1 AND owner_subject_id=$2`,
				} {
					result, err := tx.ExecContext(ctx, statement, "usr:"+subject, subject)
					if err != nil {
						t.Fatal("Stage denial probe could not inspect affected rows")
					}
					changed, err := result.RowsAffected()
					if err != nil || changed != 0 {
						t.Fatal("unproven runtime context changed onboarding rows")
					}
				}
			}()
		}
	}
}

// Public callers cannot submit arbitrary SQL/GUC changes. This separate auth
// boundary probe runs only after the real Stage personal-settings journey;
// it is not positive product proof. Every attempt rolls back, including a
// wrongly admitted INSERT. No issuer, key or schema authority enters here.
func TestStagePersonalSettingsRejectRuntimeChosenGUC(t *testing.T) {
	if os.Getenv("TECHSTACK_TEST_PERSONAL_SETTINGS_CONTEXT") != "1" {
		t.Skip("managed Stage personal-settings context probe not selected")
	}
	if integrationDSN() == "" {
		t.Fatal("the managed Stage application DSN is required")
	}
	first, second := os.Getenv("TEST_RIL_PRO_USER_ID"), os.Getenv("TEST_ALL_YOU_NEED_USER_ID")
	if !strings.HasPrefix(first, "auth0|") || !strings.HasPrefix(second, "auth0|") || first == second {
		t.Fatal("two distinct canonical shared Stage subjects are required")
	}
	database := openTestDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := VerifyStagingApplicationBinding(ctx, database.DB); err != nil {
		t.Fatal("the application pool is not the governed managed Stage database")
	}
	if err := VerifyRuntimeRolePosture(ctx, database.DB, RuntimeRoleHosted); err != nil {
		t.Fatal("the Stage login is not the bounded hosted application role")
	}
	for _, table := range []struct{ name, subject, insert string }{
		{"feature_preferences", "subject_id", `INSERT INTO feature_preferences
			(id,tenant_id,subject_id,feature_key,enabled) VALUES ($3,$1,$2,'stage_context_denial',false)`},
		{"feature_consents", "subject_id", `INSERT INTO feature_consents
			(id,tenant_id,subject_id,feature_key) VALUES ($3,$1,$2,'stage_context_denial')`},
		{"operator_self_disclosures", "owner_subject_id", `INSERT INTO operator_self_disclosures
			(id,tenant_id,owner_subject_id,schema_version,source,disclosure_json) VALUES ($3,$1,$2,1,'techstack','{}')`},
	} {
		t.Run(table.name, func(t *testing.T) {
			var active bool
			if err := database.QueryRowContext(ctx, `SELECT row_security_active($1::regclass)`, table.name).Scan(&active); err != nil || !active {
				t.Fatal("personal-settings row security is not active for the Stage application login")
			}
			for _, token := range []string{"", "v1.untrusted.e30.forged"} {
				for _, subject := range []string{first, second} {
					func() {
						tx, err := database.BeginTx(ctx, nil)
						if err != nil {
							t.Fatal("Stage probe transaction unavailable")
						}
						defer tx.Rollback()
						if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true),
							set_config('app.user_id',$2,true),set_config('kombify.ctx_token',$3,true)`, "usr:"+subject, subject, token); err != nil {
							t.Fatal("hostile runtime context setup failed")
						}
						// Identifiers come only from the fixed table descriptors above.
						predicate := " WHERE tenant_id=$1 AND " + table.subject + "=$2"
						var visible int
						if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table.name+predicate, "usr:"+subject, subject).Scan(&visible); err != nil || visible != 0 {
							t.Fatal("unproven runtime context exposed personal settings")
						}
						for _, statement := range []string{"UPDATE " + table.name + " SET tenant_id=tenant_id" + predicate, "DELETE FROM " + table.name + predicate} {
							result, err := tx.ExecContext(ctx, statement, "usr:"+subject, subject)
							if err != nil {
								t.Fatal("Stage denial probe could not inspect affected rows")
							}
							changed, err := result.RowsAffected()
							if err != nil || changed != 0 {
								t.Fatal("unproven runtime context changed personal settings")
							}
						}
						_, err = tx.ExecContext(ctx, table.insert, "usr:"+subject, subject, fmt.Sprintf("stage-context-denial-%d", time.Now().UnixNano()))
						var denied *pgconn.PgError
						if !errors.As(err, &denied) || denied.Code != "42501" {
							t.Fatal("unproven personal-settings INSERT was not refused by row security")
						}
					}()
				}
			}
		})
	}
}

// This selected real Stage auth/pagination probe uses the actual production
// page reader. Uncommitted fixtures never reach the background worker, and the
// complete transaction rolls back. It is not the positive scheduled journey.
func TestStageBackupPendingPageSkipsExpiredAndAdvances(t *testing.T) {
	if os.Getenv("TECHSTACK_TEST_BACKUP_WORKER_CONTEXT") != "1" {
		t.Skip("managed Stage backup worker probe not selected")
	}
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_BACKUP_WORKER_DATABASE_URL"))
	if dsn == "" {
		t.Fatal("the qualified managed Stage backup-worker DSN is required")
	}
	if !DeclaresStagingDatabaseBinding() {
		t.Fatal("the exact Stage physical pin is required")
	}
	subject := strings.TrimSpace(os.Getenv("TEST_ALL_YOU_NEED_USER_ID"))
	if !strings.HasPrefix(subject, "auth0|") {
		t.Fatal("the canonical All You Need Stage subject is required")
	}
	database, err := Open(Config{Backend: StoreBackendPostgres, DriverName: PostgresDriverName, DSN: dsn})
	if err != nil {
		t.Fatal("Stage worker connection failed")
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := VerifyExpectedDatabaseIdentity(ctx, database.DB); err != nil {
		t.Fatal("Stage worker physical binding failed")
	}
	if err := VerifyBackupWorkerRuntimeRole(ctx, database.DB); err != nil {
		t.Fatal("Stage worker role posture failed")
	}
	owner, err := ApplicationAuthorityOwner(ctx, database.DB)
	if err != nil {
		t.Fatal("Stage worker authority owner unavailable")
	}
	if err := VerifyApplicationPrivileges(ctx, database.DB, ApplicationPrincipal{Role: BackupWorkerRole, AuthorityOwner: owner}, true); err != nil {
		t.Fatal("Stage worker finite privileges failed")
	}
	tx, err := database.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("Stage worker rollback transaction failed")
	}
	defer tx.Rollback()
	tenant := "usr:" + subject
	if err := tenantcontext.ApplyTenant(ctx, tx, tenant); err != nil {
		t.Fatal("Stage worker transaction scope failed")
	}
	prefix := fmt.Sprintf("stage-backup-page-%d-", time.Now().UnixNano())
	for _, cohort := range []string{"expired", "fresh"} {
		for index := 0; index < 51; index++ {
			id := fmt.Sprintf("%s%s-%03d", prefix, cohort, index)
			_, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,tenant_id,type,state,payload_json,scheduled_for,created_at)
    VALUES($1,$2,'backup','pending','{}',now()-interval '1 minute',
     CASE WHEN $3='expired' THEN now()-interval '2 hours' ELSE now() END)`, id, tenant, cohort)
			if err != nil {
				t.Fatal("Stage rollback backup fixture insertion failed")
			}
		}
	}
	var after time.Time
	afterID := ""
	seen := map[string]bool{}
	for page := 0; page < 8; page++ {
		rows, err := controlplane.ReadPendingBackupJobPageTx(ctx, tx, tenant, after, afterID, 50)
		if err != nil {
			t.Fatal("actual Stage backup pending page failed")
		}
		for _, row := range rows {
			if row.Type != "backup" || row.TenantID != tenant {
				t.Fatal("private pending page crossed purpose or tenant")
			}
			if strings.HasPrefix(row.ID, prefix+"expired-") {
				t.Fatal("expired backup admission reached the active page")
			}
			if strings.HasPrefix(row.ID, prefix+"fresh-") {
				if seen[row.ID] {
					t.Fatal("backup keyset replayed a page row")
				}
				seen[row.ID] = true
			}
		}
		if len(rows) < 50 {
			break
		}
		last := rows[len(rows)-1]
		after, afterID = last.ScheduledFor, last.ID
	}
	if len(seen) != 51 {
		t.Fatal("the active backup page did not progress beyond expired rows and its first page")
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT backup_purpose_denial"); err != nil {
		t.Fatal("backup purpose savepoint failed")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,tenant_id,type,state) VALUES($1,$2,'deploy','pending')`, prefix+"foreign-purpose", tenant)
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "42501" {
		t.Fatal("backup worker admitted a non-backup job write")
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT backup_purpose_denial"); err != nil {
		t.Fatal("backup purpose rollback failed")
	}
}

// Hostile SQL/GUCs cannot be submitted through the product API. This auth
// boundary probe receives only the Stage application login and bounded signed
// contexts from the development issuer, never migration or signing custody.
func TestStageHouseholdInventoryReadDelegation(t *testing.T) {
	if os.Getenv("TECHSTACK_TEST_HOUSEHOLD_CONTEXT") != "1" {
		t.Skip("Stage household boundary probe not selected")
	}
	database := openTestDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	if _, err := VerifyStagingApplicationBinding(ctx, database.DB); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRuntimeRolePosture(ctx, database.DB, RuntimeRoleHosted); err != nil {
		t.Fatal(err)
	}
	owner := "auth0|jrz6y-stage-owner"
	member := "auth0|jrz6y-stage-member"
	tenant := "usr:" + owner
	tokenContext := func(subject, name string) context.Context {
		token := os.Getenv(name)
		if token == "" {
			t.Fatalf("missing bounded context %s", name)
		}
		return tenantdb.NewContextWithToken(identity.NewContext(ctx, &identity.Identity{UserID: subject, OrgID: "usr:" + subject}), token)
	}
	ownerCtx := tokenContext(owner, "JOURNEY_OWNER_CONTEXT")
	collectionCtx := tokenContext(member, "JOURNEY_COLLECTION_CONTEXT")
	exactCtx := tokenContext(member, "JOURNEY_SERVER_CONTEXT")
	ungrantedCtx := tokenContext(member, "JOURNEY_UNGRANTED_CONTEXT")
	store := controlplane.NewPostgresStore(database.DB)
	if _, err := store.EnsureTenant(ownerCtx, controlplane.Tenant{ID: tenant, Kind: "saas"}); err != nil {
		t.Fatal(err)
	}
	// The application intentionally cannot delete tenant anchors. This dedicated
	// synthetic anchor is cleaned by the Stage operator after the probe.

	scope, err := controlplane.NewOwnerInventoryReadScope(tenant, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"one", "two"} {
		stackID, serverID := "jrz6y_stage_stack_"+suffix, "jrz6y_stage_server_"+suffix
		if _, err := store.CreateStack(ownerCtx, controlplane.CreateStackRequest{ID: stackID, TenantID: tenant, OwnerSubjectID: owner, Name: stackID}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx := tenantdb.NewContextWithToken(identity.NewContext(context.Background(), &identity.Identity{UserID: owner, OrgID: tenant}), os.Getenv("JOURNEY_OWNER_CONTEXT"))
			if err := tenantdb.WithContextTx(cleanupCtx, database.DB, func(tx *sql.Tx) error {
				if err := tenantcontext.ApplyTenant(cleanupCtx, tx, tenant); err != nil {
					return err
				}
				if _, err := tx.ExecContext(cleanupCtx, `DELETE FROM services WHERE tenant_id=$1 AND server_id=$2`, tenant, serverID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(cleanupCtx, `DELETE FROM servers WHERE tenant_id=$1 AND id=$2`, tenant, serverID); err != nil {
					return err
				}
				_, err := tx.ExecContext(cleanupCtx, `DELETE FROM stacks WHERE tenant_id=$1 AND id=$2`, tenant, stackID)
				return err
			}); err != nil {
				t.Error("synthetic fixture cleanup", err)
			}
		})
		if _, err := store.UpsertServerRuntime(ownerCtx, controlplane.ServerRuntime{ID: serverID, TenantID: tenant, OwnerSubjectID: owner, StackID: stackID, Name: serverID}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpsertServiceRuntime(ownerCtx, controlplane.ServiceRuntime{ID: "jrz6y_stage_service_" + suffix, TenantID: tenant, StackID: stackID, ServerID: serverID, ServiceKey: "test", ServiceInstance: suffix, Name: "Household Stage boundary"}); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := store.ListInventoryServers(collectionCtx, scope, controlplane.InventoryPageRequest{})
	if err != nil || len(listed.Servers) != 2 {
		t.Fatalf("owner collection = %d rows, %v", len(listed.Servers), err)
	}
	services, err := store.ListInventoryServices(collectionCtx, scope, "", controlplane.InventoryPageRequest{})
	if err != nil || len(services.Services) != 2 {
		t.Fatalf("owner service collection = %d, %v", len(services.Services), err)
	}
	services, err = store.ListInventoryServices(exactCtx, scope, "jrz6y_stage_server_one", controlplane.InventoryPageRequest{})
	if err != nil || len(services.Services) != 1 {
		t.Fatalf("exact server services = %d, %v", len(services.Services), err)
	}
	server, err := store.GetInventoryServer(exactCtx, scope, "jrz6y_stage_server_one")
	if err != nil || server.ID != "jrz6y_stage_server_one" {
		t.Fatalf("exact server read: %v", err)
	}
	if _, err := store.GetInventoryStack(exactCtx, scope, "jrz6y_stage_stack_one"); err != nil {
		t.Fatal("dependent stack read", err)
	}
	if _, err := store.GetInventoryServer(exactCtx, scope, "jrz6y_stage_server_two"); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("other server: %v", err)
	}
	if _, err := store.GetInventoryStack(exactCtx, scope, "jrz6y_stage_stack_two"); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("other stack: %v", err)
	}
	if _, err := store.ListInventoryServers(ungrantedCtx, scope, controlplane.InventoryPageRequest{}); !errors.Is(err, tenantdb.ErrContextMismatch) {
		t.Fatalf("ungranted owner selection: %v", err)
	}
	err = tenantdb.WithContextTx(ungrantedCtx, database.DB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ungrantedCtx, `SELECT set_config('app.tenant_id',$1,true)`, tenant); err != nil {
			return err
		}
		var rows int
		if err := tx.QueryRowContext(ungrantedCtx, `SELECT count(*) FROM servers WHERE tenant_id=$1`, tenant).Scan(&rows); err != nil {
			return err
		}
		if rows != 0 {
			return fmt.Errorf("ungranted GUC exposed %d rows", rows)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Even chosen GUCs cannot turn a signed exact-server read into other rows or DML.
	err = tenantdb.WithContextTx(exactCtx, database.DB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(exactCtx, `SELECT set_config('app.tenant_id',$1,true)`, tenant); err != nil {
			return err
		}
		var rows int
		if err := tx.QueryRowContext(exactCtx, `SELECT count(*) FROM servers WHERE tenant_id=$1`, tenant).Scan(&rows); err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("exact token exposed %d servers", rows)
		}
		for _, sqlText := range []string{`UPDATE servers SET name=name WHERE tenant_id=$1`, `DELETE FROM servers WHERE tenant_id=$1`} {
			result, err := tx.ExecContext(exactCtx, sqlText, tenant)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if changed != 0 {
				return fmt.Errorf("delegated read admitted DML")
			}
		}
		if _, err := tx.ExecContext(exactCtx, "SAVEPOINT refused_insert"); err != nil {
			return err
		}
		_, insertErr := tx.ExecContext(exactCtx, `INSERT INTO stacks(id,tenant_id,owner_subject_id,name,mode,status) VALUES('jrz6y_forbidden_stack',$1,$2,'Forbidden','easy','draft')`, tenant, owner)
		var denied *pgconn.PgError
		if !errors.As(insertErr, &denied) || denied.Code != "42501" {
			return fmt.Errorf("delegated INSERT was not refused by RLS: %v", insertErr)
		}
		if _, err := tx.ExecContext(exactCtx, "ROLLBACK TO SAVEPOINT refused_insert"); err != nil {
			return err
		}
		if err := tenantcontext.ApplyTenant(exactCtx, tx, tenant); !errors.Is(err, tenantdb.ErrContextMismatch) {
			return fmt.Errorf("command context accepted delegated tenant: %v", err)
		}
		return errors.New("rollback boundary probe")
	})
	if err == nil || err.Error() != "rollback boundary probe" {
		t.Fatal(err)
	}
}
