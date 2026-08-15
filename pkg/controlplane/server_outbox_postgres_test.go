package controlplane

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func TestPostgresServerOutboxClaimUsesDBTimeAndHeadBoundCompletion(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	expires := now.Add(5 * time.Minute)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(`(?s)WITH candidate AS .*clock_timestamp.*FOR UPDATE SKIP LOCKED.*UPDATE server_registry_outbox`).
		WithArgs("tenant-1", "projection-worker/replica-1", sqlmock.AnyArg(), int64(300)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "server_id", "aggregate_revision", "generation",
			"authority", "source", "event_type", "payload_json", "occurred_at",
			"created_at", "published_at", "attempts", "claim_generation",
			"claim_owner", "claim_expires_at",
		}).AddRow(
			int64(7), "tenant-1", "server-1", int64(11), int64(3),
			"guard", "guard-inventory", "server.aggregate.updated", `{"server_id":"server-1"}`,
			now, now, nil, 2, int64(4), "projection-worker/replica-1", expires,
		))
	mock.ExpectCommit()

	claim, err := store.ClaimOutbox(t.Context(), "tenant-1", "projection-worker/replica-1", 5*time.Minute)
	if err != nil {
		t.Fatalf("ClaimOutbox: %v", err)
	}
	if claim.ClaimToken == "" || claim.ClaimGeneration != 4 || claim.AggregateRevision != 11 || claim.Generation != 3 {
		t.Fatalf("claim = %#v", claim)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectExec(`(?s)UPDATE server_registry_outbox.*published_at = clock_timestamp`).
		WithArgs("tenant-1", int64(7), "server-1", int64(11), int64(3), int64(4), "projection-worker/replica-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.CompleteOutbox(t.Context(), *claim); err != nil {
		t.Fatalf("CompleteOutbox: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresServerOutboxExpiredClaimCannotComplete(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	claim := serverregistry.OutboxClaim{OutboxItem: serverregistry.OutboxItem{
		ID: 7, TenantID: "tenant-1", ServerID: "server-1", AggregateRevision: 11,
		Generation: 3,
	}, ClaimGeneration: 4, ClaimOwner: "projection-worker/replica-1", ClaimToken: "expired-token"}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectExec(`(?s)UPDATE server_registry_outbox.*published_at = clock_timestamp`).
		WithArgs("tenant-1", int64(7), "server-1", int64(11), int64(3), int64(4), "projection-worker/replica-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	if err := store.CompleteOutbox(t.Context(), claim); !errors.Is(err, serverregistry.ErrOutboxClaimLost) {
		t.Fatalf("CompleteOutbox error = %v, want ErrOutboxClaimLost", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
