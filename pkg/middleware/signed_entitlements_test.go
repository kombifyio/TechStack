package middleware

import "testing"

func TestSignedEntitlementsCanonicalWildcardCoversNonEmptyGrants(t *testing.T) {
	wildcard, ok := SignedEntitlementsFromContext(
		WithSignedEntitlements(t.Context(), "*"),
	)
	if !ok {
		t.Fatal("signed wildcard context missing")
	}
	for _, entitlement := range []string{
		"techstack.inventory.read",
		"techstack.managed.runtime.ionos",
		"*",
	} {
		if !wildcard.Has(entitlement) {
			t.Fatalf("canonical wildcard did not grant %q", entitlement)
		}
	}
	if wildcard.Has("") || wildcard.Has("   ") {
		t.Fatal("canonical wildcard granted an empty entitlement")
	}

	concrete, ok := SignedEntitlementsFromContext(
		WithSignedEntitlements(t.Context(), "techstack.inventory.read"),
	)
	if !ok || !concrete.Has("techstack.inventory.read") {
		t.Fatal("concrete entitlement missing")
	}
	if concrete.Has("techstack.inventory.operate") {
		t.Fatal("concrete entitlement acted as wildcard")
	}
}
