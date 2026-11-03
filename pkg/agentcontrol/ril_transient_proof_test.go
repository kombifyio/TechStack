package agentcontrol

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/proto"
)

func TestRILQueueKeepsEarlierAdmittedGrantDeadline(t *testing.T) {
	now := time.Now().UTC()
	grantExpiry := now.Unix() + 5
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(grantExpiry, 0))
	defer cancel()
	expiry, err := rilQueueProofExpiry(ctx, now.Unix()+30, now)
	if err != nil || expiry != grantExpiry {
		t.Fatalf("fresh claim extended the admitted grant: expiry=%d err=%v", expiry, err)
	}
	key := make([]byte, 32)
	proof := &agentpb.RILExecutionProof{LeaseJws: "lease", ClaimJws: "claim", ReceiptJson: []byte("receipt"), EffectJson: []byte("effect")}
	proof.ProofDigest, err = stackkitcommand.RILProofDigest(proof)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealRILProof(key, "tenant", "agent", "exec", proof, expiry, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openRILProof(key, "tenant", "agent", "exec", proof.ProofDigest, now.Unix()+30, now, sealed); err == nil {
		t.Fatal("claim expiry replaced earlier admission in authenticated envelope")
	}
	if _, err := openRILProof(key, "tenant", "agent", "exec", proof.ProofDigest, expiry, time.Unix(grantExpiry, 0), sealed); err == nil {
		t.Fatal("proof remained available after admitted grant expiry")
	}
	if _, err := rilQueueProofExpiry(ctx, now.Unix()+30, time.Unix(grantExpiry, 0)); err == nil {
		t.Fatal("expired admitted request enqueued")
	}
}

func TestRILEncryptedProofAcrossReplicas(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	proof := &agentpb.RILExecutionProof{LeaseJws: "sensitive-lease", ClaimJws: "sensitive-claim", ReceiptJson: []byte("receipt"), EffectJson: []byte("effect")}
	var err error
	proof.ProofDigest, err = stackkitcommand.RILProofDigest(proof)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	expires := now.Unix() + 20
	sealed, err := sealRILProof(key, "tenant", "agent", "exec", proof, expires, now)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("sensitive")) {
		t.Fatal("ciphertext contains proof")
	}
	// A separate replica, including after sender restart, has only shared custody.
	receiverKey := append([]byte(nil), key...)
	result, err := openRILProof(receiverKey, "tenant", "agent", "exec", proof.ProofDigest, expires, now, sealed)
	if err != nil || !proto.Equal(proof, result) {
		t.Fatalf("replica failed to authenticate proof: %v", err)
	}
	for _, changed := range []struct {
		tenant, worker, id, digest string
		expiry                     int64
		when                       time.Time
		key                        []byte
	}{
		{"other", "agent", "exec", proof.ProofDigest, expires, now, key}, {"tenant", "other", "exec", proof.ProofDigest, expires, now, key}, {"tenant", "agent", "other", proof.ProofDigest, expires, now, key}, {"tenant", "agent", "exec", "sha256:bad", expires, now, key}, {"tenant", "agent", "exec", proof.ProofDigest, expires + 1, now, key}, {"tenant", "agent", "exec", proof.ProofDigest, expires, time.Unix(expires, 0), key}, {"tenant", "agent", "exec", proof.ProofDigest, expires, now, nil}, {"tenant", "agent", "exec", proof.ProofDigest, expires, now, make([]byte, 32)},
	} {
		if _, err := openRILProof(changed.key, changed.tenant, changed.worker, changed.id, changed.digest, changed.expiry, changed.when, sealed); err == nil {
			t.Fatal("altered authority was accepted")
		}
	}
	corrupt := append([]byte(nil), sealed...)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := openRILProof(key, "tenant", "agent", "exec", proof.ProofDigest, expires, now, corrupt); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, err := sealRILProof(nil, "tenant", "agent", "exec", proof, expires, now); err == nil {
		t.Fatal("missing key accepted")
	}
}

func TestRILQueueCustodyFailsClosed(t *testing.T) {
	t.Setenv(rilQueueKeyEnv, "")
	if ValidateRILQueueCustody() == nil {
		t.Fatal("missing encryption custody accepted")
	}
	t.Setenv(rilQueueKeyEnv, "not-a-key")
	if ValidateRILQueueCustody() == nil {
		t.Fatal("malformed encryption custody accepted")
	}
	t.Setenv(rilQueueKeyEnv, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := ValidateRILQueueCustody(); err != nil {
		t.Fatalf("valid synthetic custody refused: %v", err)
	}
}
