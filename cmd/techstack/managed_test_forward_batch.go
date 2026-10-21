package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/kombifyio/techstack/pkg/db"
	"github.com/jackc/pgx/v5"
)

//go:embed managed_test_forward_batch.json
var managedTestForwardBatchManifest []byte

var errManagedTestForwardBatchAdmission = errors.New("managed test forward batch source, artifact or intent is unbound")

// This purpose extends the existing assigned test migration owner. It accepts
// only the embedded reviewed batch; production Delivery and bootstrap are separate.
func runManagedTestForwardBatch(ctx context.Context, assignment db.ManagedTestAssignment, owner *db.DB, digest string, apply bool) error {
	expected := os.Getenv("TECHSTACK_EXPECTED_PREPARATION_SOURCE_SHA")
	intent := os.Getenv("TECHSTACK_MANAGED_TEST_FORWARD_BATCH_INTENT_SHA256")
	if len(expected) != 40 || expected != buildRevision || len(intent) != 64 {
		return errManagedTestForwardBatchAdmission
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return errManagedTestForwardBatchAdmission
	}
	if _, err := hex.DecodeString(intent); err != nil {
		return errManagedTestForwardBatchAdmission
	}
	manifest := bytes.ReplaceAll(managedTestForwardBatchManifest, []byte("\r\n"), []byte("\n"))
	actual := sha256.Sum256(manifest)
	if hex.EncodeToString(actual[:]) != digest {
		return errManagedTestForwardBatchAdmission
	}
	var batch []db.ForwardMigrationArtifact
	if err := json.Unmarshal(manifest, &batch); err != nil || len(batch) != 5 {
		return errManagedTestForwardBatchAdmission
	}
	// No administrative, provider or signing credential belongs in this process.
	for _, key := range []string{db.EnvManagedTestAdminDatabaseURL, providerControlRuntimeDatabaseURLEnv, db.EnvAdvancedIssuerDatabaseURL} {
		if os.Getenv(key) != "" {
			return fmt.Errorf("managed test forward batch cannot carry administrative, provider or issuer custody")
		}
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
	// Only the prospective 156 capabilities may be absent. Every previous app
	// capability and role fence must already pass before touching the schema.
	if err := db.VerifyManagedTestSubjectDirectoryForwardSession(ctx, app.DB, assignment); err != nil {
		return err
	}
	if err := owner.VerifyForwardBatch(ctx, batch); err != nil {
		return err
	}
	status, operation := "inspected", "inspect-forward"
	if apply {
		if err := owner.MigrateForwardBatch(ctx, batch, verifyManagedTestForwardBatchCapabilities); err != nil {
			return err
		}
		if err := app.VerifyMigrations(ctx); err != nil {
			return err
		}
		if err := db.VerifyManagedTestSession(ctx, app.DB, assignment, db.ManagedTestApp); err != nil {
			return err
		}
		status, operation = "acknowledged", "migrate-forward-batch"
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema": "techstack.managed-test-forward-batch/v1", "operation": operation, "status": status,
		"source_sha": buildRevision, "batch_sha256": digest, "migrations": batch, "intent_sha256": intent,
		"service_id": assignment.ServiceID, "database_identity": assignment.Database + "@" + assignment.SystemIdentifier,
		"application_role": db.ManagedTestApp, "database_effects": apply,
		"prior_role_key_grant_or_migration_replay": false, "provider_effects": false,
	})
}

// The two new 156 functions and the tenant table introduced by 159 receive only
// their existing canonical application capabilities, inside the batch transaction.
func verifyManagedTestForwardBatchCapabilities(ctx context.Context, tx *sql.Tx) error {
	if err := verifyDeliveryMigrationPosture(ctx, tx); err != nil {
		return err
	}
	if err := db.VerifyManagedTestApplicationPrivileges(ctx, tx, false); err != nil {
		return err
	}
	policy, err := db.LoadManagedTestApplicationPolicy(ctx, tx, true)
	if err != nil {
		return err
	}
	var tableOID int64
	if err := tx.QueryRowContext(ctx, "SELECT 'public.guard_repair_policy_versions'::regclass::oid::bigint").Scan(&tableOID); err != nil {
		return err
	}
	privileges := policy.Tables[tableOID]
	if len(privileges) != 4 || !privileges["SELECT"] || !privileges["INSERT"] || !privileges["UPDATE"] || !privileges["DELETE"] {
		return fmt.Errorf("new Guard policy table is outside the canonical tenant capability")
	}
	if _, err := tx.ExecContext(ctx, "GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE public.guard_repair_policy_versions TO "+pgx.Identifier{db.ManagedTestApp}.Sanitize()); err != nil {
		return err
	}
	// Reuse the bounded 156 capability installer and full effective posture check.
	return verifyManagedTestSubjectDirectoryForwardCapabilities(ctx, tx)
}
