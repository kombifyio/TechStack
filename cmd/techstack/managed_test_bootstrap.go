package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/oidcclient"
	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/jackc/pgx/v5"
)

// This existing-entrypoint extension has no SQL argument, secret reader,
// provider runner or production migration fallback. Root supplies credentials
// from existing sealed custody after immutable review and Delivery admission.
func runManagedTestBootstrap(parent context.Context, args []string) error {
	if len(args) != 1 || (args[0] != "inspect" && args[0] != "inspect-issuer" && args[0] != "issuer-open" && args[0] != "prepare" && args[0] != "prepare-issuer" && args[0] != "verify-issuer" && args[0] != "migrate" && args[0] != "admit" && args[0] != "verify" && args[0] != "ril-scope" && args[0] != "ril-scope-recover" && args[0] != "ril-scope-cancel") {
		return fmt.Errorf("managed-test-bootstrap requires inspect, inspect-issuer, issuer-open, prepare, prepare-issuer, verify-issuer, migrate, admit, verify, ril-scope, ril-scope-cancel or ril-scope-recover")
	}
	if args[0] == "inspect" {
		return db.InspectManagedTest(parent)
	}
	a, err := db.LoadManagedTestAssignment()
	if err != nil {
		return err
	}
	if err := a.VerifyPin(); err != nil {
		return err
	}
	operation := args[0]
	limit := 30 * time.Second
	if operation == "prepare" || operation == "prepare-issuer" {
		limit = 90 * time.Second
	}
	if operation == "migrate" {
		limit = 6 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	switch operation {
	case "inspect-issuer":
		return db.InspectManagedTestIssuer(ctx, *a, os.Getenv(db.EnvManagedTestAdminDatabaseURL))
	case "issuer-open":
		if os.Getenv(db.EnvMigrationDatabaseURL) != "" || os.Getenv(db.EnvManagedTestAdminDatabaseURL) != "" || os.Getenv(providerControlRuntimeDatabaseURLEnv) != "" {
			return fmt.Errorf("issuer module observation cannot carry administrative or provider credentials")
		}
		cfg, err := db.ConfigFromEnv()
		if err != nil {
			return err
		}
		app, err := db.OpenContext(ctx, cfg)
		if err != nil {
			return err
		}
		defer app.Close()
		if err := app.VerifyMigrations(ctx); err != nil {
			return err
		}
		issuer, custody, err := advancedissuer.OpenBound(ctx, app, auth.GetEncryptor())
		if err != nil {
			return err
		}
		defer custody.Close()
		issuerID, keyID := issuer.IssuerID(), issuer.KeyID()
		if err := custody.Close(); err != nil {
			return err
		}
		reopened, secondCustody, err := advancedissuer.OpenBound(ctx, app, auth.GetEncryptor())
		if err != nil {
			return err
		}
		defer secondCustody.Close()
		if reopened.IssuerID() != issuerID || reopened.KeyID() != keyID {
			return fmt.Errorf("installation issuer identity changed on reopen")
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.managed-test-issuer-open/v1", "service_id": a.ServiceID, "database": a.Database, "status": "acknowledged", "issuer_id": issuerID, "key_id": keyID, "identity_preserved_on_reopen": true, "tenant_storage": "application pool", "key_storage": "signing-only pool", "private_key_output": false, "node_or_provider_effects": false})
	case "prepare-issuer":
		dsn := os.Getenv(db.EnvAdvancedIssuerDatabaseURL)
		config, err := a.Connection(dsn, db.ManagedTestIssuer)
		if err != nil {
			return err
		}
		verifier, err := postgresSCRAMVerifier(config.Password)
		if err != nil {
			return err
		}
		if err := db.PrepareManagedTestIssuer(ctx, *a, os.Getenv(db.EnvManagedTestAdminDatabaseURL), dsn, verifier); err != nil {
			return err
		}
	case "verify-issuer":
		if os.Getenv(db.EnvMigrationDatabaseURL) != "" || os.Getenv(db.EnvManagedTestAdminDatabaseURL) != "" || os.Getenv(providerControlRuntimeDatabaseURLEnv) != "" {
			return fmt.Errorf("signing custody verification cannot carry administrative or provider credentials")
		}
		cfg, err := db.ManagedTestConfig(*a, os.Getenv(db.EnvAdvancedIssuerDatabaseURL), db.ManagedTestIssuer)
		if err != nil {
			return err
		}
		pool, err := db.OpenContext(ctx, cfg)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := db.VerifyManagedTestIssuerRuntimePrivileges(ctx, pool.DB, *a); err != nil {
			return err
		}
	case "prepare":
		p := db.ManagedTestPreparation{Assignment: *a, AdminDSN: os.Getenv(db.EnvManagedTestAdminDatabaseURL), OwnerDSN: os.Getenv(db.EnvMigrationDatabaseURL), AppDSN: os.Getenv(db.EnvDatabaseURL), ProviderDSN: os.Getenv(providerControlRuntimeDatabaseURLEnv)}
		for _, item := range []struct {
			dsn, role string
			verifier  *string
		}{{p.OwnerDSN, db.ManagedTestOwner, &p.OwnerVerifier}, {p.AppDSN, db.ManagedTestApp, &p.AppVerifier}, {p.ProviderDSN, db.ManagedTestProvider, &p.ProviderVerifier}} {
			cfg, err := a.Connection(item.dsn, item.role)
			if err != nil {
				return err
			}
			verifier, err := postgresSCRAMVerifier(cfg.Password)
			if err != nil {
				return err
			}
			*item.verifier = verifier
		}
		if err := db.PrepareManagedTest(ctx, p); err != nil {
			return err
		}
	case "migrate":
		forwardDigest := os.Getenv("TECHSTACK_MANAGED_TEST_DRIFT_FORWARD_SHA256")
		pendingDigest := os.Getenv("TECHSTACK_MANAGED_TEST_PENDING_FORWARD_SHA256")
		executionDigest := os.Getenv("TECHSTACK_MANAGED_TEST_EXECUTION_FORWARD_SHA256")
		directoryDigest := os.Getenv("TECHSTACK_MANAGED_TEST_SUBJECT_DIRECTORY_FORWARD_SHA256")
		purposes := 0
		for _, d := range []string{forwardDigest, pendingDigest, executionDigest, directoryDigest} {
			if d != "" {
				purposes++
			}
		}
		if purposes > 1 {
			return fmt.Errorf("one exact managed forward purpose is required")
		}
		selectedDigest := forwardDigest
		if pendingDigest != "" {
			selectedDigest = pendingDigest
		}
		if executionDigest != "" {
			selectedDigest = executionDigest
		}
		if directoryDigest != "" {
			selectedDigest = directoryDigest
		}
		if os.Getenv(db.EnvDatabaseMigrationMode) != "apply" ||
			(selectedDigest == "" && os.Getenv(db.EnvAllowDBGenesis) != "true") ||
			(selectedDigest != "" && os.Getenv(db.EnvAllowDBGenesis) != "false") {
			return fmt.Errorf("assigned migration requires explicit apply and purpose-bound genesis mode")
		}
		cfg, err := db.MigrationConfigFromEnv()
		if err != nil {
			return err
		}
		owner, err := db.OpenContext(ctx, cfg)
		if err != nil {
			return err
		}
		defer owner.Close()
		if err := db.VerifyManagedTestSession(ctx, owner.DB, *a, db.ManagedTestOwner); err != nil {
			return err
		}
		if directoryDigest != "" {
			return runManagedTestSubjectDirectoryForward(ctx, *a, owner, directoryDigest)
		}
		if executionDigest != "" {
			return runManagedTestExecutionForward(ctx, *a, owner, executionDigest)
		}
		if pendingDigest != "" {
			return runManagedTestPendingForward(ctx, *a, owner, pendingDigest)
		}
		if forwardDigest != "" {
			return runManagedTestDriftForward(ctx, *a, owner, forwardDigest)
		}
		if err := owner.Migrate(ctx); err != nil {
			return err
		}
		if err := installManagedTestApplicationGrants(ctx, owner.DB); err != nil {
			return err
		}
	case "admit", "verify", "ril-scope", "ril-scope-recover", "ril-scope-cancel":
		if os.Getenv(db.EnvMigrationDatabaseURL) != "" || os.Getenv(db.EnvManagedTestAdminDatabaseURL) != "" {
			return fmt.Errorf("assigned app operation cannot carry owner/admin credentials")
		}
		cfg, err := db.ConfigFromEnv()
		if err != nil {
			return err
		}
		app, err := db.OpenContext(ctx, cfg)
		if err != nil {
			return err
		}
		defer app.Close()
		if err := db.VerifyManagedTestSession(ctx, app.DB, *a, db.ManagedTestApp); err != nil {
			return err
		}
		if err := app.VerifyMigrations(ctx); err != nil {
			return err
		}
		if operation == "ril-scope" || operation == "ril-scope-cancel" {
			if err := verifyManagedTestRILScope(ctx, app, *a, operation == "ril-scope-cancel"); err != nil {
				return err
			}
		}
		if operation == "ril-scope-recover" {
			if err := recoverManagedTestRILScope(ctx, app, *a); err != nil {
				return err
			}
		}
		if operation == "admit" {
			claims, err := verifyManagedTestIdentity(ctx, *a, os.Getenv("TECHSTACK_MANAGED_TEST_ID_TOKEN"))
			if err != nil {
				return err
			}
			upsert := v2CloudUserUpsert(controlplane.NewPostgresStore(app.DB), true)
			if err := upsert(ctx, claims, a.TenantID, ""); err != nil {
				return err
			}
			membership, err := controlplane.NewPostgresStore(app.DB).GetMembership(ctx, a.TenantID, a.SubjectID)
			if err != nil || membership == nil || membership.SubjectID != a.SubjectID || membership.UserID != a.SubjectID || membership.TenantID != a.TenantID || membership.ProviderKey != "cloud" || membership.Status != "active" {
				return fmt.Errorf("assigned shared identity projection did not read back")
			}
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.managed-test-operation/v1", "operation": operation, "service_id": a.ServiceID, "database": a.Database, "status": "acknowledged", "provider_effects": false})
}

func verifyManagedTestIdentity(ctx context.Context, a db.ManagedTestAssignment, token string) (*oidcclient.Claims, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("assigned shared identity requires its verified Auth0 token")
	}
	issuer := strings.TrimSuffix(config.DefaultCloudAuthIssuer, "/") + "/"
	verifier, err := oidcclient.NewVerifier(oidcclient.VerifierConfig{Issuer: issuer, Audience: a.AuthAudience, JWKSURL: issuer + ".well-known/jwks.json", HTTPClient: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		return nil, err
	}
	claims, err := verifier.Verify(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("assigned shared-test Auth0 verification failed")
	}
	if claims.Subject != a.SubjectID || claims.Email != a.Email || claims.Raw["email_verified"] != true {
		return nil, fmt.Errorf("verified Auth0 identity differs from the shared-test assignment")
	}
	tenant := ""
	for _, key := range []string{"org_id", "tenant_id", "https://kombify.io/tenant_id"} {
		if value, ok := claims.Raw[key].(string); ok && value != "" {
			if tenant != "" && tenant != value {
				return nil, fmt.Errorf("verified Auth0 tenant claims conflict")
			}
			tenant = value
		}
	}
	if tenant == "" && a.TenantID == "usr:"+claims.Subject {
		tenant = a.TenantID
	}
	if tenant != a.TenantID {
		return nil, fmt.Errorf("verified Auth0 token does not bind the assigned tenant")
	}
	return claims, nil
}

// Installation and runtime verification share the same exact capability policy.
// Reconciliation removes stale direct/default/PUBLIC grants; residual authority
// from another owner is rejected by the effective audit before commit.
func installManagedTestApplicationGrants(ctx context.Context, owner *sql.DB) error {
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout='5s'"); err != nil {
		return err
	}
	role := pgx.Identifier{db.ManagedTestApp}.Sanitize()
	ownerRole := pgx.Identifier{db.ManagedTestOwner}.Sanitize()
	if _, err := tx.ExecContext(ctx, "REVOKE ALL ON DATABASE "+pgx.Identifier{db.ManagedTestDatabase}.Sanitize()+" FROM "+role); err != nil {
		return err
	}
	schemas, err := tx.QueryContext(ctx, `SELECT n.nspname FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner
 WHERE n.nspname<>'information_schema' AND n.nspname NOT LIKE 'pg_%' AND r.rolname=$1`, db.ManagedTestOwner)
	if err != nil {
		return err
	}
	var names []string
	for schemas.Next() {
		var name string
		if err := schemas.Scan(&name); err != nil {
			schemas.Close()
			return err
		}
		names = append(names, name)
	}
	if err := schemas.Err(); err != nil {
		schemas.Close()
		return err
	}
	schemas.Close()
	for _, schema := range names {
		quoted := pgx.Identifier{schema}.Sanitize()
		for _, grantee := range []string{role, "PUBLIC"} {
			for _, statement := range []string{
				"REVOKE ALL ON SCHEMA " + quoted + " FROM " + grantee,
				"REVOKE ALL ON ALL TABLES IN SCHEMA " + quoted + " FROM " + grantee,
				"REVOKE ALL ON ALL SEQUENCES IN SCHEMA " + quoted + " FROM " + grantee,
				"REVOKE ALL ON ALL ROUTINES IN SCHEMA " + quoted + " FROM " + grantee,
			} {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return err
				}
			}
			for _, kind := range []string{"TABLES", "SEQUENCES", "FUNCTIONS"} {
				if _, err := tx.ExecContext(ctx, "ALTER DEFAULT PRIVILEGES FOR ROLE "+ownerRole+" IN SCHEMA "+quoted+" REVOKE ALL ON "+kind+" FROM "+grantee); err != nil {
					return err
				}
			}
		}
	}
	for _, grantee := range []string{role, "PUBLIC"} {
		for _, kind := range []string{"TABLES", "SEQUENCES", "FUNCTIONS"} {
			if _, err := tx.ExecContext(ctx, "ALTER DEFAULT PRIVILEGES FOR ROLE "+ownerRole+" REVOKE ALL ON "+kind+" FROM "+grantee); err != nil {
				return err
			}
		}
	}
	if err := db.VerifyManagedTestApplicationPrivileges(ctx, tx, false); err != nil {
		return err
	}
	if err := db.InstallManagedTestApplicationCapabilities(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
