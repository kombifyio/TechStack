package actions

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/guardreach"
)

// Issued RIL authority never crosses the workflow or public request boundary.
// The same configured key already protects the subsequent typed Guard queue.
func rilLeaseCipher() (cipher.AEAD, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("TECHSTACK_RIL_QUEUE_ENCRYPTION_KEY")))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("RIL lease custody unavailable")
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func rilLeaseAAD(tenant, card, execution, digest string) ([]byte, error) {
	if tenant == "" || card == "" || execution == "" || digest == "" {
		return nil, fmt.Errorf("RIL lease binding incomplete")
	}
	return json.Marshal([]string{"techstack.ril-issued-lease/v1", tenant, card, execution, digest})
}
func SealRILIssuedLease(tenant, card, execution, digest string, issued guardreach.RILAuthorityResult) ([]byte, error) {
	if !issued.Allowed || issued.Lease == "" || len(issued.Receipt) == 0 || len(issued.Effect) == 0 {
		return nil, fmt.Errorf("RIL issued authority incomplete")
	}
	plain, err := json.Marshal(issued)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	if len(plain) > 196608 {
		return nil, fmt.Errorf("RIL issued authority oversized")
	}
	a, err := rilLeaseCipher()
	if err != nil {
		return nil, err
	}
	aad, err := rilLeaseAAD(tenant, card, execution, digest)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plain, aad), nil
}
func OpenRILIssuedLease(tenant, card, execution, digest string, sealed []byte) (guardreach.RILAuthorityResult, error) {
	var issued guardreach.RILAuthorityResult
	a, err := rilLeaseCipher()
	if err != nil {
		return issued, err
	}
	if len(sealed) < a.NonceSize() || len(sealed) > 196640 {
		return issued, fmt.Errorf("RIL issued authority unavailable")
	}
	aad, err := rilLeaseAAD(tenant, card, execution, digest)
	if err != nil {
		return issued, err
	}
	plain, err := a.Open(nil, sealed[:a.NonceSize()], sealed[a.NonceSize():], aad)
	if err != nil {
		return issued, fmt.Errorf("RIL issued authority authentication failed")
	}
	defer clear(plain)
	if json.Unmarshal(plain, &issued) != nil || !issued.Allowed || issued.Lease == "" || len(issued.Receipt) == 0 || len(issued.Effect) == 0 {
		return guardreach.RILAuthorityResult{}, fmt.Errorf("RIL issued authority invalid")
	}
	return issued, nil
}
