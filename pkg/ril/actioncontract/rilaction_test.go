package rilaction

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDecodeRequestAtAcceptsClosedProviderFreeEnvelope(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 30, 0, 0, time.UTC)
	want := validRequest(now)
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRequestAt(data, now)
	if err != nil {
		t.Fatalf("decode valid request: %v", err)
	}
	if got.Primitive != want.Primitive || got.ResolvedPlanHash != want.ResolvedPlanHash || got.Target.Scope != TargetScopeStack {
		t.Fatalf("decoded request drifted: %#v", got)
	}
	digest, err := ComputeRequestDigest(got)
	if err != nil {
		t.Fatalf("compute digest: %v", err)
	}
	if !digestPattern.MatchString(digest) {
		t.Fatalf("digest = %q", digest)
	}
	wire := string(data)
	for _, forbidden := range []string{
		`"provider`, `"lease`, `"generation`, `"executor`, `"transport`, `"url`, `"endpoint`,
		`"credential`, `"secret`, `"command`, `"path`, `"host`, `"address`, `"callback`,
	} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("valid handoff exposed forbidden field prefix %q: %s", forbidden, wire)
		}
	}
}

func TestRequestDigestBindsAuthorityAndTargetIdentity(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 30, 0, 0, time.UTC)
	base := validRequest(now)
	want, err := ComputeRequestDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Request){
		"action card":     func(r *Request) { r.ActionCardID = "action-card-2" },
		"execution":       func(r *Request) { r.ExecutionID = "execution-2" },
		"tenant":          func(r *Request) { r.TenantID = "tenant-2" },
		"stack":           func(r *Request) { r.StackID = "stack-2" },
		"primitive":       func(r *Request) { r.Primitive.ID = "verify-stackkit-state" },
		"primitive hash":  func(r *Request) { r.Primitive.ContractHash = digest("b") },
		"operation class": func(r *Request) { r.Primitive.OperationClass = "product-verify" },
		"plan":            func(r *Request) { r.ResolvedPlanHash = digest("c") },
		"approval":        func(r *Request) { r.Approval.ReceiptHash = digest("d") },
		"grant":           func(r *Request) { r.Grant.BindingHash = digest("e") },
		"input":           func(r *Request) { r.Inputs[0].OpaqueRef = "change:approved-43" },
		"nonce":           func(r *Request) { r.Nonce = "nonce-000000000002" },
		"idempotency":     func(r *Request) { r.IdempotencyKey = "idempotency-000002" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneRequest(t, base)
			mutate(&candidate)
			got, err := ComputeRequestDigest(candidate)
			if err != nil {
				t.Fatalf("compute changed digest: %v", err)
			}
			if got == want {
				t.Fatalf("%s substitution did not rotate digest", name)
			}
		})
	}
}

func TestDecodeRequestAtRejectsUnknownDuplicateTrailingAndOversizedJSON(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 30, 0, 0, time.UTC)
	data := mustJSON(t, validRequest(now))
	text := string(data)
	tests := map[string][]byte{
		"unknown top level": []byte(strings.TrimSuffix(text, "}") + `,"provider_id":"provider-1"}`),
		"unknown nested":    []byte(strings.Replace(text, `"decision":"approved"`, `"decision":"approved","callback_url":"https://example.invalid"`, 1)),
		"duplicate nested":  []byte(strings.Replace(text, `"receipt_ref":"approval:receipt-1"`, `"receipt_ref":"approval:receipt-1","receipt_ref":"approval:receipt-2"`, 1)),
		"trailing":          append(append([]byte(nil), data...), []byte(`{}`)...),
		"oversized":         append(data, make([]byte, MaxRequestBytes)...),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRequestAt(candidate, now); err == nil {
				t.Fatal("invalid JSON envelope was accepted")
			} else if strings.Contains(err.Error(), "https://example.invalid") || strings.Contains(err.Error(), "provider-1") {
				t.Fatalf("error echoed caller value: %v", err)
			}
		})
	}
}

func TestValidateRequestAtClosesFreshnessAndAuthorityWindows(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 30, 0, 0, time.UTC)
	tests := map[string]func(*Request){
		"future request": func(r *Request) {
			r.IssuedAt = canonical(now.Add(time.Second))
		},
		"expired request": func(r *Request) {
			r.ValidUntil = canonical(now)
		},
		"request ttl": func(r *Request) {
			r.ValidUntil = canonical(now.Add(MaxRequestValidity + time.Minute))
		},
		"approval ttl": func(r *Request) {
			r.Approval.ApprovedAt = canonical(now.Add(-time.Minute))
			r.Approval.ValidUntil = canonical(now.Add(MaxAuthorityValidity))
		},
		"grant ttl": func(r *Request) {
			r.Grant.GrantedAt = canonical(now.Add(-time.Minute))
			r.Grant.ValidUntil = canonical(now.Add(MaxAuthorityValidity))
		},
		"approval expires first": func(r *Request) {
			r.Approval.ValidUntil = canonical(now.Add(time.Minute))
			r.ValidUntil = canonical(now.Add(2 * time.Minute))
		},
		"grant expires first": func(r *Request) {
			r.Grant.ValidUntil = canonical(now.Add(time.Minute))
			r.ValidUntil = canonical(now.Add(2 * time.Minute))
		},
		"request predates approval": func(r *Request) {
			r.Approval.ApprovedAt = canonical(now)
			r.IssuedAt = canonical(now.Add(-time.Second))
		},
		"noncanonical timestamp": func(r *Request) {
			r.IssuedAt = "2026-07-22T02:29:55+00:00"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := validRequest(now)
			mutate(&request)
			if err := ValidateRequestAt(request, now); err == nil {
				t.Fatal("invalid freshness window was accepted")
			}
		})
	}
	nonUTCNow := time.Date(2026, 7, 22, 2, 30, 0, 0, time.FixedZone("not-utc", 0))
	if err := ValidateRequestAt(validRequest(now), nonUTCNow); err == nil {
		t.Fatal("non-UTC trusted clock was accepted")
	}
}

func TestValidateRequestShapeClosesTargetInputAndGrantAuthority(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 30, 0, 0, time.UTC)
	trueValue := true
	integerValue := int64(3)
	tests := map[string]func(*Request){
		"stack carries channel": func(r *Request) { r.Target.ExecutionChannelRef = "channel-1" },
		"module misses site": func(r *Request) {
			r.Target = TargetBinding{Scope: TargetScopeModuleInstance, ModuleInstanceRef: "module-1"}
		},
		"module node misses channel": func(r *Request) {
			r.Target = TargetBinding{Scope: TargetScopeModuleInstance, SiteRef: "site-1", NodeRef: "node-1", ModuleInstanceRef: "module-1"}
		},
		"runtime misses channel": func(r *Request) {
			r.Target = TargetBinding{Scope: TargetScopeRuntimeInstance, SiteRef: "site-1", NodeRef: "node-1", RuntimeInstanceRef: "runtime-1"}
		},
		"secret ref scheme":  func(r *Request) { r.Inputs[0].OpaqueRef = "secret:material-1" },
		"unknown ref scheme": func(r *Request) { r.Inputs[0].OpaqueRef = "runtime:material-1" },
		"url input":          func(r *Request) { r.Inputs[0].OpaqueRef = "https://example.invalid/value" },
		"two typed values":   func(r *Request) { r.Inputs[0].Boolean = &trueValue },
		"wrong typed value": func(r *Request) {
			r.Inputs[0] = Input{ID: "approved-change-ref", Type: InputTypeInteger, Boolean: &trueValue}
		},
		"unsorted inputs": func(r *Request) {
			r.Inputs = []Input{{ID: "zeta", Type: InputTypeInteger, Integer: &integerValue}, {ID: "alpha", Type: InputTypeBoolean, Boolean: &trueValue}}
		},
		"unsorted scopes":          func(r *Request) { r.Grant.Scopes = []string{"stackkit-verify", "stackkit-apply"} },
		"wrong audience":           func(r *Request) { r.Grant.Audience = "provider-control" },
		"evidence url":             func(r *Request) { r.EvidenceSinkRef = "https://example.invalid/evidence" },
		"executor field smuggling": func(r *Request) { r.Primitive.OperationClass = "ssh-executor" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := validRequest(now)
			mutate(&request)
			if err := ValidateRequestShape(request); err == nil {
				t.Fatal("widened authority was accepted")
			}
		})
	}

	validTargets := []TargetBinding{
		{Scope: TargetScopeStack},
		{Scope: TargetScopeModuleInstance, SiteRef: "site-1", ModuleInstanceRef: "module-1"},
		{Scope: TargetScopeModuleInstance, SiteRef: "site-1", NodeRef: "node-1", ModuleInstanceRef: "module-1", ExecutionChannelRef: "channel-1"},
		{Scope: TargetScopeRuntimeInstance, SiteRef: "site-1", NodeRef: "node-1", RuntimeInstanceRef: "runtime-1", ExecutionChannelRef: "channel-1"},
		{Scope: TargetScopeRuntimeInstance, SiteRef: "site-1", NodeRef: "node-1", RuntimeInstanceRef: "runtime-1", ExecutionChannelRef: "execution-channel://sha256/" + strings.Repeat("a", 64)},
	}
	for _, target := range validTargets {
		request := validRequest(now)
		request.Target = target
		if err := ValidateRequestShape(request); err != nil {
			t.Errorf("valid target %#v rejected: %v", target, err)
		}
	}

	for name, channelRef := range map[string]string{
		"endpoint":       "https://node.example.test/action",
		"ssh":            "ssh://root@node",
		"unbound scheme": "execution-channel://node-a",
	} {
		t.Run("channel "+name, func(t *testing.T) {
			request := validRequest(now)
			request.Target = TargetBinding{
				Scope: TargetScopeRuntimeInstance, SiteRef: "site-1", NodeRef: "node-1",
				RuntimeInstanceRef: "runtime-1", ExecutionChannelRef: channelRef,
			}
			if err := ValidateRequestShape(request); err == nil {
				t.Fatal("non-canonical execution channel was accepted")
			}
		})
	}
}

func TestSortedInputsReturnsDefensiveCanonicalCopy(t *testing.T) {
	inputs := []Input{{ID: "zeta"}, {ID: "alpha"}}
	got := SortedInputs(inputs)
	if got[0].ID != "alpha" || got[1].ID != "zeta" {
		t.Fatalf("sorted inputs = %#v", got)
	}
	got[0].ID = "changed"
	if inputs[0].ID != "zeta" {
		t.Fatal("SortedInputs aliased caller slice")
	}
}

func validRequest(now time.Time) Request {
	return Request{
		APIVersion:   APIVersionV1Alpha1,
		ActionCardID: "action-card-1",
		ExecutionID:  "execution-1",
		TraceID:      "trace-000000000001",
		TenantID:     "tenant-1",
		StackID:      "stack-1",
		Primitive: PrimitiveBinding{
			ID: "apply-stackkit-change", ContractHash: digest("a"), OperationClass: "product-apply",
		},
		ResolvedPlanHash: digest("1"),
		Approval: ApprovalBinding{
			ReceiptRef: "approval:receipt-1", ReceiptHash: digest("2"), Decision: "approved", Class: ApprovalClassOwnerStepUp,
			ApprovedAt: canonical(now.Add(-time.Minute)), ValidUntil: canonical(now.Add(10 * time.Minute)),
		},
		Grant: GrantBinding{
			BindingRef: "grant:binding-1", BindingHash: digest("3"), Audience: "stackkits", Scopes: []string{"stackkit-apply"},
			GrantedAt: canonical(now.Add(-time.Minute)), ValidUntil: canonical(now.Add(10 * time.Minute)),
		},
		Target:          TargetBinding{Scope: TargetScopeStack},
		Inputs:          []Input{{ID: "approved-change-ref", Type: InputTypeOpaqueReference, OpaqueRef: "change:approved-42"}},
		EvidenceSinkRef: "evidence:action-card-1",
		IssuedAt:        canonical(now.Add(-5 * time.Second)),
		ValidUntil:      canonical(now.Add(4 * time.Minute)),
		Nonce:           "nonce-000000000001",
		IdempotencyKey:  "idempotency-000001",
	}
}

func cloneRequest(t *testing.T, request Request) Request {
	t.Helper()
	data := mustJSON(t, request)
	var clone Request
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func digest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func canonical(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
