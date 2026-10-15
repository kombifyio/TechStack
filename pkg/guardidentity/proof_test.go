package guardidentity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProofValidationRejectsEveryForgeryAndAcceptsTheExactRequest(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	const url = "https://api.example.test/api/v1/workers/w1/heartbeat"
	const token = "tsga.fixture.token"
	sign := func(in ProofInput) string {
		in.Method, in.URL = firstNonEmptyString(in.Method, "POST"), firstNonEmptyString(in.URL, url)
		if in.Now.IsZero() {
			in.Now = now
		}
		proof, signErr := SignProof(key, in)
		if signErr != nil {
			t.Fatal(signErr)
		}
		return proof
	}
	expect := ProofExpectation{Method: "POST", URLs: []string{url}, AccessToken: token, Now: now}

	good, err := VerifyProof(sign(ProofInput{AccessToken: token}), expect)
	if err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	if want, _ := PublicJWK(key).Thumbprint(); good.Thumbprint != want {
		t.Fatal("proof did not carry the signing key")
	}

	otherKey, _ := GenerateKey()
	forged := func() string {
		// Header of a proof signed by another key but claiming this key's jwk.
		parts := strings.Split(sign(ProofInput{AccessToken: token}), ".")
		signature := ed25519.Sign(otherKey, []byte(parts[0]+"."+parts[1]))
		return parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(signature)
	}
	withPrivateMember := func() string {
		parts := strings.Split(sign(ProofInput{AccessToken: token}), ".")
		header, _ := base64.RawURLEncoding.DecodeString(parts[0])
		var decoded map[string]any
		_ = json.Unmarshal(header, &decoded)
		decoded["jwk"].(map[string]any)["d"] = "AAAA"
		tampered, _ := json.Marshal(decoded)
		return base64.RawURLEncoding.EncodeToString(tampered) + "." + parts[1] + "." + parts[2]
	}
	noneAlgorithm := func() string {
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"dpop+jwt"}`))
		return header + "." + strings.Split(sign(ProofInput{AccessToken: token}), ".")[1] + "."
	}

	for name, test := range map[string]struct {
		proof  string
		expect ProofExpectation
		reason string
	}{
		"other method":        {sign(ProofInput{AccessToken: token}), ProofExpectation{Method: "GET", URLs: expect.URLs, AccessToken: token, Now: now}, ReasonProofMethod},
		"other endpoint":      {sign(ProofInput{AccessToken: token, URL: "https://api.example.test/api/v1/workers/w1/inventory"}), expect, ReasonProofURI},
		"other host":          {sign(ProofInput{AccessToken: token, URL: "https://evil.example.test/api/v1/workers/w1/heartbeat"}), expect, ReasonProofURI},
		"other access token":  {sign(ProofInput{AccessToken: "tsga.other.token"}), expect, ReasonProofTokenBinding},
		"no token binding":    {sign(ProofInput{}), expect, ReasonProofTokenBinding},
		"issued too long ago": {sign(ProofInput{AccessToken: token, Now: now.Add(-ProofWindow - time.Second)}), expect, ReasonProofClock},
		"issued in future":    {sign(ProofInput{AccessToken: token, Now: now.Add(ProofWindow + time.Second)}), expect, ReasonProofClock},
		"signature of other":  {forged(), expect, ReasonProofSignature},
		"private key member":  {withPrivateMember(), expect, ReasonProofMalformed},
		"alg none":            {noneAlgorithm(), expect, ReasonProofSignature},
		"garbage":             {"not-a-jwt", expect, ReasonProofMalformed},
		"missing":             {"", expect, ReasonProofRequired},
	} {
		_, verifyErr := VerifyProof(test.proof, test.expect)
		denied, ok := verifyErr.(*Error)
		if !ok || denied.Code != CodeProofInvalid || denied.Reason != test.reason {
			t.Errorf("%s: got %v, want %s/%s", name, verifyErr, CodeProofInvalid, test.reason)
		}
	}

	// A proof inside the window is accepted at its edges.
	for _, offset := range []time.Duration{-ProofWindow + time.Second, ProofWindow - time.Second} {
		if _, err := VerifyProof(sign(ProofInput{AccessToken: token, Now: now.Add(offset)}), expect); err != nil {
			t.Errorf("proof at offset %v rejected: %v", offset, err)
		}
	}
}

func TestAccessTokenIsBoundToItsSecretAndExpires(t *testing.T) {
	secret := []byte("guard-access-secret-fixture")
	now := time.Now()
	token, _, err := IssueAccessToken(secret, AccessClaims{TenantID: "t", WorkerID: "w", JKT: "thumb", Cred: "c"}, now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyAccessToken(secret, token, now.Add(AccessTokenTTL-time.Second))
	if err != nil || claims.JKT != "thumb" {
		t.Fatalf("valid token = %+v, %v", claims, err)
	}
	if _, err := VerifyAccessToken(secret, token, now.Add(AccessTokenTTL)); err == nil || err.(*Error).Code != CodeTokenExpired {
		t.Fatalf("expired token = %v, want %s", err, CodeTokenExpired)
	}
	if _, err := VerifyAccessToken([]byte("another-secret"), token, now); err == nil || err.(*Error).Code != CodeProofInvalid {
		t.Fatalf("foreign secret = %v", err)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
