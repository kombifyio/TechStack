package db

import (
	"context"
	"fmt"
	"strings"
)

const BackupWorkerRole = "techstack_backup_worker"
const EnvBackupWorkerDatabaseURL = "TECHSTACK_BACKUP_WORKER_DATABASE_URL"

// The private worker reuses the ordinary job/lease and typed-command owners.
// It has no issuer, key, provider, migration or object ownership capability.
// The shared stack fence consumes a separate metadata projection; job payload
// reads and writes remain backup-only under the restrictive worker policies.
var backupWorkerTables = map[string]string{
	"schema_migrations":      "SELECT",
	"stack_backup_schedules": "SELECT,UPDATE", "stack_backup_stores": "SELECT",
	"stack_backup_usage": "SELECT,INSERT,UPDATE",
	"stacks":             "SELECT", "workers": "SELECT", "servers": "SELECT",
	"server_maintenance_jobs": "SELECT", "jobs": "SELECT,INSERT,UPDATE",
	"typed_agent_commands": "SELECT,INSERT,UPDATE,DELETE",
}

var backupWorkerFunctions = []string{
	"techstack_list_due_backup_schedules(timestamp with time zone,text,text,integer)",
	"techstack_list_pending_job_tenants(text,integer)",
	"techstack_compact_pending_job_tenant(text)",
	"techstack_backup_execution_conflicts(text,text)",
	"provider_control_runtime_authority()",
}

func loadPrincipalPolicy(ctx context.Context, q PrivilegeQuerier, principal ApplicationPrincipal, required bool) (ApplicationPolicy, error) {
	if principal.Role != BackupWorkerRole {
		return loadApplicationPolicy(ctx, q, required)
	}
	p := ApplicationPolicy{Tables: map[int64]map[string]bool{}, TableNames: map[int64]string{}, Sequences: map[int64]string{}, Functions: map[int64]string{}}
	rows, err := q.QueryContext(ctx, `SELECT c.oid::bigint,c.relname,c.relrowsecurity AND c.relforcerowsecurity
	 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
	 WHERE n.nspname='public' AND c.relkind IN ('r','p')`)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var oid int64
		var name string
		var rls bool
		if err := rows.Scan(&oid, &name, &rls); err != nil {
			rows.Close()
			return p, err
		}
		verbs, assigned := backupWorkerTables[name]
		if !assigned {
			continue
		}
		if name != "schema_migrations" && !rls {
			rows.Close()
			return p, fmt.Errorf("backup worker requires FORCE RLS: %s", name)
		}
		p.Tables[oid] = map[string]bool{}
		p.TableNames[oid] = name
		for _, verb := range strings.Split(verbs, ",") {
			p.Tables[oid][verb] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	if len(p.Tables) != len(backupWorkerTables) {
		return p, fmt.Errorf("backup worker table authority is incomplete")
	}
	// Restrictive policies refer to the canonical accessors even for distinct
	// system principals. Grant that same public verifier surface, never keys or
	// runtime classification/signing authority.
	var roleClass int64
	if err := q.QueryRowContext(ctx, `SELECT to_regclass('kombify_ctx.role_class')::oid::bigint`).Scan(&roleClass); err != nil {
		return p, err
	}
	p.ContextSchema = true
	p.Tables[roleClass] = map[string]bool{"SELECT": true}
	p.TableNames[roleClass] = "kombify_ctx.role_class"
	for _, signature := range applicationContextFunctions {
		var oid int64
		if err := q.QueryRowContext(ctx, `SELECT to_regprocedure($1)::oid::bigint`, signature).Scan(&oid); err != nil {
			return p, err
		}
		p.Functions[oid] = signature
	}
	for _, signature := range backupWorkerFunctions {
		var oid int64
		if err := q.QueryRowContext(ctx, `SELECT to_regprocedure($1)::oid::bigint`, "public."+signature).Scan(&oid); err != nil {
			return p, fmt.Errorf("backup worker function authority is incomplete")
		}
		p.Functions[oid] = signature
	}
	return p, nil
}
