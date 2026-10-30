package tenantdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Querier is the part of *sql.DB, *sql.Conn and *sql.Tx the role assertions
// need.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ErrRoleAssertion is wrapped by every *RoleAssertionError.
var ErrRoleAssertion = errors.New("tenantdb: database role assertion failed")

// RoleAssertionError lists every way the connected role breaks the isolation
// invariant. A service that receives it must refuse to start.
type RoleAssertionError struct {
	Role       string
	Expected   RoleClass
	Violations []string
}

func (e *RoleAssertionError) Error() string {
	return fmt.Sprintf("tenantdb: role %q is not a safe %s role: %s",
		e.Role, e.Expected, strings.Join(e.Violations, "; "))
}

func (e *RoleAssertionError) Unwrap() error { return ErrRoleAssertion }

// roleFacts is what the database reports about the connected role.
type roleFacts struct {
	CurrentUser      string
	SessionUser      string
	Superuser        bool
	BypassRLS        bool
	CreateRole       bool
	CreateDB         bool
	Replication      bool
	OwnsDatabase     bool
	DatabaseCreate   bool
	Memberships      []string
	OwnedObjects     []string
	CreateSchemas    []string
	RLSBypassGrants  []string
	WideningPolicies []string
	GUCPolicies      []string
	RegistryPresent  bool
	Class            string
	CanReadKeys      bool
}

// DefaultWideningSettings are the settings no RLS policy may read, because
// any code holding the database credential can set them.
var DefaultWideningSettings = []string{"app.service_scope"}

// RuntimeRoleOptions tightens AssertRuntimeRoleWith.
type RuntimeRoleOptions struct {
	// WideningSettings are further settings that legacy policies use as
	// escape flags (for example "app.data_subject_operation"); a policy that
	// reads any of them fails the assertion, like app.service_scope.
	WideningSettings []string
	// RequireVerifiedContext fails on every policy that reads a setting with
	// current_setting() directly instead of through kombify_ctx.subject() or
	// kombify_ctx.org_id(). Enable it once a consumer's migration is complete;
	// policies that read settings inside helper functions are not detected.
	RequireVerifiedContext bool
}

// AssertRuntimeRole is the mandatory boot check for a user-facing service.
// It fails unless the connected login role is NOSUPERUSER and NOBYPASSRLS,
// holds no CREATEROLE, CREATEDB or REPLICATION, owns no database, schema,
// table, sequence, view or function (directly or through membership), has no
// CREATE on the database or any schema, is a member of no role (so it has no
// SET ROLE), cannot TRUNCATE or add triggers to any table (TRUNCATE ignores
// RLS), cannot read kombify_ctx.keys, is not registered as a system role,
// and no RLS policy in the database reads app.service_scope. Call it with
// the service's own pool before serving and exit on error.
func AssertRuntimeRole(ctx context.Context, db Querier) error {
	return AssertRuntimeRoleWith(ctx, db, RuntimeRoleOptions{})
}

// AssertRuntimeRoleWith is AssertRuntimeRole with stricter policy checks.
func AssertRuntimeRoleWith(ctx context.Context, db Querier, opts RuntimeRoleOptions) error {
	facts, err := gatherRoleFacts(ctx, db, wideningPattern(opts.WideningSettings))
	if err != nil {
		return err
	}
	v := runtimeViolations(facts)
	if opts.RequireVerifiedContext && len(facts.GUCPolicies) > 0 {
		v = append(v, "RLS policies read settings directly instead of the verified context: "+strings.Join(facts.GUCPolicies, ", "))
	}
	if len(v) > 0 {
		return &RoleAssertionError{Role: facts.SessionUser, Expected: RoleClassRuntime, Violations: v}
	}
	return nil
}

func wideningPattern(extra []string) string {
	names := append(append([]string(nil), DefaultWideningSettings...), extra...)
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			quoted = append(quoted, regexp.QuoteMeta(n))
		}
	}
	return "(" + strings.Join(quoted, "|") + ")"
}

// AssertSystemRole checks that db is logged in as the named system role and
// that the role is registered as RoleClassSystem. A runtime credential
// therefore cannot be used for system work. System roles reach tenant rows
// only through policies targeted TO them, so they too must be NOSUPERUSER,
// NOBYPASSRLS, own nothing and hold no SET ROLE or DDL right.
func AssertSystemRole(ctx context.Context, db Querier, role string) error {
	facts, err := gatherRoleFacts(ctx, db, wideningPattern(nil))
	if err != nil {
		return err
	}
	if v := systemViolations(facts, role); len(v) > 0 {
		return &RoleAssertionError{Role: facts.SessionUser, Expected: RoleClassSystem, Violations: v}
	}
	return nil
}

func commonViolations(f roleFacts) []string {
	var v []string
	if f.CurrentUser != f.SessionUser {
		v = append(v, fmt.Sprintf("SET ROLE is active (session %s, current %s)", f.SessionUser, f.CurrentUser))
	}
	if f.Superuser {
		v = append(v, "is SUPERUSER")
	}
	if f.BypassRLS {
		v = append(v, "has BYPASSRLS")
	}
	if f.CreateRole {
		v = append(v, "has CREATEROLE")
	}
	if f.CreateDB {
		v = append(v, "has CREATEDB")
	}
	if f.Replication {
		v = append(v, "has REPLICATION")
	}
	if f.OwnsDatabase {
		v = append(v, "owns the database")
	}
	if f.DatabaseCreate {
		v = append(v, "has CREATE on the database")
	}
	if len(f.Memberships) > 0 {
		v = append(v, "is a member of "+strings.Join(f.Memberships, ", "))
	}
	if len(f.OwnedObjects) > 0 {
		v = append(v, "owns "+strings.Join(f.OwnedObjects, ", "))
	}
	if len(f.CreateSchemas) > 0 {
		v = append(v, "has CREATE on schema "+strings.Join(f.CreateSchemas, ", "))
	}
	if f.CanReadKeys {
		v = append(v, "can read kombify_ctx.keys")
	}
	if len(f.WideningPolicies) > 0 {
		v = append(v, "RLS policies read a widening setting such as app.service_scope: "+strings.Join(f.WideningPolicies, ", "))
	}
	return v
}

func runtimeViolations(f roleFacts) []string {
	v := commonViolations(f)
	if len(f.RLSBypassGrants) > 0 {
		v = append(v, "has TRUNCATE or TRIGGER on "+strings.Join(f.RLSBypassGrants, ", "))
	}
	if f.Class == string(RoleClassSystem) {
		v = append(v, "is registered as a system role")
	}
	return v
}

func systemViolations(f roleFacts, role string) []string {
	v := commonViolations(f)
	if role == "" || f.SessionUser != role {
		v = append(v, fmt.Sprintf("is logged in as %q, not the system role %q", f.SessionUser, role))
	}
	switch {
	case !f.RegistryPresent:
		v = append(v, "kombify_ctx.role_class is not installed")
	case f.Class != string(RoleClassSystem):
		v = append(v, "is not registered as a system role in kombify_ctx.role_class")
	}
	return v
}

// userSchemas filters out the catalogs, TOAST and temporary schemas.
const userSchemas = `n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'`

func gatherRoleFacts(ctx context.Context, db Querier, widening string) (roleFacts, error) {
	var f roleFacts
	err := db.QueryRowContext(ctx, `
		SELECT current_user, session_user, r.rolsuper, r.rolbypassrls, r.rolcreaterole,
		       r.rolcreatedb, r.rolreplication, d.datdba = r.oid,
		       has_database_privilege(current_database(), 'CREATE'),
		       to_regclass('kombify_ctx.role_class') IS NOT NULL,
		       COALESCE(has_table_privilege(to_regclass('kombify_ctx.keys'), 'SELECT'), false)
		FROM pg_roles r, pg_database d
		WHERE r.rolname = session_user AND d.datname = current_database()`).Scan(
		&f.CurrentUser, &f.SessionUser, &f.Superuser, &f.BypassRLS, &f.CreateRole,
		&f.CreateDB, &f.Replication, &f.OwnsDatabase, &f.DatabaseCreate,
		&f.RegistryPresent, &f.CanReadKeys)
	if err != nil {
		return f, fmt.Errorf("tenantdb: read role attributes: %w", err)
	}

	lists := []struct {
		dst   *[]string
		query string
		args  []any
	}{
		{&f.Memberships, `
			SELECT g.rolname FROM pg_auth_members m
			JOIN pg_roles g ON g.oid = m.roleid
			JOIN pg_roles u ON u.oid = m.member
			WHERE u.rolname = session_user ORDER BY 1`, nil},
		{&f.OwnedObjects, `
			SELECT o.label FROM (
				SELECT format('%s %I.%I', CASE c.relkind WHEN 'S' THEN 'sequence'
				       WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view'
				       WHEN 'f' THEN 'foreign table' ELSE 'table' END,
				       n.nspname, c.relname) AS label, c.relowner AS owner
				FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S') AND ` + userSchemas + `
				UNION ALL
				SELECT format('schema %I', n.nspname), n.nspowner
				FROM pg_namespace n WHERE ` + userSchemas + `
				UNION ALL
				SELECT format('function %I.%I', n.nspname, p.proname), p.proowner
				FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE ` + userSchemas + `
			) o
			WHERE pg_has_role(session_user, o.owner, 'MEMBER')
			ORDER BY 1 LIMIT 25`, nil},
		{&f.CreateSchemas, `
			SELECT n.nspname FROM pg_namespace n
			WHERE ` + userSchemas + ` AND has_schema_privilege(session_user, n.oid, 'CREATE')
			ORDER BY 1`, nil},
		{&f.RLSBypassGrants, `
			SELECT format('%I.%I', n.nspname, c.relname)
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind IN ('r', 'p') AND ` + userSchemas + `
			  AND (has_table_privilege(session_user, c.oid, 'TRUNCATE')
			       OR has_table_privilege(session_user, c.oid, 'TRIGGER'))
			ORDER BY 1 LIMIT 25`, nil},
		{&f.WideningPolicies, `
			SELECT format('%I.%I/%I', schemaname, tablename, policyname) FROM pg_policies
			WHERE COALESCE(qual, '') ~* $1 OR COALESCE(with_check, '') ~* $1
			ORDER BY 1 LIMIT 25`, []any{widening}},
		{&f.GUCPolicies, `
			SELECT format('%I.%I/%I', schemaname, tablename, policyname) FROM pg_policies
			WHERE COALESCE(qual, '') ~* 'current_setting' OR COALESCE(with_check, '') ~* 'current_setting'
			ORDER BY 1 LIMIT 25`, nil},
	}
	for _, l := range lists {
		values, err := queryStrings(ctx, db, l.query, l.args...)
		if err != nil {
			return f, err
		}
		*l.dst = values
	}

	if f.RegistryPresent {
		err := db.QueryRowContext(ctx,
			`SELECT class FROM kombify_ctx.role_class WHERE role_name = session_user`).Scan(&f.Class)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return f, fmt.Errorf("tenantdb: read role class: %w", err)
		}
	}
	return f, nil
}

func queryStrings(ctx context.Context, db Querier, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tenantdb: role assertion query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("tenantdb: role assertion scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tenantdb: role assertion rows: %w", err)
	}
	return out, nil
}

// SystemDB is a pool verified to be logged in as a registered system role.
// Cross-tenant work takes a *SystemDB, never the runtime *sql.DB, so the
// type system keeps the two credentials apart.
type SystemDB struct {
	db   *sql.DB
	role string
}

// OpenSystemDB verifies db with AssertSystemRole and wraps it. Open db from
// the system role's own credential; user-facing services, previews, tests
// and agents never receive that credential.
func OpenSystemDB(ctx context.Context, db *sql.DB, role string) (*SystemDB, error) {
	if err := AssertSystemRole(ctx, db, role); err != nil {
		return nil, err
	}
	return &SystemDB{db: db, role: role}, nil
}

// Role returns the verified system role name.
func (s *SystemDB) Role() string { return s.role }

// DB returns the underlying pool.
func (s *SystemDB) DB() *sql.DB { return s.db }

// WithTx runs fn in a transaction on the system pool and commits.
func (s *SystemDB) WithTx(ctx context.Context, opts *sql.TxOptions, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("tenantdb: begin system tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
