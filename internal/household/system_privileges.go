package household

import (
	"context"
	"database/sql"

	"github.com/kombifyio/techstack/internal/systemdb"
)

// This is a startup assertion of the two migration-owned capabilities, not a
// grant installer. Unexpected inherited/PUBLIC/column rights and any
// SECURITY DEFINER routine beyond the expected ones refuse execution; the
// worker cannot repair or widen its own authority.
func verifyHouseholdPrivileges(ctx context.Context, pool *sql.DB, cloud bool) error {
	tables := map[string]string{"kombify_ctx.role_class": "SELECT"}
	columns := map[string]string{}
	functions := map[string]bool{"kombify_ctx.verified_context()": true, "kombify_ctx.subject()": true, "kombify_ctx.org_id()": true}
	if cloud {
		tables["public.kombify_db_migrations"] = "SELECT"
		for _, name := range []string{"spaces", "space_members", "space_invites", "space_member_grants", "household_lifecycle_receipts"} {
			tables["public."+name] = "SELECT"
		}
		tables["public.household_lifecycle_outbox"] = "SELECT,UPDATE"
		tables["public.household_node_projections"] = "SELECT,INSERT,UPDATE"
		columns["public.users"] = "id,auth_subject,email,name,avatar_url"
		functions["public.household_reduce_lifecycle(integer)"] = true
	} else {
		for _, name := range []string{"schema_migrations", "techstack_memberships", "techstack_tenants", "homelabs", "stacks", "workers", "servers"} {
			tables["public."+name] = "SELECT"
		}
		tables["public.household_projection_bindings"] = "SELECT,INSERT,UPDATE"
		tables["public.household_projection_attempts"] = "SELECT,INSERT,UPDATE"
		tables["public.household_identity_refs"] = "SELECT,INSERT,UPDATE,DELETE"
		tables["public.typed_agent_commands"] = "SELECT,INSERT,UPDATE,DELETE"
	}
	return systemdb.VerifyPrivileges(ctx, pool, tables, columns, functions)
}
