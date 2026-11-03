package hostpin

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestHostIdentityProof(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	r, err := NewRequest("tenant-a", "server-a", signer, now)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := Verify(r, now)
	if err != nil || fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatal("valid proof refused")
	}
	changed := r
	changed.ServerID = "server-b"
	if _, err := Verify(changed, now); err == nil {
		t.Fatal("changed server admitted")
	}
	changed = r
	changed.Nonce = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := Verify(changed, now); err == nil {
		t.Fatal("changed nonce admitted")
	}
	if _, err := Verify(r, now.Add(31*time.Second)); err == nil {
		t.Fatal("expired proof admitted")
	}
	response := Response{TenantID: r.TenantID, ServerID: r.ServerID, OwnerID: "owner-a", StackID: "stack-a", Nonce: r.Nonce, ExpiresAt: now.Unix() + 30}
	if ValidateResponse(r, response, now) != nil {
		t.Fatal("valid response denied")
	}
	response.Nonce = "wrong"
	if ValidateResponse(r, response, now) == nil {
		t.Fatal("wrong nonce admitted")
	}
	response.Nonce = r.Nonce
	if ValidateResponse(r, response, now.Add(30*time.Second)) == nil {
		t.Fatal("expired response admitted")
	}
}
