package db

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
)

// Production changes only the explicit application's direct capabilities.
// PUBLIC/default/foreign-owner excess fails closed; no blanket custody reset.
func InstallApplicationCapabilities(ctx context.Context, tx *sql.Tx, principal ApplicationPrincipal) error {
	if err := principal.validate(); err != nil {
		return err
	}
	var databaseName, login string
	if err := tx.QueryRowContext(ctx, "SELECT current_database(),current_user").Scan(&databaseName, &login); err != nil || login != principal.AuthorityOwner {
		return fmt.Errorf("application grants require exact existing migration authority")
	}
	if err := verifyApplicationPrivileges(ctx, tx, principal, false); err != nil {
		return err
	}
	policy, err := loadPrincipalPolicy(ctx, tx, principal, true)
	if err != nil {
		return err
	}
	role := pgx.Identifier{principal.Role}.Sanitize()
	if _, err := tx.ExecContext(ctx, "GRANT CONNECT ON DATABASE "+pgx.Identifier{databaseName}.Sanitize()+" TO "+role); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "GRANT USAGE ON SCHEMA public TO "+role); err != nil {
		return err
	}
	if policy.ContextSchema {
		if _, err := tx.ExecContext(ctx, "GRANT USAGE ON SCHEMA kombify_ctx TO "+role); err != nil {
			return err
		}
	}
	for oid, privileges := range policy.Tables {
		var selected []string
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			if privileges[privilege] {
				selected = append(selected, privilege)
			}
		}
		table := pgx.Identifier{"public", policy.TableNames[oid]}
		if policy.TableNames[oid] == "kombify_ctx.role_class" {
			table = pgx.Identifier{"kombify_ctx", "role_class"}
		}
		if _, err := tx.ExecContext(ctx, "GRANT "+strings.Join(selected, ",")+" ON TABLE "+table.Sanitize()+" TO "+role); err != nil {
			return err
		}
	}
	for _, sequence := range policy.Sequences {
		if _, err := tx.ExecContext(ctx, "GRANT USAGE ON SEQUENCE "+pgx.Identifier{"public", sequence}.Sanitize()+" TO "+role); err != nil {
			return err
		}
	}
	for _, signature := range policy.Functions {
		qualified := "public." + signature
		if strings.HasPrefix(signature, "kombify_ctx.") {
			qualified = signature
		}
		if _, err := tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION "+qualified+" TO "+role); err != nil {
			return err
		}
	}
	pin, pinned, err := ExpectedDatabaseIdentityFromEnv()
	if err != nil {
		return err
	}
	if !pinned || pin.SystemIdentifier == "" {
		return fmt.Errorf("application preparation requires the exact physical identity pin")
	}
	if _, err = tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO "+role); err != nil {
		return err
	}
	if err := verifyApplicationPrivileges(ctx, tx, principal, true); err != nil {
		return err
	}
	return nil
}
