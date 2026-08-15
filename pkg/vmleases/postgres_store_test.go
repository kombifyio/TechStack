package vmleases

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

type leaseJSONDesiredStateMatcher struct {
	desiredState vmlease.DesiredState
}

func (matcher leaseJSONDesiredStateMatcher) Match(value driver.Value) bool {
	var payload []byte
	switch typed := value.(type) {
	case []byte:
		payload = typed
	case string:
		payload = []byte(typed)
	default:
		return false
	}
	var lease vmlease.Lease
	return json.Unmarshal(payload, &lease) == nil && lease.DesiredState == matcher.desiredState
}

func expectVMLeaseIdempotencyLock(mock sqlmock.Sqlmock, tenantID, key string) {
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`)).
		WithArgs(tenantID, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestPostgresStoreUpsertCreatesImmutableInventory(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.tenant_id', $1, true)`)).WithArgs("org-1").WillReturnResult(sqlmock.NewResult(0, 1))
	expectVMLeaseIdempotencyLock(mock, "org-1", "inventory-1")
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT lease_json::text FROM techstack_vm_leases WHERE tenant_id = $1 AND idempotency_key = $2`)).
		WithArgs("org-1", "inventory-1").WillReturnError(sql.ErrNoRows)
	lease := testLease(time.Now().UTC())
	lease.DesiredState = vmlease.DesiredStateArchived
	mock.ExpectQuery(`INSERT INTO techstack_vm_leases[\s\S]+ON CONFLICT\(id\) DO NOTHING[\s\S]+RETURNING id`).
		WithArgs(lease.ID, "org-1", lease.Subject.ID, lease.Subject.OrgID, lease.Resource.ProviderID,
			lease.Resource.EngineVMID, "absent", "inventory-1", leaseJSONDesiredStateMatcher{desiredState: vmlease.DesiredStateArchived}).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("lease-1"))
	mock.ExpectCommit()

	created, err := NewPostgresStore(db).Upsert(context.Background(), lease, "inventory-1")
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if created.DesiredState != vmlease.DesiredStateArchived {
		t.Fatalf("returned desired state = %q, want archived", created.DesiredState)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreUpsertReturnsExistingGenerationOnIDConflict(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	existing := testLease(now)
	existing.Metadata = map[string]string{MetadataKeyResourceGenerationID: "550e8400-e29b-41d4-a716-446655440000"}
	payload, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	incoming := cloneLease(existing)
	incoming.Metadata[MetadataKeyResourceGenerationID] = "f47ac10b-58cc-4372-a567-0e02b2c3d479"

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.tenant_id', $1, true)`)).WithArgs("org-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO techstack_vm_leases[\s\S]+ON CONFLICT\(id\) DO NOTHING[\s\S]+RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT lease_json::text[\s\S]+FROM techstack_vm_leases[\s\S]+WHERE tenant_id = \$1 AND id = \$2[\s\S]+FOR UPDATE`).
		WithArgs("org-1", vmlease.LeaseID("lease-1")).
		WillReturnRows(sqlmock.NewRows([]string{"lease_json"}).AddRow(payload))
	mock.ExpectCommit()

	got, err := NewPostgresStore(db).Upsert(t.Context(), incoming, "")
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if ResourceGenerationID(*got) != ResourceGenerationID(existing) {
		t.Fatalf("generation = %q, want %q", ResourceGenerationID(*got), ResourceGenerationID(existing))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreUpdateProjectsLifecycleColumnsAtomically(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	existing := testLease(now)
	existing.Metadata = map[string]string{
		MetadataKeyResourceGenerationID: "550e8400-e29b-41d4-a716-446655440000",
	}
	existingPayload, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	updated := cloneLease(existing)
	cancelledAt := now.Add(5 * time.Minute)
	updated.CancelledAt = &cancelledAt
	updated.RenewedAt = cancelledAt
	updated.ValidUntil = now.Add(2 * time.Hour)
	updated.DesiredState = vmlease.DesiredStateArchived

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.tenant_id', $1, true)`)).
		WithArgs("org-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT lease_json::text[\s\S]+FROM techstack_vm_leases[\s\S]+WHERE tenant_id = \$1 AND id = \$2[\s\S]+FOR UPDATE`).
		WithArgs("org-1", vmlease.LeaseID("lease-1")).
		WillReturnRows(sqlmock.NewRows([]string{"lease_json"}).AddRow(existingPayload))
	mock.ExpectExec(`UPDATE techstack_vm_leases SET[\s\S]+desired_state = \$2,[\s\S]+valid_until = \$3,[\s\S]+renewed_at = \$4,[\s\S]+cancelled_at = \$5,[\s\S]+lease_json = \$6::jsonb,[\s\S]+updated_at = now\(\)[\s\S]+WHERE tenant_id = \$7 AND id = \$1`).
		WithArgs(
			vmlease.LeaseID("lease-1"),
			"absent",
			updated.ValidUntil,
			updated.RenewedAt,
			updated.CancelledAt,
			leaseJSONDesiredStateMatcher{desiredState: vmlease.DesiredStateArchived},
			"org-1",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	got, err := NewPostgresStore(db).Update(t.Context(), "org-1", updated)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.CancelledAt == nil || !got.CancelledAt.Equal(cancelledAt) {
		t.Fatalf("cancelled_at = %v, want %s", got.CancelledAt, cancelledAt)
	}
	if got.DesiredState != vmlease.DesiredStateArchived {
		t.Fatalf("returned desired state = %q, want archived", got.DesiredState)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreGetUsesCanonicalLifecycleColumnsOverStaleJSON(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 14, 4, 0, 0, 0, time.UTC)
	stale := testLease(now.Add(-24 * time.Hour))
	stale.DesiredState = vmlease.DesiredStateRunning
	stale.CancelledAt = nil
	payload, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	cancelledAt := now.Add(-time.Hour)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.tenant_id', $1, true)`)).
		WithArgs("org-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT lease_json::text, desired_state, valid_from, valid_until, renewed_at, cancelled_at[\s\S]+FROM techstack_vm_leases[\s\S]+WHERE tenant_id = \$1 AND id = \$2`).
		WithArgs("org-1", stale.ID).
		WillReturnRows(sqlmock.NewRows([]string{"lease_json", "desired_state", "valid_from", "valid_until", "renewed_at", "cancelled_at"}).
			AddRow(payload, "absent", stale.ValidFrom, stale.ValidUntil, stale.RenewedAt, cancelledAt))
	mock.ExpectCommit()

	got, err := NewPostgresStore(db).Get(t.Context(), "org-1", stale.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DesiredState != vmlease.DesiredStateArchived || got.CancelledAt == nil || !got.CancelledAt.Equal(cancelledAt) {
		t.Fatalf("canonical lifecycle = desired %q cancelled %v", got.DesiredState, got.CancelledAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresDesiredStateProjection(t *testing.T) {
	tests := []struct {
		state vmlease.DesiredState
		want  string
	}{
		{state: vmlease.DesiredStateRunning, want: "running"},
		{state: vmlease.DesiredStateStopped, want: "stopped"},
		{state: vmlease.DesiredStateArchived, want: "absent"},
	}
	for _, test := range tests {
		if got := postgresDesiredState(test.state); got != test.want {
			t.Fatalf("postgresDesiredState(%q) = %q, want %q", test.state, got, test.want)
		}
	}
}
