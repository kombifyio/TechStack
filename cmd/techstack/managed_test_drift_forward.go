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

const managedTestDriftForwardMigration = "153_drift_schedule_authority.sql"

var errManagedTestDriftForwardAdmission = errors.New("drift forward artifact or intent is unbound")

// Existing managed-test-bootstrap migrate purpose, bounded to one new forward
// migration. The governed controller binds fresh durable intent/ACK custody;
// this native stage never replays initial roles, global grants or issuer keys.
func runManagedTestDriftForward(ctx context.Context, assignment db.ManagedTestAssignment, owner *db.DB, digest string) error {
	expected := os.Getenv("TECHSTACK_EXPECTED_PREPARATION_SOURCE_SHA")
	intent := os.Getenv("TECHSTACK_MANAGED_TEST_DRIFT_FORWARD_INTENT_SHA256")
	if len(expected) != 40 || expected != buildRevision || len(intent) != 64 {
		return errManagedTestDriftForwardAdmission
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return errManagedTestDriftForwardAdmission
	}
	if _, err := hex.DecodeString(intent); err != nil {
		return errManagedTestDriftForwardAdmission
	}
	if os.Getenv(db.EnvManagedTestAdminDatabaseURL) != "" {
		return fmt.Errorf("drift forward cannot carry cluster administrator custody")
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
	if err := db.VerifyManagedTestDriftForwardSession(ctx, app.DB, assignment); err != nil {
		return err
	}
	if err := db.VerifyManagedTestApplicationPrivileges(ctx, owner.DB, false); err != nil {
		return err
	}
	if err := owner.MigrateForward(ctx, managedTestDriftForwardMigration, digest, verifyManagedTestDriftForwardCapabilities); err != nil {
		return err
	}
	if err := app.VerifyMigrations(ctx); err != nil {
		return err
	}
	if err := db.VerifyManagedTestApplicationPrivileges(ctx, app.DB, true); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.managed-drift-forward/v1", "operation": "migrate-drift-schedule", "status": "acknowledged", "source_sha": buildRevision, "migration": managedTestDriftForwardMigration, "migration_sha256": digest, "intent_sha256": intent, "service_id": assignment.ServiceID, "database_identity": assignment.Database + "@" + assignment.SystemIdentifier, "application_role": db.ManagedTestApp, "direct_directory_grant": false, "prior_role_key_grant_or_migration_replay": false})
}

// Runs inside the existing migration transaction before its ledger commit.
func verifyManagedTestDriftForwardCapabilities(ctx context.Context, tx *sql.Tx) error {
	// Only the two newly created canonical protected functions gain EXECUTE.
	// Every table, sequence, role and unrelated ACL remains unchanged.
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout='5s'"); err != nil {
		return err
	}
	if err := db.VerifyManagedTestApplicationPrivileges(ctx, tx, false); err != nil {
		return err
	}
	for _, signature := range []string{db.ManagedTestDriftScheduleListFunction, db.ManagedTestDriftScheduleCompactFunction} {
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
