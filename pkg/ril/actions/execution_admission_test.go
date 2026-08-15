package actions

import (
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

const (
	testAdmissionGeneration = "a58debb7-0d79-4a0f-b20d-bdf09b67d790"
	testPersistedAdmission  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestPostgresAuthorityBeginBindsCurrentInventoryAndLease(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	shortLeaseUntil := now.Add(2 * time.Minute)
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).
		WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+governedCardColumns+" FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 FOR UPDATE")).
		WithArgs("tenant-1", "owner-1", "action-card-1").
		WillReturnRows(governedCardRow(t, now, "approved", false))
	mock.ExpectQuery(`SELECT server\.inventory_revision, server\.revision, server\.generation,[\s\S]+FOR UPDATE OF server, lease`).
		WithArgs("tenant-1", "owner-1", "server-1", "stack-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"inventory_revision", "server_revision", "server_generation", "worker_id",
			"lifecycle_state", "server_desired_state", "connection_state",
			"lease_id", "lease_revision", "resource_generation_id",
			"desired_state", "valid_from", "valid_until", "cancelled_at",
		}).AddRow(int64(7), int64(11), int64(3), "agent-1", "active", "running", "connected", "lease-1", int64(4), testAdmissionGeneration, "running", now.Add(-time.Minute), shortLeaseUntil, nil))
	mock.ExpectQuery(`UPDATE ril_action_cards SET status='executing'[\s\S]+execution_admission_digest=\$15[\s\S]+RETURNING`).
		WithArgs("tenant-1", "owner-1", "action-card-1", sqlmock.AnyArg(), "execution-1", "trace-000000000001", "idempotency-000001", now, int64(7), int64(11), int64(3), "lease-1", int64(4), testAdmissionGeneration, sqlmock.AnyArg()).
		WillReturnRows(governedCardRow(t, now, "executing", true))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ril_action_transition_audit")).
		WithArgs("tenant-1", "action-card-1", "approved", "executing", "trace-000000000001", "owner-1", "execution-1", "trace-000000000001", now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := NewPostgresAuthority(database).Begin(t.Context(), BeginExecution{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", CardID: "action-card-1",
		ExecutionID: "execution-1", TraceID: "trace-000000000001",
		IdempotencyKey: "idempotency-000001", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != BeginAcquired || result.Admission.InventoryRevision != 7 ||
		result.Admission.ServerRevision != 11 || result.Admission.ServerGeneration != 3 ||
		result.Admission.LeaseID != "lease-1" || result.Admission.LeaseRevision != 4 ||
		result.Admission.ResourceGenerationID != testAdmissionGeneration ||
		!regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(result.Admission.Digest) {
		t.Fatalf("execution admission = %#v", result.Admission)
	}
	requestDigest, err := rilaction.ComputeRequestDigest(result.Request)
	if err != nil || result.Admission.RequestDigest != requestDigest {
		t.Fatalf("request digest binding = %q, want %q, error=%v", result.Admission.RequestDigest, requestDigest, err)
	}
	if result.Card.ExecutionAdmission == nil || result.Card.ExecutionAdmission.Digest != result.Admission.Digest {
		t.Fatalf("card admission = %#v, result admission = %#v", result.Card.ExecutionAdmission, result.Admission)
	}
	if result.Request.ValidUntil != shortLeaseUntil.Format(time.RFC3339Nano) {
		t.Fatalf("request validity = %q, want locked lease boundary %q", result.Request.ValidUntil, shortLeaseUntil.Format(time.RFC3339Nano))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityBeginRejectsMissingOrInvalidCurrentAdmission(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		rows *sqlmock.Rows
	}{
		{name: "missing inventory lease join", rows: emptyExecutionAdmissionRows()},
		{name: "stale inventory", rows: executionAdmissionRows(now, 0, now.Add(time.Hour), nil)},
		{name: "expired lease", rows: executionAdmissionRows(now, 7, now.Add(-time.Second), nil)},
		{name: "cancelled lease", rows: executionAdmissionRows(now, 7, now.Add(time.Hour), now.Add(-time.Second))},
		{name: "stopped lease", rows: executionAdmissionRowsWithState(now, 7, now.Add(time.Hour), nil, "active", "running", "connected", "stopped", "agent-1")},
		{name: "inactive server", rows: executionAdmissionRowsWithState(now, 7, now.Add(time.Hour), nil, "failed", "running", "connected", "running", "agent-1")},
		{name: "server absent desired", rows: executionAdmissionRowsWithState(now, 7, now.Add(time.Hour), nil, "active", "absent", "connected", "running", "agent-1")},
		{name: "degraded connection", rows: executionAdmissionRowsWithState(now, 7, now.Add(time.Hour), nil, "active", "running", "degraded", "running", "agent-1")},
		{name: "substituted runtime worker", rows: executionAdmissionRowsWithState(now, 7, now.Add(time.Hour), nil, "active", "running", "connected", "running", "agent-other")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).
				WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT "+governedCardColumns+" FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 FOR UPDATE")).
				WithArgs("tenant-1", "owner-1", "action-card-1").
				WillReturnRows(governedCardRow(t, now, "approved", false))
			mock.ExpectQuery(`SELECT server\.inventory_revision, server\.revision, server\.generation,[\s\S]+FOR UPDATE OF server, lease`).
				WithArgs("tenant-1", "owner-1", "server-1", "stack-1").
				WillReturnRows(test.rows)
			mock.ExpectRollback()

			_, err = NewPostgresAuthority(database).Begin(t.Context(), BeginExecution{
				TenantID: "tenant-1", OwnerSubjectID: "owner-1", CardID: "action-card-1",
				ExecutionID: "execution-1", TraceID: "trace-000000000001",
				IdempotencyKey: "idempotency-000001", Now: now,
			})
			if !errors.Is(err, ErrExecutionAdmission) {
				t.Fatalf("error = %v, want ErrExecutionAdmission", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresAuthorityDeniedCardNeverReachesExecutionAdmission(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).
		WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+governedCardColumns+" FROM ril_action_cards WHERE tenant_id=$1 AND owner_subject_id=$2 AND id=$3 FOR UPDATE")).
		WithArgs("tenant-1", "owner-1", "action-card-1").
		WillReturnRows(governedCardRow(t, now, "denied", false))
	mock.ExpectRollback()

	_, err = NewPostgresAuthority(database).Begin(t.Context(), BeginExecution{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", CardID: "action-card-1",
		ExecutionID: "execution-1", TraceID: "trace-000000000001",
		IdempotencyKey: "idempotency-000001", Now: now,
	})
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("error = %v, want ErrApprovalRequired", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func executionAdmissionRows(now time.Time, inventoryRevision int64, validUntil time.Time, cancelledAt any) *sqlmock.Rows {
	return executionAdmissionRowsWithState(now, inventoryRevision, validUntil, cancelledAt, "active", "running", "connected", "running", "agent-1")
}

func executionAdmissionRowsWithState(now time.Time, inventoryRevision int64, validUntil time.Time, cancelledAt any, lifecycleState, serverDesiredState, connectionState, leaseDesiredState, workerID string) *sqlmock.Rows {
	return emptyExecutionAdmissionRows().AddRow(
		inventoryRevision, int64(11), int64(3), workerID, lifecycleState, serverDesiredState, connectionState,
		"lease-1", int64(4), testAdmissionGeneration, leaseDesiredState, now.Add(-time.Minute), validUntil, cancelledAt,
	)
}

func emptyExecutionAdmissionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"inventory_revision", "server_revision", "server_generation", "worker_id",
		"lifecycle_state", "server_desired_state", "connection_state",
		"lease_id", "lease_revision", "resource_generation_id",
		"desired_state", "valid_from", "valid_until", "cancelled_at",
	})
}

func governedCardRow(t *testing.T, now time.Time, status string, admitted bool) *sqlmock.Rows {
	t.Helper()
	template := ActionTemplate{
		StackID:          "stack-1",
		Primitive:        rilaction.PrimitiveBinding{ID: "verify-stackkit-state", ContractHash: digestText("primitive"), OperationClass: "verification"},
		ResolvedPlanHash: digestText("plan"),
		Grant: &rilaction.GrantBinding{
			BindingRef: "grant:binding-1", BindingHash: digestText("grant"), Audience: "stackkits",
			Scopes: []string{"stackkit-verify"}, GrantedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ValidUntil: now.Add(10 * time.Minute).Format(time.RFC3339Nano),
		},
		Target:          rilaction.TargetBinding{Scope: rilaction.TargetScopeRuntimeInstance, SiteRef: "site-1", NodeRef: "node-1", RuntimeInstanceRef: "agent-1", ExecutionChannelRef: "host-channel-node-1"},
		EvidenceSinkRef: "evidence:action-card-1",
	}
	approval := ApprovalRecord{
		ReceiptRef: "approval:action-card-1", ReceiptHash: digestText("approval"),
		ActorSubjectID: "owner-1", Class: rilaction.ApprovalClassOwnerStepUp,
		AuditCorrelation: "audit-000000000001", ApprovedAt: now.Add(-time.Minute), ValidUntil: now.Add(10 * time.Minute),
	}
	templateJSON, _ := json.Marshal(template)
	approvalJSON, _ := json.Marshal(approval)
	var (
		executionID, idempotencyKey, traceID                               string
		executionStartedAt                                                 any
		inventoryRevision, serverRevision, serverGeneration, leaseRevision any
		leaseID, generationID, admissionDigest                             string
	)
	if admitted {
		executionID, idempotencyKey, traceID = "execution-1", "idempotency-000001", "trace-000000000001"
		executionStartedAt = now
		inventoryRevision, serverRevision, serverGeneration, leaseRevision = int64(7), int64(11), int64(3), int64(4)
		leaseID, generationID, admissionDigest = "lease-1", testAdmissionGeneration, testPersistedAdmission
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
