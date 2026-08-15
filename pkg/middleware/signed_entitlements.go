package middleware

import (
	"context"
	"sort"
	"strings"
)

type signedEntitlementsContextKey struct{}

// SignedEntitlements is the immutable set extracted only after a v2 Edge
// identity signature has been verified. It is deliberately separate from
// feature flags because those are not authorization grants.
type SignedEntitlements struct {
	values map[string]struct{}
}

// Has reports whether the signed envelope granted entitlement. The exact "*"
// value is the Gateway's canonical all_features grant and therefore covers
// every non-empty entitlement name; it is never inferred from partial values.
func (s SignedEntitlements) Has(entitlement string) bool {
	entitlement = strings.TrimSpace(entitlement)
	if entitlement == "" {
		return false
	}
	if _, ok := s.values[entitlement]; ok {
		return true
	}
	_, wildcard := s.values["*"]
	return wildcard
}

// Values returns a detached, deterministic copy for a server-owned in-process
// handoff. It never exposes the internal map for mutation.
func (s SignedEntitlements) Values() []string {
	values := make([]string, 0, len(s.values))
	for value := range s.values {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

// SignedEntitlementsFromContext returns the verified Edge entitlement set.
func SignedEntitlementsFromContext(ctx context.Context) (SignedEntitlements, bool) {
	if ctx == nil {
		return SignedEntitlements{}, false
	}
	value, ok := ctx.Value(signedEntitlementsContextKey{}).(SignedEntitlements)
	return value, ok
}

// WithSignedEntitlements attaches grants already authenticated by a trusted
// identity adapter. Request handlers must never call it with client input.
func WithSignedEntitlements(ctx context.Context, entitlements ...string) context.Context {
	values := make(map[string]struct{}, len(entitlements))
	for _, entitlement := range entitlements {
		values[entitlement] = struct{}{}
	}
	return context.WithValue(ctx, signedEntitlementsContextKey{}, SignedEntitlements{values: values})
}
