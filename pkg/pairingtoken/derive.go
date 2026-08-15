package pairingtoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// deriveDomain separates derived pairing secrets from every other use of the
// same seed. Changing it invalidates every previously derived token.
const deriveDomain = "kombify-techstack/pairing-token-derivation/v1"

// minimumSeedBytes rejects a seed that is too short to carry 256 bits of
// entropy. A derived token is only as unguessable as its seed, so a missing or
// placeholder secret must fail rather than mint a predictable capability.
const minimumSeedBytes = 32

// ErrWeakDeriveSeed means the supplied seed cannot back an unguessable token.
var ErrWeakDeriveSeed = errors.New("pairing token derive seed is too short")

// ErrEmptyDeriveScope means the caller supplied no binding for the derivation.
var ErrEmptyDeriveScope = errors.New("pairing token derive scope is required")

// Derive returns the canonical token whose secret is a deterministic function
// of seed, tenantID and scope. It is wire-identical to Generate — Parse cannot
// tell the two apart — so every redemption, expiry and single-use rule applies
// unchanged.
//
// It exists because a provisioning request that carries a pairing token in its
// cloud-init payload folds that token into the at-most-once request digest. A
// randomly generated token would therefore produce a different digest on every
// retry, the dispatch guard would reject the replay, and a VM that had already
// been created would be stranded with no cleanup handle. Deriving the secret
// makes the replay byte-identical.
//
// scope must bind the derivation to one provisioning attempt (the operation id
// is the natural choice); reusing a scope reissues the same capability.
func Derive(seed []byte, tenantID, scope string) (rawToken string, tokenHashHex string, err error) {
	if len(seed) < minimumSeedBytes {
		return "", "", ErrWeakDeriveSeed
	}
	if !validTenantID(tenantID) {
		return "", "", ErrInvalidToken
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "", "", ErrEmptyDeriveScope
	}

	mac := hmac.New(sha256.New, seed)
	// Length-free separation is safe here because NUL cannot occur in a valid
	// tenant id (validTenantID rejects control characters) and the domain is a
	// fixed literal, so no other (tenant, scope) pair can produce this preimage.
	mac.Write([]byte(deriveDomain))
	mac.Write([]byte{0})
	mac.Write([]byte(tenantID))
	mac.Write([]byte{0})
	mac.Write([]byte(scope))
	secret := mac.Sum(nil)
	if len(secret) != secretBytes {
		return "", "", ErrInvalidToken
	}

	rawToken = strings.Join([]string{
		Prefix,
		base64.RawURLEncoding.EncodeToString([]byte(tenantID)),
		base64.RawURLEncoding.EncodeToString(secret),
	}, ".")
	return rawToken, sha256Hex(rawToken), nil
}
