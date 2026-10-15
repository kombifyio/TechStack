package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/db"
	"github.com/jackc/pgx/v5"
)

// The original assigned preparation created the existing provider login with
// CONNECT/USAGE only. Admit its previously unapplied canonical grant stage
// separately, without replaying roles, migrations or issuer/key preparation.
func prepareAssignedProviderRuntimeGrants(ctx context.Context) error {
	if os.Getenv("TECHSTACK_EXPECTED_PREPARATION_SOURCE_SHA") == "" || os.Getenv(db.EnvManagedTestAssignment) == "" ||
		os.Getenv(db.EnvDatabaseMigrationMode) != "verify" || os.Getenv("TECHSTACK_ALLOW_DB_GENESIS") != "false" ||
		os.Getenv(db.EnvManagedTestAdminDatabaseURL) != "" {
		return errors.New("provider grant preparation requires exact source and existing assigned verify-only owner/app/provider custody")
	}
	assignment, err := db.LoadManagedTestAssignment()
	if err != nil {
		return err
	}
	if assignment.ServiceID != "44d4056d-5f7b-82d0-b6bf-9010682190cb" || assignment.Database != "techstack_test" || assignment.SystemIdentifier != "7695426324331921794" {
		return errors.New("provider grant preparation is limited to the original diagnosed shared-test assignment")
	}
	ownerConfig, err := db.MigrationConfigFromEnv()
	if err != nil {
		return err
	}
	owner, err := db.OpenContext(ctx, ownerConfig)
	if err != nil {
		return err
	}
	defer owner.Close()
	if err = db.VerifyManagedTestSession(ctx, owner.DB, *assignment, db.ManagedTestOwner); err != nil {
		return err
	}
	if err = owner.VerifyMigrations(ctx); err != nil {
		return err
	}
	var admitted bool
	if err = owner.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_roles o ON o.oid=p.proowner
 JOIN pg_catalog.pg_roles r ON r.rolname='techstack_provider_control_runtime'
 WHERE p.oid='public.provider_control_runtime_authority()'::regprocedure AND p.oid=25305 AND p.proowner=22759
 AND o.rolname=current_user AND p.prosecdef AND md5(p.prosrc)='19c3f0a299fa58726fd3f35372da55f9'
 AND p.proconfig @> ARRAY['search_path=pg_catalog, public, pg_temp']
 AND has_function_privilege(o.oid,'pg_catalog.pg_control_system()','EXECUTE')
 AND r.rolcanlogin AND NOT(r.rolsuper OR r.rolbypassrls OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members m WHERE m.member=r.oid)
 AND NOT has_function_privilege(r.oid,p.oid,'EXECUTE'))`).Scan(&admitted); err != nil || !admitted {
		return errors.New("provider grant preparation authority changed or already applied; inspect receipt before any replay")
	}
	// This governed preparation carries owner and app custody together. Use
	// the existing typed app binding; ordinary startup keeps refusing owner env.
	applicationConfig, err := db.ManagedTestConfig(*assignment, os.Getenv(db.EnvDatabaseURL), db.ManagedTestApp)
	if err != nil {
		return err
	}
	application, err := db.OpenContext(ctx, applicationConfig)
	if err != nil {
		return err
	}
	defer application.Close()
	if err = db.VerifyManagedTestSession(ctx, application.DB, *assignment, db.ManagedTestApp); err != nil {
		return err
	}
	appIdentity, err := loadProviderControlMigrationRoleIdentity(ctx, application.DB)
	if err != nil || appIdentity.authorityOwnerOID != 22759 || appIdentity.roleOID == appIdentity.authorityOwnerOID {
		return errors.New("provider grant preparation requires independently authenticated existing app custody and canonical owner")
	}
	providerConfig, err := providerControlRuntimeDatabaseConfigFromEnv()
	if err != nil {
		return err
	}
	provider, err := db.OpenContext(ctx, providerConfig)
	if err != nil {
		return err
	}
	defer provider.Close()
	var role string
	if err = provider.QueryRowContext(ctx, `SELECT current_user WHERE current_user=session_user`).Scan(&role); err != nil || role != providerControlRuntimeRoleName {
		return errors.New("provider grant preparation requires independently authenticated existing canonical login")
	}
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		return err
	}
	catalog, err := loadProviderControlGrantPreparationCatalog(ctx, tx)
	if err != nil {
		return err
	}
	if catalog.identity.roleName != providerControlRuntimeRoleName || catalog.identity.authorityOwnerOID != 22759 ||
		catalog.superuser || catalog.bypassRLS || catalog.createDatabase || catalog.createRole || catalog.replication ||
		catalog.setRoleMembership || catalog.untrustedObjectCreation || catalog.hasApplicationOwnership ||
		catalog.hasExcessTableAuthority || catalog.hasExcessSequenceAuthority || catalog.hasExcessFunctionAuthority ||
		catalog.missingTenantRLSFence || catalog.malformedSecurityDefinerFence {
		return errors.New("provider grant preparation existing PUBLIC/role/tenant/definer fences drifted; no revocation or grant admitted")
	}
	// Restore only absent capabilities from the original canonical definitions.
	// No PUBLIC, app, owner, unrelated schema or existing grant is modified.
	for _, grant := range providerControlRuntimeTableGrants {
		for _, privilege := range strings.Split(grant.Privileges, ",") {
			object := pgx.Identifier{"public", grant.Table}.Sanitize()
			var present bool
			if err = tx.QueryRowContext(ctx, "SELECT has_table_privilege($1,$2,$3)", providerControlRuntimeRoleName, object, privilege).Scan(&present); err != nil {
				return err
			}
			if !present {
				if _, err = tx.ExecContext(ctx, "GRANT "+privilege+" ON TABLE "+object+" TO "+pgx.Identifier{providerControlRuntimeRoleName}.Sanitize()); err != nil {
					return err
				}
			}
		}
	}
	for _, sequence := range providerControlRuntimeSequences {
		object := pgx.Identifier{"public", sequence}.Sanitize()
		var present bool
		if err = tx.QueryRowContext(ctx, "SELECT has_sequence_privilege($1,$2,'USAGE')", providerControlRuntimeRoleName, object).Scan(&present); err != nil {
			return err
		}
		if !present {
			if _, err = tx.ExecContext(ctx, "GRANT USAGE ON SEQUENCE "+object+" TO "+pgx.Identifier{providerControlRuntimeRoleName}.Sanitize()); err != nil {
				return err
			}
		}
	}
	for _, function := range providerControlRuntimeFunctions {
		var present bool
		if err = tx.QueryRowContext(ctx, "SELECT has_function_privilege($1,$2,'EXECUTE')", providerControlRuntimeRoleName, "public."+function).Scan(&present); err != nil {
			return err
		}
		if !present {
			if _, err = tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION public."+function+" TO "+pgx.Identifier{providerControlRuntimeRoleName}.Sanitize()); err != nil {
				return err
			}
		}
	}
	catalog, err = loadProviderControlGrantCatalog(ctx, tx)
	if err != nil {
		return err
	}
	if err = validateProviderControlRuntimeAuthorityForOwner(catalog, appIdentity, appIdentity.authorityOwnerOID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = verifyProviderControlRuntimeRolePosture(ctx, provider.DB, application.DB); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema": "techstack.credential-preparation/v1", "operation": "prepare-provider-grants", "status": "acknowledged",
		"source_sha": buildRevision, "service_id": assignment.ServiceID, "database": assignment.Database,
		"provider_role": role, "provider_privileges_verified": true, "tenant_fences_verified": true,
		"role_key_or_migration_replay": false,
	})
}
