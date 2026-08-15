package db

import (
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/google/uuid"
)

func TestIntegrationRILExpiredExecutionLeaseHasOneWinnerAndFencesStaleCompletion(t *testing.T) {
	database := openTestDB(t)
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate execution lease schema: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := uuid.NewString()
	request := rilaction.LedgerReservationRequest{
		TenantID: "tenant-ril-ledger-" + suffix, IdempotencyKey: "idempotency-" + suffix,
		ExecutionID:        "execution-" + suffix,
		RequestDigest:      "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		AdmissionDigest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AuditCorrelationID: "trace-" + suffix,
		RequestedAt:        now.Format(time.RFC3339Nano),
		ValidUntil:         now.Add(time.Minute).Format(time.RFC3339Nano),
	}
	store := controlplane.NewPostgresStore(database.DB)
	initial, err := store.Reserve(t.Context(), request)
	if err != nil || initial.Disposition != rilaction.LedgerAcquired {
		t.Fatalf("initial reservation = %#v, error=%v", initial, err)
	}
	if _, err := database.ExecContext(t.Context(), `
		UPDATE ril_action_execution_ledger
		SET requested_at=$3, valid_until=$4
		WHERE tenant_id=$1 AND idempotency_key=$2
	`, request.TenantID, request.IdempotencyKey, now.Add(-2*time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("expire initial reservation: %v", err)
	}

	takeover := request
	takeover.RequestedAt = now.Format(time.RFC3339Nano)
	takeover.ValidUntil = now.Add(2 * time.Minute).Format(time.RFC3339Nano)
	results := make(chan rilaction.LedgerReservation, 2)
	errors := make(chan error, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, reserveErr := store.Reserve(t.Context(), takeover)
			results <- result
			errors <- reserveErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	for reserveErr := range errors {
		if reserveErr != nil {
			t.Fatalf("concurrent takeover: %v", reserveErr)
		}
	}
	var winner rilaction.LedgerReservation
	dispositions := map[rilaction.LedgerDisposition]int{}
	for result := range results {
		dispositions[result.Disposition]++
		if result.Disposition == rilaction.LedgerAcquired {
			winner = result
		}
	}
	if dispositions[rilaction.LedgerAcquired] != 1 || dispositions[rilaction.LedgerInProgress] != 1 {
		t.Fatalf("takeover dispositions = %v", dispositions)
	}
	if winner.ReservationToken == "" || winner.ReservationToken == initial.ReservationToken {
		t.Fatalf("takeover fencing token = %q, initial=%q", winner.ReservationToken, initial.ReservationToken)
	}

	evidence := rilaction.Evidence{
		TenantID: request.TenantID, ExecutionID: request.ExecutionID,
		RequestDigest: request.RequestDigest, Status: "succeeded",
	}
	completion := rilaction.LedgerCompletion{
		TenantID: request.TenantID, IdempotencyKey: request.IdempotencyKey,
		ExecutionID: request.ExecutionID, RequestDigest: request.RequestDigest,
		AdmissionDigest: request.AdmissionDigest, AuditCorrelationID: request.AuditCorrelationID,
		ReservationToken: initial.ReservationToken, Evidence: evidence,
	}
	if err := store.Complete(t.Context(), completion); err == nil {
		t.Fatal("stale pre-takeover token completed execution")
	}
	completion.ReservationToken = winner.ReservationToken
	if err := store.Complete(t.Context(), completion); err != nil {
		t.Fatalf("takeover winner completion: %v", err)
	}

	rows, err := database.QueryContext(t.Context(), `
		SELECT event_type, takeover_count, audit_correlation_id
		FROM ril_execution_lease_audit
		WHERE tenant_id=$1 AND idempotency_key=$2
		ORDER BY sequence_id
	`, request.TenantID, request.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []string
	for rows.Next() {
		var event, correlation string
		var count int64
		if err := rows.Scan(&event, &count, &correlation); err != nil {
			t.Fatal(err)
		}
		if correlation != request.AuditCorrelationID {
			t.Fatalf("audit correlation = %q", correlation)
		}
		events = append(events, event)
		if event == "taken-over" && count != 1 {
			t.Fatalf("takeover count = %d", count)
		}
	}
	if got := len(events); got != 3 || events[0] != "acquired" || events[1] != "taken-over" || events[2] != "completed" {
		t.Fatalf("lease audit events = %v", events)
	}
}
