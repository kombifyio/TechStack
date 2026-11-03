package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

// RILQueueAdapterEvidence identifies the exact bounded pending-183 correction.
// The original migration artifact remains immutable; this digest covers the
// complete effective SQL after the source-bound guard replacement.
func RILQueueAdapterEvidence() map[string]any {
	return map[string]any{"id": "ril-183-worker-policy-admission", "version": 1,
		"migration":            "183_ril_queue_proof_expiry.sql",
		"original_sha256":      "a86e7b7427aa844258114824ff044879356e911a8e8e5597db05a8d1259cf874",
		"effective_sql_sha256": "57ca78d44175b5e0f73fed5c8a69a7d35cf6a9bd4266a558fdb32e155717e093"}
}

// executePendingMigration is shared by the normal and exact forward-batch
// engines. The caller has already excluded committed ledger entries and owns
// the migration transaction; denial rolls back without recording this version.
func executePendingMigration(ctx context.Context, tx *sql.Tx, version string, body []byte) error {
	if version == "168_household_projection_bindings.sql" {
		// Preserve the published migration and its manifest checksum. Its role
		// creation is NOLOGIN, but its existing-role branch must not adopt a
		// usable credential into the cross-tenant capability. Activation belongs
		// to the separately governed private worker, never to schema application.
		// Use the same canonical LF identity as forwardBatchBodies on Windows.
		canonical := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
		if fmt.Sprintf("%x", sha256.Sum256(canonical)) != "5a5b05d69c313258a46d103862ea005441388527d5136d935d5e37ef38d5a179" {
			return fmt.Errorf("household migration source binding differs")
		}
		var canLogin bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_roles
			WHERE rolname = 'techstack_household_worker' AND rolcanlogin
		)`).Scan(&canLogin); err != nil {
			return fmt.Errorf("household migration role posture unavailable: %w", err)
		}
		if canLogin {
			return fmt.Errorf("household migration refuses an existing login credential")
		}
	}
	if version == "183_ril_queue_proof_expiry.sql" {
		// Keep the shipped SQL and forward manifest immutable. The original
		// guard rejected every restrictive read policy, including the two
		// source-owned worker ceilings that are true for the migration session.
		// Only their exact inspected definitions are admitted; any changed or
		// additional restrictive policy still refuses the snapshot transaction.
		canonical := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
		if fmt.Sprintf("%x", sha256.Sum256(canonical)) != "a86e7b7427aa844258114824ff044879356e911a8e8e5597db05a8d1259cf874" {
			return fmt.Errorf("RIL queue migration source binding differs")
		}
		const original = `((NOT p.polpermissive AND p.polcmd IN ('r','*')) OR p.polname='techstack_ril_queue_snapshot')`
		const corrected = `(p.polname='techstack_ril_queue_snapshot' OR (NOT p.polpermissive AND p.polcmd IN ('r','*') AND NOT (p.polcmd='*' AND p.polroles=ARRAY[0::oid] AND session_user NOT IN ('techstack_backup_worker','techstack_household_worker') AND ((p.polname='backup_worker_typed_commands' AND pg_get_expr(p.polqual,p.polrelid) IS NOT DISTINCT FROM $ril_policy$((SESSION_USER <> 'techstack_backup_worker'::name) OR ((command_family = 'stackkit'::text) AND ((command_json ->> 'operation'::text) = 'STACKKIT_OPERATION_BACKUP_RUN'::text)))$ril_policy$ AND pg_get_expr(p.polwithcheck,p.polrelid) IS NOT DISTINCT FROM $ril_policy$((SESSION_USER <> 'techstack_backup_worker'::name) OR ((command_family = 'stackkit'::text) AND ((command_json ->> 'operation'::text) = 'STACKKIT_OPERATION_BACKUP_RUN'::text)))$ril_policy$) OR (p.polname='household_worker_command_ceiling' AND pg_get_expr(p.polqual,p.polrelid) IS NOT DISTINCT FROM $ril_policy$((SESSION_USER <> 'techstack_household_worker'::name) OR ((command_family = 'stackkit'::text) AND ((command_json ->> 'operation'::text) = ANY (ARRAY['STACKKIT_OPERATION_HOUSEHOLD_PROJECT'::text, 'STACKKIT_OPERATION_HOUSEHOLD_LIST'::text, 'STACKKIT_OPERATION_HOUSEHOLD_REMOVE'::text]))))$ril_policy$ AND pg_get_expr(p.polwithcheck,p.polrelid) IS NOT DISTINCT FROM $ril_policy$((SESSION_USER <> 'techstack_household_worker'::name) OR ((command_family = 'stackkit'::text) AND ((command_json ->> 'operation'::text) = ANY (ARRAY['STACKKIT_OPERATION_HOUSEHOLD_PROJECT'::text, 'STACKKIT_OPERATION_HOUSEHOLD_LIST'::text, 'STACKKIT_OPERATION_HOUSEHOLD_REMOVE'::text]))))$ril_policy$)))))`
		if bytes.Count(canonical, []byte(original)) != 1 {
			return fmt.Errorf("RIL queue migration guard differs")
		}
		body = bytes.Replace(canonical, []byte(original), []byte(corrected), 1)
		// The source revision and this digest bind the exact effective SQL in the native receipt.
		if fmt.Sprintf("%x", sha256.Sum256(body)) != "57ca78d44175b5e0f73fed5c8a69a7d35cf6a9bd4266a558fdb32e155717e093" {
			return fmt.Errorf("RIL queue effective migration binding differs")
		}
	}
	_, err := tx.ExecContext(ctx, string(body))
	return err
}
