package systemdb

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
)

// RoleRefusal names the refused principal facts (role, table and policy
// names only), so a refused start is diagnosable without any credential.
type RoleRefusal struct{ Violations []string }

func (e *RoleRefusal) Error() string {
	return "system role denied: " + strings.Join(e.Violations, "; ")
}

const wideningViolation = "RLS policies read a widening setting such as app.service_scope: "

// AssertSystemRoleBesideWidening is tenantdb.AssertSystemRole with one admitted
// neighbour. Production Cloud keeps the two infra projection policies that read
// app.service_scope until Cloud 000277 retires them together with the live
// projection ingest (platform-ld78c.4.4), and tenantdb refuses any such policy
// anywhere in the database. The household role starts beside them only when
// that is its sole violation and it holds no table or column privilege on any
// table such a policy guards, so no widening setting can reach its rows. Views,
// materialized views and SECURITY DEFINER routines cannot reach them either:
// the household startup runs VerifyPrivileges right after its first call, which
// refuses every relation privilege and every definer routine outside the
// expected set; the reducer and store constructors run only after that startup.
// Only the household worker uses this; the platform projector writes the
// guarded tables and keeps the unchanged tenantdb check.
func AssertSystemRoleBesideWidening(ctx context.Context, pool *sql.DB, role string) error {
	err := tenantdb.AssertSystemRole(ctx, pool, role)
	if err == nil {
		return nil
	}
	var assertion *tenantdb.RoleAssertionError
	if !errors.As(err, &assertion) {
		return errors.New("system database principal denied")
	}
	refusal := &RoleRefusal{Violations: assertion.Violations}
	if len(assertion.Violations) != 1 || !strings.HasPrefix(assertion.Violations[0], wideningViolation) {
		return refusal
	}
	var reachable bool
	if pool.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_policies p
  JOIN pg_catalog.pg_namespace n ON n.nspname=p.schemaname JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=p.tablename
  WHERE (COALESCE(p.qual,'') ~* 'app\.service_scope' OR COALESCE(p.with_check,'') ~* 'app\.service_scope')
   AND (has_table_privilege(c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER,MAINTAIN')
    OR has_any_column_privilege(c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))`).Scan(&reachable) != nil || reachable {
		return refusal
	}
	return nil
}
