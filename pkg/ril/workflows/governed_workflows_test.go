package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

func TestActionCardRemediationCarriesVerifiedEvidenceToCompletion(t *testing.T) {
	request, evidence := validActionContract(t)
	fake := &fakeActivities{outputs: map[string]map[string]any{
		ActBeginGovernedAction: {
			fieldRequest: mustObjectMap(t, request), fieldAdmissionDigest: testHash("admission"),
		},
		ActExecuteGovernedAction: {fieldEvidence: mustObjectMap(t, evidence)},
	}}
	rc := &workflow.RunContext{Run: &workflow.Run{
		RunID: "run-action-1", Type: workflow.TypeActionCardRemediation,
		Input: map[string]any{
			InputTenantID: "tenant-1", InputOwnerSubjectID: "owner-1", InputCardID: request.ActionCardID,
			InputExecutionID: request.ExecutionID, InputTraceID: request.TraceID, InputIdempotencyKey: request.IdempotencyKey,
		}, Context: map[string]any{},
	}, Activities: fake}

	for _, step := range NewActionCardRemediationWorkflow().Steps() {
		if _, err := step.Run(t.Context(), rc); err != nil {
			t.Fatalf("action remediation step %q: %v", step.Name, err)
		}
	}
	if calls := fake.callsTo(ActCompleteGovernedAction); len(calls) != 1 || mapString(calls[0].input[fieldEvidence].(map[string]any), "evidence_id") != evidence.EvidenceID {
		t.Fatalf("verified evidence was not completed exactly once: %#v", calls)
	}
}

func TestActionCardRemediationTerminalizesDispatchWithoutEvidence(t *testing.T) {
	request, _ := validActionContract(t)
	fake := &fakeActivities{
		outputs: map[string]map[string]any{ActBeginGovernedAction: {
			fieldRequest: mustObjectMap(t, request), fieldAdmissionDigest: testHash("admission"),
		}},
		errs: map[string]error{ActExecuteGovernedAction: errors.New("transport unavailable")},
	}
	rc := &workflow.RunContext{Run: &workflow.Run{
		RunID: "run-action-failure", Type: workflow.TypeActionCardRemediation,
		Input: map[string]any{
			InputTenantID: "tenant-1", InputOwnerSubjectID: "owner-1", InputCardID: request.ActionCardID,
			InputExecutionID: request.ExecutionID, InputTraceID: request.TraceID, InputIdempotencyKey: request.IdempotencyKey,
		}, Context: map[string]any{},
	}, Activities: fake}
	steps := NewActionCardRemediationWorkflow().Steps()
	if _, err := steps[0].Run(t.Context(), rc); err != nil {
		t.Fatal(err)
	}
	if _, err := steps[1].Run(t.Context(), rc); err == nil {
		t.Fatal("dispatch without evidence unexpectedly succeeded")
	}
	if _, err := steps[0].Compensate(t.Context(), rc); err != nil {
		t.Fatal(err)
	}
	if calls := fake.callsTo(ActFailGovernedAction); len(calls) != 1 || calls[0].input[fieldErrorCode] != "workflow_execution_unavailable" {
		t.Fatalf("admitted action was not safely terminalized: %#v", calls)
	}
}

func TestClosedOperationalWorkflowsDispatchVerifyAndAudit(t *testing.T) {
	definitions := []workflow.WorkflowDefinition{
		NewDriftCorrectionWorkflow(), NewCertRotationWorkflow(), NewRollingUpdateWorkflow(),
	}
	for _, definition := range definitions {
		t.Run(string(definition.Type()), func(t *testing.T) {
			fake := &fakeActivities{outputs: map[string]map[string]any{
				ActQueryMonitoringSignal:     {"active": true},
				ActDispatchGovernedOperation: {"receipt_ref": "operation:1"},
				ActVerifyGovernedOperation:   {"verified": true},
			}}
			input := map[string]any{
				InputAuthorizationRef: "authorization:1", InputOperationRef: "operation:1",
				InputCompensationRef: "compensation:1", InputTargets: []any{"server-1", "server-2"},
			}
			rc := &workflow.RunContext{Run: &workflow.Run{RunID: "run-" + string(definition.Type()), Type: definition.Type(), Input: input, Context: map[string]any{}}, Activities: fake}
			for _, step := range definition.Steps() {
				if _, err := step.Run(context.Background(), rc); err != nil {
					t.Fatalf("step %q: %v", step.Name, err)
				}
			}
			if len(fake.callsTo(ActDispatchGovernedOperation)) != 1 || len(fake.callsTo(ActVerifyGovernedOperation)) != 1 || len(fake.callsTo(ActRecordGovernedOperationAudit)) != 1 {
				t.Fatalf("workflow did not complete its governed operation: %v", fake.names())
			}
		})
	}
}

func validActionContract(t *testing.T) (rilaction.Request, rilaction.Evidence) {
	t.Helper()
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	request := rilaction.Request{
		APIVersion: rilaction.APIVersionV1Alpha1, ActionCardID: "action-card-1", ExecutionID: "execution-1",
		TraceID: "trace-000000000001", TenantID: "tenant-1", StackID: "stack-1",
		Primitive:        rilaction.PrimitiveBinding{ID: "verify-stackkit-state", ContractHash: testHash("primitive"), OperationClass: "verification"},
		ResolvedPlanHash: testHash("plan"),
		Approval:         rilaction.ApprovalBinding{ReceiptRef: "approval:1", ReceiptHash: testHash("approval"), Decision: "approved", Class: rilaction.ApprovalClassOwnerStepUp, ApprovedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ValidUntil: now.Add(10 * time.Minute).Format(time.RFC3339Nano)},
		Grant:            rilaction.GrantBinding{BindingRef: "grant:1", BindingHash: testHash("grant"), Audience: "stackkits", Scopes: []string{"stackkit-verify"}, GrantedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ValidUntil: now.Add(10 * time.Minute).Format(time.RFC3339Nano)},
		Target:           rilaction.TargetBinding{Scope: rilaction.TargetScopeRuntimeInstance, SiteRef: "site-1", NodeRef: "node-1", RuntimeInstanceRef: "agent-1", ExecutionChannelRef: "host-channel-node-1"},
		EvidenceSinkRef:  "evidence:action-card-1", IssuedAt: now.Format(time.RFC3339Nano), ValidUntil: now.Add(5 * time.Minute).Format(time.RFC3339Nano), Nonce: "nonce-000000000001", IdempotencyKey: "idempotency-000001",
	}
	requestDigest, err := rilaction.ComputeRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	executorRef := "stackkits-pinned-cli-verify-v1"
	evidenceID, err := rilaction.ComputeEvidenceID(requestDigest, executorRef)
	if err != nil {
		t.Fatal(err)
	}
	targetRef, err := rilaction.TargetReference(request)
	if err != nil {
		t.Fatal(err)
	}
	evidence := rilaction.Evidence{
		APIVersion: rilaction.EvidenceAPIVersionV1, EvidenceID: evidenceID, EvidenceSinkRef: request.EvidenceSinkRef,
		ActionCardID: request.ActionCardID, ExecutionID: request.ExecutionID, TraceID: request.TraceID,
		TenantID: request.TenantID, StackID: request.StackID, PrimitiveID: request.Primitive.ID,
		PrimitiveContractHash: request.Primitive.ContractHash, ResolvedPlanHash: request.ResolvedPlanHash,
		RequestDigest: requestDigest, ExecutorRef: executorRef, TargetRef: targetRef, Status: "succeeded",
		Verification: rilaction.VerificationEvidence{Kind: "stackkit-pinned-cli-verify", Status: "passed", RuntimeStateObserved: true, Checks: []rilaction.VerificationCheck{{ID: "stackkit-verify", Status: "passed"}}},
		Recovery:     rilaction.RecoveryEvidence{Kind: "none", Status: "not-required"}, SummaryCodes: []string{"stackkit-verify-passed"}, EvaluatedAt: now.Add(time.Minute).Format(time.RFC3339Nano),
	}
	if err := rilaction.ValidateEvidenceForRequest(request, evidence); err != nil {
		t.Fatal(err)
	}
	return request, evidence
}

func mustObjectMap(t *testing.T, value any) map[string]any {
	t.Helper()
	out, err := objectMapForTest(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func objectMapForTest(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func testHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
