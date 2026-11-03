package agentcontrol

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/proto"
)

// Dedicated custody: the deployment supplies this 32-byte random key from
// company secrets. Rotation deliberately invalidates in-flight envelopes.
const rilQueueKeyEnv = "TECHSTACK_RIL_QUEUE_ENCRYPTION_KEY"

// A fresh Gateway claim cannot extend the original admission. Carry the
// earliest caller/grant deadline into both queue authority and ciphertext AAD,
// so the worker's signed Guard binding expires with that same authority.
func rilQueueProofExpiry(ctx context.Context, claimExpiry int64, now time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if deadline, ok := ctx.Deadline(); ok && deadline.Unix() < claimExpiry {
		claimExpiry = deadline.Unix()
	}
	if claimExpiry <= now.Unix() {
		return 0, fmt.Errorf("RIL request authority expired before enqueue")
	}
	return claimExpiry, nil
}

type rilProofBinding struct {
	Version string `json:"version"`
	Tenant  string `json:"tenant"`
	Worker  string `json:"worker"`
	Command string `json:"command"`
	Digest  string `json:"digest"`
	Expires int64  `json:"expires"`
}

func rilQueueKey() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(rilQueueKeyEnv))
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("RIL queue encryption custody unavailable")
	}
	return key, nil
}
func rilAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid RIL queue key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func rilProofAAD(tenant, worker, id, digest string, expires int64) ([]byte, error) {
	if tenant == "" || worker == "" || id == "" || digest == "" || expires <= 0 {
		return nil, fmt.Errorf("RIL proof binding incomplete")
	}
	return json.Marshal(rilProofBinding{"techstack.ril-queue-proof/v1", tenant, worker, id, digest, expires})
}
func sealRILProof(key []byte, tenant, worker, id string, proof *agentpb.RILExecutionProof, expires int64, now time.Time) ([]byte, error) {
	digest, err := stackkitcommand.RILProofDigest(proof)
	if err != nil || digest != proof.GetProofDigest() || expires <= now.Unix() || expires > now.Unix()+30 {
		return nil, fmt.Errorf("RIL proof authority invalid")
	}
	aad, err := rilProofAAD(tenant, worker, id, digest, expires)
	if err != nil {
		return nil, err
	}
	aead, err := rilAEAD(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := proto.MarshalOptions{Deterministic: true}.Marshal(proof)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	if len(plaintext) > 196608 {
		return nil, fmt.Errorf("RIL proof too large")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}
func openRILProof(key []byte, tenant, worker, id, digest string, expires int64, now time.Time, sealed []byte) (*agentpb.RILExecutionProof, error) {
	if now.Unix() >= expires || len(sealed) > 196640 {
		return nil, fmt.Errorf("RIL proof expired or oversized")
	}
	aad, err := rilProofAAD(tenant, worker, id, digest, expires)
	if err != nil {
		return nil, err
	}
	aead, err := rilAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, fmt.Errorf("RIL proof unavailable")
	}
	plaintext, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], aad)
	if err != nil {
		return nil, fmt.Errorf("RIL proof authentication failed")
	}
	defer clear(plaintext)
	proof := &agentpb.RILExecutionProof{}
	if err = proto.Unmarshal(plaintext, proof); err != nil {
		return nil, fmt.Errorf("invalid RIL proof")
	}
	actual, err := stackkitcommand.RILProofDigest(proof)
	if err != nil || actual != digest || proof.ProofDigest != digest {
		return nil, fmt.Errorf("RIL proof digest mismatch")
	}
	return proof, nil
}

// ValidateRILQueueCustody fails before a one-use Gateway claim when the
// replica cannot encrypt proof material for its peers.
func ValidateRILQueueCustody() error { key, err := rilQueueKey(); clear(key); return err }

// scheduleRILProofExpiry clears ciphertext under the same tenant RLS when the
// sender remains alive. A later worker poll also clears expired envelopes.
func (h *Hub) scheduleRILProofExpiry(tenant, id string, expires int64) {
	delay := time.Until(time.Unix(expires, 0)) + time.Second
	if delay < 0 {
		delay = 0
	}
	time.AfterFunc(delay, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.clearExpiredRILProof(ctx, tenant, id); err != nil {
			slog.Warn("ril_proof_expiry_cleanup_failed", "error", err)
		}
	})
}

func (h *Hub) clearExpiredRILProof(ctx context.Context, tenant, id string) error {
	tx, err := h.tenantTx(ctx, tenant)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE typed_agent_commands SET ril_proof_ciphertext=NULL, ril_proof_expires_at=NULL WHERE tenant_id=$1 AND command_id=$2 AND ril_proof_expires_at<=clock_timestamp()`, tenant, id); err != nil {
		return err
	}
	return tx.Commit()
}
