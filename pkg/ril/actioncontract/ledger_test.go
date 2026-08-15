package rilaction

import (
	"context"
	"testing"
	"time"
)

func TestLedgerReservationClosesAtomicDispositionVocabulary(t *testing.T) {
	now := time.Date(2026, 7, 22, 6, 0, 0, 0, time.UTC)
	request := validRequest(now)
	requestDigest, err := ComputeRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	reservationRequest, err := NewLedgerReservationRequest(request, requestDigest, digest("a"), now)
	if err != nil {
		t.Fatal(err)
	}
	evidence := validEvidence(t, request, now)
	valid := []LedgerReservation{
		{Disposition: LedgerAcquired, ReservationToken: "reservation-000000001"},
		{Disposition: LedgerReplay, Evidence: &evidence},
		{Disposition: LedgerInProgress},
		{Disposition: LedgerConflict},
	}
	for _, reservation := range valid {
		if err := ValidateLedgerReservation(request, reservationRequest, digest("a"), reservation); err != nil {
			t.Errorf("valid %q reservation rejected: %v", reservation.Disposition, err)
		}
	}

	invalid := []LedgerReservation{
		{Disposition: LedgerAcquired},
		{Disposition: LedgerAcquired, ReservationToken: "short"},
		{Disposition: LedgerAcquired, ReservationToken: "reservation-000000001", Evidence: &evidence},
		{Disposition: LedgerReplay},
		{Disposition: LedgerReplay, ReservationToken: "reservation-000000001", Evidence: &evidence},
		{Disposition: LedgerInProgress, Evidence: &evidence},
		{Disposition: "unknown"},
	}
	for _, reservation := range invalid {
		if err := ValidateLedgerReservation(request, reservationRequest, digest("a"), reservation); err == nil {
			t.Errorf("invalid reservation accepted: %#v", reservation)
		}
	}
}

func TestLedgerCompletionBindsExactRequestTokenAndEvidence(t *testing.T) {
	now := time.Date(2026, 7, 22, 6, 0, 0, 0, time.UTC)
	request := validRequest(now)
	requestDigest, _ := ComputeRequestDigest(request)
	reservationRequest, _ := NewLedgerReservationRequest(request, requestDigest, digest("a"), now)
	evidence := validEvidence(t, request, now)
	completion, err := NewLedgerCompletion(request, reservationRequest, digest("a"), "reservation-000000001", evidence)
	if err != nil {
		t.Fatal(err)
	}
	if completion.TenantID != request.TenantID || completion.RequestDigest != requestDigest || completion.Evidence.EvidenceID != evidence.EvidenceID {
		t.Fatalf("completion = %#v", completion)
	}

	changed := reservationRequest
	changed.RequestDigest = digest("f")
	if _, err := NewLedgerCompletion(request, changed, digest("a"), "reservation-000000001", evidence); err == nil {
		t.Fatal("substituted reservation request was accepted")
	}
	changed = reservationRequest
	changed.AdmissionDigest = digest("f")
	if _, err := NewLedgerCompletion(request, changed, digest("a"), "reservation-000000001", evidence); err == nil {
		t.Fatal("substituted execution admission digest was accepted")
	}
	if _, err := NewLedgerCompletion(request, reservationRequest, digest("a"), "short", evidence); err == nil {
		t.Fatal("invalid reservation token was accepted")
	}
	tampered := evidence
	tampered.ExecutionID = "execution-other"
	if _, err := NewLedgerCompletion(request, reservationRequest, digest("a"), "reservation-000000001", tampered); err == nil {
		t.Fatal("substituted evidence was accepted")
	}
}

func TestNewLedgerReservationRequestRejectsStaleOrSubstitutedAuthority(t *testing.T) {
	now := time.Date(2026, 7, 22, 6, 0, 0, 0, time.UTC)
	request := validRequest(now)
	requestDigest, _ := ComputeRequestDigest(request)
	if _, err := NewLedgerReservationRequest(request, digest("e"), digest("a"), now); err == nil {
		t.Fatal("substituted request digest was accepted")
	}
	if _, err := NewLedgerReservationRequest(request, requestDigest, digest("a"), now.Add(5*time.Minute)); err == nil {
		t.Fatal("expired request was accepted")
	}
	if _, err := NewLedgerReservationRequest(request, requestDigest, "", now); err == nil {
		t.Fatal("missing execution admission digest was accepted")
	}
}

type testExecutionLedger struct{}

func (testExecutionLedger) Reserve(context.Context, LedgerReservationRequest) (LedgerReservation, error) {
	return LedgerReservation{Disposition: LedgerInProgress}, nil
}

func (testExecutionLedger) Complete(context.Context, LedgerCompletion) error { return nil }

var _ ExecutionLedger = testExecutionLedger{}
