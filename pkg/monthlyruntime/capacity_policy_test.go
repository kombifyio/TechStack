package monthlyruntime

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/edgeauth"
)

func TestManagedRuntimeCapacityPolicyRejectsInvalidConstruction(t *testing.T) {
	for name, authority := range map[string]CapacityAuthority{
		"caller-selected authority":             CapacityAuthority("caller-selected"),
		"signed authority without entitlements": CapacityAuthoritySignedEdge,
	} {
		t.Run(name, func(t *testing.T) {
			resolver, err := NewManagedRuntimeCapacityPolicy(authority, nil)
			if !errors.Is(err, ErrCapacityPolicyInvalidConfiguration) || resolver != nil {
				t.Fatalf("resolver=%#v error=%v, want fail-closed invalid configuration", resolver, err)
			}
		})
	}
}

func TestSignedManagedRuntimeCapacityPolicyResolvesLimitedBudget(t *testing.T) {
	resolver := newCapacityPolicyForTest(t, CapacityAuthoritySignedEdge)
	grant, err := resolver.ResolveCapacity(signedCapacityContext(t, ProviderIONOS, json.RawMessage(`{"managed_servers":{"mode":"limited","limit":3}}`)), CapacityPolicyRequest{
		TenantID: " tenant-1 ", OwnerSubjectID: " owner-1 ", ProviderID: ProviderIONOS,
	})
	if err != nil {
		t.Fatalf("ResolveCapacity: %v", err)
	}
	want := CapacityGrant{
		ScopeKind: CapacityScopeKindOwnerSubject, ScopeID: "owner-1",
		Mode: CapacityModeLimited, Limit: 3, DecisionSource: CapacityDecisionSourceSignedRuntimeBudget,
	}
	if grant != want {
		t.Fatalf("grant=%#v, want %#v", grant, want)
	}
}

func TestSignedManagedRuntimeCapacityPolicyRequiresCommercialEntitlements(t *testing.T) {
	resolver, err := NewManagedRuntimeCapacityPolicy(CapacityAuthoritySignedEdge, func(_ context.Context, entitlement string) (bool, error) {
		return entitlement == FeatureTechStackManagedRuntime, nil
	})
	if err != nil {
		t.Fatalf("NewManagedRuntimeCapacityPolicy: %v", err)
	}
	ctx := signedCapacityContext(t, ProviderIONOS, json.RawMessage(`{"managed_servers":{"mode":"limited","limit":3}}`))
	_, err = resolver.ResolveCapacity(ctx, CapacityPolicyRequest{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", ProviderID: ProviderIONOS,
	})
	if !errors.Is(err, ErrCapacityPolicyAuthorityUnavailable) {
		t.Fatalf("error=%v, want missing signed provider entitlement", err)
	}
}

func TestSignedManagedRuntimeCapacityPolicyResolvesOnlyExplicitUnlimitedMode(t *testing.T) {
	resolver := newCapacityPolicyForTest(t, CapacityAuthoritySignedEdge)
	grant, err := resolver.ResolveCapacity(signedCapacityContext(t, ProviderCentron, json.RawMessage(`{"managed_servers":{"mode":"unlimited"}}`)), CapacityPolicyRequest{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", ProviderID: ProviderCentron,
	})
	if err != nil {
		t.Fatalf("ResolveCapacity: %v", err)
	}
	if grant.Mode != CapacityModeUnlimited || grant.Limit != 0 || grant.DecisionSource != CapacityDecisionSourceSignedRuntimeBudget {
		t.Fatalf("grant=%#v, want explicit signed unlimited", grant)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"managed_servers":null}`),
		json.RawMessage(`{"managed_servers":{"mode":"limited","limit":0}}`),
		json.RawMessage(`{"managed_servers":{"mode":"limited","limit":3.5}}`),
		json.RawMessage(`{"managed_servers":{"mode":"unlimited","limit":3}}`),
		json.RawMessage(`{"managed_servers":{"mode":"unknown"}}`),
		json.RawMessage(`{"managed_servers":{"mode":"limited","limit":3,"caller":true}}`),
		json.RawMessage(`{"managed_servers":{"mode":"limited","limit":3},"override":true}`),
		json.RawMessage(`{"managed_servers":true}`),
	} {
		if _, err := resolver.ResolveCapacity(signedCapacityContext(t, ProviderCentron, raw), CapacityPolicyRequest{
			TenantID: "tenant-1", OwnerSubjectID: "owner-1", ProviderID: ProviderCentron,
		}); !errors.Is(err, ErrCapacityPolicyAuthorityUnavailable) {
			t.Fatalf("budget=%s error=%v, want fail-closed", raw, err)
		}
	}
}

func TestSignedManagedRuntimeCapacityPolicyRejectsUnverifiedOrMismatchedDecision(t *testing.T) {
	validRequest := CapacityPolicyRequest{TenantID: "tenant-1", OwnerSubjectID: "owner-1", ProviderID: ProviderIONOS}
	validBudget := json.RawMessage(`{"managed_servers":{"mode":"limited","limit":3}}`)
	for _, test := range []struct {
		name           string
		contextFactory func(*testing.T) context.Context
		request        CapacityPolicyRequest
	}{
		{name: "missing verified decision", contextFactory: func(t *testing.T) context.Context { return t.Context() }},
		{
			name: "application-constructed budget",
			contextFactory: func(t *testing.T) context.Context {
				return edgeauth.FlagsToContext(t.Context(), edgeauth.FlagSet{Budgets: map[string]json.RawMessage{
					CapacityBudgetCloudRuntimeCredits: json.RawMessage(`{"managed_servers":{"mode":"unlimited"}}`),
				}})
			},
		},
		{name: "tenant transplant", request: CapacityPolicyRequest{TenantID: "tenant-other", OwnerSubjectID: "owner-1", ProviderID: ProviderIONOS}},
		{name: "owner transplant", request: CapacityPolicyRequest{TenantID: "tenant-1", OwnerSubjectID: "owner-other", ProviderID: ProviderIONOS}},
		{
			name: "mutated verified decision",
			contextFactory: func(t *testing.T) context.Context {
				ctx := signedCapacityContext(t, ProviderIONOS, validBudget)
				decisions, ok := edgeauth.FlagsFromContext(ctx)
				if !ok {
					t.Fatal("verified decision missing from test context")
				}
				decisions.Budgets[CapacityBudgetCloudRuntimeCredits] = json.RawMessage(`{"managed_servers":{"mode":"unlimited"}}`)
				return edgeauth.FlagsToContext(t.Context(), decisions)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var ctx context.Context
			if test.contextFactory != nil {
				ctx = test.contextFactory(t)
			} else {
				ctx = signedCapacityContext(t, ProviderIONOS, validBudget)
			}
			request := test.request
			if request.TenantID == "" {
				request = validRequest
			}
			resolver := newCapacityPolicyForTest(t, CapacityAuthoritySignedEdge)
			if _, err := resolver.ResolveCapacity(ctx, request); !errors.Is(err, ErrCapacityPolicyAuthorityUnavailable) {
				t.Fatalf("ResolveCapacity error = %v, want ErrCapacityPolicyAuthorityUnavailable", err)
			}
		})
	}
}

func TestSelfHostManagedRuntimeCapacityPolicyIsConstructionFixedUnlimited(t *testing.T) {
	resolver := newCapacityPolicyForTest(t, CapacityAuthoritySelfHostOSS)
	grant, err := resolver.ResolveCapacity(t.Context(), CapacityPolicyRequest{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", ProviderID: ProviderIONOS,
	})
	if err != nil {
		t.Fatalf("ResolveCapacity: %v", err)
	}
	if grant.Mode != CapacityModeUnlimited || grant.Limit != 0 || grant.DecisionSource != CapacityDecisionSourceSelfHostManifest {
		t.Fatalf("grant=%#v, want static self-host unlimited", grant)
	}
}

func TestManagedRuntimeCapacityPolicyRejectsInvalidRequest(t *testing.T) {
	resolver := newCapacityPolicyForTest(t, CapacityAuthoritySelfHostOSS)
	for _, request := range []CapacityPolicyRequest{
		{OwnerSubjectID: "owner-1", ProviderID: ProviderIONOS},
		{TenantID: "tenant-1", ProviderID: ProviderIONOS},
		{TenantID: "tenant-1", OwnerSubjectID: "owner-1", ProviderID: "ionos-managed"},
	} {
		if _, err := resolver.ResolveCapacity(t.Context(), request); !errors.Is(err, ErrCapacityPolicyInvalidRequest) {
			t.Fatalf("request=%#v error=%v, want invalid request", request, err)
		}
	}
}

func TestNilManagedRuntimeCapacityPolicyFailsClosed(t *testing.T) {
	var policy *managedRuntimeCapacityPolicy
	if _, err := policy.ResolveCapacity(t.Context(), CapacityPolicyRequest{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", ProviderID: ProviderIONOS,
	}); !errors.Is(err, ErrCapacityPolicyInvalidConfiguration) {
		t.Fatalf("error=%v, want invalid configuration", err)
	}
}

func newCapacityPolicyForTest(t *testing.T, authority CapacityAuthority) CapacityPolicyResolver {
	t.Helper()
	var entitlements CommercialEntitlementResolver
	if authority == CapacityAuthoritySignedEdge {
		entitlements = func(context.Context, string) (bool, error) { return true, nil }
	}
	resolver, err := NewManagedRuntimeCapacityPolicy(authority, entitlements)
	if err != nil {
		t.Fatalf("NewManagedRuntimeCapacityPolicy: %v", err)
	}
	return resolver
}

func signedCapacityContext(t *testing.T, providerID string, budget json.RawMessage) context.Context {
	t.Helper()
	const (
		edgeSecret  = "edge-secret"
		flagsSecret = "flags-secret"
	)
	now := time.Now().UTC().Truncate(time.Second)
	signedPath := "/api/v1/stacks/stack-1/managed-runtimes?provider=" + providerID
	r, err := http.NewRequest(http.MethodPost, "https://techstack.internal"+signedPath, nil)
	if err != nil {
		t.Fatalf("new capacity decision request: %v", err)
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	r.Header.Set(edgeauth.HeaderEdgeAuth, edgeauth.EdgeAuthValueJWT)
	r.Header.Set(edgeauth.HeaderEdgeService, "techstack")
	r.Header.Set(edgeauth.HeaderPublicPrefix, "/v1/techstack")
	r.Header.Set(edgeauth.HeaderUserID, "owner-1")
	r.Header.Set(edgeauth.HeaderOrgID, "tenant-1")
	r.Header.Set(edgeauth.HeaderRequestID, "request-1")
	r.Header.Set(edgeauth.HeaderEdgeKeyID, "edge-primary")
	r.Header.Set(edgeauth.HeaderEdgeTimestamp, timestamp)
	r.Header.Set(edgeauth.HeaderEdgeNonce, "nonce-1")
	r.Header.Set(edgeauth.HeaderEdgeSignedPath, signedPath)
	edgePayload := strings.Join([]string{
		"v2", "edge-primary", http.MethodPost, signedPath,
		edgeauth.EdgeAuthValueJWT, "techstack", "/v1/techstack",
		"owner-1", "tenant-1", "", "", "", "", "", "",
		timestamp, "nonce-1",
	}, "\n")
	edgeMAC := hmac.New(sha256.New, []byte(edgeSecret))
	_, _ = edgeMAC.Write([]byte(edgePayload))
	edgeSignature := "v2=" + base64.RawURLEncoding.EncodeToString(edgeMAC.Sum(nil))
	r.Header.Set(edgeauth.HeaderEdgeSignature, edgeSignature)

	decisionHeaders, err := edgeauth.SignDecisionHeaders(edgeauth.DecisionSignInput{
		Secret:        flagsSecret,
		KeyID:         "flags-primary",
		Method:        r.Method,
		SignedPath:    signedPath,
		Audience:      "techstack",
		PublicPrefix:  "/v1/techstack",
		SubjectID:     "owner-1",
		TenantID:      "tenant-1",
		RequestID:     "request-1",
		EdgeKeyID:     "edge-primary",
		EdgeTimestamp: timestamp,
		EdgeNonce:     "nonce-1",
		EdgeSignature: edgeSignature,
		Flags: map[string]bool{
			FeatureTechStackManagedRuntime:         true,
			FeatureTechStackManagedRuntimeCloudKit: true,
			FeatureTechStackManagedRuntimeIONOS:    providerID == ProviderIONOS,
			FeatureTechStackManagedRuntimeCentron:  providerID == ProviderCentron,
		},
		Budgets: map[string]any{CapacityBudgetCloudRuntimeCredits: budget},
	})
	if err != nil {
		t.Fatalf("sign capacity decision: %v", err)
	}
	for header, values := range decisionHeaders {
		for _, value := range values {
			r.Header.Set(header, value)
		}
	}
	decisions, err := edgeauth.VerifyDecisionHeaders(r, edgeauth.DecisionVerifyConfig{
		PrimarySecret:        flagsSecret,
		PrimaryKeyID:         "flags-primary",
		ExpectedAudience:     "techstack",
		ExpectedPublicPrefix: "/v1/techstack",
		SignatureWindow:      5 * time.Minute,
		Now:                  func() time.Time { return now },
		IdentityConfig: edgeauth.Config{
			EdgeAuthSecret:  edgeSecret,
			EdgeAuthKeyID:   "edge-primary",
			SignatureWindow: 5 * time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("verify capacity decision: %v", err)
	}
	return edgeauth.FlagsToContext(t.Context(), decisions)
}
