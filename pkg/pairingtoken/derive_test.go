package pairingtoken

import (
	"bytes"
	"strings"
	"testing"
)

func testSeed(fill byte) []byte {
	return bytes.Repeat([]byte{fill}, 32)
}

// The whole reason Derive exists: a provisioning retry must reproduce the exact
// bytes, or the token changes the at-most-once request digest and the dispatch
// guard rejects the replay while the VM already exists.
func TestDeriveIsByteIdenticalAcrossCalls(t *testing.T) {
	first, firstHash, err := Derive(testSeed(0x11), "tenant-demo", "op_58534d9e")
	if err != nil {
		t.Fatalf("first derive: %v", err)
	}
	second, secondHash, err := Derive(testSeed(0x11), "tenant-demo", "op_58534d9e")
	if err != nil {
		t.Fatalf("second derive: %v", err)
	}
	if first != second {
		t.Fatalf("derive is not deterministic:\n first=%q\nsecond=%q", first, second)
	}
	if firstHash != secondHash {
		t.Fatalf("derived hash is not deterministic: %q vs %q", firstHash, secondHash)
	}
}

// A derived token must be indistinguishable from a generated one, so that every
// redemption, expiry and single-use rule already written applies unchanged.
func TestDerivedTokenIsCanonicalAndTenantRoutable(t *testing.T) {
	raw, hash, err := Derive(testSeed(0x22), "tenant-demo", "op_1")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse rejected a derived token: %v", err)
	}
	if parsed.Legacy {
		t.Fatal("derived token was classified as a retired opaque token")
	}
	if parsed.TenantID != "tenant-demo" {
		t.Fatalf("tenant = %q, want tenant-demo", parsed.TenantID)
	}
	if parsed.TokenHash != hash {
		t.Fatalf("Parse hash %q != returned hash %q", parsed.TokenHash, hash)
	}
	if !strings.HasPrefix(raw, Prefix+".") {
		t.Fatalf("derived token %q lacks the canonical prefix", raw)
	}
	if len(raw) > MaxWireBytes {
		t.Fatalf("derived token exceeds MaxWireBytes: %d", len(raw))
	}
}

// Two provisioning attempts, two tenants, or two seeds must never collide —
// otherwise one VM could redeem another's capability.
func TestDeriveSeparatesSeedTenantAndScope(t *testing.T) {
	base, _, err := Derive(testSeed(0x33), "tenant-a", "op_1")
	if err != nil {
		t.Fatalf("base derive: %v", err)
	}
	for name, derive := range map[string]func() (string, string, error){
		"different scope":  func() (string, string, error) { return Derive(testSeed(0x33), "tenant-a", "op_2") },
		"different tenant": func() (string, string, error) { return Derive(testSeed(0x33), "tenant-b", "op_1") },
		"different seed":   func() (string, string, error) { return Derive(testSeed(0x44), "tenant-a", "op_1") },
	} {
		t.Run(name, func(t *testing.T) {
			other, _, err := derive()
			if err != nil {
				t.Fatalf("derive: %v", err)
			}
			if other == base {
				t.Fatal("derivation collided with the base token")
			}
		})
	}
}

// Separator injection must not let one (tenant, scope) pair impersonate another.
func TestDeriveResistsSeparatorAmbiguity(t *testing.T) {
	left, _, err := Derive(testSeed(0x55), "ab", "cd")
	if err != nil {
		t.Fatalf("left derive: %v", err)
	}
	right, _, err := Derive(testSeed(0x55), "a", "bcd")
	if err != nil {
		t.Fatalf("right derive: %v", err)
	}
	if left == right {
		t.Fatal("concatenation ambiguity: (\"ab\",\"cd\") and (\"a\",\"bcd\") derived the same token")
	}
}

// A missing or placeholder secret must fail rather than mint a guessable
// capability that would enroll an attacker's host into the tenant.
func TestDeriveRefusesWeakOrUnboundInput(t *testing.T) {
	for name, tc := range map[string]struct {
		seed    []byte
		tenant  string
		scope   string
		wantErr error
	}{
		"nil seed":         {nil, "tenant-demo", "op_1", ErrWeakDeriveSeed},
		"short seed":       {bytes.Repeat([]byte{0x66}, 31), "tenant-demo", "op_1", ErrWeakDeriveSeed},
		"empty tenant":     {testSeed(0x77), "", "op_1", ErrInvalidToken},
		"control tenant":   {testSeed(0x77), "tenant\x00demo", "op_1", ErrInvalidToken},
		"empty scope":      {testSeed(0x77), "tenant-demo", "", ErrEmptyDeriveScope},
		"blank-only scope": {testSeed(0x77), "tenant-demo", "   ", ErrEmptyDeriveScope},
	} {
		t.Run(name, func(t *testing.T) {
			raw, hash, err := Derive(tc.seed, tc.tenant, tc.scope)
			if err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if raw != "" || hash != "" {
				t.Fatalf("a refused derivation still returned material: raw=%q hash=%q", raw, hash)
			}
		})
	}
}
