package guardidentity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A host proof is the Guard's Connect and relay credential: it must be bound to
// one audience, worker, server, tenant and generation, live at most five
// minutes, carry only the public key, and be verifiable the way both downstream
// verifiers do.
func TestHostProofIsBoundToAudienceKeyAndClaims(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	input := HostProofInput{Issuer: "worker-1", Subject: "server-1", TenantID: "org_acme", Audience: AudienceConnect, Generation: 3, Now: now}
	proof, err := SignHostProof(key, input)
	if err != nil {
		t.Fatal(err)
	}

	// Exactly the profile's wire shape: three header members, string audience,
	// no private key material anywhere in the token.
	parts := strings.Split(proof, ".")
	header, claims := decodePart(t, parts[0]), decodePart(t, parts[1])
	if len(header) != 3 || header["alg"] != "EdDSA" || header["typ"] != HostProofType {
		t.Fatalf("header = %v", header)
	}
	jwk, _ := header["jwk"].(map[string]any)
	if len(jwk) != 3 || jwk["x"] != PublicJWK(key).X {
		t.Fatalf("jwk = %v", jwk)
	}
	if claims["aud"] != AudienceConnect || claims["iss"] != "worker-1" || claims["sub"] != "server-1" || claims["tid"] != "org_acme" || claims["gen"] != float64(3) {
		t.Fatalf("claims = %v", claims)
	}
	seed := base64.RawURLEncoding.EncodeToString(key.Seed())
	if strings.Contains(proof, seed) || strings.Contains(proof, base64.RawURLEncoding.EncodeToString(key)) {
		t.Fatal("the proof carries private key material")
	}

	expect := HostProofExpectation{Issuer: "worker-1", Subject: "server-1", TenantID: "org_acme", Audience: AudienceConnect, Generation: 3, Now: now}
	verified, err := VerifyHostProof(proof, expect)
	if err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	if want, _ := PublicJWK(key).Thumbprint(); verified.Thumbprint != want {
		t.Fatal("verified thumbprint is not the signing key's")
	}

	// A request for a lifetime beyond five minutes is capped, never honored.
	long, err := SignHostProof(key, HostProofInput{Issuer: "worker-1", Subject: "server-1", TenantID: "t", Audience: AudienceRelay, Generation: 1, Now: now, Lifetime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	longClaims := decodePart(t, strings.Split(long, ".")[1])
	if lifetime := longClaims["exp"].(float64) - longClaims["iat"].(float64); lifetime > HostProofMaxLifetime.Seconds() {
		t.Fatalf("lifetime = %vs", lifetime)
	}

	otherKey, _ := GenerateKey()
	resign := func(tamper func(header, claims map[string]any)) string {
		h, c := decodePart(t, parts[0]), decodePart(t, parts[1])
		tamper(h, c)
		hb, _ := json.Marshal(h)
		cb, _ := json.Marshal(c)
		signing := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
		return signing + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(signing)))
	}
	forged := func() string {
		return parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(otherKey, []byte(parts[0]+"."+parts[1])))
	}
	for name, test := range map[string]struct {
		proof  string
		expect HostProofExpectation
		reason string
	}{
		"relay audience":     {proof, HostProofExpectation{Issuer: "worker-1", Subject: "server-1", TenantID: "org_acme", Audience: AudienceRelay, Generation: 3, Now: now}, ReasonHostProofAudience},
		"other worker":       {proof, HostProofExpectation{Issuer: "worker-2", Subject: "server-1", TenantID: "org_acme", Audience: AudienceConnect, Generation: 3, Now: now}, ReasonHostProofMismatch},
		"other server":       {proof, HostProofExpectation{Issuer: "worker-1", Subject: "server-2", TenantID: "org_acme", Audience: AudienceConnect, Generation: 3, Now: now}, ReasonHostProofMismatch},
		"other tenant":       {proof, HostProofExpectation{Issuer: "worker-1", Subject: "server-1", TenantID: "org_other", Audience: AudienceConnect, Generation: 3, Now: now}, ReasonHostProofMismatch},
		"other generation":   {proof, HostProofExpectation{Issuer: "worker-1", Subject: "server-1", TenantID: "org_acme", Audience: AudienceConnect, Generation: 4, Now: now}, ReasonHostProofMismatch},
		"expired":            {proof, HostProofExpectation{Issuer: "worker-1", Subject: "server-1", TenantID: "org_acme", Audience: AudienceConnect, Generation: 3, Now: now.Add(HostProofDefaultLifetime + HostProofSkew + time.Second)}, ReasonHostProofExpired},
		"not yet valid":      {proof, HostProofExpectation{Issuer: "worker-1", Subject: "server-1", TenantID: "org_acme", Audience: AudienceConnect, Generation: 3, Now: now.Add(-HostProofSkew - time.Second)}, ReasonHostProofFuture},
		"signature of other": {forged(), expect, ReasonHostProofSignature},
		"extra header":       {resign(func(h, _ map[string]any) { h["crit"] = []string{"b64"} }), expect, ReasonHostProofHeader},
		"kid header":         {resign(func(h, _ map[string]any) { h["kid"] = "k" }), expect, ReasonHostProofHeader},
		"private member":     {resign(func(h, _ map[string]any) { h["jwk"].(map[string]any)["d"] = "AAAA" }), expect, ReasonHostProofHeader},
		"six minute life":    {resign(func(_, c map[string]any) { c["exp"] = c["iat"].(float64) + 360 }), expect, ReasonHostProofLifetime},
		"short jti":          {resign(func(_, c map[string]any) { c["jti"] = "short" }), expect, ReasonHostProofClaims},
		"array audience":     {resign(func(_, c map[string]any) { c["aud"] = []string{AudienceConnect} }), expect, ReasonHostProofClaims},
		"garbage":            {"not-a-jwt", expect, ReasonHostProofMalformed},
	} {
		_, verifyErr := VerifyHostProof(test.proof, test.expect)
		denied, ok := verifyErr.(*Error)
		if !ok || denied.Code != CodeProofInvalid || denied.Reason != test.reason {
			t.Errorf("%s: got %v, want %s", name, verifyErr, test.reason)
		}
	}

	// The signer refuses what no verifier would accept.
	for name, bad := range map[string]HostProofInput{
		"unknown audience": {Issuer: "w", Subject: "s", TenantID: "t", Audience: "https://evil.example", Generation: 1},
		"zero generation":  {Issuer: "w", Subject: "s", TenantID: "t", Audience: AudienceRelay},
		"no tenant":        {Issuer: "w", Subject: "s", Audience: AudienceRelay, Generation: 1},
	} {
		if _, signErr := SignHostProof(key, bad); signErr == nil {
			t.Errorf("%s: signed", name)
		}
	}
}

func decodePart(t *testing.T, part string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
