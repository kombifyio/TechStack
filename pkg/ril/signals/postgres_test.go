package signals

import (
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresOutboxEmitDedupeKeepsTenantServerAndCorrelation(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	observation := testObservation(now)
	_, envelope, err := normalizeObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"signalId":"` + envelope.SignalID + `","actionCardId":"` + envelope.ActionCardID + `","tenantId":"tenant-1","serverId":"server-1","source":"health","severity":"high","priority":"high","receivedAt":"2026-08-03T12:00:00Z","traceId":"trace-1","auditId":"audit-1"}`

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)WITH authorized_server AS .*FROM servers.*tenant_id=\$1 AND id=\$2.*owner_subject_id=\$12.*ON CONFLICT \(tenant_id, dedupe_key\) DO NOTHING`).
		WithArgs("tenant-1", "server-1", envelope.SignalID, "health:server-1:api", SourceHealth, SeverityHigh, PriorityHigh, "trace-1", "audit-1", sqlmock.AnyArg(), envelope.ActionCardID, "").
		WillReturnRows(sqlmock.NewRows([]string{"sequence_id", "envelope_json", "attempts", "created_at", "inserted"}).AddRow(7, payload, 0, now, true))
	mock.ExpectCommit()

	record, inserted, err := NewPostgresOutbox(database).Emit(t.Context(), observation)
	if err != nil || !inserted {
		t.Fatalf("Emit = inserted:%t error:%v", inserted, err)
	}
	if record.SequenceID != 7 || record.Envelope.TenantID != "tenant-1" || record.Envelope.ServerID != "server-1" ||
		record.Envelope.TraceID != "trace-1" || record.Envelope.AuditID != "audit-1" {
		t.Fatalf("record = %+v", record)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresOutboxDoesNotEmitForUnauthorizedServer(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)WITH authorized_server AS .*FROM servers.*tenant_id=\$1 AND id=\$2`).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, _, err = NewPostgresOutbox(database).Emit(t.Context(), testObservation(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)))
	if !errors.Is(err, ErrServerUnauthorized) {
		t.Fatalf("Emit error = %v, want ErrServerUnauthorized", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresOutboxResolvesCanonicalServerOwnerWithinTenantRLS(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT COALESCE\(owner_subject_id, ''\).*FROM servers.*tenant_id=\$1 AND id=\$2`).
		WithArgs("tenant-1", "server-1").
		WillReturnRows(sqlmock.NewRows([]string{"owner_subject_id"}).AddRow("auth0|owner-1"))
	mock.ExpectCommit()
	owner, err := NewPostgresOutbox(database).ResolveServerOwner(t.Context(), "tenant-1", "server-1")
	if err != nil || owner != "auth0|owner-1" {
		t.Fatalf("ResolveServerOwner = %q, %v", owner, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresOutboxClaimAndRetryUseDBTimeAndFencing(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	payload := `{"signalId":"signal-1","actionCardId":"ril-action-card:signal-1","tenantId":"tenant-1","serverId":"server-1","source":"health","severity":"high","priority":"high","receivedAt":"2026-08-03T12:00:00Z","traceId":"trace-1","auditId":"audit-1"}`

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)WITH candidate AS .*clock_timestamp.*FOR UPDATE SKIP LOCKED.*UPDATE ril_signal_outbox`).
		WithArgs("tenant-1", "ril-publisher/one", sqlmock.AnyArg(), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"sequence_id", "envelope_json", "attempts", "created_at", "claim_generation", "claim_owner", "claim_expires_at"}).
			AddRow(9, payload, 3, now, 2, "ril-publisher/one", now.Add(30*time.Second)))
	mock.ExpectCommit()

	outbox := NewPostgresOutbox(database)
	claim, err := outbox.Claim(t.Context(), "tenant-1", "ril-publisher/one", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Token == "" || claim.Attempts != 3 || claim.Generation != 2 {
		t.Fatalf("claim = %+v", claim)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE ril_signal_outbox.*clock_timestamp\(\).*claim_generation=\$4.*claim_token_digest=\$6`).
		WithArgs("tenant-1", int64(9), "signal-1", int64(2), "ril-publisher/one", sqlmock.AnyArg(), false, int64(8), "gateway unavailable").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := outbox.Retry(t.Context(), claim, errors.New("gateway unavailable")); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func testObservation(now time.Time) Observation {
	return Observation{
		DedupeKey: "health:server-1:api", TenantID: "tenant-1", ServerID: "server-1",
		Source: SourceHealth, Severity: SeverityHigh, TraceID: "trace-1", AuditID: "audit-1", ReceivedAt: now,
	}
}
