package rilaction

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestExecutorInvocationBindsExactRequestAndTrustedInstant(t *testing.T) {
	now := time.Date(2026, 7, 23, 7, 0, 0, 123, time.UTC)
	request := validRequest(now)
	requestDigest, err := ComputeRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := NewExecutorInvocation(request, requestDigest, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateExecutorInvocation(invocation); err != nil {
		t.Fatalf("ValidateExecutorInvocation() error = %v", err)
	}
	if invocation.RequestDigest() != requestDigest || invocation.EvaluatedAt() != canonical(now) ||
		!reflect.DeepEqual(invocation.Request(), request) {
		t.Fatalf("invocation lost exact authority: %#v", invocation)
	}

	request.Grant.Scopes[0] = "attacker"
	request.Inputs[0].OpaqueRef = "artifact:attacker"
	if got := invocation.Request(); got.Grant.Scopes[0] == "attacker" || got.Inputs[0].OpaqueRef == "artifact:attacker" {
		t.Fatal("invocation aliases caller-owned request data")
	}
	returned := invocation.Request()
	returned.Grant.Scopes[0] = "returned-attacker"
	if invocation.Request().Grant.Scopes[0] == "returned-attacker" {
		t.Fatal("Request() exposed mutable invocation authority")
	}
}

func TestExecutorInvocationRejectsSubstitutionAndNonUTCClock(t *testing.T) {
	now := time.Date(2026, 7, 23, 7, 0, 0, 0, time.UTC)
	request := validRequest(now)
	requestDigest, err := ComputeRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, invocationErr := NewExecutorInvocation(request, digest("9"), now); invocationErr == nil {
		t.Fatal("substituted request digest was accepted")
	}
	if _, invocationErr := NewExecutorInvocation(request, requestDigest, now.In(time.FixedZone("offset", 3600))); invocationErr == nil {
		t.Fatal("non-UTC invocation clock was accepted")
	}
	invocation, err := NewExecutorInvocation(request, requestDigest, now)
	if err != nil {
		t.Fatal(err)
	}
	invocation.request.Primitive.ID = "attacker"
	if validationErr := ValidateExecutorInvocation(invocation); validationErr == nil {
		t.Fatal("mutated invocation request was accepted")
	}
	invocation, err = NewExecutorInvocation(request, requestDigest, now)
	if err != nil {
		t.Fatal(err)
	}
	invocation.evaluatedAt = "2026-07-23T08:00:00+01:00"
	if err := ValidateExecutorInvocation(invocation); err == nil {
		t.Fatal("noncanonical invocation instant was accepted")
	}
}

func TestExecutorIdentityIsClosedAndInterfaceIsProviderFree(t *testing.T) {
	identity := ExecutorIdentity{Ref: "stackkits-governed-state-verifier-v1", Version: "1.0.0", ContractHash: digest("a")}
	if err := ValidateExecutorIdentity(identity); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []ExecutorIdentity{
		{},
		{Ref: "https://executor.invalid", Version: "1.0.0", ContractHash: digest("a")},
		{Ref: identity.Ref, Version: "1.0.0 unsafe", ContractHash: digest("a")},
		{Ref: identity.Ref, Version: "1.0.0", ContractHash: "sha256:attacker"},
	} {
		if err := ValidateExecutorIdentity(candidate); err == nil {
			t.Fatalf("invalid executor identity accepted: %#v", candidate)
		}
	}
	var _ Executor = executorContractProbe{identity: identity}
}

type executorContractProbe struct {
	identity ExecutorIdentity
}

func (e executorContractProbe) Identity() ExecutorIdentity {
	return e.identity
}

func (executorContractProbe) Execute(context.Context, ExecutorInvocation) (Evidence, error) {
	return Evidence{}, nil
}
