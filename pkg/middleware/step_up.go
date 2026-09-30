package middleware

import (
	"context"
	"strings"
	"time"
)

// Step-up headers the Gateway signs into the v7 edge identity envelope.
// EdgeIdentityMiddleware attaches them as StepUpClaims only after go-common
// verified a v7 envelope, then strips them; any older version, a missing
// envelope or the legacy shared-secret hop attaches nothing, so every step-up
// check fails closed.
const (
	// HeaderUserAMR carries the Auth0 access token `amr` values, comma separated.
	HeaderUserAMR = "X-User-AMR"
	// HeaderUserACR carries the token `acr` value.
	HeaderUserACR = "X-User-ACR"
	// HeaderUserAuthTime carries the token `auth_time` in Unix seconds.
	HeaderUserAuthTime = "X-User-Auth-Time"
)

// ACRMultiFactor is the OpenID PAPE multi-factor policy Auth0 reports in `acr`
// after an MFA step-up.
const ACRMultiFactor = "http://schemas.openid.net/pape/policies/2007/06/multi-factor"

// PrincipalTypeUser is the signed principal type of a direct human token.
const PrincipalTypeUser = "user"

// StepUpClaims are the authentication facts of the verified envelope.
type StepUpClaims struct {
	AMR      []string
	ACR      string
	AuthTime time.Time
	// PrincipalType is the v7-signed X-Kombify-Principal-Type. Only a direct
	// human token ("user") can present its own step-up; agent, on-behalf-of
	// and machine tokens never can.
	PrincipalType string
}

type stepUpContextKey struct{}

// WithVerifiedStepUp attaches claims already bound by a verified edge
// signature. Request handlers must never call it with client input.
func WithVerifiedStepUp(ctx context.Context, claims StepUpClaims) context.Context {
	claims.AMR = append([]string(nil), claims.AMR...)
	return context.WithValue(ctx, stepUpContextKey{}, claims)
}

// VerifiedStepUpFromContext returns the verified step-up claims, if any.
func VerifiedStepUpFromContext(ctx context.Context) (StepUpClaims, bool) {
	if ctx == nil {
		return StepUpClaims{}, false
	}
	claims, ok := ctx.Value(stepUpContextKey{}).(StepUpClaims)
	return claims, ok
}

// FreshMultiFactor reports a multi-factor authentication (amr contains "mfa"
// or acr is the PAPE multi-factor policy) no older than maxAge at now, by a
// principal signed as a human user.
func (c StepUpClaims) FreshMultiFactor(now time.Time, maxAge time.Duration) bool {
	if strings.TrimSpace(c.PrincipalType) != PrincipalTypeUser {
		return false
	}
	multiFactor := strings.TrimSpace(c.ACR) == ACRMultiFactor
	for _, method := range c.AMR {
		if strings.EqualFold(strings.TrimSpace(method), "mfa") {
			multiFactor = true
		}
	}
	if !multiFactor || c.AuthTime.IsZero() {
		return false
	}
	age := now.Sub(c.AuthTime)
	return age >= -time.Minute && age <= maxAge
}
