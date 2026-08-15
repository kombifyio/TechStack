package workerauth

import "testing"

func TestDeriveOpaqueTokenIsDeterministicAndFullyBound(t *testing.T) {
	t.Parallel()

	secret := []byte("unit-test-worker-token-secret")
	base := OpaqueTokenContext{
		TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1",
		ServerID: "server-1", RuntimeAgentID: "runtime-1",
		RequestDigest: "request-digest", IdempotencyKey: "connect-attempt-1",
		Generation: 1,
	}
	first, err := DeriveOpaqueToken(secret, base)
	if err != nil {
		t.Fatalf("derive first token: %v", err)
	}
	second, err := DeriveOpaqueToken(secret, base)
	if err != nil {
		t.Fatalf("derive replay token: %v", err)
	}
	if first != second || !IsOpaqueToken(first) {
		t.Fatalf("deterministic opaque token mismatch: first=%q second=%q", first, second)
	}

	mutations := []struct {
		name string
		edit func(*OpaqueTokenContext)
	}{
		{name: "tenant", edit: func(ctx *OpaqueTokenContext) { ctx.TenantID = "tenant-2" }},
		{name: "owner", edit: func(ctx *OpaqueTokenContext) { ctx.OwnerID = "owner-2" }},
		{name: "stack", edit: func(ctx *OpaqueTokenContext) { ctx.StackID = "stack-2" }},
		{name: "server", edit: func(ctx *OpaqueTokenContext) { ctx.ServerID = "server-2" }},
		{name: "worker", edit: func(ctx *OpaqueTokenContext) { ctx.RuntimeAgentID = "runtime-2" }},
		{name: "request", edit: func(ctx *OpaqueTokenContext) { ctx.RequestDigest = "other-request" }},
		{name: "idempotency", edit: func(ctx *OpaqueTokenContext) { ctx.IdempotencyKey = "connect-attempt-2" }},
		{name: "generation", edit: func(ctx *OpaqueTokenContext) { ctx.Generation = 2 }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := base
			mutation.edit(&changed)
			token, deriveErr := DeriveOpaqueToken(secret, changed)
			if deriveErr != nil {
				t.Fatalf("derive changed token: %v", deriveErr)
			}
			if token == first {
				t.Fatalf("%s change did not change credential", mutation.name)
			}
		})
	}
}

func TestDeriveOpaqueTokenRejectsIncompleteContext(t *testing.T) {
	t.Parallel()

	if _, err := DeriveOpaqueToken(nil, OpaqueTokenContext{}); err == nil {
		t.Fatal("missing secret accepted")
	}
	if _, err := DeriveOpaqueToken([]byte("secret"), OpaqueTokenContext{
		TenantID: "tenant", OwnerID: "owner", ServerID: "server",
		RuntimeAgentID: "runtime", RequestDigest: "digest",
		IdempotencyKey: "key", Generation: 0,
	}); err == nil {
		t.Fatal("non-positive generation accepted")
	}
}

func TestKeyedDigestIsDomainSeparated(t *testing.T) {
	t.Parallel()

	secret := []byte("secret")
	first, err := KeyedDigest(secret, "credential-idempotency/v1", "same-value")
	if err != nil {
		t.Fatal(err)
	}
	replay, _ := KeyedDigest(secret, "credential-idempotency/v1", "same-value")
	otherDomain, _ := KeyedDigest(secret, "other-domain/v1", "same-value")
	if first != replay || first == otherDomain || len(first) != 64 {
		t.Fatalf("unexpected keyed digests: first=%q replay=%q other=%q", first, replay, otherDomain)
	}
}
