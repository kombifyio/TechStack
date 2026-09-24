package middleware

import (
	"context"
	"sort"
	"strings"
)

type signedEntitlementsContextKey struct{}

type membershipEntitlementsContextKey struct{}

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

// WithMembershipEntitlements attaches grants that were persisted server-side
// from a verified session's identity claims (the Cloud membership fallback).
// They are NOT Edge-signed: the Gateway never vouched for them per request,
// so they live in a separate bucket. Consumers that authorize cost-bearing or
// provider-mutating work must keep reading SignedEntitlementsFromContext;
// read-side surfaces that intentionally accept the membership fallback use
// AuthorizedEntitlementsFromContext.
func WithMembershipEntitlements(ctx context.Context, entitlements ...string) context.Context {
	values := make(map[string]struct{}, len(entitlements))
	for _, entitlement := range entitlements {
		values[entitlement] = struct{}{}
	}
	return context.WithValue(ctx, membershipEntitlementsContextKey{}, SignedEntitlements{values: values})
}

// MembershipEntitlementsFromContext returns the server-owned membership grants.
func MembershipEntitlementsFromContext(ctx context.Context) (SignedEntitlements, bool) {
	if ctx == nil {
		return SignedEntitlements{}, false
	}
	value, ok := ctx.Value(membershipEntitlementsContextKey{}).(SignedEntitlements)
	return value, ok
}

// EntitlementSource names where an authorized entitlement set came from.
type EntitlementSource string

const (
	EntitlementSourceSignedEdge EntitlementSource = "signed_edge"
	EntitlementSourceMembership EntitlementSource = "membership"
)

// AuthorizedEntitlementsFromContext returns the Edge-signed grants when
// present, otherwise the membership fallback, together with its source.
// Existing Edge-signed grants always win; the fallback never merges into them.
func AuthorizedEntitlementsFromContext(ctx context.Context) (SignedEntitlements, EntitlementSource, bool) {
	if signed, ok := SignedEntitlementsFromContext(ctx); ok {
		return signed, EntitlementSourceSignedEdge, true
	}
	if membership, ok := MembershipEntitlementsFromContext(ctx); ok {
		return membership, EntitlementSourceMembership, true
	}
	return SignedEntitlements{}, "", false
}
