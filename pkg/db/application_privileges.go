package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type PrivilegeQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type ApplicationPolicy struct {
	Tables     map[int64]map[string]bool
	TableNames map[int64]string
	Sequences  map[int64]string
	Functions  map[int64]string
}

// Principal names are explicit purpose inputs, never a legacy DSN alias.
type ApplicationPrincipal struct{ Role, AuthorityOwner string }

func (p ApplicationPrincipal) validate() error {
	if strings.TrimSpace(p.Role) == "" || strings.TrimSpace(p.AuthorityOwner) == "" || p.Role == p.AuthorityOwner {
		return fmt.Errorf("distinct explicit application and authority principals required")
	}
	return nil
}

var applicationCapabilityFunctions = []string{
	"techstack_lookup_subject_directory(text)", "techstack_forget_subject_directory(text,text)",
	"techstack_list_job_execution_reclaim_tenants(text,timestamp with time zone,integer)", "techstack_compact_job_execution_reclaim_tenant(text)",
	"techstack_list_pending_job_tenants(text,integer)", "techstack_compact_pending_job_tenant(text)",
	"techstack_list_drift_schedule_tenants(text,integer)", "techstack_compact_drift_schedule_tenant(text)",
	"techstack_list_ril_workflow_tenants(text,integer)", "provider_control_runtime_authority()", "provider_incident_list_tenant_ids()",
	"techstack_list_due_guard_reach(timestamp with time zone,text,text,integer)", "techstack_find_guard_reach_by_connect(text,text)",
	"techstack_list_due_backup_schedules(timestamp with time zone,text,text,integer)", "techstack_list_due_backup_wipes(timestamp with time zone,text,text,integer)",
}

// This is the single application capability selection used both by installation
// and effective runtime verification. Unlisted schemas, objects and privileges
// remain denied; an ACL left over from an earlier run is not an authority.
func LoadApplicationPolicy(ctx context.Context, q PrivilegeQuerier, requireFunctions bool) (ApplicationPolicy, error) {
	return loadApplicationPolicy(ctx, q, requireFunctions)
}

func loadApplicationPolicy(ctx context.Context, q PrivilegeQuerier, requireFunctions bool) (ApplicationPolicy, error) {
	p := ApplicationPolicy{Tables: map[int64]map[string]bool{}, TableNames: map[int64]string{}, Sequences: map[int64]string{}, Functions: map[int64]string{}}
	rows, err := q.QueryContext(ctx, `SELECT c.oid::bigint,c.relname,c.relrowsecurity AND c.relforcerowsecurity,
	 EXISTS(SELECT 1 FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attname='tenant_id' AND a.attnum>0 AND NOT a.attisdropped)
	 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p')`)
	if err != nil {
		return p, err
	}
	provider := map[string]string{}
	for _, capability := range ProviderRuntimeTableGrants {
		provider[capability.Table] = capability.Privileges
	}
	// Non-RLS loop tables from migration 161. The same privileges are the exact
	// capability: preparation must not treat that grant as excess, and a role
	// created after the migration still receives them here. Registry directory
	// inserts stay on the SECURITY DEFINER triggers.
	global := map[string]string{
		"schema_migrations": "SELECT", "techstack_users": "SELECT,INSERT,UPDATE", "techstack_tenants": "SELECT,INSERT,UPDATE",
		"identity_providers": "SELECT", "feature_rollouts": "SELECT", "provider_catalog_versions": "SELECT",
		"provider_catalog_profiles": "SELECT", "guard_reach_cursors": "SELECT,INSERT,UPDATE",
		"techstack_notification_outbox": "SELECT,INSERT,UPDATE", "ril_signal_delivery_tenants": "SELECT,INSERT,UPDATE,DELETE",
		"server_maintenance_tenants": "SELECT,INSERT,UPDATE,DELETE", "guard_drive_health_tenants": "SELECT,INSERT,UPDATE",
		"server_registry_sweep_tenants": "SELECT,UPDATE,DELETE", "server_registry_outbox_prune_tenants": "SELECT,UPDATE,DELETE",
	}
	for rows.Next() {
		var oid int64
		var name string
		var rls, tenant bool
		if err := rows.Scan(&oid, &name, &rls, &tenant); err != nil {
			rows.Close()
			return p, err
		}
		privileges := global[name]
		if privileges == "" && rls && tenant {
			if strings.HasPrefix(name, "provider_") {
				privileges = provider[name]
			} else {
				privileges = "SELECT,INSERT,UPDATE,DELETE"
			}
		}
		if privileges == "" {
			continue
		}
		allowed := map[string]bool{}
		for _, privilege := range strings.Split(privileges, ",") {
			allowed[privilege] = true
		}
		p.Tables[oid] = allowed
		p.TableNames[oid] = name
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return p, err
	}
	rows.Close()
	for oid, privileges := range p.Tables {
		if !privileges["INSERT"] {
			continue
		}
		sequences, err := q.QueryContext(ctx, `SELECT DISTINCT s.oid::bigint,s.relname FROM pg_attribute a
		 CROSS JOIN LATERAL (SELECT to_regclass(pg_get_serial_sequence(format('public.%I',$1::text),a.attname)) AS oid) owned
		 JOIN pg_class s ON s.oid=owned.oid WHERE a.attrelid=$2 AND a.attnum>0 AND NOT a.attisdropped`, p.TableNames[oid], oid)
		if err != nil {
			return p, err
		}
		for sequences.Next() {
			var sequenceOID int64
			var name string
			if err := sequences.Scan(&sequenceOID, &name); err != nil {
				sequences.Close()
				return p, err
			}
			p.Sequences[sequenceOID] = name
		}
		if err := sequences.Err(); err != nil {
			sequences.Close()
			return p, err
		}
		sequences.Close()
	}
	for _, signature := range applicationCapabilityFunctions {
		var oid sql.NullInt64
		if err := q.QueryRowContext(ctx, `SELECT to_regprocedure($1)::oid::bigint`, "public."+signature).Scan(&oid); err != nil {
			return p, err
		}
		if !oid.Valid {
			if requireFunctions {
				return p, fmt.Errorf("assigned application authority is absent: %s", signature)
			}
			continue
		}
		p.Functions[oid.Int64] = signature
	}
	return p, nil
}

// VerifyApplicationPrivileges inspects effective privileges, including PUBLIC,
// column ACLs and grant options, across every application schema.
func VerifyApplicationPrivileges(ctx context.Context, q PrivilegeQuerier, principal ApplicationPrincipal, requireCapabilities bool) error {
	return verifyApplicationPrivileges(ctx, q, principal, requireCapabilities)
}

func verifyApplicationPrivileges(ctx context.Context, q PrivilegeQuerier, principal ApplicationPrincipal, requireCapabilities bool) error {
	if err := principal.validate(); err != nil {
		return err
	}
	if err := VerifyApplicationPrincipalPosture(ctx, q, principal); err != nil {
		return err
	}
	p, err := loadApplicationPolicy(ctx, q, requireCapabilities)
	if err != nil {
		return err
	}
	var unsafe bool
	if err := q.QueryRowContext(ctx, `SELECT has_database_privilege($1,current_database(),'CREATE') OR has_database_privilege($1,current_database(),'TEMP')
	 OR has_database_privilege($1,current_database(),'CONNECT WITH GRANT OPTION')
	 OR EXISTS(SELECT 1 FROM pg_namespace n WHERE n.nspname<>'information_schema' AND n.nspname NOT LIKE 'pg_%'
	 AND (has_schema_privilege($1,n.oid,'CREATE') OR has_schema_privilege($1,n.oid,'USAGE WITH GRANT OPTION')
	 OR (n.nspname<>'public' AND has_schema_privilege($1,n.oid,'USAGE'))))`, principal.Role).Scan(&unsafe); err != nil {
		return err
	}
	if !unsafe {
		unsafe, err = applicationHasOtherDatabaseAuthority(ctx, q, principal.Role)
		if err != nil {
			return err
		}
	}
	if unsafe {
		return fmt.Errorf("managed test app has excess database/schema authority")
	}
	if requireCapabilities {
		var connected bool
		if err := q.QueryRowContext(ctx, `SELECT has_database_privilege($1,current_database(),'CONNECT') AND has_schema_privilege($1,'public','USAGE')`, principal.Role).Scan(&connected); err != nil {
			return err
		}
		if !connected {
			return fmt.Errorf("managed test app lacks assigned database/schema access")
		}
	}
	tableRows, err := q.QueryContext(ctx, `SELECT c.oid::bigint,format('%I.%I',n.nspname,c.relname),v.privilege,has_table_privilege($1,c.oid,v.privilege),has_table_privilege($1,c.oid,v.privilege||' WITH GRANT OPTION')
	 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN (VALUES('SELECT'),('INSERT'),('UPDATE'),('DELETE'),('TRUNCATE'),('REFERENCES'),('TRIGGER'),('MAINTAIN')) v(privilege)
	 WHERE n.nspname<>'information_schema' AND n.nspname NOT LIKE 'pg_%' AND c.relkind IN ('r','p','v','m','f')`, principal.Role)
	if err != nil {
		return err
	}
	for tableRows.Next() {
		var oid int64
		var name, privilege string
		var actual, grant bool
		if err := tableRows.Scan(&oid, &name, &privilege, &actual, &grant); err != nil {
			tableRows.Close()
			return err
		}
		expected := p.Tables[oid][privilege]
		if grant || (actual && !expected) || (requireCapabilities && expected && !actual) {
			tableRows.Close()
			return fmt.Errorf("managed test app table authority differs from its exact capability: %s %s %s", name, privilege, authorityDifference(grant, actual, expected))
		}
	}
	if err := tableRows.Err(); err != nil {
		tableRows.Close()
		return err
	}
	tableRows.Close()
	columnRows, err := q.QueryContext(ctx, `SELECT c.oid::bigint,format('%I.%I.%I',n.nspname,c.relname,a.attname),v.privilege,has_column_privilege($1,c.oid,a.attnum,v.privilege),has_column_privilege($1,c.oid,a.attnum,v.privilege||' WITH GRANT OPTION')
	 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
	 CROSS JOIN (VALUES('SELECT'),('INSERT'),('UPDATE'),('REFERENCES')) v(privilege)
	 WHERE n.nspname<>'information_schema' AND n.nspname NOT LIKE 'pg_%' AND c.relkind IN ('r','p','v','m','f')`, principal.Role)
	if err != nil {
		return err
	}
	for columnRows.Next() {
		var oid int64
		var name, privilege string
		var actual, grant bool
		if err := columnRows.Scan(&oid, &name, &privilege, &actual, &grant); err != nil {
			columnRows.Close()
			return err
		}
		if grant || (actual && !p.Tables[oid][privilege]) {
			columnRows.Close()
			return fmt.Errorf("managed test app has excess column authority: %s %s %s", name, privilege, authorityDifference(grant, actual, p.Tables[oid][privilege]))
		}
	}
	if err := columnRows.Err(); err != nil {
		columnRows.Close()
		return err
	}
	columnRows.Close()
	sequenceRows, err := q.QueryContext(ctx, `SELECT c.oid::bigint,format('%I.%I',n.nspname,c.relname),v.privilege,has_sequence_privilege($1,c.oid,v.privilege),has_sequence_privilege($1,c.oid,v.privilege||' WITH GRANT OPTION')
	 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN (VALUES('USAGE'),('SELECT'),('UPDATE')) v(privilege)
	 WHERE n.nspname<>'information_schema' AND n.nspname NOT LIKE 'pg_%' AND c.relkind='S'`, principal.Role)
	if err != nil {
		return err
	}
	for sequenceRows.Next() {
		var oid int64
		var name, privilege string
		var actual, grant bool
		if err := sequenceRows.Scan(&oid, &name, &privilege, &actual, &grant); err != nil {
			sequenceRows.Close()
			return err
		}
		_, allowed := p.Sequences[oid]
		expected := allowed && privilege == "USAGE"
		if grant || (actual && !expected) || (requireCapabilities && expected && !actual) {
			sequenceRows.Close()
			return fmt.Errorf("managed test app sequence authority differs from its exact capability: %s %s %s", name, privilege, authorityDifference(grant, actual, expected))
		}
	}
	if err := sequenceRows.Err(); err != nil {
		sequenceRows.Close()
		return err
	}
	sequenceRows.Close()
	functionRows, err := q.QueryContext(ctx, `SELECT f.oid::bigint,f.oid::regprocedure::text,has_function_privilege($1,f.oid,'EXECUTE'),has_function_privilege($1,f.oid,'EXECUTE WITH GRANT OPTION')
	 FROM pg_proc f JOIN pg_namespace n ON n.oid=f.pronamespace WHERE n.nspname<>'information_schema' AND n.nspname NOT LIKE 'pg_%'`, principal.Role)
	if err != nil {
		return err
	}
	for functionRows.Next() {
		var oid int64
		var name string
		var actual, grant bool
		if err := functionRows.Scan(&oid, &name, &actual, &grant); err != nil {
			functionRows.Close()
			return err
		}
		_, expected := p.Functions[oid]
		if grant || (actual && !expected) || (requireCapabilities && expected && !actual) {
			functionRows.Close()
			return fmt.Errorf("managed test app routine authority differs from its exact capability: %s EXECUTE %s", name, authorityDifference(grant, actual, expected))
		}
	}
	if err := functionRows.Err(); err != nil {
		functionRows.Close()
		return err
	}
	functionRows.Close()
	for oid := range p.Functions {
		var safe bool
		// Migration authority may provision roles; it never becomes an app login.
		// Definers remain exact, non-super/non-bypass, with fixed search path.
		if err := q.QueryRowContext(ctx, `SELECT f.prosecdef AND owner.rolname=$2
		 AND NOT(owner.rolsuper OR owner.rolbypassrls OR owner.rolreplication)
		 AND EXISTS(SELECT 1 FROM unnest(f.proconfig) configuration
		 WHERE replace(configuration,' ','')='search_path=pg_catalog,public,pg_temp')
		 FROM pg_proc f JOIN pg_roles owner ON owner.oid=f.proowner WHERE f.oid=$1`, oid, principal.AuthorityOwner).Scan(&safe); err != nil {
			return err
		}
		if !safe {
			return fmt.Errorf("managed test application definer owner/search path is unsafe")
		}
	}
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_default_acl d JOIN pg_roles r ON r.oid=d.defaclrole
	 CROSS JOIN LATERAL aclexplode(d.defaclacl) acl WHERE r.rolname=$1
	 AND acl.grantee IN (0,(SELECT oid FROM pg_roles WHERE rolname=$2)))`, principal.AuthorityOwner, principal.Role).Scan(&unsafe); err != nil {
		return err
	}
	if unsafe {
		return fmt.Errorf("managed test app has unassigned future-object default authority")
	}
	return nil
}

// authorityDifference names how an effective privilege differs from the
// capability. Object and privilege names are catalog facts, never custody, so
// a refused preparation or pre-deploy says which grant to reconcile.
func authorityDifference(grant, actual, expected bool) string {
	switch {
	case grant:
		return "held with grant option"
	case actual && !expected:
		return "held but not in the capability"
	default:
		return "in the capability but not held"
	}
}

// This audit works through an owner preparation transaction or the actual app
// login. Both use the same explicit principal and deny all inherited authority.
func VerifyApplicationPrincipalPosture(ctx context.Context, q PrivilegeQuerier, principal ApplicationPrincipal) error {
	if err := principal.validate(); err != nil {
		return err
	}
	var unsafe bool
	err := q.QueryRowContext(ctx, `SELECT NOT r.rolcanlogin OR r.rolsuper OR r.rolcreatedb OR r.rolcreaterole
 OR r.rolreplication OR r.rolbypassrls OR r.rolinherit OR COALESCE(array_length(r.rolconfig,1),0)>0
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members WHERE member=r.oid)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_database WHERE datdba=r.oid)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspowner=r.oid)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE relowner=r.oid)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_proc WHERE proowner=r.oid)
 FROM pg_catalog.pg_roles r WHERE rolname=$1`, principal.Role).Scan(&unsafe)
	if err != nil || unsafe {
		return fmt.Errorf("application principal is absent or carries ownership, membership or administrative authority")
	}
	var owner string
	if err = q.QueryRowContext(ctx, "SELECT pg_get_userbyid(nspowner) FROM pg_catalog.pg_namespace WHERE nspname='public'").Scan(&owner); err != nil || owner != principal.AuthorityOwner {
		return fmt.Errorf("application authority differs from the existing public schema owner")
	}
	return nil
}

// Resolve object authority as metadata; never authenticate or SET ROLE as it.
func ApplicationAuthorityOwner(ctx context.Context, q PrivilegeQuerier) (string, error) {
	var owner string
	err := q.QueryRowContext(ctx, "SELECT pg_get_userbyid(nspowner) FROM pg_catalog.pg_namespace WHERE nspname='public'").Scan(&owner)
	return owner, err
}

// otherDatabaseGrant is one CONNECT/TEMPORARY ACL entry of another connectable
// database, resolved against the application role.
type otherDatabaseGrant struct {
	OwnedByRole   bool // the role is, or is a member of, the database owner
	PublicGrantee bool // the entry is the PUBLIC pseudo-role
	RoleGrantee   bool // the role is, or is a member of, the named grantee
}

// otherDatabaseAuthorityExcess judges the role's own authority on other
// databases. Managed Postgres keeps PUBLIC CONNECT/TEMPORARY on its provider
// databases (postgres, template1, ubi_admin), which no tenant role can revoke;
// that inheritance is not the role's authority. Ownership and explicit grants,
// directly or through a membership, remain excess.
func otherDatabaseAuthorityExcess(grants []otherDatabaseGrant) bool {
	for _, grant := range grants {
		if grant.OwnedByRole || (!grant.PublicGrantee && grant.RoleGrantee) {
			return true
		}
	}
	return false
}

func applicationHasOtherDatabaseAuthority(ctx context.Context, q PrivilegeQuerier, role string) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT pg_has_role($1,d.datdba,'MEMBER'), COALESCE(a.grantee=0,false),
	 COALESCE(a.grantee<>0 AND pg_has_role($1,a.grantee,'MEMBER'),false)
	 FROM pg_database d LEFT JOIN LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) a
	 ON a.privilege_type IN ('CONNECT','TEMPORARY')
	 WHERE d.datallowconn AND d.datname<>current_database()`, role)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var grants []otherDatabaseGrant
	for rows.Next() {
		var grant otherDatabaseGrant
		if err := rows.Scan(&grant.OwnedByRole, &grant.PublicGrantee, &grant.RoleGrantee); err != nil {
			return false, err
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return otherDatabaseAuthorityExcess(grants), nil
}
