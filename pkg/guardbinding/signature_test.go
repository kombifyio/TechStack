package guardbinding

import (
	"bytes"
	"crypto/ed25519"
	"testing"
	"time"
)

// A signed binding verifies only unchanged: altering any admitted field, or
// relabelling it with another pinned key, voids the signature.
func TestSignedBindingVerifiesOnlyUnchanged(t *testing.T) {
	signer, err := NewSignerFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewSignerFromSeed(bytes.Repeat([]byte{2}, ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	pins := append(signer.PublicKeys(), other.PublicKeys()...)
	signed := Binding{
		CommandID: "cmd-1", TenantID: "tenant-1", WorkerID: "worker-1", ServerID: "server-1",
		Operation: "STACKKIT_OPERATION_APPLY", PayloadDigest: "digest", WorkerGeneration: 2, CurrentGeneration: 2,
		AuthorityExpiresAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), ContractVersion: ContractVersion,
	}
	if err := signer.Sign(&signed); err != nil {
		t.Fatal(err)
	}
	if denial := signed.VerifySignature(pins); denial != nil {
		t.Fatalf("an unchanged signed binding was refused: %v", denial)
	}
	for name, tamper := range map[string]func(*Binding){
		"tenant":     func(b *Binding) { b.TenantID = "tenant-2" },
		"operation":  func(b *Binding) { b.Operation = "STACKKIT_OPERATION_REMOVE" },
		"digest":     func(b *Binding) { b.PayloadDigest = "other" },
		"generation": func(b *Binding) { b.CurrentGeneration = 3 },
		"expiry":     func(b *Binding) { b.AuthorityExpiresAt = b.AuthorityExpiresAt.Add(time.Hour) },
		"reoffer":    func(b *Binding) { b.Reoffer = true },
		"key id":     func(b *Binding) { b.KeyID = pins[1].KeyID },
	} {
		forged := signed
		tamper(&forged)
		if forged.VerifySignature(pins) == nil {
			t.Errorf("a binding with a changed %s still verified", name)
		}
	}
}
