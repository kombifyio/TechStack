package guardidentity

import (
	"encoding/json"
	"testing"
)

// RFC 8037 Appendix A.3 publishes the thumbprint of its Ed25519 example key.
const rfc8037X = "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"

func TestThumbprintMatchesRFC8037AndRejectsNonCanonicalKeys(t *testing.T) {
	jwk := JWK{Kty: "OKP", Crv: "Ed25519", X: rfc8037X}
	thumbprint, err := jwk.Thumbprint()
	if err != nil {
		t.Fatal(err)
	}
	if thumbprint != "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k" {
		t.Fatalf("thumbprint = %q, want the RFC 8037 A.3 value", thumbprint)
	}
	canonical, err := jwk.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"crv":"Ed25519","kty":"OKP","x":"` + rfc8037X + `"}`; canonical != want {
		t.Fatalf("canonical = %s, want %s", canonical, want)
	}

	// The base64 decoder skips CR and LF, so these decode to the same key.
	// Accepting them would give one key a second thumbprint and persist a
	// binding with a raw control character inside a JSON string.
	for _, x := range []string{rfc8037X[:20] + "\n" + rfc8037X[20:], rfc8037X + "\r\n", rfc8037X + "="} {
		variant := JWK{Kty: "OKP", Crv: "Ed25519", X: x}
		if _, err := variant.PublicKey(); err == nil {
			t.Fatalf("non-canonical x %q was accepted", x)
		}
		if _, err := variant.Thumbprint(); err == nil {
			t.Fatalf("non-canonical x %q produced a thumbprint", x)
		}
		raw, err := json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "x": x})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseJWK(raw); err == nil {
			t.Fatalf("ParseJWK accepted non-canonical x %q", x)
		}
	}
}
