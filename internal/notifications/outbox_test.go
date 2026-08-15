package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

type fakeDispatchClient struct {
	result *DispatchResult
	err    error
	event  ProductEvent
}

func (f *fakeDispatchClient) DispatchProduct(_ context.Context, event ProductEvent) (*DispatchResult, error) {
	f.event = event
	return f.result, f.err
}

func TestOutboxEnqueuePersistsIdempotentProductEvent(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mock.ExpectExec(`(?s)INSERT INTO techstack_notification_outbox.*ON CONFLICT \(idempotency_key\) DO NOTHING`).
		WithArgs("monitor:key", "org-1", "auth0|owner", "system.service-degraded", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	outbox := NewOutbox(database, &fakeDispatchClient{}, nil)
	err = outbox.Enqueue(context.Background(), ProductEvent{
		Topic: "system.service-degraded", Auth0UserID: "auth0|owner", OrganizationID: "org-1",
		IdempotencyKey: "monitor:key", Payload: map[string]any{"subject": "DiskFull"},
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxProcessOneDispatchesAndCompletes(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT idempotency_key.*FROM techstack_notification_outbox`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"idempotency_key", "tenant_id", "auth0_user_id", "topic_slug", "payload_json", "attempts"}).
			AddRow("monitor:key", "org-1", "auth0|owner", "system.service-degraded", `{"subject":"DiskFull"}`, 0))
	mock.ExpectExec(`(?s)UPDATE techstack_notification_outbox SET next_attempt_at`).
		WithArgs(now, now.Add(outboxClaimLease), "monitor:key").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`(?s)UPDATE techstack_notification_outbox.*status='delivered'`).
		WithArgs("monitor:key", "dispatch-1", "feed-1", now).
		WillReturnResult(sqlmock.NewResult(0, 1))

	client := &fakeDispatchClient{result: &DispatchResult{DispatchID: "dispatch-1", FeedItemID: "feed-1"}}
	outbox := NewOutbox(database, client, nil)
	outbox.now = func() time.Time { return now }
	if err := outbox.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if client.event.Auth0UserID != "auth0|owner" || client.event.OrganizationID != "org-1" {
		t.Fatalf("dispatch identity changed: %#v", client.event)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxProcessOneMakesNonRetryableDenialTerminal(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT idempotency_key.*FROM techstack_notification_outbox`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"idempotency_key", "tenant_id", "auth0_user_id", "topic_slug", "payload_json", "attempts"}).
			AddRow("monitor:key", "org-1", "auth0|owner", "system.service-degraded", `{}`, 0))
	mock.ExpectExec(`(?s)UPDATE techstack_notification_outbox SET next_attempt_at`).
		WithArgs(now, now.Add(outboxClaimLease), "monitor:key").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`(?s)UPDATE techstack_notification_outbox.*SET status=\$2`).
		WithArgs("monitor:key", "failed", 1, now, sqlmock.AnyArg(), now).
		WillReturnResult(sqlmock.NewResult(0, 1))

	client := &fakeDispatchClient{err: &DispatchError{StatusCode: 403, Code: "feature_denied", Retryable: false}}
	outbox := NewOutbox(database, client, nil)
	outbox.now = func() time.Time { return now }
	if err := outbox.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !isTerminalDispatchError(client.err) || errors.Is(client.err, context.Canceled) {
		t.Fatal("expected a terminal dispatch denial")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxResolveWorkerRecipientUsesTenantRLS(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.tenant_id', \$1, true\)`).
		WithArgs("org-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT COALESCE\(owner_subject_id, ''\).*FROM workers WHERE tenant_id=\$1 AND id=\$2`).
		WithArgs("org-1", "agent-1").
		WillReturnRows(sqlmock.NewRows([]string{"owner_subject_id"}).AddRow("auth0|owner"))
	mock.ExpectCommit()

	outbox := NewOutbox(database, &fakeDispatchClient{}, nil)
	recipient, err := outbox.ResolveWorkerRecipient(context.Background(), "org-1", "agent-1")
	if err != nil {
		t.Fatalf("ResolveWorkerRecipient: %v", err)
	}
	if recipient != "auth0|owner" {
		t.Fatalf("recipient=%q", recipient)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
