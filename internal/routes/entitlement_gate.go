package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/denial"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/middleware"
)

// EntitlementGate decides whether the request's verified principal holds a
// commercial entitlement. The composition root picks the deployment's gate;
// routes never infer it from request data.
type EntitlementGate interface {
	// SignedGrant accepts only an Edge-signed (v2) grant. Mutations that change
	// a customer's host use it: the server-owned membership fallback is not a
	// per-request Gateway decision.
	SignedGrant(ctx context.Context, entitlement string) bool
	// AuthorizedGrant accepts a signed grant or the membership fallback that a
	// verified Techstack session persisted (see contextWithMembershipAuthorization).
	AuthorizedGrant(ctx context.Context, entitlement string) bool
	// FreshMultiFactor reports a multi-factor sign-in no older than maxAge,
	// read only from the verified edge envelope.
	FreshMultiFactor(ctx context.Context, now time.Time, maxAge time.Duration) bool
}

type hostedEntitlementGate struct{}

func (hostedEntitlementGate) SignedGrant(ctx context.Context, entitlement string) bool {
	grants, ok := middleware.SignedEntitlementsFromContext(ctx)
	return ok && grants.Has(entitlement)
}

func (hostedEntitlementGate) AuthorizedGrant(ctx context.Context, entitlement string) bool {
	grants, _, ok := middleware.AuthorizedEntitlementsFromContext(ctx)
	return ok && grants.Has(entitlement)
}

func (hostedEntitlementGate) FreshMultiFactor(ctx context.Context, now time.Time, maxAge time.Duration) bool {
	claims, ok := middleware.VerifiedStepUpFromContext(ctx)
	return ok && claims.FreshMultiFactor(now, maxAge)
}

type selfHostedEntitlementGate struct{}

func (selfHostedEntitlementGate) SignedGrant(context.Context, string) bool     { return true }
func (selfHostedEntitlementGate) AuthorizedGrant(context.Context, string) bool { return true }
func (selfHostedEntitlementGate) FreshMultiFactor(context.Context, time.Time, time.Duration) bool {
	return true
}

// EntitlementGateForDeployment returns the fail-closed hosted gate for SaaS and
// the owner-operated gate for a self-hosted control plane, which has no
// commercial entitlement authority; there the owner scope and inventory
// policy remain the boundary, as in NewSelfHostedInventoryPolicy.
func EntitlementGateForDeployment(saas bool) EntitlementGate {
	if saas {
		return hostedEntitlementGate{}
	}
	return selfHostedEntitlementGate{}
}

// entitlementGateOrHosted keeps an unset gate fail-closed.
func entitlementGateOrHosted(gate EntitlementGate) EntitlementGate {
	if gate == nil {
		return hostedEntitlementGate{}
	}
	return gate
}

// writeEntitlementDenial writes the 403 client-error-envelope/v1 for a missing
// commercial entitlement and returns httpx.ErrResponseWritten.
func writeEntitlementDenial(e *httpx.Event, capability, required string) error {
	env := denial.Envelope{
		ErrorCode: "entitlement_required", ReasonCode: "missing_entitlement", Capability: capability,
		RequiredFeatures: []string{required}, MissingFeatures: []string{required},
		UserGuidance: denial.UserGuidance{
			Title:     "Your plan does not include this action",
			Body:      "This action needs an entitlement your account does not hold.",
			NextSteps: []string{"Upgrade to a plan that includes it, or ask your organization owner for access."},
		},
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return httpx.Reject(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Entitlement check unavailable", nil)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return httpx.Reject(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Entitlement check unavailable", nil)
	}
	payload["error"] = map[string]any{"code": ksapi.ErrCodeForbidden, "message": env.UserGuidance.Title, "details": map[string]any{"reason": env.ReasonCode}}
	if err := e.JSON(http.StatusForbidden, payload); err != nil {
		return err
	}
	return httpx.ErrResponseWritten
}
