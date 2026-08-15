package rilactionexecution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

func TestServiceExecutesOnceAndCommitsExactEvidence(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	request := testRequest(now)
	evidence := testEvidence(t, request, now)
	ledger := &testLedger{reservation: rilaction.LedgerReservation{
		Disposition: rilaction.LedgerAcquired, ReservationToken: "reservation-000000001",
	}}
	dispatcher := &testDispatcher{evidence: evidence}
	service, err := New(Config{Ledger: ledger, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Execute(t.Context(), request, testAdmissionDigest)
	if err != nil {
		t.Fatal(err)
	}
	if got.EvidenceID != evidence.EvidenceID || dispatcher.calls != 1 || ledger.completeCalls != 1 {
		t.Fatalf("result=%#v dispatcherCalls=%d completeCalls=%d", got, dispatcher.calls, ledger.completeCalls)
	}
	if ledger.completion.ReservationToken != "reservation-000000001" || ledger.completion.RequestDigest != evidence.RequestDigest ||
		ledger.reservationRequest.AdmissionDigest != testAdmissionDigest || ledger.completion.AdmissionDigest != testAdmissionDigest {
		t.Fatalf("completion = %#v", ledger.completion)
	}
}

func TestServiceReturnsDurableReplayWithoutDispatch(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	request := testRequest(now)
	evidence := testEvidence(t, request, now)
	ledger := &testLedger{reservation: rilaction.LedgerReservation{Disposition: rilaction.LedgerReplay, Evidence: &evidence}}
	dispatcher := &testDispatcher{}
	service, _ := New(Config{Ledger: ledger, Dispatcher: dispatcher, Now: func() time.Time { return now }})
	got, err := service.Execute(t.Context(), request, testAdmissionDigest)
	if err != nil || got.EvidenceID != evidence.EvidenceID || dispatcher.calls != 0 || ledger.completeCalls != 0 {
		t.Fatalf("result=%#v error=%v dispatcherCalls=%d completeCalls=%d", got, err, dispatcher.calls, ledger.completeCalls)
	}
}

func TestServiceReturnsTypedErrorForDurableFailedReplay(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	request := testRequest(now)
	evidence := testEvidence(t, request, now)
	evidence.Status = "failed"
	evidence.Verification.Status = "failed"
	evidence.Verification.Checks[0].Status = "failed"
	evidence.Recovery = rilaction.RecoveryEvidence{Kind: "manual", Status: "manual-required"}
	evidence.SummaryCodes = []string{"governed-plan-readback-failed"}
	ledger := &testLedger{reservation: rilaction.LedgerReservation{Disposition: rilaction.LedgerReplay, Evidence: &evidence}}
	service, _ := New(Config{Ledger: ledger, Dispatcher: &testDispatcher{}, Now: func() time.Time { return now }})
	got, err := service.Execute(t.Context(), request, testAdmissionDigest)
	if !errors.Is(err, ErrRemoteExecutionFailed) || got.Status != "failed" {
		t.Fatalf("evidence=%#v error=%v", got, err)
	}
}

func TestServiceFailsClosedOnLedgerAndEvidenceWidening(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	request := testRequest(now)
	for name, reservation := range map[string]rilaction.LedgerReservation{
		"in progress": {Disposition: rilaction.LedgerInProgress},
		"conflict":    {Disposition: rilaction.LedgerConflict},
	} {
		t.Run(name, func(t *testing.T) {
			ledger := &testLedger{reservation: reservation}
			service, _ := New(Config{Ledger: ledger, Dispatcher: &testDispatcher{}, Now: func() time.Time { return now }})
			_, err := service.Execute(t.Context(), request, testAdmissionDigest)
			if err == nil {
				t.Fatal("closed ledger result was accepted")
			}
		})
	}

	evidence := testEvidence(t, request, now)
	evidence.TenantID = "tenant-other"
	ledger := &testLedger{reservation: rilaction.LedgerReservation{
		Disposition: rilaction.LedgerAcquired, ReservationToken: "reservation-000000001",
	}}
	service, _ := New(Config{Ledger: ledger, Dispatcher: &testDispatcher{evidence: evidence}, Now: func() time.Time { return now }})
	if _, err := service.Execute(t.Context(), request, testAdmissionDigest); err == nil {
		t.Fatal("substituted evidence was accepted")
	}
	if ledger.completeCalls != 0 {
		t.Fatal("invalid evidence reached durable completion")
	}
}

func TestServiceCommitsValidFailureEvidenceAndReturnsDispatchError(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	request := testRequest(now)
	evidence := testEvidence(t, request, now)
	evidence.Status = "failed"
	evidence.Verification.Status = "failed"
	evidence.Verification.Checks[0].Status = "failed"
	evidence.Recovery = rilaction.RecoveryEvidence{Kind: "manual", Status: "manual-required"}
	evidence.SummaryCodes = []string{"governed-plan-readback-failed"}
	ledger := &testLedger{reservation: rilaction.LedgerReservation{
		Disposition: rilaction.LedgerAcquired, ReservationToken: "reservation-000000001",
	}}
	dispatcher := &testDispatcher{evidence: evidence, err: errors.New("remote execution failed")}
	service, _ := New(Config{Ledger: ledger, Dispatcher: dispatcher, Now: func() time.Time { return now }})

	got, err := service.Execute(t.Context(), request, testAdmissionDigest)
	if err == nil || got.Status != "failed" {
		t.Fatalf("evidence=%#v error=%v", got, err)
	}
	if ledger.completeCalls != 1 || ledger.completion.Evidence.Status != "failed" {
		t.Fatalf("completion = %#v", ledger.completion)
	}
}

type testLedger struct {
	reservation        rilaction.LedgerReservation
	reservationRequest rilaction.LedgerReservationRequest
	reserveErr         error
	completion         rilaction.LedgerCompletion
	completeErr        error
	completeCalls      int
}

func (l *testLedger) Reserve(_ context.Context, request rilaction.LedgerReservationRequest) (rilaction.LedgerReservation, error) {
	l.reservationRequest = request
	return l.reservation, l.reserveErr
}

func (l *testLedger) Complete(_ context.Context, completion rilaction.LedgerCompletion) error {
	l.completeCalls++
	l.completion = completion
	return l.completeErr
}

type testDispatcher struct {
	evidence rilaction.Evidence
	err      error
	calls    int
}

func (d *testDispatcher) Execute(context.Context, rilaction.Request) (rilaction.Evidence, error) {
	d.calls++
	return d.evidence, d.err
}

func testRequest(now time.Time) rilaction.Request {
	return rilaction.Request{
		APIVersion: rilaction.APIVersionV1Alpha1, ActionCardID: "action-card-1", ExecutionID: "execution-1",
		TraceID: "trace-000000000001", TenantID: "tenant-1", StackID: "stack-1",
		Primitive:        rilaction.PrimitiveBinding{ID: "verify-stackkit-state", ContractHash: testDigest("1"), OperationClass: "product-verify"},
		ResolvedPlanHash: testDigest("2"),
		Approval: rilaction.ApprovalBinding{
			ReceiptRef: "approval:receipt-1", ReceiptHash: testDigest("3"), Decision: "approved", Class: rilaction.ApprovalClassOwnerStepUp,
			ApprovedAt: testTime(now.Add(-time.Minute)), ValidUntil: testTime(now.Add(10 * time.Minute)),
		},
		Grant: rilaction.GrantBinding{
			BindingRef: "grant:binding-1", BindingHash: testDigest("4"), Audience: "stackkits", Scopes: []string{"stackkit-verify"},
			GrantedAt: testTime(now.Add(-time.Minute)), ValidUntil: testTime(now.Add(10 * time.Minute)),
		},
		Target: rilaction.TargetBinding{Scope: rilaction.TargetScopeStack}, EvidenceSinkRef: "evidence:action-card-1",
		IssuedAt: testTime(now.Add(-time.Second)), ValidUntil: testTime(now.Add(4 * time.Minute)),
		Nonce: "nonce-000000000001", IdempotencyKey: "idempotency-000001",
	}
}

func testEvidence(t *testing.T, request rilaction.Request, now time.Time) rilaction.Evidence {
	t.Helper()
	requestDigest, err := rilaction.ComputeRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	evidenceID, _ := rilaction.ComputeEvidenceID(requestDigest, "stackkits-governed-state-verifier-v1")
	targetRef, _ := rilaction.TargetReference(request)
	return rilaction.Evidence{
		APIVersion: rilaction.EvidenceAPIVersionV1, EvidenceID: evidenceID, EvidenceSinkRef: request.EvidenceSinkRef,
		ActionCardID: request.ActionCardID, ExecutionID: request.ExecutionID, TraceID: request.TraceID,
		TenantID: request.TenantID, StackID: request.StackID, PrimitiveID: request.Primitive.ID,
		PrimitiveContractHash: request.Primitive.ContractHash, ResolvedPlanHash: request.ResolvedPlanHash,
		RequestDigest: requestDigest, ExecutorRef: "stackkits-governed-state-verifier-v1", TargetRef: targetRef,
		Status: "succeeded", Verification: rilaction.VerificationEvidence{
			Kind: "governed-plan-readback", Status: "passed", Checks: []rilaction.VerificationCheck{{ID: "current-resolution", Status: "passed"}},
		},
		Recovery:     rilaction.RecoveryEvidence{Kind: "none", Status: "not-required"},
		SummaryCodes: []string{"governed-plan-readback-passed"}, EvaluatedAt: testTime(now),
	}
}

func testDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
func testTime(value time.Time) string    { return value.UTC().Format(time.RFC3339Nano) }

const testAdmissionDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var (
	_ rilaction.ExecutionLedger = (*testLedger)(nil)
	_ Dispatcher                = (*testDispatcher)(nil)
)
