package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/kombifyio/techstack/pkg/db"
	"github.com/jackc/pgx/v5"
)

const managedTestExecutionForwardMigration = "155_job_execution_reclaim_authority.sql"

var errManagedTestExecutionForwardAdmission = errors.New("execution forward artifact or intent is unbound")

// Existing managed-test-bootstrap migrate purpose, bounded to one new forward
// migration. The governed controller binds fresh durable intent/ACK custody;
// this native stage never replays initial roles, global grants or issuer keys.
func runManagedTestExecutionForward(ctx context.Context, assignment db.ManagedTestAssignment, owner *db.DB, digest string) error {
	expected := os.Getenv("TECHSTACK_EXPECTED_PREPARATION_SOURCE_SHA")
	intent := os.Getenv("TECHSTACK_MANAGED_TEST_EXECUTION_FORWARD_INTENT_SHA256")
	if len(expected) != 40 || expected != buildRevision || len(intent) != 64 {
		return errManagedTestExecutionForwardAdmission
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return errManagedTestExecutionForwardAdmission
	}
	if _, err := hex.DecodeString(intent); err != nil {
		return errManagedTestExecutionForwardAdmission
	}
	if os.Getenv(db.EnvManagedTestAdminDatabaseURL) != "" {
		return fmt.Errorf("execution forward cannot carry cluster administrator custody")
	}
	appCfg, err := db.ManagedTestConfig(assignment, os.Getenv(db.EnvDatabaseURL), db.ManagedTestApp)
	if err != nil {
		return err
	}
	app, err := db.OpenContext(ctx, appCfg)
	if err != nil {
		return err
	}
	defer app.Close()
	if err := db.VerifyManagedTestExecutionForwardSession(ctx, app.DB, assignment); err != nil {
		return err
	}
	if err := db.VerifyManagedTestApplicationPrivileges(ctx, owner.DB, false); err != nil {
		return err
	}
	if err := owner.MigrateForward(ctx, managedTestExecutionForwardMigration, digest, verifyManagedTestExecutionForwardCapabilities); err != nil {
		return err
	}
	if err := app.VerifyMigrations(ctx); err != nil {
		return err
	}
	if err := db.VerifyManagedTestApplicationPrivileges(ctx, app.DB, true); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.managed-execution-forward/v1", "operation": "migrate-execution-reclaim", "status": "acknowledged", "source_sha": buildRevision, "migration": managedTestExecutionForwardMigration, "migration_sha256": digest, "intent_sha256": intent, "service_id": assignment.ServiceID, "database_identity": assignment.Database + "@" + assignment.SystemIdentifier, "application_role": db.ManagedTestApp, "direct_directory_grant": false, "prior_role_key_grant_or_migration_replay": false})
}

// Runs inside the existing migration transaction before its ledger commit.
func verifyManagedTestExecutionForwardCapabilities(ctx context.Context, tx *sql.Tx) error {
	// Only the two newly created canonical protected functions gain EXECUTE.
	// Every table, sequence, role and unrelated ACL remains unchanged.
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout='5s'"); err != nil {
		return err
	}
	if err := db.VerifyManagedTestApplicationPrivileges(ctx, tx, false); err != nil {
		return err
	}
	for _, signature := range []string{db.ManagedTestExecutionReclaimListFunction, db.ManagedTestExecutionReclaimCompactFunction} {
		var executable bool
		if err := tx.QueryRowContext(ctx, "SELECT has_function_privilege($1,$2,'EXECUTE')", db.ManagedTestApp, "public."+signature).Scan(&executable); err != nil {
			return err
		}
		if !executable {
			if _, err := tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION public."+signature+" TO "+pgx.Identifier{db.ManagedTestApp}.Sanitize()); err != nil {
				return err
			}
		}
	}
	if err := db.VerifyManagedTestApplicationPrivileges(ctx, tx, true); err != nil {
		return err
	}
	return nil
}
