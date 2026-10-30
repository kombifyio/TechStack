package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
	"github.com/kombifyio/techstack/internal/executioncustody"
	"github.com/kombifyio/techstack/pkg/auth/testidentity"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/db/tenantcontext"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

// Public metadata linked by the original protected artifact producer. It is not
// an environment admission switch and grants no tenant or execution authority.
var compiledBackupRuntimeContractDigest string

// Qualification shares the production verifiers, but starts no server, policy,
// scanner, provider lifecycle or worker. Every connection is session read-only;
// the only context is a fresh real Gateway token verified by Stage PostgreSQL.
func runExecutionQualification(parent context.Context) error {
	fail := func(code string) error { return errors.New("EXECUTION_QUALIFY_" + code) }
	if len(os.Args) != 2 || config.ReleaseChannel() != "preview" || !db.DeclaresStagingDatabaseBinding() ||
		os.Getenv(backupWorkerModeEnv) != "execution" || os.Getenv(db.EnvDatabaseMigrationMode) != "verify" ||
		os.Getenv(db.EnvAllowDBGenesis) != "false" || os.Getenv(db.EnvMigrationDatabaseURL) != "" ||
		os.Getenv("TECHSTACK_MANAGED_TEST_ADMIN_DATABASE_URL") != "" || "" != "" {
		return fail("PROFILE")
	}
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(buildRevision) || os.Getenv("TECHSTACK_EXECUTION_EXPECTED_SOURCE_SHA") != buildRevision ||
		!regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(compiledBackupRuntimeContractDigest) ||
		os.Getenv("TECHSTACK_EXECUTION_EXPECTED_BACKUP_CONTRACT") != compiledBackupRuntimeContractDigest {
		return fail("SOURCE")
	}
	runtimeConfig, err := config.Load("")
	if err != nil || !runtimeConfig.DeploymentMode.IsSaaS() {
		return fail("HOSTED_BINDING")
	}
	var grants map[string]executioncustody.CallerGrant
	decoder := json.NewDecoder(strings.NewReader(os.Getenv("TECHSTACK_ADVANCED_ISSUER_CALLER_GRANTS")))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&grants) != nil || len(grants) != 1 || decoder.Decode(new(any)) != io.EOF {
		return fail("OWNER")
	}
	var grant executioncustody.CallerGrant
	for _, candidate := range grants {
		grant = candidate
	}
	if grant.Caller != "techstack-preview" || !testidentity.IsTestSubject(grant.OwnerID) ||
		grant.TenantID != testidentity.OwnerTenant(grant.OwnerID) || grant.DeploymentAdmission != "owned-records" ||
		grant.OwnerRef != "" || grant.DeploymentIDs == nil || len(grant.DeploymentIDs) != 0 {
		return fail("OWNER")
	}
	var input struct {
		ContextToken string `json:"context_token"`
	}
	limited := io.LimitReader(os.Stdin, 16385)
	raw, err := io.ReadAll(limited)
	if err != nil || len(raw) > 16384 {
		return fail("CONTEXT")
	}
	decoder = json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || len(input.ContextToken) > 4096 ||
		!regexp.MustCompile(`^v1\.techstack-stage-context-g1\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{43}$`).MatchString(input.ContextToken) {
		return fail("CONTEXT")
	}

	// The public CA is prepared by the existing source-pinned Stage projection.
	// No arbitrary pathname or credential file can become a native trust root.
	ca := os.Getenv("TECHSTACK_EXECUTION_PUBLIC_CA_FILE")
	expectedCA := filepath.Join(os.Getenv("RUNNER_TEMP"), "techstack-private-execution", "clickhouse-pg-ca.pem")
	info, err := os.Lstat(ca)
	if !filepath.IsAbs(ca) || filepath.Clean(ca) != expectedCA || err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return fail("CA")
	}
	for _, name := range []string{"DATABASE_URL", db.EnvBackupWorkerDatabaseURL, backupEntitlementsDSNEnv, db.EnvAdvancedIssuerDatabaseURL, providerControlRuntimeDatabaseURLEnv} {
		u, parseErr := url.Parse(os.Getenv(name))
		if parseErr != nil || u == nil || u.Scheme != "postgresql" || u.User == nil || u.User.Username() == "" || u.Fragment != "" ||
			u.Query().Get("sslmode") != "verify-full" || len(u.Query()["sslrootcert"]) != 1 ||
			(u.Query().Get("sslrootcert") != "/app/clickhouse-pg-ca.pem" && u.Query().Get("sslrootcert") != "/data/techstack-stg-ca.pem") {
			return fail("DATABASE_BINDING")
		}
		q := u.Query()
		if q.Has("options") || q.Has("role") || q.Has("session_authorization") || q.Has("default_transaction_read_only") {
			return fail("DATABASE_BINDING")
		}
		q.Set("sslrootcert", ca)
		// The Common projection opener already installs read-only before either
		// its posture query or its independent evaluator connection is opened.
		if name != backupEntitlementsDSNEnv {
			q.Set("default_transaction_read_only", "on")
		}
		u.RawQuery = q.Encode()
		if os.Setenv(name, u.String()) != nil {
			return fail("DATABASE_BINDING")
		}
	}
	ctx, cancel := context.WithTimeout(parent, 75*time.Second)
	defer cancel()
	open := func(name, role string) (*db.DB, error) {
		u, parseErr := url.Parse(os.Getenv(name))
		if parseErr != nil || u.User.Username() != role {
			return nil, fail("DATABASE_BINDING")
		}
		pool, openErr := db.OpenContext(ctx, db.Config{Backend: db.StoreBackendPostgres, DriverName: db.PostgresDriverName,
			DSN: u.String(), MaxOpenConns: 1, MaxIdleConns: 1})
		if openErr != nil {
			return nil, fail("DATABASE_BINDING")
		}
		var readOnly bool
		if pool.QueryRowContext(ctx, `SELECT current_setting('transaction_read_only')='on'`).Scan(&readOnly) != nil || !readOnly ||
			db.VerifyExpectedDatabaseIdentity(ctx, pool.DB) != nil {
			pool.Close()
			return nil, fail("DATABASE_BINDING")
		}
		return pool, nil
	}
	app, err := open("DATABASE_URL", db.HostedApplicationRole)
	if err != nil {
		return err
	}
	defer app.Close()
	if db.VerifyPrivateExecutionApplicationRole(ctx, app.DB) != nil || app.VerifyMigrations(ctx) != nil {
		return fail("APPLICATION_ROLE")
	}
	if _, err = db.VerifyStagingApplicationBinding(ctx, app.DB); err != nil {
		return fail("DATABASE_BINDING")
	}
	contextScope := tenantdb.NewContextWithToken(ctx, input.ContextToken)
	verifyContext := func() bool {
		tx, beginErr := app.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if beginErr != nil {
			return false
		}
		defer tx.Rollback()
		if tenantcontext.ApplyTenant(contextScope, tx, grant.TenantID) != nil {
			return false
		}
		var subject, tenant, key string
		var fresh bool
		if tx.QueryRowContext(ctx, `SELECT subject,org_id,key_id,expires_at>statement_timestamp()
		 FROM kombify_ctx.verified_context()`).Scan(&subject, &tenant, &key, &fresh) != nil {
			return false
		}
		return subject == grant.OwnerID && tenant == grant.TenantID && key == "techstack-stage-context-g1" && fresh
	}
	if !verifyContext() {
		return fail("CONTEXT")
	}
	worker, err := open(db.EnvBackupWorkerDatabaseURL, db.BackupWorkerRole)
	if err != nil {
		return err
	}
	defer worker.Close()
	owner, err := db.ApplicationAuthorityOwner(ctx, worker.DB)
	if err != nil || db.VerifyBackupWorkerRuntimeRole(ctx, worker.DB) != nil || worker.VerifyMigrations(ctx) != nil ||
		db.VerifyApplicationPrivileges(ctx, worker.DB, db.ApplicationPrincipal{Role: db.BackupWorkerRole, AuthorityOwner: owner}, true) != nil {
		return fail("WORKER_ROLE")
	}
	issuer, err := open(db.EnvAdvancedIssuerDatabaseURL, "techstack_advanced_issuer")
	if err != nil {
		return err
	}
	defer issuer.Close()
	if db.VerifyAdvancedIssuerRuntimePrivileges(ctx, issuer.DB, "techstack_advanced_issuer", db.HostedApplicationRole) != nil {
		return fail("ISSUER_ROLE")
	}
	provider, err := openProviderControlRuntimeDatabase(ctx, app.DB)
	if err != nil {
		return fail("PROVIDER_ROLE")
	}
	defer provider.Close()
	if _, err = db.VerifyStagingRuntimeBinding(ctx, provider.DB); err != nil {
		return fail("PROVIDER_ROLE")
	}
	projection, err := openBackupEntitlements(ctx)
	if err != nil {
		return fail("PROJECTION_ROLE")
	}
	defer projection.Close()
	decision, err := readBackupEntitlements(ctx, projection, grant.OwnerID, true)
	if err != nil {
		var reason backupEntitlementsError
		if errors.As(err, &reason) {
			return fail(string(reason))
		}
		return fail("ENTITLEMENTS")
	}
	for _, feature := range monthlyruntime.RequiredFeatureKeysForBackup(true) {
		if !decision.enabled[feature] {
			return fail("ENTITLEMENTS_DISABLED")
		}
	}
	// Recheck the real verifier's expiry after every independent SQL/Common read;
	// a token that became stale while qualifying cannot produce current proof.
	if !verifyContext() {
		return fail("CONTEXT")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema": "techstack.execution-qualification/v1", "status": "verified", "source_sha": buildRevision,
		"contract_digest": compiledBackupRuntimeContractDigest, "worker_role": db.BackupWorkerRole,
		"worker_identity": os.Getenv(db.EnvExpectedDatabaseIdentity), "projection_role": backupEntitlementsRole,
		"projection_identity": os.Getenv(backupEntitlementsIdentityEnv), "stage_owner_subject_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(grant.OwnerID))),
		"role_qualified": true, "fresh_entitlements_qualified": true, "stage_context_key_id": "techstack-stage-context-g1",
		"stage_context_registered": true, "lease_job_contract_qualified": true, "issuer_role_qualified": true, "provider_role_qualified": true,
	})
}
