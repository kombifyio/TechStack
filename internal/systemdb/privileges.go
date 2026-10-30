package systemdb

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// VerifyPrivileges checks the exact migration-owned table, column and routine capability.
func denied() error {
	return errors.New("system database privileges differ from the exact migrated capability")
}
func VerifyPrivileges(ctx context.Context, pool *sql.DB, tables, columns map[string]string, functions map[string]bool) error {
	if err := verifyRole(ctx, pool); err != nil {
		return err
	}
	if err := verifyTables(ctx, pool, tables); err != nil {
		return err
	}
	if err := verifyColumns(ctx, pool, tables, columns); err != nil {
		return err
	}
	if err := verifyRoutines(ctx, pool, functions); err != nil {
		return err
	}
	return verifySequences(ctx, pool)
}
func verifyRole(ctx context.Context, pool *sql.DB) error {
	var safe bool
	err := pool.QueryRowContext(ctx, `SELECT NOT r.rolinherit AND NOT has_database_privilege(current_database(),'TEMP')
  AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE roleid=r.oid OR member=r.oid)
  AND NOT EXISTS(SELECT 1 FROM pg_db_role_setting s CROSS JOIN LATERAL unnest(s.setconfig) v
   WHERE s.setrole IN(0,r.oid) AND s.setdatabase IN(0,(SELECT oid FROM pg_database WHERE datname=current_database()))
   AND (v LIKE 'app.%' OR v LIKE 'kombify.%' OR v LIKE 'role=%' OR v LIKE 'session_authorization=%'))
  FROM pg_roles r WHERE r.rolname=session_user`).Scan(&safe)
	if err != nil || !safe {
		return denied()
	}
	return nil
}
func verifyTables(ctx context.Context, pool *sql.DB, tables map[string]string) error {
	rows, err := pool.QueryContext(ctx, `SELECT n.nspname||'.'||c.relname,v.p,has_table_privilege(c.oid,v.p),has_table_privilege(c.oid,v.p||' WITH GRANT OPTION'),c.relrowsecurity AND c.relforcerowsecurity
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  CROSS JOIN(VALUES('SELECT'),('INSERT'),('UPDATE'),('DELETE'),('TRUNCATE'),('REFERENCES'),('TRIGGER'),('MAINTAIN'))v(p)
  WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND c.relkind IN('r','p','v','m','f')`)
	if err != nil {
		return denied()
	}
	seen := map[string]bool{}
	for rows.Next() {
		var name, verb string
		var actual, grant, rls bool
		if rows.Scan(&name, &verb, &actual, &grant, &rls) != nil {
			_ = rows.Close()
			return denied()
		}
		assigned := tables[name] != ""
		expected := containsPrivilege(tables[name], verb)
		if grant || actual != expected || (assigned && requiresRLS(name) && !rls) {
			_ = rows.Close()
			return denied()
		}
		if assigned {
			seen[name] = true
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil || len(seen) != len(tables) {
		return denied()
	}
	return nil
}
func verifyColumns(ctx context.Context, pool *sql.DB, tables, columns map[string]string) error {
	rows, err := pool.QueryContext(ctx, `SELECT n.nspname||'.'||c.relname,a.attname,v.p,has_column_privilege(c.oid,a.attnum,v.p),has_column_privilege(c.oid,a.attnum,v.p||' WITH GRANT OPTION')
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
  CROSS JOIN(VALUES('SELECT'),('INSERT'),('UPDATE'),('REFERENCES'))v(p)
  WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND c.relkind IN('r','p','v','m','f')`)
	if err != nil {
		return denied()
	}
	seenColumns := 0
	for rows.Next() {
		var name, column, verb string
		var actual, grant bool
		if rows.Scan(&name, &column, &verb, &actual, &grant) != nil {
			_ = rows.Close()
			return denied()
		}
		boundedColumn := verb == "SELECT" && containsPrivilege(columns[name], column)
		expected := containsPrivilege(tables[name], verb) || boundedColumn
		if grant || actual != expected {
			_ = rows.Close()
			return denied()
		}
		if boundedColumn {
			seenColumns++
		}
	}
	err = rows.Err()
	_ = rows.Close()
	expectedColumns := 0
	for _, list := range columns {
		expectedColumns += len(strings.Split(list, ","))
	}
	if err != nil || seenColumns != expectedColumns {
		return denied()
	}
	return nil
}
func verifyRoutines(ctx context.Context, pool *sql.DB, functions map[string]bool) error {
	oids := map[int64]bool{}
	for signature := range functions {
		var oid sql.NullInt64
		if pool.QueryRowContext(ctx, `SELECT to_regprocedure($1)::oid::bigint`, signature).Scan(&oid) != nil || !oid.Valid {
			return denied()
		}
		oids[oid.Int64] = true
	}
	rows, err := pool.QueryContext(ctx, `SELECT f.oid::bigint,has_function_privilege(f.oid,'EXECUTE'),has_function_privilege(f.oid,'EXECUTE WITH GRANT OPTION') FROM pg_proc f JOIN pg_namespace n ON n.oid=f.pronamespace WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema'`)
	if err != nil {
		return denied()
	}
	for rows.Next() {
		var oid int64
		var actual, grant bool
		if rows.Scan(&oid, &actual, &grant) != nil || grant || actual != oids[oid] {
			_ = rows.Close()
			return denied()
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return denied()
	}
	return nil
}
func verifySequences(ctx context.Context, pool *sql.DB) error {
	var safe bool
	err := pool.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname!~'^pg_' AND n.nspname<>'information_schema' AND CASE WHEN c.relkind='S' THEN has_sequence_privilege(c.oid,'USAGE,SELECT,UPDATE') ELSE false END)`).Scan(&safe)
	if err != nil || !safe {
		return denied()
	}
	return nil
}

func containsPrivilege(list, value string) bool {
	for _, entry := range strings.Split(list, ",") {
		if entry == value {
			return true
		}
	}
	return false
}

func requiresRLS(name string) bool {
	switch name {
	case "public.schema_migrations", "public.kombify_db_migrations", "kombify_ctx.role_class":
		return false
	}
	return true
}
