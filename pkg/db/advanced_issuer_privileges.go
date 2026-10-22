package db

import (
	"context"
	"database/sql"
	"errors"
)

// The same effective privilege inspection gates preparation before COMMIT and
// actual runtime login. Direct grants, PUBLIC rights and role ownership count.
const advancedIssuerKeyPrivilegesSQL = `SELECT 
 r.rolsuper OR r.rolbypassrls OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolinherit OR NOT r.rolcanlogin
 OR COALESCE(array_length(r.rolconfig,1),0)>0
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members WHERE member=r.oid)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_database WHERE datdba=r.oid)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE relowner=r.oid)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_proc WHERE proowner=r.oid)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspowner=r.oid)
 OR has_database_privilege(r.oid,current_database(),'CREATE') OR has_database_privilege(r.oid,current_database(),'TEMP')
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspname<>'information_schema' AND left(n.nspname,3) <> 'pg_' AND has_schema_privilege(r.oid,n.oid,'CREATE'))
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname<>'information_schema' AND left(n.nspname,3) <> 'pg_' AND c.relkind IN ('r','p','v','m','f')
  AND NOT(n.nspname='public' AND c.relname='advanced_issuer_keys')
  AND (has_table_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') OR has_any_column_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname<>'information_schema' AND left(n.nspname,3) <> 'pg_'
  AND CASE WHEN c.relkind='S' THEN has_sequence_privilege(r.oid,c.oid,'USAGE,SELECT,UPDATE') ELSE false END)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
  WHERE n.nspname<>'information_schema' AND left(n.nspname,3) <> 'pg_'
  AND p.oid<>COALESCE(to_regprocedure(NULLIF($2::text,'')),0)
  AND has_function_privilege(r.oid,p.oid,'EXECUTE'))
 OR has_table_privilege(r.oid,'public.advanced_issuer_keys','UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
	OR has_any_column_privilege(r.oid,'public.advanced_issuer_keys','UPDATE,REFERENCES')
 OR has_table_privilege(r.oid,'public.advanced_issuer_keys','SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION')
 OR has_any_column_privilege(r.oid,'public.advanced_issuer_keys','SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION')
 OR has_database_privilege(r.oid,current_database(),'CONNECT WITH GRANT OPTION')
 OR has_schema_privilege(r.oid,'public','USAGE WITH GRANT OPTION')
 OR COALESCE(has_function_privilege(r.oid,to_regprocedure(NULLIF($2::text,'')),'EXECUTE WITH GRANT OPTION'),false),
 has_table_privilege(r.oid,'public.advanced_issuer_keys','SELECT') AND has_table_privilege(r.oid,'public.advanced_issuer_keys','INSERT')
 FROM pg_catalog.pg_roles r WHERE r.rolname=$1`

func inspectAdvancedIssuerKeyPrivileges(ctx context.Context, q PrivilegeQuerier, role, identityFunction string, requireGrants bool) error {
	var unsafe, allowed bool
	if q == nil || role == "" {
		return errors.New("advanced issuer custody principal is absent")
	}
	stage := "pre-grant-admission"
	if requireGrants {
		stage = "post-grant-admission"
	}
	if err := q.QueryRowContext(ctx, advancedIssuerKeyPrivilegesSQL, role, identityFunction).Scan(&unsafe, &allowed); err != nil {
		return issuerPreparationError(stage+"-query", err)
	}
	if unsafe {
		return issuerPreparationError(stage+"-unassigned-privileges", nil)
	}
	if requireGrants && !allowed {
		return issuerPreparationError(stage+"-missing-key-grants", nil)
	}
	return nil
}

// VerifyAdvancedIssuerRuntimePrivileges admits only signing-key custody.
// Tenant tables and administrative/provider authority use other connections.
func VerifyAdvancedIssuerRuntimePrivileges(ctx context.Context, database *sql.DB, expectedLogin, applicationLogin string) error {
	if database == nil || expectedLogin == "" || applicationLogin == "" || expectedLogin == applicationLogin {
		return errors.New("advanced issuer custody requires a distinct declared principal")
	}
	var login, session, schema string
	if database.QueryRowContext(ctx, `SELECT current_user,session_user,current_schema()`).Scan(&login, &session, &schema) != nil || login != expectedLogin || session != login || schema != "public" {
		return errors.New("advanced issuer custody has an unassigned principal or schema")
	}
	return inspectAdvancedIssuerKeyPrivileges(ctx, database, expectedLogin, "", true)
}
