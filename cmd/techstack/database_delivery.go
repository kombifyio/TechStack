package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/pkg/auth/testidentity"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/identity"
)

var errDatabaseDeliveryAdmission = errors.New("database delivery requires the owning compiled production publish authority")

const stagingDeliveryHost = "kombify-stg-pg-8146d168.pgv20v39ze9j4j3d3dwbj6p979.c0.eu-central-1.aws.pg.clickhouse.cloud"
const stagingDeliveryIdentity = "kombify_runtime@7699436836349883199"

// The existing governed staging runner may inspect/forward the exact product
// manifest without pretending to hold production publication authority.
func stagingDeliveryContext() error {
	// GitHub's immutable repository ID survives an owner/repository rename and
	// keeps hosted admission out of the public source's private organization map.
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_REPOSITORY_ID") != "1252283457" ||
		os.Getenv("GITHUB_REF") != "refs/heads/main" || os.Getenv("GITHUB_RUN_ATTEMPT") != "1" ||
		!regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(os.Getenv("GITHUB_REPOSITORY")) ||
		os.Getenv("GITHUB_WORKFLOW_REF") != os.Getenv("GITHUB_REPOSITORY")+"/.github/workflows/privileged-managed-pg.yml@refs/heads/main" ||
		!regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(buildRevision) || os.Getenv("TECHSTACK_DATABASE_SOURCE_SHA") != buildRevision ||
		os.Getenv("TECHSTACK_DATABASE_MANIFEST_SHA256") != db.DeliveryForwardManifestDigest() ||
		os.Getenv(db.EnvExpectedDatabaseIdentity) != stagingDeliveryIdentity {
		return errDatabaseDeliveryAdmission
	}
	switch os.Getenv("TECHSTACK_DATABASE_CONTEXT") {
	case "stage-development":
		if os.Getenv("TECHSTACK_DATABASE_PLAN_DIGEST") != "" || os.Getenv("TECHSTACK_DATABASE_RELEASE_ID") != "" {
			return errDatabaseDeliveryAdmission
		}
	case "release":
		if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(os.Getenv("TECHSTACK_DATABASE_PLAN_DIGEST")) ||
			!regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`).MatchString(os.Getenv("TECHSTACK_DATABASE_RELEASE_ID")) {
			return errDatabaseDeliveryAdmission
		}
	default:
		return errDatabaseDeliveryAdmission
	}
	return nil
}

func stagingDeliveryBinding(dsn, role string) error {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || u.User.Username() != role || u.Hostname() != stagingDeliveryHost ||
		(u.Port() != "" && u.Port() != "5432" && u.Port() != "6432") || u.Path != "/kombify_runtime" || u.Fragment != "" ||
		(u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return errDatabaseDeliveryAdmission
	}
	if password, ok := u.User.Password(); !ok || password == "" {
		return errDatabaseDeliveryAdmission
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || query.Get("sslmode") != "verify-full" || query.Get("sslrootcert") == "" {
		return errDatabaseDeliveryAdmission
	}
	for name, values := range query {
		if len(values) != 1 || values[0] == "" {
			return errDatabaseDeliveryAdmission
		}
		switch name {
		case "sslmode", "sslrootcert", "application_name", "connect_timeout", "channel_binding":
		default:
			return errDatabaseDeliveryAdmission
		}
	}
	return nil
}

func runDatabaseDelivery(ctx context.Context, args []string) error {
	staging := len(args) == 1 && strings.HasPrefix(args[0], "staging-")
	if staging {
		if err := stagingDeliveryContext(); err != nil {
			return err
		}
		args = []string{strings.TrimPrefix(args[0], "staging-")}
	}
	if staging && args[0] == "fixture" {
		return prepareStagingPreviewFixture(ctx)
	}
	if !staging && len(args) == 1 && args[0] == "contract" {
		return runContractMigrations(ctx)
	}
	if len(args) != 1 || (args[0] != "inspect" && args[0] != "apply" && args[0] != "verify") {
		return errDatabaseDeliveryAdmission
	}
	apply := args[0] == "apply"
	var contextKey databaseContextKeyRegistration
	if apply {
		var err error
		contextKey, err = databaseContextKeyFromMigrationEnv()
		if err != nil {
			return err
		}
	}
	if os.Getenv(db.EnvAllowDBGenesis) != "false" || db.ReleaseChannel() != "stable" {
		return errDatabaseDeliveryAdmission
	}
	// Production applies its own expand migrations inside its own Delivery.
	// A staging run is no antecedent (D-2026-10-27-06, platform-c1ez5):
	// merge-time expand-only lint and the old-binary-on-new-schema check
	// carry the safety, and the schema floor admits any newer ledger.
	if apply && !staging {
		if os.Getenv(db.EnvDatabaseMigrationMode) != "apply" || os.Getenv("DELIVERY_OPERATION") != "publish" ||
			os.Getenv("DELIVERY_ARTIFACT") != "database-schema" || os.Getenv("SOURCE_REPOSITORY") != "kombify-Techstack" ||
			os.Getenv("SOURCE_SHA") != buildRevision || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(buildRevision) ||
			!regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(os.Getenv("DELIVERY_PLAN_DIGEST")) ||
			os.Getenv("DELIVERY_DATABASE_ADMITTED") != "true" {
			return errDatabaseDeliveryAdmission
		}
	} else if (apply && os.Getenv(db.EnvDatabaseMigrationMode) != "apply") || (!apply && os.Getenv(db.EnvDatabaseMigrationMode) != "verify") {
		return errDatabaseDeliveryAdmission
	}
	pin, pinned, err := db.ExpectedDatabaseIdentityFromEnv()
	if err != nil || !pinned || pin.SystemIdentifier == "" {
		return errDatabaseDeliveryAdmission
	}
	batch, err := db.DeliveryForwardBatch()
	if err != nil {
		return err
	}
	evidence := map[string]any{"manifest_sha256": db.DeliveryForwardManifestDigest(), "phase": "production"}
	if staging {
		evidence["phase"] = "staging"
		evidence["context"] = os.Getenv("TECHSTACK_DATABASE_CONTEXT")
		evidence["plan_digest"] = os.Getenv("TECHSTACK_DATABASE_PLAN_DIGEST")
		evidence["release_id"] = os.Getenv("TECHSTACK_DATABASE_RELEASE_ID")
		for name, role := range map[string]string{"DATABASE_URL": db.HostedApplicationRole, "TECHSTACK_ADVANCED_ISSUER_DATABASE_URL": "techstack_advanced_issuer"} {
			if err := stagingDeliveryBinding(os.Getenv(name), role); err != nil {
				return err
			}
		}
		if args[0] == "verify" {
			if os.Getenv(db.EnvMigrationDatabaseURL) != "" {
				return errDatabaseDeliveryAdmission
			}
			appConfig, err := db.ConfigFromEnv()
			if err != nil {
				return err
			}
			application, err := db.OpenContext(ctx, appConfig)
			if err != nil {
				return err
			}
			state, err := application.InspectForwardBatch(ctx, batch)
			application.Close()
			if err != nil {
				return err
			}
			evidence["schema"] = "techstack.database-delivery/v1"
			evidence["operation"] = "verify"
			evidence["migrations"] = batch
			evidence["database_effects"] = false
			evidence["issuer_preserved"] = true
			evidence["forward"] = state
			return verifyAdvancedIssuerRuntimeWithEvidence(ctx, evidence)
		}
		if err := stagingDeliveryBinding(os.Getenv(db.EnvMigrationDatabaseURL), "kombify_runtime_user"); err != nil {
			return err
		}
	}
	cfg, err := advancedIssuerBootstrapConfigFromEnv()
	if err != nil {
		return err
	}
	host, err := db.DeliveryForwardDatabaseHost()
	if err != nil {
		return err
	}
	if staging {
		host = stagingDeliveryHost
	}
	for _, dsn := range []string{cfg.application.DSN, cfg.migration.DSN, cfg.issuerDSN} {
		uri, err := url.Parse(dsn)
		if err != nil || uri.Hostname() != host || uri.Query().Get("sslmode") != "verify-full" {
			return errors.New("database delivery target or transport differs")
		}
	}
	authority, err := db.OpenContext(ctx, cfg.migration)
	if err != nil {
		return err
	}
	defer authority.Close()
	if err := db.VerifyExpectedDatabaseIdentity(ctx, authority.DB); err != nil {
		return err
	}
	issuerID, keyID, err := verifyRetainedIssuerBeforePreparation(ctx, authority, cfg)
	if err != nil {
		return err
	}
	owner, err := db.ApplicationAuthorityOwner(ctx, authority.DB)
	if err != nil {
		return err
	}
	principal := db.ApplicationPrincipal{Role: cfg.appRole, AuthorityOwner: owner}
	backupPrincipal := db.ApplicationPrincipal{Role: db.BackupWorkerRole, AuthorityOwner: owner}
	before, err := authority.InspectForwardBatch(ctx, batch)
	if err != nil {
		return err
	}
	verify := func(ctx context.Context, tx *sql.Tx) error {
		if err := verifyDeliveryMigrationPosture(ctx, tx); err != nil {
			return err
		}
		if apply {
			// The exact migration and the capabilities it removes from PUBLIC
			// commit together. No role provisioning, blanket grant or owner login
			// enters the application pool.
			if err := db.InstallApplicationCapabilities(ctx, tx, principal); err != nil {
				return err
			}
			if err := db.InstallApplicationCapabilities(ctx, tx, backupPrincipal); err != nil {
				return err
			}
			// Register only in this governed migration transaction, after the
			// canonical schema exists. Runtime/fixture/verify children never
			// receive issuer custody, and an existing key is never reactivated.
			return registerDatabaseContextKey(ctx, tx, contextKey)
		}
		if err := db.VerifyApplicationPrivileges(ctx, tx, principal, true); err != nil {
			return err
		}
		return db.VerifyApplicationPrivileges(ctx, tx, backupPrincipal, true)
	}
	if apply {
		err = authority.MigrateForwardBatch(ctx, batch, verify)
		if err == nil {
			err = authority.VerifyMigrations(ctx)
		}
	} else if args[0] == "verify" {
		err = authority.VerifyMigrations(ctx)
		if err == nil {
			tx, txErr := authority.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if txErr != nil {
				return txErr
			}
			defer tx.Rollback()
			err = verify(ctx, tx)
		}
	} else {
		err = nil // The original read-only admission above supplies the inspect receipt.
	}
	if err != nil {
		return err
	}
	if args[0] != "inspect" {
		application, err := db.OpenContext(ctx, cfg.application)
		if err != nil {
			return err
		}
		defer application.Close()
		if err = db.VerifyExpectedDatabaseIdentity(ctx, application.DB); err != nil {
			return err
		}
		if err = db.VerifyRuntimeRolePosture(ctx, application.DB, db.RuntimeRoleHosted); err != nil {
			return err
		}
		if err = db.VerifyApplicationPrivileges(ctx, application.DB, principal, true); err != nil {
			return err
		}
	}
	observedIssuer, observedKey, err := verifyRetainedIssuerBeforePreparation(ctx, authority, cfg)
	if err != nil {
		return err
	}
	if observedIssuer != issuerID || observedKey != keyID {
		return errors.New("database delivery retained issuer changed")
	}
	state, err := authority.InspectForwardBatch(ctx, batch)
	if err != nil {
		return err
	}
	evidence["forward"] = state
	if staging && args[0] == "inspect" && os.Getenv("TECHSTACK_DATABASE_CONTEXT") == "stage-development" {
		posture, postureErr := authority.InspectStageForwardPosture(ctx, batch, state)
		if postureErr != nil {
			return postureErr
		}
		evidence["forward_posture"] = posture
	}
	if apply {
		evidence["forward_before"] = before
		evidence["db_context_key_id"] = contextKey.id
		evidence["db_context_registered"] = true
	}
	receipt := map[string]any{
		"schema": "techstack.database-delivery/v1", "operation": args[0], "source_sha": buildRevision,
		"migrations": batch, "database_effects": apply, "status": "acknowledged", "plan_digest": os.Getenv("DELIVERY_PLAN_DIGEST"),
		"database_identity": os.Getenv(db.EnvExpectedDatabaseIdentity), "issuer_id": issuerID, "key_id": keyID,
		"issuer_preserved": true, "wrapping_verified": true, "role_or_provider_mutations": false,
	}
	for name, value := range evidence {
		receipt[name] = value
	}
	return json.NewEncoder(os.Stdout).Encode(receipt)
}

// Only the governed development operation can materialize the canonical test
// user's normal owner-tenant projection. It holds the application login only;
// no migration, signing, provider authority or server startup enters this path.
func prepareStagingPreviewFixture(ctx context.Context) error {
	subject := os.Getenv("TECHSTACK_STAGE_FIXTURE_SUBJECT")
	if subject == "" {
		subject = os.Getenv(testidentity.EnvFixtureSubject)
	}
	tenantID := testidentity.OwnerTenant(subject)
	if os.Getenv("TECHSTACK_DATABASE_CONTEXT") != "stage-development" ||
		!regexp.MustCompile(`^auth0\|[A-Za-z0-9_-]{1,128}$`).MatchString(subject) ||
		os.Getenv(testidentity.EnvSubjects) != subject || testidentity.Admit(subject, tenantID) != nil ||
		os.Getenv(db.EnvAllowDBGenesis) != "false" || os.Getenv(db.EnvDatabaseMigrationMode) != "verify" ||
		db.ReleaseChannel() != "stable" {
		return errDatabaseDeliveryAdmission
	}
	for _, name := range []string{db.EnvMigrationDatabaseURL, "TECHSTACK_ADVANCED_ISSUER_DATABASE_URL",
		"TECHSTACK_PROVIDER_CONTROL_RUNTIME_DATABASE_URL", "TECHSTACK_ENCRYPTION_KEY"} {
		if os.Getenv(name) != "" {
			return errDatabaseDeliveryAdmission
		}
	}
	if err := stagingDeliveryBinding(os.Getenv("DATABASE_URL"), db.HostedApplicationRole); err != nil {
		return err
	}
	cfg, err := db.ConfigFromEnv()
	if err != nil {
		return err
	}
	application, err := db.OpenContext(ctx, cfg)
	if err != nil {
		return err
	}
	defer application.Close()
	if err := db.VerifyExpectedDatabaseIdentity(ctx, application.DB); err != nil {
		return err
	}
	if err := db.VerifyRuntimeRolePosture(ctx, application.DB, db.RuntimeRoleHosted); err != nil {
		return err
	}
	if err := application.VerifyMigrations(ctx); err != nil {
		return err
	}
	store := controlplane.NewPostgresStore(application.DB)
	_, err = store.GetMembership(ctx, tenantID, subject)
	created := false
	if errors.Is(err, controlplane.ErrNotFound) {
		// Reuse the login/request projection, including its test-tenant fence.
		// A repeat verifies existing authority instead of overwriting grants.
		_, err = bootstrapIdentityTenantProjection(ctx, store, &identity.Identity{UserID: subject}, tenantID, subject)
		created = err == nil
	}
	if err != nil {
		return err
	}
	tenant, err := store.GetTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	membership, err := store.GetMembership(ctx, tenantID, subject)
	if err != nil {
		return err
	}
	if tenant.ID != tenantID || tenant.Kind != "saas" || tenant.Status != "active" ||
		membership.TenantID != tenantID || membership.UserID != subject || membership.SubjectID != subject ||
		membership.ProviderKey != "cloud" || membership.Status != "active" || membership.RoleKey != "member" {
		return errors.New("staging preview fixture differs from its canonical owner projection")
	}
	serverCreated, err := ensureStagingPreviewFixtureServer(ctx, store, tenantID, subject)
	if err != nil {
		return err
	}
	created = created || serverCreated
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema": "techstack.database-delivery/v1", "operation": "fixture", "status": "acknowledged",
		"source_sha": buildRevision, "manifest_sha256": db.DeliveryForwardManifestDigest(),
		"phase": "staging", "context": "stage-development", "database_identity": stagingDeliveryIdentity,
		"fixture_subject": subject, "tenant_id": tenantID, "application_role": db.HostedApplicationRole,
		"migration_mode": "verify", "genesis": false, "database_effects": created,
		"plan_digest": "", "release_id": "",
	})
}

// The Preview has no enrolled host of its own (the Stage Execution host is
// gone), so the owner fixture carries one synthetic, never-connected server in
// one draft stack. IDs derive from the subject, so a repeat changes nothing.
func ensureStagingPreviewFixtureServer(ctx context.Context, store *controlplane.PostgresStore, tenantID, subject string) (bool, error) {
	stackID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("techstack-stage-fixture-stack:"+subject)).String()
	serverID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("techstack-stage-fixture-server:"+subject)).String()
	created := false
	if _, err := store.GetStack(ctx, tenantID, stackID); errors.Is(err, controlplane.ErrNotFound) {
		if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: stackID, TenantID: tenantID,
			OwnerSubjectID: subject, Name: "Preview fixture", Description: "Synthetic Stage fixture stack"}); err != nil {
			return false, err
		}
		created = true
	} else if err != nil {
		return false, err
	}
	if _, err := store.GetServerRuntime(ctx, tenantID, serverID); errors.Is(err, controlplane.ErrNotFound) {
		if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{ID: serverID, TenantID: tenantID,
			StackID: stackID, OwnerSubjectID: subject, Name: "preview-fixture"}); err != nil {
			return false, err
		}
		created = true
	} else if err != nil {
		return false, err
	}
	return created, nil
}

func verifyDeliveryMigrationPosture(ctx context.Context, tx *sql.Tx) error {
	var safe bool
	err := tx.QueryRowContext(ctx, `SELECT NOT rolsuper AND NOT rolbypassrls
		FROM pg_roles WHERE rolname=current_user`).Scan(&safe)
	if err != nil {
		return err
	}
	if !safe {
		return errors.New("database migration owner bypasses tenant authority")
	}
	// Migration157 closes the helper's implicit PUBLIC EXECUTE. Verify that
	// effective capability before the entire batch and ledger can commit.
	err = tx.QueryRowContext(ctx, `SELECT NOT EXISTS (
		SELECT 1 FROM pg_proc p, LATERAL aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a
		WHERE p.oid='public.techstack_subject_ref(text)'::regprocedure
		AND a.grantee=0 AND a.privilege_type='EXECUTE')`).Scan(&safe)
	if err != nil {
		return err
	}
	if !safe {
		return errors.New("subject helper retains public execution")
	}
	return nil
}
