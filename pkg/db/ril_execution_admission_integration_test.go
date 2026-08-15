package db

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/google/uuid"
)

func TestIntegrationRILExecutionAdmissionCommitsCurrentHeadsAtomically(t *testing.T) {
	database := openTestDB(t)
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate execution admission schema: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	leaseValidUntil := now.Add(2 * time.Minute)
	suffix := uuid.NewString()
	tenantID, ownerID := "tenant-ril-admission-"+suffix, "owner-ril-admission-"+suffix
	stackID, serverID := "stack-ril-admission-"+suffix, "server-ril-admission-"+suffix
	leaseID, cardID := "lease-ril-admission-"+suffix, "card-ril-admission-"+suffix
	workerID, generationID := "worker-ril-admission-"+suffix, uuid.NewString()

	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO techstack_tenants (id, display_name, kind, status)
		VALUES ($1, $1, 'saas', 'active')
	`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO stacks (id, tenant_id, owner_subject_id, name, status)
		VALUES ($1, $2, $3, $1, 'active')
	`, stackID, tenantID, ownerID); err != nil {
		t.Fatalf("seed stack: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO workers (id, tenant_id, hostname, status, approved, owner_subject_id)
		VALUES ($1, $2, $1, 'online', true, $3)
	`, workerID, tenantID, ownerID); err != nil {
		t.Fatalf("seed current runtime worker: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO servers (
			id, tenant_id, stack_id, owner_subject_id, worker_id, lease_id, name,
			lifecycle_state, desired_state, connection_state, health_state,
			inventory_revision, revision, generation
		) VALUES ($1, $2, $3, $4, $5, $6, $1,
			'active', 'running', 'connected', 'unhealthy', 7, 11, 3)
	`, serverID, tenantID, stackID, ownerID, workerID, leaseID); err != nil {
		t.Fatalf("seed runtime inventory head: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO techstack_vm_leases (
			id, tenant_id, subject_id, org_id, provider_id, desired_state,
			idempotency_key, lease_json, lease_revision, owner_subject_id,
			server_id, resource_generation_id, valid_from, valid_until, renewed_at
		) VALUES (
			$1, $2, $3, $2, 'ionos', 'running', $1 || '-idempotency',
			jsonb_build_object(
				'id', $1::text, 'revision', 4, 'tenant_id', $2::text,
				'owner_id', $3::text, 'server_id', $4::text,
				'resource_generation_id', $5::text, 'desired_state', 'running',
				'valid_from', $6::timestamptz, 'valid_until', $7::timestamptz,
				'resource', jsonb_build_object('provider_id', 'ionos'),
				'metadata', jsonb_build_object('resource_generation_id', $5::text)
			),
			4, $3, $4, $5::uuid, $6, $7, $6
		)
	`, leaseID, tenantID, ownerID, serverID, generationID, now.Add(-time.Minute), leaseValidUntil); err != nil {
		t.Fatalf("seed typed RuntimeLease: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit admission fixture: %v", err)
	}

	authority := actions.NewPostgresAuthority(database.DB)
	template := actions.ActionTemplate{
		StackID: stackID,
		Primitive: rilaction.PrimitiveBinding{
			ID: "verify-stackkit-state", ContractHash: integrationDigest("1"), OperationClass: "verification",
		},
		ResolvedPlanHash: integrationDigest("2"),
		Grant: &rilaction.GrantBinding{
			BindingRef: "grant:" + suffix, BindingHash: integrationDigest("3"), Audience: "stackkits",
			Scopes: []string{"stackkit-verify"}, GrantedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			ValidUntil: now.Add(10 * time.Minute).Format(time.RFC3339Nano),
		},
		Target: rilaction.TargetBinding{
			Scope: rilaction.TargetScopeRuntimeInstance, SiteRef: "site-1", NodeRef: "node-1",
			RuntimeInstanceRef: workerID, ExecutionChannelRef: "host-channel-node-1",
		},
		EvidenceSinkRef: "evidence:" + cardID,
	}
	if _, err := authority.Create(t.Context(), actions.CreateGovernedCard{
		ID: cardID, TenantID: tenantID, OwnerSubjectID: ownerID, ServerID: serverID,
		Title: "Verify current runtime", Template: template,
	}); err != nil {
		t.Fatalf("create governed card: %v", err)
	}
	if _, err := authority.Approve(t.Context(), tenantID, ownerID, cardID, "audit-"+suffix, now); err != nil {
		t.Fatalf("approve governed card: %v", err)
	}

	if _, err := database.ExecContext(t.Context(), `UPDATE servers SET connection_state='offline' WHERE tenant_id=$1 AND id=$2`, tenantID, serverID); err != nil {
		t.Fatalf("make current server non-runnable: %v", err)
	}
	beginInput := actions.BeginExecution{
		TenantID: tenantID, OwnerSubjectID: ownerID, CardID: cardID,
		ExecutionID: "execution-" + suffix, TraceID: "trace-" + suffix,
		IdempotencyKey: "idempotency-" + suffix, Now: now,
	}
	if _, err := authority.Begin(t.Context(), beginInput); !errors.Is(err, actions.ErrExecutionAdmission) {
		t.Fatalf("non-runnable current server admission error = %v, want ErrExecutionAdmission", err)
	}
	var status string
	var admissionDigest *string
	if err := database.QueryRowContext(t.Context(), `SELECT status, execution_admission_digest FROM ril_action_cards WHERE id=$1`, cardID).Scan(&status, &admissionDigest); err != nil {
		t.Fatalf("read rolled-back card: %v", err)
	}
	if status != "approved" || admissionDigest != nil {
		t.Fatalf("rejected admission mutated card: status=%q digest=%v", status, admissionDigest)
	}

	if _, err := database.ExecContext(t.Context(), `UPDATE servers SET connection_state='connected' WHERE tenant_id=$1 AND id=$2`, tenantID, serverID); err != nil {
		t.Fatalf("restore runnable current server: %v", err)
	}
	begin, err := authority.Begin(t.Context(), beginInput)
	if err != nil {
		t.Fatalf("admit exact current inventory and lease heads: %v", err)
	}
	if begin.Admission.InventoryRevision != 7 || begin.Admission.ServerRevision != 11 ||
		begin.Admission.ServerGeneration != 3 || begin.Admission.LeaseID != leaseID ||
		begin.Admission.LeaseRevision != 4 || begin.Admission.ResourceGenerationID != generationID {
		t.Fatalf("persisted admission does not match current heads: %#v", begin.Admission)
	}
	if begin.Request.ValidUntil != leaseValidUntil.Format(time.RFC3339Nano) {
		t.Fatalf("admitted request validity = %q, want lease boundary %q", begin.Request.ValidUntil, leaseValidUntil.Format(time.RFC3339Nano))
	}
	var gotInventoryRevision, gotServerRevision, gotServerGeneration, gotLeaseRevision int64
	var gotLeaseID, gotGenerationID, gotDigest string
	if err := database.QueryRowContext(t.Context(), `
		SELECT admission_inventory_revision, admission_server_revision,
		       admission_server_generation, admission_lease_id,
		       admission_lease_revision, admission_resource_generation_id::text,
		       execution_admission_digest
		FROM ril_action_cards WHERE id=$1
	`, cardID).Scan(&gotInventoryRevision, &gotServerRevision, &gotServerGeneration,
		&gotLeaseID, &gotLeaseRevision, &gotGenerationID, &gotDigest); err != nil {
		t.Fatalf("read committed execution admission: %v", err)
	}
	if gotInventoryRevision != 7 || gotServerRevision != 11 || gotServerGeneration != 3 ||
		gotLeaseID != leaseID || gotLeaseRevision != 4 || gotGenerationID != generationID ||
		gotDigest != begin.Admission.Digest {
		t.Fatalf("card admission tuple = inventory:%d server:%d/%d lease:%s/%d generation:%s digest:%s",
			gotInventoryRevision, gotServerRevision, gotServerGeneration, gotLeaseID, gotLeaseRevision, gotGenerationID, gotDigest)
	}

	evidence := rilaction.Evidence{
		ExecutionID: beginInput.ExecutionID, TraceID: beginInput.TraceID, Status: "succeeded",
	}
	if _, err := authority.Complete(t.Context(), tenantID, cardID, evidence, "", now.Add(time.Second)); err != nil {
		t.Fatalf("complete governed card: %v", err)
	}

	deniedCardID := "card-ril-denied-" + suffix
	if _, err := authority.Create(t.Context(), actions.CreateGovernedCard{
		ID: deniedCardID, TenantID: tenantID, OwnerSubjectID: ownerID, ServerID: serverID,
		Title: "Denied action", Template: template,
	}); err != nil {
		t.Fatalf("create denied governed card: %v", err)
	}
	if _, err := authority.Deny(t.Context(), tenantID, ownerID, deniedCardID, "audit-denied-"+suffix, now); err != nil {
		t.Fatalf("deny governed card: %v", err)
	}
	deniedBegin := beginInput
	deniedBegin.CardID = deniedCardID
	deniedBegin.ExecutionID = "execution-denied-" + suffix
	deniedBegin.IdempotencyKey = "idempotency-denied-" + suffix
	if _, err := authority.Begin(t.Context(), deniedBegin); !errors.Is(err, actions.ErrApprovalRequired) {
		t.Fatalf("denied card execution error = %v, want ErrApprovalRequired", err)
	}

	rows, err := database.QueryContext(t.Context(), `
		SELECT action_card_id, COALESCE(from_status, ''), to_status, audit_correlation_id
		FROM ril_action_transition_audit
		WHERE tenant_id=$1 AND action_card_id IN ($2, $3)
		ORDER BY sequence_id
	`, tenantID, cardID, deniedCardID)
	if err != nil {
		t.Fatalf("read transition audit: %v", err)
	}
	defer rows.Close()
	transitions := make(map[string][]string)
	var firstAuditSequenceID int64
	for rows.Next() {
		var gotCardID, from, to, correlation string
		if err := rows.Scan(&gotCardID, &from, &to, &correlation); err != nil {
			t.Fatalf("scan transition audit: %v", err)
		}
		if strings.TrimSpace(correlation) == "" {
			t.Fatalf("transition %s -> %s omitted audit correlation", from, to)
		}
		transitions[gotCardID] = append(transitions[gotCardID], from+"->"+to)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantExecuted := []string{"->awaiting_approval", "awaiting_approval->approved", "approved->executing", "executing->completed"}
	wantDenied := []string{"->awaiting_approval", "awaiting_approval->denied"}
	if strings.Join(transitions[cardID], ",") != strings.Join(wantExecuted, ",") ||
		strings.Join(transitions[deniedCardID], ",") != strings.Join(wantDenied, ",") {
		t.Fatalf("transition audit executed=%v denied=%v", transitions[cardID], transitions[deniedCardID])
	}
	if err := database.QueryRowContext(t.Context(), `SELECT MIN(sequence_id) FROM ril_action_transition_audit WHERE tenant_id=$1`, tenantID).Scan(&firstAuditSequenceID); err != nil {
		t.Fatalf("read transition audit sequence: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `UPDATE ril_action_transition_audit SET to_status='tampered' WHERE sequence_id=$1`, firstAuditSequenceID); err == nil {
		t.Fatal("append-only transition audit accepted mutation")
	}
}

func integrationDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
