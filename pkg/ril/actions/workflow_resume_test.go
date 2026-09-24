package actions

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

func TestBeginResumesTheSameAdmittedExecutionAfterCheckpointLoss(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	request := rilaction.Request{ExecutionID: "execution-1", TraceID: "trace-000000000001", IdempotencyKey: "idempotency-000001"}
	requestJSON, _ := json.Marshal(request)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id,tenant_id,owner_subject_id,[\s\S]+FROM ril_action_cards[\s\S]+FOR UPDATE`).
		WithArgs("tenant-1", "owner-1", "action-card-1").
		WillReturnRows(governedCardRow(t, now, "executing", true))
	mock.ExpectQuery(`SELECT COALESCE\(execution_request_json::text, ''\) FROM ril_action_cards`).
		WithArgs("tenant-1", "owner-1", "action-card-1").
		WillReturnRows(sqlmock.NewRows([]string{"execution_request_json"}).AddRow(string(requestJSON)))
	mock.ExpectCommit()

	result, err := NewPostgresAuthority(database).Begin(t.Context(), BeginExecution{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", CardID: "action-card-1",
		ExecutionID: "execution-1", TraceID: "trace-000000000001", IdempotencyKey: "idempotency-000001", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != BeginAcquired || result.Request.ExecutionID != "execution-1" || result.Admission.Digest != testPersistedAdmission {
		t.Fatalf("persisted execution was not resumed: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
