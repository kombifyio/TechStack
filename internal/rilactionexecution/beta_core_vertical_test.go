package rilactionexecution

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimeinventory"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimelease"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/stackaction"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
)

func TestBetaCoreInventoryApprovalLeasePinnedCLIVerifyDurableEvidence(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	inventory := runtimeinventory.ServerList{
		ObservedAt:        now,
		InventoryRevision: 7,
		Servers: []runtimeinventory.Server{{
			ID: "server-1", StackID: "stack-1", Name: "windows-runtime-1",
			Platform:   runtimeinventory.Platform{OS: "windows", Arch: "amd64"},
			Connection: runtimeinventory.ObservedState{State: "connected", ObservedAt: &now},
		}},
	}
	if len(inventory.Servers) != 1 || inventory.Servers[0].Platform.OS != "windows" || inventory.Servers[0].Connection.State != "connected" {
		t.Fatalf("connected Windows runtime missing from inventory: %#v", inventory)
	}

	lease := runtimelease.Lease{
		ID: "lease-1", Revision: 4, TenantID: "tenant-1", OwnerID: "owner-1",
		ServerID:             runtimelease.RuntimeServerID(inventory.Servers[0].ID),
		ResourceGenerationID: runtimelease.ResourceGenerationID("a58debb7-0d79-4a0f-b20d-bdf09b67d790"),
		DesiredState:         runtimelease.DesiredStateRunning,
		ValidFrom:            now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
	}
	if err := lease.Validate(now); err != nil {
		t.Fatalf("runtime lease is not valid: %v", err)
	}

	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).
		WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,tenant_id,owner_subject_id,[\s\S]+FROM ril_action_cards[\s\S]+FOR UPDATE`).
		WithArgs("tenant-1", "owner-1", "action-card-1").
		WillReturnRows(verticalGovernedCardRow(t, now, "approved", false))
	mock.ExpectQuery(`SELECT server\.inventory_revision, server\.revision, server\.generation,[\s\S]+FOR UPDATE OF server, lease`).
		WithArgs("tenant-1", "owner-1", "server-1", "stack-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"inventory_revision", "server_revision", "server_generation", "worker_id",
			"lifecycle_state", "server_desired_state", "connection_state",
			"lease_id", "lease_revision", "resource_generation_id",
			"desired_state", "valid_from", "valid_until", "cancelled_at",
		}).AddRow(inventory.InventoryRevision, int64(11), int64(3), "agent-1", "active", "running", "connected", string(lease.ID), int64(lease.Revision), string(lease.ResourceGenerationID), string(lease.DesiredState), lease.ValidFrom, lease.ValidUntil, nil))
	mock.ExpectQuery(`UPDATE ril_action_cards SET status='executing'[\s\S]+execution_admission_digest=\$15[\s\S]+RETURNING`).
		WithArgs("tenant-1", "owner-1", "action-card-1", sqlmock.AnyArg(), "execution-1", "trace-000000000001", "idempotency-000001", now, inventory.InventoryRevision, int64(11), int64(3), string(lease.ID), int64(lease.Revision), string(lease.ResourceGenerationID), sqlmock.AnyArg()).
		WillReturnRows(verticalGovernedCardRow(t, now, "executing", true))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ril_action_transition_audit")).
		WithArgs("tenant-1", "action-card-1", "approved", "executing", "trace-000000000001", "owner-1", "execution-1", "trace-000000000001", now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	begin, err := actions.NewPostgresAuthority(database).Begin(t.Context(), actions.BeginExecution{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", CardID: "action-card-1",
		ExecutionID: "execution-1", TraceID: "trace-000000000001",
		IdempotencyKey: "idempotency-000001", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if begin.Admission.InventoryRevision != inventory.InventoryRevision || begin.Admission.ServerRevision != 11 || begin.Admission.ServerGeneration != 3 || begin.Admission.LeaseID != string(lease.ID) ||
		begin.Admission.LeaseRevision != lease.Revision || begin.Admission.ResourceGenerationID != string(lease.ResourceGenerationID) {
		t.Fatalf("authority did not bind current inventory and lease: %#v", begin.Admission)
	}
	request := begin.Request

	t.Run("denied authority never obtains execution lease", func(t *testing.T) {
		denied := request
		denied.Approval.Decision = "denied"
		ledger := &testLedger{reservation: rilaction.LedgerReservation{
			Disposition: rilaction.LedgerAcquired, ReservationToken: "reservation-000000001",
		}}
		dispatcher := &pinnedEvidenceDispatcher{pinned: &PinnedCLIDispatcher{now: func() time.Time { return now }}}
		service, err := New(Config{Ledger: ledger, Dispatcher: dispatcher, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Execute(t.Context(), denied, begin.Admission.Digest); err == nil {
			t.Fatal("denied approval reached execution")
		}
		if dispatcher.calls != 0 || ledger.completeCalls != 0 {
			t.Fatalf("denied action dispatched=%d completed=%d", dispatcher.calls, ledger.completeCalls)
		}
	})

	operation, err := pinnedCLIStackActionOperation(stackaction.ActionVerifyRollout)
	if err != nil || operation != agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY {
		t.Fatalf("StackAction did not map to typed pinned CLI VERIFY: operation=%v err=%v", operation, err)
	}

	ledger := &testLedger{reservation: rilaction.LedgerReservation{
		Disposition: rilaction.LedgerAcquired, ReservationToken: "reservation-000000001",
	}}
	dispatcher := &pinnedEvidenceDispatcher{pinned: &PinnedCLIDispatcher{now: func() time.Time { return now }}}
	service, err := New(Config{Ledger: ledger, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := service.Execute(t.Context(), request, begin.Admission.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.calls != 1 || ledger.completeCalls != 1 || evidence.Status != "succeeded" {
		t.Fatalf("approved execution calls=%d completions=%d evidence=%#v", dispatcher.calls, ledger.completeCalls, evidence)
	}

	ledger.reservation = rilaction.LedgerReservation{Disposition: rilaction.LedgerReplay, Evidence: &ledger.completion.Evidence}
	replayed, err := service.Execute(t.Context(), request, begin.Admission.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.calls != 1 || ledger.completeCalls != 1 || replayed.EvidenceID != evidence.EvidenceID {
		t.Fatalf("idempotent replay dispatched=%d completed=%d evidence=%#v", dispatcher.calls, ledger.completeCalls, replayed)
	}

	encoded, err := json.Marshal(ledger.completion.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"provider", "credential", "secret", "stderr", "command_result", "raw_log"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("durable evidence exposed %q: %s", forbidden, encoded)
		}
	}
	if evidence.ProtectedDiagnosticRef != "" {
		t.Fatalf("successful public evidence exposed a diagnostic reference: %#v", evidence)
	}
	failed, err := dispatcher.pinned.evidence(request, false)
	if err != nil || !strings.HasPrefix(failed.ProtectedDiagnosticRef, "diagnostic:") {
		t.Fatalf("failed evidence did not retain only an opaque diagnostic reference: %#v err=%v", failed, err)
	}
	if ledger.completion.AdmissionDigest != begin.Admission.Digest {
		t.Fatalf("durable execution lost admission digest: completion=%#v admission=%#v", ledger.completion, begin.Admission)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type pinnedEvidenceDispatcher struct {
	pinned *PinnedCLIDispatcher
	calls  int
}

func (d *pinnedEvidenceDispatcher) Execute(_ context.Context, request rilaction.Request) (rilaction.Evidence, error) {
	d.calls++
	return d.pinned.evidence(request, true)
}

var _ Dispatcher = (*pinnedEvidenceDispatcher)(nil)

func verticalGovernedCardRow(t *testing.T, now time.Time, status string, admitted bool) *sqlmock.Rows {
	t.Helper()
	template := actions.ActionTemplate{
		StackID:          "stack-1",
		Primitive:        rilaction.PrimitiveBinding{ID: "verify-stackkit-state", ContractHash: testDigest("1"), OperationClass: "verification"},
		ResolvedPlanHash: testDigest("2"),
		Grant: &rilaction.GrantBinding{
			BindingRef: "grant:binding-1", BindingHash: testDigest("4"), Audience: "stackkits", Scopes: []string{"stackkit-verify"},
			GrantedAt: testTime(now.Add(-time.Minute)), ValidUntil: testTime(now.Add(10 * time.Minute)),
		},
		Target: rilaction.TargetBinding{
			Scope: rilaction.TargetScopeRuntimeInstance, SiteRef: "site-1", NodeRef: "node-1",
			RuntimeInstanceRef: "agent-1", ExecutionChannelRef: "host-channel-node-1",
		},
		EvidenceSinkRef: "evidence:action-card-1",
	}
	approval := actions.ApprovalRecord{
		ReceiptRef: "approval:action-card-1", ReceiptHash: testDigest("3"), ActorSubjectID: "owner-1",
		Class: rilaction.ApprovalClassOwnerStepUp, AuditCorrelation: "audit-000000000001",
		ApprovedAt: now.Add(-time.Minute), ValidUntil: now.Add(10 * time.Minute),
	}
	templateJSON, _ := json.Marshal(template)
	approvalJSON, _ := json.Marshal(approval)
	var executionID, idempotencyKey, traceID, leaseID, generationID, admissionDigest string
	var executionStartedAt, inventoryRevision, serverRevision, serverGeneration, leaseRevision any
	if admitted {
		executionID, idempotencyKey, traceID = "execution-1", "idempotency-000001", "trace-000000000001"
		executionStartedAt, inventoryRevision, serverRevision, serverGeneration, leaseRevision = now, int64(7), int64(11), int64(3), int64(4)
		leaseID, generationID, admissionDigest = "lease-1", "a58debb7-0d79-4a0f-b20d-bdf09b67d790", testAdmissionDigest
	}
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "owner_subject_id", "server_id", "title", "severity", "status",
		"action_template_json", "approval_json", "execution_id", "idempotency_key", "trace_id",
		"evidence_json", "error_code", "created_at", "updated_at", "approved_at", "denied_at",
		"execution_started_at", "completed_at", "admission_inventory_revision", "admission_server_revision",
		"admission_server_generation", "admission_lease_id",
		"admission_lease_revision", "admission_resource_generation_id", "execution_admission_digest",
	}).AddRow(
		"action-card-1", "tenant-1", "owner-1", "server-1", "Verify StackKit state", "info", status,
		string(templateJSON), string(approvalJSON), executionID, idempotencyKey, traceID, "", "",
		now.Add(-2*time.Minute), now.Add(-time.Minute), now.Add(-time.Minute), nil,
		executionStartedAt, nil, inventoryRevision, serverRevision, serverGeneration, leaseID, leaseRevision, generationID, admissionDigest,
	)
}
