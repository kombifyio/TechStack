package db

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"
)

// Production changes only the explicit application's direct capabilities.
// PUBLIC/default/foreign-owner excess fails closed; no blanket custody reset.
func InstallApplicationCapabilities(ctx context.Context, tx *sql.Tx, principal ApplicationPrincipal) error {
	pin, pinned, err := ExpectedDatabaseIdentityFromEnv()
	if err != nil {
		return err
	}
	if !pinned {
		return fmt.Errorf("application preparation requires the exact physical identity pin")
	}
	return InstallApplicationCapabilitiesWithIdentity(ctx, tx, principal, pin)
}

// InstallApplicationCapabilitiesWithIdentity shares the canonical policy with
// a native operator whose independently verified installation is not Stage.
// It never changes process-wide database admission or migration settings.
func InstallApplicationCapabilitiesWithIdentity(ctx context.Context, tx *sql.Tx, principal ApplicationPrincipal, pin ExpectedDatabaseIdentity) error {
	if pin.DatabaseName == "" || pin.SystemIdentifier == "" {
		return fmt.Errorf("application preparation requires the exact physical identity pin")
	}
	if err := verifyMigrationIdentity(ctx, tx, pin); err != nil {
		return err
	}
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
	// One GRANT per privilege set, object kind: the same grants as one statement
	// per object, in a few round trips across the fixed database egress.
	tablesByPrivileges := map[string][]string{}
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
		key := strings.Join(selected, ",")
		tablesByPrivileges[key] = append(tablesByPrivileges[key], table.Sanitize())
	}
	privilegeSets := make([]string, 0, len(tablesByPrivileges))
	for key := range tablesByPrivileges {
		privilegeSets = append(privilegeSets, key)
	}
	sort.Strings(privilegeSets)
	for _, key := range privilegeSets {
		tables := tablesByPrivileges[key]
		sort.Strings(tables)
		if _, err := tx.ExecContext(ctx, "GRANT "+key+" ON TABLE "+strings.Join(tables, ",")+" TO "+role); err != nil {
			return err
		}
	}
	if len(policy.Sequences) > 0 {
		sequences := make([]string, 0, len(policy.Sequences))
		for _, sequence := range policy.Sequences {
			sequences = append(sequences, pgx.Identifier{"public", sequence}.Sanitize())
		}
		sort.Strings(sequences)
		if _, err := tx.ExecContext(ctx, "GRANT USAGE ON SEQUENCE "+strings.Join(sequences, ",")+" TO "+role); err != nil {
			return err
		}
	}
	if len(policy.Functions) > 0 {
		functions := make([]string, 0, len(policy.Functions))
		for _, signature := range policy.Functions {
			qualified := "public." + signature
			if strings.HasPrefix(signature, "kombify_ctx.") {
				qualified = signature
			}
			functions = append(functions, qualified)
		}
		sort.Strings(functions)
		if _, err := tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION "+strings.Join(functions, ",")+" TO "+role); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO "+role); err != nil {
		return err
	}
	if err := verifyApplicationPrivileges(ctx, tx, principal, true); err != nil {
		return err
	}
	return nil
}
