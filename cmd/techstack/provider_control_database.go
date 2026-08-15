package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/db"
	"github.com/jackc/pgx/v5"
)

const (
	providerControlRuntimeDatabaseURLEnv = "TECHSTACK_PROVIDER_CONTROL_RUNTIME_DATABASE_URL"
	providerControlRuntimeRoleName       = "techstack_provider_control_runtime"
)

var errProviderControlRuntimeDatabaseNotConfigured = errors.New("provider-control runtime database is not configured")

const providerControlRuntimeRolePostureQuery = `
WITH required_table_capability(table_name, privilege) AS (
    VALUES
        ('provider_catalog_versions', 'SELECT'),
        ('provider_catalog_profiles', 'SELECT'),
        ('provider_credential_handles', 'SELECT'),
        ('provider_credential_handles', 'INSERT'),
        ('servers', 'SELECT'),
        ('servers', 'INSERT'),
        ('servers', 'UPDATE'),
        ('server_state_transitions', 'SELECT'),
        ('server_state_transitions', 'INSERT'),
        ('server_registry_outbox', 'SELECT'),
        ('server_registry_outbox', 'INSERT'),
        ('techstack_vm_leases', 'SELECT'),
        ('techstack_vm_leases', 'INSERT'),
        ('techstack_vm_leases', 'UPDATE'),
        ('runtime_lease_execution_authorities', 'SELECT'),
        ('runtime_lease_execution_authorities', 'INSERT'),
        ('runtime_lease_idempotency_records', 'SELECT'),
        ('runtime_lease_idempotency_records', 'INSERT'),
        ('managed_runtime_capacity_reservations', 'SELECT'),
        ('managed_runtime_capacity_reservations', 'INSERT'),
		('managed_runtime_server_slots', 'SELECT'),
		('managed_runtime_server_slots', 'INSERT'),
		('managed_runtime_server_slot_generations', 'SELECT'),
		('managed_runtime_server_slot_generations', 'INSERT'),
        ('managed_runtime_capacity_release_facts', 'SELECT'),
        ('managed_runtime_capacity_release_facts', 'INSERT'),
		('managed_runtime_capacity_quarantine_retirements', 'SELECT'),
        ('provider_desired_spec_revisions', 'SELECT'),
        ('provider_desired_spec_revisions', 'INSERT'),
        ('provider_desired_spec_revisions', 'UPDATE'),
        ('provider_operations', 'SELECT'),
        ('provider_operations', 'INSERT'),
        ('provider_operations', 'UPDATE'),
		('provider_async_requests', 'SELECT'),
		('provider_async_requests', 'INSERT'),
		('provider_async_requests', 'UPDATE'),
        ('provider_operation_receipts', 'SELECT'),
        ('provider_operation_receipts', 'INSERT'),
        ('provider_operation_resources', 'SELECT'),
        ('provider_operation_resources', 'INSERT'),
        ('provider_operation_resources', 'UPDATE'),
        ('provider_operation_evidence', 'SELECT'),
        ('provider_operation_evidence', 'INSERT'),
        ('provider_absence_observations', 'SELECT'),
        ('provider_absence_observations', 'INSERT'),
        ('provider_operation_evidence', 'UPDATE'),
        ('provider_operation_execution_claims', 'SELECT'),
        ('provider_operation_execution_claims', 'INSERT'),
        ('provider_operation_execution_claims', 'UPDATE'),
        ('provider_provision_dispatch_guards', 'SELECT'),
        ('provider_provision_dispatch_guards', 'INSERT'),
        ('provider_provision_resolution_decisions', 'SELECT'),
        ('server_provider_resource_bindings', 'SELECT'),
        ('server_provider_resource_bindings', 'INSERT'),
        ('provider_operation_resource_free_terminalizations', 'SELECT'),
        ('provider_operation_resource_free_terminalizations', 'INSERT'),
        ('pairing_tokens', 'SELECT'),
        ('pairing_tokens', 'INSERT'),
        ('provider_incidents', 'SELECT'),
        ('provider_incidents', 'INSERT'),
        ('ril_workflow_runs', 'SELECT'),
        ('ril_workflow_runs', 'INSERT')
),
required_sequence(sequence_name) AS (
    VALUES
        ('server_state_transitions_id_seq'),
        ('server_registry_outbox_id_seq')
),
required_tenant_rls(table_name) AS (
    VALUES
        ('provider_credential_handles'),
        ('servers'),
        ('server_state_transitions'),
        ('server_registry_outbox'),
        ('techstack_vm_leases'),
        ('runtime_lease_execution_authorities'),
        ('runtime_lease_idempotency_records'),
        ('managed_runtime_capacity_reservations'),
		('managed_runtime_server_slots'),
		('managed_runtime_server_slot_generations'),
        ('managed_runtime_capacity_release_facts'),
		('managed_runtime_capacity_quarantine_retirements'),
        ('provider_absence_observations'),
        ('provider_desired_spec_revisions'),
        ('provider_operations'),
		('provider_async_requests'),
        ('provider_operation_receipts'),
        ('provider_operation_resources'),
        ('provider_operation_evidence'),
        ('provider_operation_execution_claims'),
        ('provider_provision_dispatch_guards'),
        ('provider_provision_resolution_decisions'),
        ('server_provider_resource_bindings'),
        ('provider_operation_resource_free_terminalizations'),
        ('pairing_tokens'),
        ('provider_incidents')
),
security_definer_boundary(function_name, runtime_executable) AS (
    VALUES
        ('provider_execution_immutable_update', false),
        ('provider_provision_dispatch_guard_validate_insert', false),
        ('provider_provision_discovery_active_claim_guard', false),
        ('provider_provision_resolution_active_claim_guard', false),
        ('provider_execution_claim_current_head', false),
        ('provider_execution_claim_credential_guard', false),
        ('provider_operation_runtime_generation_guard', false),
        ('provider_operation_head_update_guard', false),
        ('provider_execution_claim_runtime_generation_guard', false),
        ('provider_resource_free_terminalization_validate_insert', false),
        ('provider_resource_free_terminalization_validate_commit', false),
        ('managed_runtime_capacity_release_validate_insert', false),
		('provider_control_refresh_runnable_tenant', false),
		('provider_control_refresh_decommission_wait_tenant', false),
		('provider_control_refresh_provider_provision_wait', false),
		('provider_control_lock_runtime_lease_projection', true),
		('provider_control_count_unsettled_generation_dispatch_guards', true),
		('provider_control_list_runnable_tenants', true),
		('provider_control_list_due_decommission_wait_tenants', true),
		('provider_control_list_provider_provision_waits', true),
		('provider_control_list_stale_capacity_recovery_candidates', true),
        ('provider_incident_refresh_pending_dispatch_tenant', false),
        ('provider_incident_list_tenant_ids', false),
        ('provider_control_runtime_authority', true)
)
SELECT
    runtime_role.oid::bigint,
    runtime_role.rolname,
    session_role.oid::bigint,
    session_role.rolname,
    physical_authority.system_identifier,
    physical_authority.database_oid,
    physical_authority.database_name,
    physical_authority.schema_oid,
    physical_authority.schema_name,
    runtime_authority_function.proowner::bigint,
    runtime_role.rolsuper,
    runtime_role.rolbypassrls,
    runtime_role.rolcreatedb,
    runtime_role.rolcreaterole,
    runtime_role.rolreplication,
    EXISTS (
        SELECT 1
        FROM pg_catalog.pg_roles AS granted_role
        WHERE granted_role.oid <> runtime_role.oid
          AND pg_catalog.pg_has_role(runtime_role.oid, granted_role.oid, 'MEMBER')
    ) AS has_set_role_membership,
    (
        pg_catalog.has_database_privilege(runtime_role.oid, physical_authority.database_oid, 'CREATE')
        OR pg_catalog.has_database_privilege(runtime_role.oid, physical_authority.database_oid, 'TEMP')
        OR EXISTS (
            SELECT 1
            FROM pg_catalog.pg_namespace AS namespace
            WHERE namespace.nspname <> 'information_schema'
              AND namespace.nspname NOT LIKE 'pg_%'
              AND pg_catalog.has_schema_privilege(runtime_role.oid, namespace.oid, 'CREATE')
        )
    ) AS has_untrusted_object_creation_authority,
    (
        EXISTS (
            SELECT 1
            FROM pg_catalog.pg_database AS database
            WHERE database.oid = physical_authority.database_oid
              AND pg_catalog.pg_has_role(runtime_role.oid, database.datdba, 'MEMBER')
        )
        OR EXISTS (
            SELECT 1
            FROM pg_catalog.pg_namespace AS namespace
            WHERE namespace.nspname <> 'information_schema'
              AND namespace.nspname NOT LIKE 'pg_%'
              AND pg_catalog.pg_has_role(runtime_role.oid, namespace.nspowner, 'MEMBER')
        )
        OR EXISTS (
            SELECT 1
            FROM pg_catalog.pg_class AS object
            JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = object.relnamespace
            WHERE namespace.nspname <> 'information_schema'
              AND namespace.nspname NOT LIKE 'pg_%'
              AND pg_catalog.pg_has_role(runtime_role.oid, object.relowner, 'MEMBER')
        )
        OR EXISTS (
            SELECT 1
            FROM pg_catalog.pg_proc AS object
            JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = object.pronamespace
            WHERE namespace.nspname <> 'information_schema'
              AND namespace.nspname NOT LIKE 'pg_%'
              AND pg_catalog.pg_has_role(runtime_role.oid, object.proowner, 'MEMBER')
        )
        OR EXISTS (
            SELECT 1
            FROM pg_catalog.pg_type AS object
            JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = object.typnamespace
            WHERE namespace.nspname <> 'information_schema'
              AND namespace.nspname NOT LIKE 'pg_%'
              AND pg_catalog.pg_has_role(runtime_role.oid, object.typowner, 'MEMBER')
        )
    ) AS has_application_ownership,
    (
        NOT pg_catalog.has_schema_privilege(runtime_role.oid, active_schema.oid, 'USAGE')
        OR EXISTS (
            SELECT 1
            FROM required_table_capability AS required
            WHERE pg_catalog.to_regclass(
                    pg_catalog.format('%I.%I', active_schema.nspname, required.table_name)
                  ) IS NULL
               OR NOT pg_catalog.has_table_privilege(
                    runtime_role.oid,
                    pg_catalog.to_regclass(
                        pg_catalog.format('%I.%I', active_schema.nspname, required.table_name)
                    ),
                    required.privilege
                  )
        )
        OR EXISTS (
            SELECT 1
            FROM required_sequence AS required
            WHERE pg_catalog.to_regclass(
                    pg_catalog.format('%I.%I', active_schema.nspname, required.sequence_name)
                  ) IS NULL
               OR NOT pg_catalog.has_sequence_privilege(
                    runtime_role.oid,
                    pg_catalog.to_regclass(
                        pg_catalog.format('%I.%I', active_schema.nspname, required.sequence_name)
                    ),
                    'USAGE'
                  )
        )
        OR NOT pg_catalog.has_function_privilege(
            runtime_role.oid,
            pg_catalog.to_regprocedure(
                pg_catalog.format(
                    '%I.managed_runtime_capacity_policy_digest(text,text,text,text,text,integer,text)',
                    active_schema.nspname
                )
            ),
            'EXECUTE'
        )
        OR NOT pg_catalog.has_function_privilege(
            runtime_role.oid,
            pg_catalog.to_regprocedure(
                pg_catalog.format('%I.provider_control_lock_runtime_lease_projection(text)', active_schema.nspname)
            ),
            'EXECUTE'
        )
        OR NOT pg_catalog.has_function_privilege(
            runtime_role.oid,
            pg_catalog.to_regprocedure(
                pg_catalog.format(
                    '%I.provider_control_count_unsettled_generation_dispatch_guards(text,uuid)',
                    active_schema.nspname
                )
            ),
            'EXECUTE'
        )
        OR NOT pg_catalog.has_function_privilege(
            runtime_role.oid,
            pg_catalog.to_regprocedure(
                pg_catalog.format('%I.provider_control_list_runnable_tenants(text,integer)', active_schema.nspname)
            ),
            'EXECUTE'
        )
		OR NOT pg_catalog.has_function_privilege(
			runtime_role.oid,
			pg_catalog.to_regprocedure(
				pg_catalog.format('%I.provider_control_list_due_decommission_wait_tenants(text,integer)', active_schema.nspname)
			),
			'EXECUTE'
		)
		OR NOT pg_catalog.has_function_privilege(
			runtime_role.oid,
			pg_catalog.to_regprocedure(
				pg_catalog.format('%I.provider_control_list_provider_provision_waits(text,text,integer)', active_schema.nspname)
			),
			'EXECUTE'
		)
        OR NOT pg_catalog.has_function_privilege(
            runtime_role.oid,
            pg_catalog.to_regprocedure(
                pg_catalog.format(
                    '%I.provider_control_list_stale_capacity_recovery_candidates(text,text,integer)',
                    active_schema.nspname
                )
            ),
            'EXECUTE'
        )
        OR NOT pg_catalog.has_function_privilege(
            runtime_role.oid,
            pg_catalog.to_regprocedure(
                pg_catalog.format('%I.provider_control_runtime_authority()', active_schema.nspname)
            ),
            'EXECUTE'
        )
    ) AS missing_required_runtime_capability,
    EXISTS (
        SELECT 1
        FROM pg_catalog.pg_class AS object
        JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = object.relnamespace
        CROSS JOIN LATERAL pg_catalog.aclexplode(
            COALESCE(object.relacl, pg_catalog.acldefault('r', object.relowner))
        ) AS acl
        WHERE namespace.nspname <> 'information_schema'
          AND namespace.nspname NOT LIKE 'pg_%'
          AND object.relkind IN ('r', 'p', 'v', 'm', 'f')
          AND acl.grantee IN (0, runtime_role.oid)
          AND NOT (
              namespace.oid = active_schema.oid
              AND acl.grantee = runtime_role.oid
              AND NOT acl.is_grantable
              AND EXISTS (
                  SELECT 1
                  FROM required_table_capability AS allowed
                  WHERE allowed.table_name = object.relname
                    AND allowed.privilege = acl.privilege_type
              )
          )
        UNION ALL
        SELECT 1
        FROM pg_catalog.pg_attribute AS attribute
        JOIN pg_catalog.pg_class AS object ON object.oid = attribute.attrelid
        JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = object.relnamespace
        CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) AS acl
        WHERE attribute.attnum > 0
          AND NOT attribute.attisdropped
          AND namespace.nspname <> 'information_schema'
          AND namespace.nspname NOT LIKE 'pg_%'
          AND acl.grantee IN (0, runtime_role.oid)
    ) AS has_excess_table_authority,
    EXISTS (
        SELECT 1
        FROM pg_catalog.pg_class AS object
        JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = object.relnamespace
        CROSS JOIN LATERAL pg_catalog.aclexplode(
            COALESCE(object.relacl, pg_catalog.acldefault('s', object.relowner))
        ) AS acl
        WHERE namespace.nspname <> 'information_schema'
          AND namespace.nspname NOT LIKE 'pg_%'
          AND object.relkind = 'S'
          AND acl.grantee IN (0, runtime_role.oid)
          AND NOT (
              namespace.oid = active_schema.oid
              AND acl.grantee = runtime_role.oid
              AND NOT acl.is_grantable
              AND acl.privilege_type = 'USAGE'
              AND EXISTS (
                  SELECT 1
                  FROM required_sequence AS allowed
                  WHERE allowed.sequence_name = object.relname
              )
          )
    ) AS has_excess_sequence_authority,
    EXISTS (
        SELECT 1
        FROM pg_catalog.pg_proc AS object
        JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = object.pronamespace
        CROSS JOIN LATERAL pg_catalog.aclexplode(
            COALESCE(object.proacl, pg_catalog.acldefault('f', object.proowner))
        ) AS acl
        WHERE namespace.nspname <> 'information_schema'
          AND namespace.nspname NOT LIKE 'pg_%'
          AND acl.grantee IN (0, runtime_role.oid)
          AND acl.privilege_type = 'EXECUTE'
          AND NOT EXISTS (
              SELECT 1
              FROM pg_catalog.pg_depend AS dependency
              WHERE dependency.classid = 'pg_catalog.pg_proc'::pg_catalog.regclass
                AND dependency.objid = object.oid
                AND dependency.deptype = 'e'
          )
          AND NOT (
              namespace.oid = active_schema.oid
              AND acl.grantee = runtime_role.oid
              AND NOT acl.is_grantable
              AND object.oid IN (
                   pg_catalog.to_regprocedure(
                       pg_catalog.format(
                           '%I.managed_runtime_capacity_policy_digest(text,text,text,text,text,integer,text)',
                           active_schema.nspname
                       )
                   ),
                   pg_catalog.to_regprocedure(
                       pg_catalog.format('%I.provider_control_lock_runtime_lease_projection(text)', active_schema.nspname)
                   ),
                   pg_catalog.to_regprocedure(
                       pg_catalog.format(
                           '%I.provider_control_count_unsettled_generation_dispatch_guards(text,uuid)',
                           active_schema.nspname
                       )
                   ),
                   pg_catalog.to_regprocedure(
                      pg_catalog.format('%I.provider_control_list_runnable_tenants(text,integer)', active_schema.nspname)
                   ),
				   pg_catalog.to_regprocedure(
					   pg_catalog.format('%I.provider_control_list_due_decommission_wait_tenants(text,integer)', active_schema.nspname)
				   ),
				   pg_catalog.to_regprocedure(
					   pg_catalog.format('%I.provider_control_list_provider_provision_waits(text,text,integer)', active_schema.nspname)
				   ),
                   pg_catalog.to_regprocedure(
                       pg_catalog.format(
                           '%I.provider_control_list_stale_capacity_recovery_candidates(text,text,integer)',
                           active_schema.nspname
                       )
                   ),
                   pg_catalog.to_regprocedure(
                       pg_catalog.format('%I.provider_control_runtime_authority()', active_schema.nspname)
                   )
              )
          )
    ) AS has_excess_function_authority,
    EXISTS (
        SELECT 1
        FROM required_tenant_rls AS required
        LEFT JOIN pg_catalog.pg_class AS object
          ON object.relnamespace = active_schema.oid
         AND object.relname = required.table_name
         AND object.relkind IN ('r', 'p')
        WHERE object.oid IS NULL
           OR NOT object.relrowsecurity
           OR NOT object.relforcerowsecurity
    ) AS missing_tenant_rls_fence,
    (
        (
			SELECT COUNT(*) <> 24
				OR COUNT(boundary_function.oid) <> 24
                OR COALESCE(pg_catalog.bool_or(
                    boundary_function.oid IS NOT NULL
                    AND (
                        NOT boundary_function.prosecdef
                        OR boundary_function.proowner <> runtime_authority_function.proowner
                        OR COALESCE(
                            boundary_function.proconfig @> ARRAY[
                                pg_catalog.format(
                                    'search_path=pg_catalog, %I, pg_temp',
                                    active_schema.nspname
                                )
                            ],
                            false
                        ) IS DISTINCT FROM true
                        OR EXISTS (
                            SELECT 1
                            FROM pg_catalog.aclexplode(
                                COALESCE(
                                    boundary_function.proacl,
                                    pg_catalog.acldefault('f', boundary_function.proowner)
                                )
                            ) AS acl
                            WHERE acl.grantee = 0
                              AND acl.privilege_type = 'EXECUTE'
                        )
                        OR pg_catalog.has_function_privilege(
                            runtime_role.oid,
                            boundary_function.oid,
                            'EXECUTE'
                        ) IS DISTINCT FROM boundary.runtime_executable
                    )
                ), true)
            FROM security_definer_boundary AS boundary
            LEFT JOIN pg_catalog.pg_proc AS boundary_function
              ON boundary_function.pronamespace = active_schema.oid
             AND boundary_function.proname = boundary.function_name
        )
        OR EXISTS (
            SELECT 1
            FROM pg_catalog.pg_proc AS unexpected_function
            WHERE unexpected_function.pronamespace = active_schema.oid
              AND unexpected_function.prosecdef
              AND pg_catalog.has_function_privilege(
                  runtime_role.oid,
                  unexpected_function.oid,
                  'EXECUTE'
              )
               AND unexpected_function.proname NOT IN (
                   'provider_control_lock_runtime_lease_projection',
                   'provider_control_count_unsettled_generation_dispatch_guards',
                   'provider_control_list_runnable_tenants',
				   'provider_control_list_due_decommission_wait_tenants',
				   'provider_control_list_provider_provision_waits',
                   'provider_control_list_stale_capacity_recovery_candidates',
                  'provider_control_runtime_authority'
              )
        )
    ) AS malformed_security_definer_boundary
FROM pg_catalog.pg_roles AS runtime_role
JOIN pg_catalog.pg_roles AS session_role ON session_role.rolname = session_user
JOIN pg_catalog.pg_namespace AS active_schema ON active_schema.nspname = current_schema()
CROSS JOIN LATERAL provider_control_runtime_authority() AS physical_authority
JOIN pg_catalog.pg_proc AS runtime_authority_function
  ON runtime_authority_function.oid = pg_catalog.to_regprocedure(
      pg_catalog.format('%I.provider_control_runtime_authority()', active_schema.nspname)
  )
WHERE runtime_role.rolname = current_user
`

const providerControlMigrationRoleIdentityQuery = `
SELECT
    migration_role.oid::bigint,
    migration_role.rolname,
    session_role.oid::bigint,
    session_role.rolname,
    physical_authority.system_identifier,
    physical_authority.database_oid,
    physical_authority.database_name,
    physical_authority.schema_oid,
    physical_authority.schema_name,
    runtime_authority_function.proowner::bigint
FROM pg_catalog.pg_roles AS migration_role
JOIN pg_catalog.pg_roles AS session_role ON session_role.rolname = session_user
JOIN pg_catalog.pg_namespace AS active_schema ON active_schema.nspname = current_schema()
CROSS JOIN LATERAL provider_control_runtime_authority() AS physical_authority
JOIN pg_catalog.pg_proc AS runtime_authority_function
  ON runtime_authority_function.oid = pg_catalog.to_regprocedure(
      pg_catalog.format('%I.provider_control_runtime_authority()', active_schema.nspname)
  )
WHERE migration_role.rolname = current_user
`

type providerControlDatabaseIdentity struct {
	roleOID           int64
	roleName          string
	sessionRoleOID    int64
	sessionRoleName   string
	systemIdentifier  string
	databaseOID       int64
	databaseName      string
	schemaOID         int64
	schemaName        string
	authorityOwnerOID int64
}

type providerControlRuntimeRolePosture struct {
	identity                      providerControlDatabaseIdentity
	superuser                     bool
	bypassRLS                     bool
	createDatabase                bool
	createRole                    bool
	replication                   bool
	setRoleMembership             bool
	untrustedObjectCreation       bool
	hasApplicationOwnership       bool
	missingRequiredCapability     bool
	hasExcessTableAuthority       bool
	hasExcessSequenceAuthority    bool
	hasExcessFunctionAuthority    bool
	missingTenantRLSFence         bool
	malformedSecurityDefinerFence bool
}

// openProviderControlRuntimeDatabase opens the dedicated provider-control
// runtime pool without applying migrations. The caller retains the migrated
// control-plane pool so this function can reject mismatched physical clusters,
// database/schema namespaces, role ownership, or runtime privileges. It never
// falls back to the migration DSN.
func openProviderControlRuntimeDatabase(ctx context.Context, migrationDatabase *sql.DB) (*db.DB, error) {
	if migrationDatabase == nil {
		return nil, fmt.Errorf("open provider-control runtime database: migration database is required")
	}
	cfg, err := providerControlRuntimeDatabaseConfigFromEnv()
	if err != nil {
		return nil, err
	}
	opened, err := db.Open(cfg)
	if err != nil {
		return nil, fmt.Errorf("open provider-control runtime database: %w", err)
	}
	if err := verifyProviderControlRuntimeRolePosture(ctx, opened.DB, migrationDatabase); err != nil {
		_ = opened.Close()
		return nil, err
	}
	return opened, nil
}

func providerControlRuntimeDatabaseConfigFromEnv() (db.Config, error) {
	dsn := strings.TrimSpace(os.Getenv(providerControlRuntimeDatabaseURLEnv))
	if dsn == "" {
		return db.Config{}, fmt.Errorf("%w: set %s", errProviderControlRuntimeDatabaseNotConfigured, providerControlRuntimeDatabaseURLEnv)
	}
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		return db.Config{}, fmt.Errorf("provider-control runtime database configuration is invalid: %w", err)
	}
	if parsed.User != providerControlRuntimeRoleName {
		return db.Config{}, fmt.Errorf("provider-control runtime database user must be %q", providerControlRuntimeRoleName)
	}
	for parameter := range parsed.RuntimeParams {
		switch strings.ToLower(strings.TrimSpace(parameter)) {
		case "role", "session_authorization", "options":
			return db.Config{}, fmt.Errorf("provider-control runtime database configuration must not override role or session authority")
		}
	}
	return db.Config{
		Backend:         db.StoreBackendPostgres,
		DSN:             dsn,
		DriverName:      db.PostgresDriverName,
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: 30 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
	}, nil
}

func verifyProviderControlRuntimeRolePosture(ctx context.Context, runtimeDatabase, migrationDatabase *sql.DB) error {
	if runtimeDatabase == nil || migrationDatabase == nil {
		return fmt.Errorf("verify provider-control runtime role posture: both database pools are required")
	}

	posture, err := loadProviderControlRuntimeRolePosture(ctx, runtimeDatabase)
	if err != nil {
		return fmt.Errorf("verify provider-control runtime role posture: %w", err)
	}
	migrationIdentity, err := loadProviderControlMigrationRoleIdentity(ctx, migrationDatabase)
	if err != nil {
		return fmt.Errorf("verify provider-control migration role identity: %w", err)
	}
	if posture.identity.roleName != providerControlRuntimeRoleName {
		return fmt.Errorf("verify provider-control runtime role posture: runtime role must be %q", providerControlRuntimeRoleName)
	}
	if posture.identity.sessionRoleOID != posture.identity.roleOID ||
		posture.identity.sessionRoleName != posture.identity.roleName {
		return fmt.Errorf("verify provider-control runtime role posture: authenticated session role must equal the canonical runtime role")
	}
	if migrationIdentity.sessionRoleOID != migrationIdentity.roleOID ||
		migrationIdentity.sessionRoleName != migrationIdentity.roleName {
		return fmt.Errorf("verify provider-control migration role identity: authenticated session role must equal the migration role")
	}
	if posture.identity.systemIdentifier == "" || posture.identity.systemIdentifier != migrationIdentity.systemIdentifier {
		return fmt.Errorf("verify provider-control runtime role posture: runtime and migration pools must address the same physical PostgreSQL cluster")
	}
	if posture.identity.databaseOID != migrationIdentity.databaseOID || posture.identity.databaseName != migrationIdentity.databaseName {
		return fmt.Errorf("verify provider-control runtime role posture: runtime and migration pools must address the same database")
	}
	if posture.identity.schemaOID != migrationIdentity.schemaOID || posture.identity.schemaName != migrationIdentity.schemaName {
		return fmt.Errorf("verify provider-control runtime role posture: runtime and migration pools must use the same trusted current schema")
	}
	// Extend the same-cluster binding with the operator-pinned expected
	// identity: both pools have just proved they address one physical
	// database, so anchoring the migration identity to the
	// TECHSTACK_EXPECTED_DATABASE_IDENTITY pin transitively anchors the
	// runtime pool as well.
	if pin, pinned, pinErr := db.ExpectedDatabaseIdentityFromEnv(); pinErr != nil {
		return fmt.Errorf("verify provider-control runtime role posture: %w", pinErr)
	} else if pinned {
		if err := pin.Verify(migrationIdentity.databaseName, migrationIdentity.systemIdentifier); err != nil {
			return fmt.Errorf("verify provider-control database identity: %w", err)
		}
	}
	if posture.identity.roleOID == migrationIdentity.roleOID {
		return fmt.Errorf("verify provider-control runtime role posture: runtime and migration role OIDs must differ")
	}
	if posture.identity.authorityOwnerOID != migrationIdentity.roleOID || migrationIdentity.authorityOwnerOID != migrationIdentity.roleOID {
		return fmt.Errorf("verify provider-control runtime role posture: bounded authority functions must be owned by the migration role")
	}
	if posture.superuser || posture.bypassRLS || posture.createDatabase || posture.createRole || posture.replication || posture.setRoleMembership {
		return fmt.Errorf("verify provider-control runtime role posture: runtime role has forbidden cluster privileges or SET ROLE membership")
	}
	if posture.untrustedObjectCreation {
		return fmt.Errorf("verify provider-control runtime role posture: runtime role has database TEMP or application schema CREATE authority")
	}
	if posture.hasApplicationOwnership {
		return fmt.Errorf("verify provider-control runtime role posture: runtime role has application object ownership authority")
	}
	if posture.missingRequiredCapability {
		return fmt.Errorf("verify provider-control runtime role posture: runtime role is missing an exact provider-control runtime capability")
	}
	if posture.hasExcessTableAuthority || posture.hasExcessSequenceAuthority || posture.hasExcessFunctionAuthority {
		return fmt.Errorf("verify provider-control runtime role posture: runtime role has authority outside the exact table, sequence, or function allowlist")
	}
	if posture.missingTenantRLSFence {
		return fmt.Errorf("verify provider-control runtime role posture: a required tenant table is missing FORCE RLS")
	}
	if posture.malformedSecurityDefinerFence {
		return fmt.Errorf("verify provider-control runtime role posture: bounded SECURITY DEFINER authority is missing, overgranted, or has an unsafe owner/search path")
	}
	return nil
}

func loadProviderControlRuntimeRolePosture(ctx context.Context, database *sql.DB) (providerControlRuntimeRolePosture, error) {
	var posture providerControlRuntimeRolePosture
	err := database.QueryRowContext(ctx, providerControlRuntimeRolePostureQuery).Scan(
		&posture.identity.roleOID,
		&posture.identity.roleName,
		&posture.identity.sessionRoleOID,
		&posture.identity.sessionRoleName,
		&posture.identity.systemIdentifier,
		&posture.identity.databaseOID,
		&posture.identity.databaseName,
		&posture.identity.schemaOID,
		&posture.identity.schemaName,
		&posture.identity.authorityOwnerOID,
		&posture.superuser,
		&posture.bypassRLS,
		&posture.createDatabase,
		&posture.createRole,
		&posture.replication,
		&posture.setRoleMembership,
		&posture.untrustedObjectCreation,
		&posture.hasApplicationOwnership,
		&posture.missingRequiredCapability,
		&posture.hasExcessTableAuthority,
		&posture.hasExcessSequenceAuthority,
		&posture.hasExcessFunctionAuthority,
		&posture.missingTenantRLSFence,
		&posture.malformedSecurityDefinerFence,
	)
	if err != nil {
		return providerControlRuntimeRolePosture{}, err
	}
	return posture, nil
}

func loadProviderControlMigrationRoleIdentity(ctx context.Context, database *sql.DB) (providerControlDatabaseIdentity, error) {
	var identity providerControlDatabaseIdentity
	err := database.QueryRowContext(ctx, providerControlMigrationRoleIdentityQuery).Scan(
		&identity.roleOID,
		&identity.roleName,
		&identity.sessionRoleOID,
		&identity.sessionRoleName,
		&identity.systemIdentifier,
		&identity.databaseOID,
		&identity.databaseName,
		&identity.schemaOID,
		&identity.schemaName,
		&identity.authorityOwnerOID,
	)
	if err != nil {
		return providerControlDatabaseIdentity{}, err
	}
	return identity, nil
}
