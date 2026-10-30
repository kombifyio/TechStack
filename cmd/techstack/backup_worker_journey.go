package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
	"github.com/kombifyio/techstack/internal/backupjobs"
	"github.com/kombifyio/techstack/internal/executioncustody"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/auth/testidentity"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/db/tenantcontext"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/encoding/protojson"
)

// This probe never starts a scanner, enqueues a job, claims a lease or submits
// a Guard result. The separately running private process must perform them.
// It changes only the due time of an existing owned test schedule and restores
// that schedule. Durable job/command/snapshot history remains as evidence.
func runBackupWorkerJourney(parent context.Context) (returnErr error) {
	fail := func(code string) error { return errors.New("BACKUP_JOURNEY_" + code) }
	if len(os.Args) != 2 || config.ReleaseChannel() != "preview" || !db.DeclaresStagingDatabaseBinding() ||
		os.Getenv(backupWorkerModeEnv) != "execution" || os.Getenv(db.EnvDatabaseMigrationMode) != "verify" ||
		os.Getenv(db.EnvAllowDBGenesis) != "false" || os.Getenv(db.EnvMigrationDatabaseURL) != "" ||
		os.Getenv("TECHSTACK_MANAGED_TEST_ADMIN_DATABASE_URL") != "" || "" != "" ||
		!regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(buildRevision) || os.Getenv("TECHSTACK_EXECUTION_EXPECTED_SOURCE_SHA") != buildRevision ||
		!regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(compiledBackupRuntimeContractDigest) ||
		os.Getenv("TECHSTACK_EXECUTION_EXPECTED_BACKUP_CONTRACT") != compiledBackupRuntimeContractDigest {
		return fail("PROFILE")
	}
	runtimeConfig, err := config.Load("")
	if err != nil || !runtimeConfig.DeploymentMode.IsSaaS() {
		return fail("HOSTED_BINDING")
	}
	var input struct {
		ContextToken         string `json:"context_token"`
		StackID              string `json:"stack_id"`
		ExecutionOwnerSHA256 string `json:"execution_owner_sha256"`
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 16385))
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err != nil || len(raw) > 16384 || decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF ||
		!regexp.MustCompile(`^v1\.techstack-stage-context-g1\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{43}$`).MatchString(input.ContextToken) ||
		!regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(input.StackID) ||
		!regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(input.ExecutionOwnerSHA256) {
		return fail("FIXTURE_INPUT")
	}
	var grants map[string]executioncustody.CallerGrant
	decoder = json.NewDecoder(strings.NewReader(os.Getenv("TECHSTACK_ADVANCED_ISSUER_CALLER_GRANTS")))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&grants) != nil || len(grants) != 1 || decoder.Decode(new(any)) != io.EOF {
		return fail("OWNER")
	}
	var grant executioncustody.CallerGrant
	for _, candidate := range grants {
		grant = candidate
	}
	if grant.Caller != "techstack-preview" || !testidentity.IsTestSubject(grant.OwnerID) || grant.TenantID != testidentity.OwnerTenant(grant.OwnerID) ||
		grant.DeploymentAdmission != "owned-records" || grant.OwnerRef != "" || grant.DeploymentIDs == nil || len(grant.DeploymentIDs) != 0 {
		return fail("OWNER")
	}
	ca := os.Getenv("TECHSTACK_EXECUTION_PUBLIC_CA_FILE")
	info, err := os.Lstat(ca)
	if !filepath.IsAbs(ca) || filepath.Clean(ca) != filepath.Join(os.Getenv("RUNNER_TEMP"), "techstack-private-execution", "clickhouse-pg-ca.pem") ||
		err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return fail("CA")
	}
	u, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil || u == nil || u.Scheme != "postgresql" || u.User == nil || u.User.Username() != db.HostedApplicationRole || u.Fragment != "" ||
		u.Query().Get("sslmode") != "verify-full" || len(u.Query()["sslrootcert"]) != 1 ||
		(u.Query().Get("sslrootcert") != "/app/clickhouse-pg-ca.pem" && u.Query().Get("sslrootcert") != "/data/techstack-stg-ca.pem") {
		return fail("DATABASE_BINDING")
	}
	q := u.Query()
	for _, key := range []string{"options", "role", "session_authorization", "default_transaction_read_only"} {
		if q.Has(key) {
			return fail("DATABASE_BINDING")
		}
	}
	q.Set("sslrootcert", ca)
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(parent, 220*time.Second)
	defer cancel()
	pool, err := db.OpenContext(ctx, db.Config{Backend: db.StoreBackendPostgres, DriverName: db.PostgresDriverName, DSN: u.String(), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		return fail("DATABASE_BINDING")
	}
	defer pool.Close()
	if db.VerifyExpectedDatabaseIdentity(ctx, pool.DB) != nil || db.VerifyPrivateExecutionApplicationRole(ctx, pool.DB) != nil || pool.VerifyMigrations(ctx) != nil {
		return fail("APPLICATION_ROLE")
	}
	if _, err = db.VerifyStagingApplicationBinding(ctx, pool.DB); err != nil {
		return fail("DATABASE_BINDING")
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return fail("DATABASE_BINDING")
	}
	defer conn.Close()
	begin := func(operationContext context.Context, readOnly bool) (*sql.Tx, error) {
		tx, e := conn.BeginTx(operationContext, &sql.TxOptions{ReadOnly: readOnly})
		if e != nil {
			return nil, e
		}
		if e = tenantcontext.ApplyTenant(tenantdb.NewContextWithToken(operationContext, input.ContextToken), tx, grant.TenantID); e != nil {
			tx.Rollback()
			return nil, e
		}
		var owner, tenant, key string
		var expires time.Time
		if e = tx.QueryRowContext(operationContext, `SELECT subject,org_id,key_id,expires_at FROM kombify_ctx.verified_context()`).Scan(&owner, &tenant, &key, &expires); e != nil ||
			owner != grant.OwnerID || tenant != grant.TenantID || key != "techstack-stage-context-g1" || !expires.After(time.Now()) {
			tx.Rollback()
			return nil, fail("CONTEXT")
		}
		return tx, nil
	}
	var locked bool
	if conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "backup-journey:"+grant.TenantID+":"+input.StackID).Scan(&locked) != nil || !locked {
		return fail("CONCURRENT_JOURNEY")
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "backup-journey:"+grant.TenantID+":"+input.StackID)
	tx, err := begin(ctx, false)
	if err != nil {
		return fail("CONTEXT")
	}
	defer tx.Rollback()
	var fresh bool
	if tx.QueryRowContext(ctx, `SELECT expires_at>statement_timestamp()+interval '230 seconds' FROM kombify_ctx.verified_context()`).Scan(&fresh) != nil || !fresh {
		return fail("CONTEXT_TOO_SHORT")
	}
	var originalNext time.Time
	var originalLast sql.NullTime
	var policy string
	var schedule backupjobs.Schedule
	var cadence, agent string
	// Already-due schedules and active jobs are customer work, never a fixture.
	err = tx.QueryRowContext(ctx, `SELECT b.next_due_at,b.last_run_at,(to_jsonb(b)-'next_due_at'-'last_run_at'-'updated_at')::text,
	 b.cadence,b.hour_utc,b.minute_utc,b.weekday_utc FROM stack_backup_schedules b JOIN stacks s ON s.id=b.stack_id AND s.tenant_id=b.tenant_id
	 WHERE b.tenant_id=$1 AND b.stack_id=$2 AND b.owner_id=$3 AND s.owner_subject_id=$3 AND b.enabled
	 AND b.next_due_at>statement_timestamp()+interval '5 minutes'
	 AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.tenant_id=b.tenant_id AND j.stack_id=b.stack_id AND j.state IN ('pending','running'))
	 FOR UPDATE OF b`, grant.TenantID, input.StackID, grant.OwnerID).Scan(&originalNext, &originalLast, &policy, &cadence, &schedule.HourUTC, &schedule.MinuteUTC, &schedule.WeekdayUTC)
	if err != nil {
		return fail("EXISTING_IDLE_OWNED_SCHEDULE_REQUIRED")
	}
	schedule.Cadence = backupjobs.Cadence(cadence)
	err = tx.QueryRowContext(ctx, `SELECT min(id) FROM workers WHERE tenant_id=$1 AND stack_id=$2 AND owner_subject_id=$3 AND approved
	 AND last_seen_at>statement_timestamp()-interval '90 seconds' HAVING count(*)=1`, grant.TenantID, input.StackID, grant.OwnerID).Scan(&agent)
	if err != nil || agent == "" {
		return fail("CONNECTED_STAGE_GUARD_REQUIRED")
	}
	var slot time.Time
	if tx.QueryRowContext(ctx, `SELECT date_trunc('microseconds',statement_timestamp())`).Scan(&slot) != nil {
		return fail("CLOCK")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE stack_backup_schedules SET next_due_at=$3 WHERE tenant_id=$1 AND stack_id=$2`, grant.TenantID, input.StackID, slot); err != nil {
		return fail("SCHEDULE")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("backup.schedule/v1:%d:%s%d:%s:%s", len(grant.TenantID), grant.TenantID, len(input.StackID), input.StackID, slot.UTC().Format(time.RFC3339Nano))))
	jobID := fmt.Sprintf("backup-%x", digest)
	// Install cleanup before commit: an ambiguous commit also requires inspection.
	restored, restorationAttempted := false, false
	sameLast := func(a, b sql.NullTime) bool { return a.Valid == b.Valid && (!a.Valid || a.Time.Equal(b.Time)) }
	restore := func() error {
		restorationAttempted = true
		// A cancelled read does not waive tenant authority or suppress cleanup.
		// The same actual SQL token must still be fresh; no expiry bypass exists.
		cleanupContext, cleanupCancel := context.WithTimeout(context.WithoutCancel(parent), 20*time.Second)
		defer cleanupCancel()
		cleanup, e := begin(cleanupContext, false)
		if e != nil {
			return fail("RESTORE_CONTEXT")
		}
		defer cleanup.Rollback()
		var next time.Time
		var last sql.NullTime
		var currentPolicy string
		if cleanup.QueryRowContext(cleanupContext, `SELECT next_due_at,last_run_at,(to_jsonb(b)-'next_due_at'-'last_run_at'-'updated_at')::text FROM stack_backup_schedules b WHERE tenant_id=$1 AND stack_id=$2 FOR UPDATE`, grant.TenantID, input.StackID).Scan(&next, &last, &currentPolicy) != nil || currentPolicy != policy {
			return fail("RESTORE_CONCURRENT_CHANGE")
		}
		untouched := next.Equal(slot) && sameLast(last, originalLast)
		advanced := last.Valid && !last.Time.Before(slot) && last.Time.Before(slot.Add(190*time.Second)) && next.Equal(schedule.NextDue(last.Time))
		already := next.Equal(originalNext) && sameLast(last, originalLast)
		if !untouched && !advanced && !already {
			return fail("RESTORE_CONCURRENT_CHANGE")
		}
		if _, e = cleanup.ExecContext(cleanupContext, `UPDATE stack_backup_schedules SET next_due_at=$3,last_run_at=$4 WHERE tenant_id=$1 AND stack_id=$2`, grant.TenantID, input.StackID, originalNext, originalLast); e != nil {
			return fail("RESTORE_WRITE")
		}
		if e = cleanup.Commit(); e != nil {
			return fail("RESTORE_UNKNOWN")
		}
		readback, e := begin(cleanupContext, true)
		if e != nil {
			return fail("RESTORE_READBACK")
		}
		defer readback.Rollback()
		if readback.QueryRowContext(cleanupContext, `SELECT next_due_at,last_run_at,(to_jsonb(b)-'next_due_at'-'last_run_at'-'updated_at')::text FROM stack_backup_schedules b WHERE tenant_id=$1 AND stack_id=$2`, grant.TenantID, input.StackID).Scan(&next, &last, &currentPolicy) != nil || !next.Equal(originalNext) || !sameLast(last, originalLast) || currentPolicy != policy {
			return fail("RESTORE_READBACK")
		}
		restored = true
		return nil
	}
	defer func() {
		if !restorationAttempted {
			if e := restore(); e != nil {
				returnErr = e
			}
		}
	}()
	if tx.Commit() != nil {
		return fail("SCHEDULE_EFFECT_UNKNOWN")
	}
	deadline := time.Now().Add(180 * time.Second)
	claimed := false
	var evidence stackkitcommand.BackupRunEvidence
	var resultDigest string
	for time.Now().Before(deadline) {
		read, e := begin(ctx, true)
		if e != nil {
			return fail("CONTEXT")
		}
		var state, owner string
		var lease sql.NullTime
		e = read.QueryRowContext(ctx, `SELECT state,COALESCE(execution_owner_id,''),execution_lease_expires_at FROM jobs WHERE tenant_id=$1 AND stack_id=$2 AND id=$3 AND type='backup' AND payload_json->>'owner_id'=$4`, grant.TenantID, input.StackID, jobID, grant.OwnerID).Scan(&state, &owner, &lease)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			read.Rollback()
			return fail("JOB_READ")
		}
		if state == "running" && lease.Valid && lease.Time.After(time.Now()) {
			if fmt.Sprintf("%x", sha256.Sum256([]byte(owner))) != input.ExecutionOwnerSHA256 {
				read.Rollback()
				return fail("FOREIGN_EXECUTION_OWNER")
			}
			claimed = true
		}
		if state == "failed" || state == "cancelled" {
			read.Rollback()
			return fail("JOB_FAILED")
		}
		if state == "completed" {
			var commandJSON, resultJSON []byte
			e = read.QueryRowContext(ctx, `SELECT c.command_json,c.result_json,c.result_digest FROM typed_agent_commands c
			 JOIN typed_agent_command_revisions r ON r.command_id=c.command_id AND r.tenant_id=c.tenant_id AND r.revision=c.result_revision
			 WHERE c.tenant_id=$1 AND c.command_id=$2 AND c.agent_id=$3 AND c.state='completed' AND c.command_family='stackkit'
			 AND c.created_at >= $4 AND c.dispatched_at IS NOT NULL AND c.acked_at IS NOT NULL
			 AND c.worker_generation>0 AND c.result_generation=c.worker_generation AND r.worker_generation=c.worker_generation
			 AND r.state='terminal' AND NOT r.truncated AND r.digest=c.result_digest AND r.result_json=c.result_json`, grant.TenantID, jobID, agent, slot).Scan(&commandJSON, &resultJSON, &resultDigest)
			read.Rollback()
			command, result := new(agentpb.StackKitCommand), new(agentpb.StackKitResult)
			if e != nil || !claimed || protojson.Unmarshal(commandJSON, command) != nil || protojson.Unmarshal(resultJSON, result) != nil || command.CommandId != jobID || stackkitcommand.ValidateResult(result, command) != nil {
				return fail("TYPED_COMMAND_EVIDENCE")
			}
			evidence, e = stackkitcommand.ParseBackupRunEvidence(command, result)
			if e != nil {
				return fail("SNAPSHOT_EVIDENCE")
			}
			break
		}
		read.Rollback()
		select {
		case <-ctx.Done():
			return fail("TIMEOUT")
		case <-time.After(250 * time.Millisecond):
		}
	}
	if evidence.SnapshotAnchorID == "" {
		return fail("JOURNEY_TIMEOUT")
	}
	if err = restore(); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema": "techstack.private-backup-journey/v1", "status": "verified", "source_sha": buildRevision, "contract_digest": compiledBackupRuntimeContractDigest,
		"stage_owner_subject_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(grant.OwnerID))), "stack_id": input.StackID, "job_id": jobID,
		"execution_owner_sha256": input.ExecutionOwnerSHA256, "command_result_digest": resultDigest, "snapshot_anchor_id": evidence.SnapshotAnchorID,
		"snapshot_operation_id": evidence.OperationID, "snapshot_plan_hash": evidence.PlanHash, "schedule_slot": slot.UTC(),
		"completed_at": time.Now().UTC(), "durable_claim_qualified": claimed, "typed_command_completed": true, "snapshot_recorded": true,
		"owned_schedule_restored": restored, "application_data_restored": false,
	})
}
