package rilaction

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDecodeEvidenceForRequestAcceptsExactPublicSafeResult(t *testing.T) {
	now := time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)
	request := validRequest(now)
	evidence := validEvidence(t, request, now)
	data := mustJSON(t, evidence)
	got, err := DecodeEvidenceForRequest(data, request)
	if err != nil {
		t.Fatalf("DecodeEvidenceForRequest() error = %v", err)
	}
	if !reflect.DeepEqual(got, evidence) {
		t.Fatalf("decoded evidence drifted:\ngot  = %#v\nwant = %#v", got, evidence)
	}
	for _, forbidden := range []string{
		`"provider`, `"lease`, `"generation`, `"transport`, `"url`, `"endpoint`,
		`"credential`, `"secret`, `"command`, `"path`, `"host`, `"address`, `"callback`, `"log`,
	} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("evidence exposed forbidden field prefix %q: %s", forbidden, data)
		}
	}
}

func TestValidateEvidenceForRequestRejectsCorrelationAndAuthoritySubstitution(t *testing.T) {
	now := time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)
	request := validRequest(now)
	tests := map[string]func(*Evidence){
		"evidence id":       func(e *Evidence) { e.EvidenceID = digest("9") },
		"sink":              func(e *Evidence) { e.EvidenceSinkRef = "evidence:other" },
		"action card":       func(e *Evidence) { e.ActionCardID = "action-card-2" },
		"execution":         func(e *Evidence) { e.ExecutionID = "execution-2" },
		"tenant":            func(e *Evidence) { e.TenantID = "tenant-2" },
		"stack":             func(e *Evidence) { e.StackID = "stack-2" },
		"primitive":         func(e *Evidence) { e.PrimitiveID = "verify-stackkit-state" },
		"primitive hash":    func(e *Evidence) { e.PrimitiveContractHash = digest("8") },
		"plan":              func(e *Evidence) { e.ResolvedPlanHash = digest("7") },
		"request digest":    func(e *Evidence) { e.RequestDigest = digest("6") },
		"target":            func(e *Evidence) { e.TargetRef = "stack:other" },
		"free-form summary": func(e *Evidence) { e.SummaryCodes = []string{"raw output is unsafe"} },
		"diagnostic url":    func(e *Evidence) { e.ProtectedDiagnosticRef = "https://example.invalid/log" },
		"late evidence":     func(e *Evidence) { e.EvaluatedAt = request.ValidUntil },
		"failed without check": func(e *Evidence) {
			e.Status = "failed"
			e.Verification.Status = "failed"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			evidence := validEvidence(t, request, now)
			mutate(&evidence)
			if err := ValidateEvidenceForRequest(request, evidence); err == nil {
				t.Fatal("substituted evidence was accepted")
			}
		})
	}
}

func TestDecodeEvidenceForRequestRejectsUnknownDuplicateAndRawOutput(t *testing.T) {
	now := time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)
	request := validRequest(now)
	data := string(mustJSON(t, validEvidence(t, request, now)))
	tests := map[string]string{
		"provider":  strings.TrimSuffix(data, "}") + `,"provider_id":"provider-1"}`,
		"raw log":   strings.TrimSuffix(data, "}") + `,"raw_log":"docker ps"}`,
		"duplicate": strings.Replace(data, `"status":"succeeded"`, `"status":"succeeded","status":"failed"`, 1),
		"trailing":  data + `{}`,
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeEvidenceForRequest([]byte(candidate), request); err == nil {
				t.Fatal("widened evidence was accepted")
			} else if strings.Contains(err.Error(), "provider-1") || strings.Contains(err.Error(), "docker ps") {
				t.Fatalf("error echoed protected caller value: %v", err)
			}
		})
	}
}

func TestRecoveryEvidenceRequiresSeparateApprovedPrimitive(t *testing.T) {
	valid := []RecoveryEvidence{
		{Kind: "none", Status: "not-required"},
		{Kind: "primitive", Status: "required", PrimitiveRef: "rollback-stackkit-change"},
		{Kind: "manual", Status: "manual-required"},
	}
	for _, evidence := range valid {
		if err := validateRecoveryEvidence(evidence); err != nil {
			t.Errorf("validateRecoveryEvidence(%#v) error = %v", evidence, err)
		}
	}

	invalid := []RecoveryEvidence{
		{Kind: "primitive", Status: "succeeded", PrimitiveRef: "rollback-stackkit-change"},
		{Kind: "primitive", Status: "failed", PrimitiveRef: "rollback-stackkit-change"},
		{Kind: "primitive", Status: "required"},
		{Kind: "none", Status: "not-required", PrimitiveRef: "rollback-stackkit-change"},
		{Kind: "manual", Status: "manual-required", PrimitiveRef: "rollback-stackkit-change"},
	}
	for _, evidence := range invalid {
		if err := validateRecoveryEvidence(evidence); err == nil {
			t.Errorf("validateRecoveryEvidence(%#v) accepted an unbound recovery claim", evidence)
		}
	}

	now := time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)
	request := validRequest(now)
	evidence := validEvidence(t, request, now)
	evidence.Recovery = RecoveryEvidence{Kind: "primitive", Status: "required", PrimitiveRef: "rollback-stackkit-change"}
	if err := ValidateEvidenceForRequest(request, evidence); err == nil {
		t.Fatal("succeeded action claimed that recovery is required")
	}
	evidence.Status = "failed"
	evidence.Verification.Status = "failed"
	evidence.Verification.Checks[0].Status = "failed"
	evidence.SummaryCodes = []string{"apply-failed"}
	if err := ValidateEvidenceForRequest(request, evidence); err != nil {
		t.Fatalf("failed action with separately approved recovery requirement was rejected: %v", err)
	}
}

func TestTargetReferenceIsDerivedFromClosedRequestTarget(t *testing.T) {
	now := time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		target TargetBinding
		want   string
	}{
		{TargetBinding{Scope: TargetScopeStack}, "stack:stack-1"},
		{TargetBinding{Scope: TargetScopeModuleInstance, SiteRef: "site-1", ModuleInstanceRef: "module-1"}, "module:site-1/module-1"},
		{TargetBinding{Scope: TargetScopeRuntimeInstance, SiteRef: "site-1", NodeRef: "node-1", RuntimeInstanceRef: "runtime-1", ExecutionChannelRef: "channel-1"}, "runtime:site-1/node-1/runtime-1"},
	}
	for _, test := range tests {
		request := validRequest(now)
		request.Target = test.target
		got, err := TargetReference(request)
		if err != nil || got != test.want {
			t.Errorf("TargetReference(%#v) = %q, %v; want %q", test.target, got, err, test.want)
		}
	}
}

func validEvidence(t *testing.T, request Request, now time.Time) Evidence {
	t.Helper()
	requestDigest, err := ComputeRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	executorRef := "stackkits-governed-state-verifier-v1"
	evidenceID, err := ComputeEvidenceID(requestDigest, executorRef)
	if err != nil {
		t.Fatal(err)
	}
	targetRef, err := TargetReference(request)
	if err != nil {
		t.Fatal(err)
	}
	return Evidence{
		APIVersion: EvidenceAPIVersionV1, EvidenceID: evidenceID, EvidenceSinkRef: request.EvidenceSinkRef,
		ActionCardID: request.ActionCardID, ExecutionID: request.ExecutionID, TraceID: request.TraceID,
		TenantID: request.TenantID, StackID: request.StackID,
		PrimitiveID: request.Primitive.ID, PrimitiveContractHash: request.Primitive.ContractHash,
		ResolvedPlanHash: request.ResolvedPlanHash, RequestDigest: requestDigest,
		ExecutorRef: executorRef, TargetRef: targetRef, Status: "succeeded",
		Verification: VerificationEvidence{
			Kind: "governed-plan-readback", Status: "passed", RuntimeStateObserved: false,
			Checks: []VerificationCheck{
				{ID: "canonical-plan", Status: "passed"},
				{ID: "cue-contract", Status: "passed"},
				{ID: "current-resolution", Status: "passed"},
			},
		},
		Recovery:     RecoveryEvidence{Kind: "none", Status: "not-required"},
		SummaryCodes: []string{"governed-plan-readback-passed"}, EvaluatedAt: canonical(now),
	}
}
