package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// ForwardPosture is optional diagnostic metadata, never permission to apply or
// replay a migration. Unavailable diagnostics retain only a fixed phase/SQLSTATE.
type ForwardPosture struct {
	Schema        string                 `json:"schema"`
	Status        string                 `json:"status"`
	Phase         string                 `json:"phase"`
	SQLState      *string                `json:"sqlstate"`
	AppliedPrefix int                    `json:"applied_prefix"`
	Catalog       *forwardPostureCatalog `json:"catalog"`
}

type forwardPostureIdentity struct {
	SystemIdentifier string `json:"system_identifier"`
	Database         string `json:"database"`
	CurrentUser      string `json:"current_user"`
	SessionUser      string `json:"session_user"`
	ServerVersion    int    `json:"server_version_num"`
	ReadOnly         bool   `json:"read_only"`
}

type forwardPostureRole struct {
	Name                   string `json:"name"`
	Exists                 bool   `json:"exists"`
	Login                  *bool  `json:"login"`
	Superuser              *bool  `json:"superuser"`
	BypassRLS              *bool  `json:"bypassrls"`
	CreateRole             *bool  `json:"createrole"`
	CreateDB               *bool  `json:"createdb"`
	Inherit                *bool  `json:"inherit"`
	Replication            *bool  `json:"replication"`
	MembershipCount        *int64 `json:"membership_count"`
	AdminMembershipCount   *int64 `json:"admin_membership_count"`
	InheritMembershipCount *int64 `json:"inherit_membership_count"`
	SetMembershipCount     *int64 `json:"set_membership_count"`
}

type forwardPostureSchema struct {
	Name            string `json:"name"`
	Exists          bool   `json:"exists"`
	OwnerIsMigrator *bool  `json:"owner_is_migrator"`
	OwnerInherited  *bool  `json:"owner_inherited"`
	Usage           *bool  `json:"usage"`
	Create          *bool  `json:"create"`
	UsageGrant      *bool  `json:"usage_grant"`
	CreateGrant     *bool  `json:"create_grant"`
}

type forwardPostureRelation struct {
	Name            string  `json:"name"`
	Exists          bool    `json:"exists"`
	Kind            *string `json:"kind"`
	OwnerIsMigrator *bool   `json:"owner_is_migrator"`
	OwnerInherited  *bool   `json:"owner_inherited"`
	RLS             *bool   `json:"rls"`
	Forced          *bool   `json:"forced"`
	Select          *bool   `json:"select"`
	Insert          *bool   `json:"insert"`
	Update          *bool   `json:"update"`
	Delete          *bool   `json:"delete"`
	Truncate        *bool   `json:"truncate"`
	Maintain        *bool   `json:"maintain"`
	SelectGrant     *bool   `json:"select_grant"`
	InsertGrant     *bool   `json:"insert_grant"`
	UpdateGrant     *bool   `json:"update_grant"`
	DeleteGrant     *bool   `json:"delete_grant"`
	PolicyCount     *int64  `json:"policy_count"`
}

type forwardPostureRoutine struct {
	Name               string  `json:"name"`
	Exists             bool    `json:"exists"`
	Kind               *string `json:"kind"`
	OwnerIsMigrator    *bool   `json:"owner_is_migrator"`
	OwnerInherited     *bool   `json:"owner_inherited"`
	SecurityDefiner    *bool   `json:"security_definer"`
	Execute            *bool   `json:"execute"`
	ExecuteGrant       *bool   `json:"execute_grant"`
	ApplicationExecute *bool   `json:"application_execute"`
	BackupExecute      *bool   `json:"backup_execute"`
}

type forwardPostureCatalog struct {
	Identity  forwardPostureIdentity   `json:"identity"`
	Roles     []forwardPostureRole     `json:"roles"`
	Schemas   []forwardPostureSchema   `json:"schemas"`
	Relations []forwardPostureRelation `json:"relations"`
	Routines  []forwardPostureRoutine  `json:"routines"`
}

// InspectStageForwardPosture reads only fixed catalog targets on the existing
// migration connection. No relation payload, key row, password or SQL message is
// selected. The forward ledger is requalified in this same read-only snapshot.
func (db *DB) InspectStageForwardPosture(ctx context.Context, batch []ForwardMigrationArtifact, expected ForwardBatchState) (ForwardPosture, error) {
	proof := ForwardPosture{Schema: "techstack.forward-inspect-posture/v1", Status: "unavailable", Phase: "begin", AppliedPrefix: expected.AppliedPrefix}
	if err := requireForwardPostureContext(); err != nil {
		return proof, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return proof.unavailable(err)
	}
	defer tx.Rollback()
	if _, setupErr := tx.ExecContext(ctx, "SET LOCAL search_path=pg_catalog,public; SET LOCAL statement_timeout='5000'; SET LOCAL lock_timeout='2000'"); setupErr != nil {
		return proof.unavailable(setupErr)
	}
	proof.Phase = "identity"
	var identity forwardPostureIdentity
	err = tx.QueryRowContext(ctx, `SELECT system_identifier::text,current_database(),current_user,session_user,
		current_setting('server_version_num')::int,current_setting('transaction_read_only')::boolean
		FROM pg_catalog.pg_control_system()`).Scan(&identity.SystemIdentifier, &identity.Database, &identity.CurrentUser, &identity.SessionUser, &identity.ServerVersion, &identity.ReadOnly)
	if err != nil {
		return proof.unavailable(err)
	}
	if !identity.fixedStage() {
		return proof, errors.New("forward posture identity differs")
	}
	proof.Phase = "ledger"
	prefix, err := forwardBatchPrefix(ctx, tx, batch)
	if errors.Is(err, ErrForwardBatchLedger) || err == nil && prefix != expected.AppliedPrefix {
		return proof, ErrForwardBatchLedger
	}
	if err != nil {
		return proof.unavailable(err)
	}
	proof.Phase = "catalog"
	var raw []byte
	if catalogErr := tx.QueryRowContext(ctx, forwardPostureSQL).Scan(&raw); catalogErr != nil {
		return proof.unavailable(catalogErr)
	}
	var catalog forwardPostureCatalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&catalog); decodeErr != nil {
		return proof.unavailable(decodeErr)
	}
	catalog.Identity = identity
	proof.Phase = "rollback"
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		return proof.unavailable(rollbackErr)
	}
	proof.Status, proof.Phase, proof.Catalog = "observed", "catalog", &catalog
	return proof, nil
}

func requireForwardPostureContext() error {
	pin, pinned, err := ExpectedDatabaseIdentityFromEnv()
	if err != nil || !pinned || pin.DatabaseName != "kombify_runtime" || pin.SystemIdentifier != "7699436836349883199" ||
		os.Getenv("TECHSTACK_DATABASE_CONTEXT") != "stage-development" || os.Getenv(EnvDatabaseMigrationMode) != "verify" || os.Getenv(EnvAllowDBGenesis) != "false" {
		return errors.New("forward posture requires the fixed read-only Stage context")
	}
	return nil
}

func (identity forwardPostureIdentity) fixedStage() bool {
	return identity.SystemIdentifier == "7699436836349883199" && identity.Database == "kombify_runtime" &&
		identity.CurrentUser == "kombify_runtime_user" && identity.SessionUser == "kombify_runtime_user" &&
		identity.ServerVersion >= 180000 && identity.ServerVersion < 190000 && identity.ReadOnly
}

func (proof ForwardPosture) unavailable(cause error) (ForwardPosture, error) {
	proof.Catalog = nil
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) && len(pgErr.Code) == 5 {
		valid := true
		for _, c := range pgErr.Code {
			valid = valid && (c >= '0' && c <= '9' || c >= 'A' && c <= 'Z')
		}
		if valid {
			code := pgErr.Code
			proof.SQLState = &code
		}
	}
	return proof, nil
}

const forwardPostureSQL = `WITH
role_targets(ord,name) AS (VALUES (1,'kombify_runtime_user'),(2,'techstack_application'),
 (3,'techstack_backup_worker'),(4,'techstack_household_worker'),(5,'techstack_advanced_issuer')),
schema_targets(ord,name) AS (VALUES (1,'public'),(2,'kombify_ctx')),
relation_targets(ord,name) AS (VALUES (1,'public.schema_migrations'),(2,'public.user_onboarding_state'),
 (3,'public.feature_preferences'),(4,'public.feature_consents'),(5,'public.operator_self_disclosures'),
 (6,'public.jobs'),(7,'public.typed_agent_commands'),(8,'public.household_identity_refs'),
 (9,'public.techstack_memberships'),(10,'public.techstack_tenants'),(11,'public.homelabs'),
 (12,'public.stacks'),(13,'public.workers'),(14,'public.servers'),(15,'kombify_ctx.role_class'),
 (16,'kombify_ctx.keys'),(17,'public.household_projection_bindings'),(18,'public.household_projection_attempts')),
routine_targets(ord,name) AS (VALUES (1,'pg_catalog.pg_control_system()'),(2,'kombify_ctx.verified_context()'),
 (3,'kombify_ctx.subject()'),(4,'kombify_ctx.org_id()'),(5,'public.techstack_backup_execution_conflicts(text,text)'),
 (6,'public.household_reference_immutable()'),(7,'public.household_projection_binding_monotonic()'))
SELECT json_build_object(
 'roles',(SELECT json_agg(json_build_object('name',t.name,'exists',r.oid IS NOT NULL,
   'login',r.rolcanlogin,'superuser',r.rolsuper,'bypassrls',r.rolbypassrls,'createrole',r.rolcreaterole,
   'createdb',r.rolcreatedb,'inherit',r.rolinherit,'replication',r.rolreplication,
   'membership_count',CASE WHEN r.oid IS NOT NULL THEN (SELECT count(*) FROM pg_auth_members m WHERE m.roleid=r.oid OR m.member=r.oid) END,
   'admin_membership_count',CASE WHEN r.oid IS NOT NULL THEN (SELECT count(*) FROM pg_auth_members m WHERE (m.roleid=r.oid OR m.member=r.oid) AND m.admin_option) END,
   'inherit_membership_count',CASE WHEN r.oid IS NOT NULL THEN (SELECT count(*) FROM pg_auth_members m WHERE (m.roleid=r.oid OR m.member=r.oid) AND m.inherit_option) END,
   'set_membership_count',CASE WHEN r.oid IS NOT NULL THEN (SELECT count(*) FROM pg_auth_members m WHERE (m.roleid=r.oid OR m.member=r.oid) AND m.set_option) END)
   ORDER BY t.ord) FROM role_targets t LEFT JOIN pg_roles r ON r.rolname=t.name),
 'schemas',(SELECT json_agg(json_build_object('name',t.name,'exists',n.oid IS NOT NULL,
   'owner_is_migrator',n.nspowner=current_user::regrole,'owner_inherited',pg_has_role(current_user,n.nspowner,'USAGE'),
   'usage',has_schema_privilege(current_user,n.oid,'USAGE'),'create',has_schema_privilege(current_user,n.oid,'CREATE'),
   'usage_grant',has_schema_privilege(current_user,n.oid,'USAGE WITH GRANT OPTION'),
   'create_grant',has_schema_privilege(current_user,n.oid,'CREATE WITH GRANT OPTION')) ORDER BY t.ord)
   FROM schema_targets t LEFT JOIN pg_namespace n ON n.nspname=t.name),
 'relations',(SELECT json_agg(json_build_object('name',t.name,'exists',c.oid IS NOT NULL,'kind',c.relkind,
   'owner_is_migrator',c.relowner=current_user::regrole,'owner_inherited',pg_has_role(current_user,c.relowner,'USAGE'),
   'rls',c.relrowsecurity,'forced',c.relforcerowsecurity,
   'select',has_table_privilege(current_user,c.oid,'SELECT'),'insert',has_table_privilege(current_user,c.oid,'INSERT'),
   'update',has_table_privilege(current_user,c.oid,'UPDATE'),'delete',has_table_privilege(current_user,c.oid,'DELETE'),
   'truncate',has_table_privilege(current_user,c.oid,'TRUNCATE'),'maintain',has_table_privilege(current_user,c.oid,'MAINTAIN'),
   'select_grant',has_table_privilege(current_user,c.oid,'SELECT WITH GRANT OPTION'),
   'insert_grant',has_table_privilege(current_user,c.oid,'INSERT WITH GRANT OPTION'),
   'update_grant',has_table_privilege(current_user,c.oid,'UPDATE WITH GRANT OPTION'),
   'delete_grant',has_table_privilege(current_user,c.oid,'DELETE WITH GRANT OPTION'),
   'policy_count',CASE WHEN c.oid IS NOT NULL THEN (SELECT count(*) FROM pg_policy p WHERE p.polrelid=c.oid) END)
   ORDER BY t.ord) FROM relation_targets t LEFT JOIN pg_class c ON c.oid=to_regclass(t.name)),
 'routines',(SELECT json_agg(json_build_object('name',t.name,'exists',p.oid IS NOT NULL,'kind',p.prokind,
   'owner_is_migrator',p.proowner=current_user::regrole,'owner_inherited',pg_has_role(current_user,p.proowner,'USAGE'),
   'security_definer',p.prosecdef,'execute',has_function_privilege(current_user,p.oid,'EXECUTE'),
   'execute_grant',has_function_privilege(current_user,p.oid,'EXECUTE WITH GRANT OPTION'),
   'application_execute',has_function_privilege(to_regrole('techstack_application'),p.oid,'EXECUTE'),
   'backup_execute',has_function_privilege(to_regrole('techstack_backup_worker'),p.oid,'EXECUTE')) ORDER BY t.ord)
   FROM routine_targets t LEFT JOIN pg_proc p ON p.oid=to_regprocedure(t.name)))`
