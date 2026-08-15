package controlplane

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

func TestPostgresStoreRILActionLedgerAcquiresAndCompletesWithTokenFence(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	request := testLedgerReservationRequest()

	mock.ExpectBegin()
	expectTenantGUC(mock, request.TenantID)
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_action_execution_ledger")).
		WithArgs(request.TenantID, request.IdempotencyKey, request.ExecutionID, request.RequestDigest, request.AdmissionDigest, request.AuditCorrelationID, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"reservation_token"}).AddRow("reservation-000000001"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ril_execution_lease_audit")).
		WithArgs(request.TenantID, request.IdempotencyKey, request.ExecutionID, "acquired", "reservation-000000001", request.AuditCorrelationID, int64(0)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	reservation, err := store.Reserve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Disposition != rilaction.LedgerAcquired || reservation.ReservationToken != "reservation-000000001" {
		t.Fatalf("reservation = %#v", reservation)
	}

	evidence := testLedgerEvidence(request)
	completion := rilaction.LedgerCompletion{
		TenantID: request.TenantID, IdempotencyKey: request.IdempotencyKey,
		ExecutionID: request.ExecutionID, RequestDigest: request.RequestDigest,
		AdmissionDigest:    request.AdmissionDigest,
		AuditCorrelationID: request.AuditCorrelationID,
		ReservationToken:   reservation.ReservationToken, Evidence: evidence,
	}
	mock.ExpectBegin()
	expectTenantGUC(mock, request.TenantID)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE ril_action_execution_ledger")).
		WithArgs(request.TenantID, request.IdempotencyKey, request.ExecutionID, request.RequestDigest, request.AdmissionDigest, reservation.ReservationToken, sqlmock.AnyArg(), request.AuditCorrelationID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ril_execution_lease_audit")).
		WithArgs(request.TenantID, request.IdempotencyKey).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := store.Complete(context.Background(), completion); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreRILActionLedgerRejectsAlreadyExpiredNewLease(t *testing.T) {
	request := testLedgerReservationRequest()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	expectTenantGUC(mock, request.TenantID)
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_action_execution_ledger")).
		WillReturnRows(sqlmock.NewRows([]string{"reservation_token"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT execution_id, request_digest, COALESCE(execution_admission_digest, ''),")).
		WithArgs(request.TenantID, request.IdempotencyKey).
		WillReturnRows(sqlmock.NewRows([]string{"execution_id", "request_digest", "admission_digest", "status", "evidence", "valid_until", "audit_correlation_id", "takeover_count"}))
	mock.ExpectRollback()

	_, err = NewPostgresStore(db).Reserve(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "window expired or unavailable") {
		t.Fatalf("expired reservation error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreRILActionLedgerReturnsConflictOrReplay(t *testing.T) {
	request := testLedgerReservationRequest()
	tests := []struct {
		name string
		row  []driver.Value
		want rilaction.LedgerDisposition
	}{
		{name: "conflict", row: []driver.Value{"execution-other", request.RequestDigest, request.AdmissionDigest, "in-progress", "", time.Now().Add(time.Minute), request.AuditCorrelationID, int64(0)}, want: rilaction.LedgerConflict},
		{name: "replay", row: []driver.Value{request.ExecutionID, request.RequestDigest, request.AdmissionDigest, "completed", string(mustLedgerJSON(t, testLedgerEvidence(request))), time.Now().Add(time.Minute), request.AuditCorrelationID, int64(0)}, want: rilaction.LedgerReplay},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			expectTenantGUC(mock, request.TenantID)
			mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_action_execution_ledger")).WillReturnRows(
				sqlmock.NewRows([]string{"reservation_token"}),
			)
			mock.ExpectQuery(regexp.QuoteMeta("SELECT execution_id, request_digest, COALESCE(execution_admission_digest, ''),")).
				WithArgs(request.TenantID, request.IdempotencyKey).
				WillReturnRows(sqlmock.NewRows([]string{"execution_id", "request_digest", "admission_digest", "status", "evidence", "valid_until", "audit_correlation_id", "takeover_count"}).AddRow(test.row...))
			mock.ExpectCommit()
			got, err := NewPostgresStore(db).Reserve(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if got.Disposition != test.want {
				t.Fatalf("reservation = %#v", got)
			}
			if test.want == rilaction.LedgerReplay && got.Evidence == nil {
				t.Fatal("replay omitted evidence")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func testLedgerReservationRequest() rilaction.LedgerReservationRequest {
	return rilaction.LedgerReservationRequest{
		TenantID: "tenant-1", IdempotencyKey: "idempotency-000001", ExecutionID: "execution-1",
		RequestDigest:      "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		AdmissionDigest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AuditCorrelationID: "trace-000000000001",
		RequestedAt:        "2026-07-22T07:00:00Z", ValidUntil: "2026-07-22T07:04:00Z",
	}
}

func TestPostgresStoreRILActionLedgerExpiredLeaseHasOneWinnerTakeover(t *testing.T) {
	request := testLedgerReservationRequest()
	databaseNow := time.Date(2026, 7, 22, 7, 1, 0, 0, time.UTC)
	request.RequestedAt = databaseNow.Format(time.RFC3339Nano)
	request.ValidUntil = databaseNow.Add(4 * time.Minute).Format(time.RFC3339Nano)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	expectTenantGUC(mock, request.TenantID)
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_action_execution_ledger")).WillReturnRows(sqlmock.NewRows([]string{"reservation_token"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT execution_id, request_digest, COALESCE(execution_admission_digest, ''),")).
		WithArgs(request.TenantID, request.IdempotencyKey).
		WillReturnRows(sqlmock.NewRows([]string{"execution_id", "request_digest", "admission_digest", "status", "evidence", "valid_until", "audit_correlation_id", "takeover_count"}).
			AddRow(request.ExecutionID, request.RequestDigest, request.AdmissionDigest, "in-progress", "", databaseNow.Add(-time.Second), request.AuditCorrelationID, int64(0)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT clock_timestamp()")).WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(databaseNow))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE ril_action_execution_ledger")).
		WithArgs(request.TenantID, request.IdempotencyKey, sqlmock.AnyArg(), databaseNow, databaseNow.Add(4*time.Minute), request.AuditCorrelationID, databaseNow).
		WillReturnRows(sqlmock.NewRows([]string{"reservation_token", "takeover_count"}).AddRow("reservation-takeover-0001", int64(1)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ril_execution_lease_audit")).
		WithArgs(request.TenantID, request.IdempotencyKey, request.ExecutionID, "taken-over", "reservation-takeover-0001", request.AuditCorrelationID, int64(1)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	reservation, err := NewPostgresStore(db).Reserve(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Disposition != rilaction.LedgerAcquired || reservation.ReservationToken != "reservation-takeover-0001" {
		t.Fatalf("takeover reservation = %#v", reservation)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func testLedgerEvidence(request rilaction.LedgerReservationRequest) rilaction.Evidence {
	return rilaction.Evidence{
		APIVersion: rilaction.EvidenceAPIVersionV1, EvidenceID: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		EvidenceSinkRef: "evidence:action-card-1", ActionCardID: "action-card-1", ExecutionID: request.ExecutionID,
		TraceID: "trace-000000000001", TenantID: request.TenantID, StackID: "stack-1",
		PrimitiveID: "verify-stackkit-state", PrimitiveContractHash: "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		ResolvedPlanHash: "sha256:4444444444444444444444444444444444444444444444444444444444444444",
		RequestDigest:    request.RequestDigest, ExecutorRef: "stackkits-governed-state-verifier-v1", TargetRef: "stack:stack-1",
		Status: "succeeded", Verification: rilaction.VerificationEvidence{Kind: "governed-plan-readback", Status: "passed", Checks: []rilaction.VerificationCheck{{ID: "current-resolution", Status: "passed"}}},
		Recovery: rilaction.RecoveryEvidence{Kind: "none", Status: "not-required"}, SummaryCodes: []string{"governed-plan-readback-passed"},
		EvaluatedAt: "2026-07-22T07:00:00Z",
	}
}

func mustLedgerJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
