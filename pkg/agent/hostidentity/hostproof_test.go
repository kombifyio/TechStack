package hostidentity

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/guardidentity"
)

// The Guard signs a host proof only while its identity is usable, and only with
// its own key: a revoked or recovery-needed Guard mints nothing, so stale
// state cannot keep reach alive.
func TestMintHostProofSignsWithOwnKeyOnlyWhileIdentityIsUsable(t *testing.T) {
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := guardidentity.HostProofInput{Issuer: "worker-1", Subject: "server-1", TenantID: "t1", Audience: guardidentity.AudienceRelay, Generation: 2}
	if _, err := manager.MintHostProof(input); !errors.Is(err, ErrNoIdentityKey) {
		t.Fatalf("before the key exists = %v", err)
	}
	if err := manager.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	proof, err := manager.MintHostProof(input)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := guardidentity.VerifyHostProof(proof, guardidentity.HostProofExpectation{
		Issuer: "worker-1", Subject: "server-1", TenantID: "t1", Audience: guardidentity.AudienceRelay, Generation: 2,
	})
	if err != nil || verified.Thumbprint != manager.Record().KeyThumbprint {
		t.Fatalf("proof = %v, %v", verified, err)
	}
	for state, want := range map[State]error{StateRevoked: ErrRevoked, StateRecoveryNeeded: ErrRecoveryNeeded} {
		if err := manager.SetState(state, "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.MintHostProof(input); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", state, err, want)
		}
	}
}
