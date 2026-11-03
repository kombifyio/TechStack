// Package hostpin binds a root-owned SSH host key to a fresh RIL identity query.
package hostpin

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"golang.org/x/crypto/ssh"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const Path = "/v1/techstack/internal/ril-host-identity"

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@|\-]{0,255}$`)

type Request struct {
	TenantID  string `json:"tenantId"`
	ServerID  string `json:"serverId"`
	Nonce     string `json:"nonce"`
	IssuedAt  int64  `json:"issuedAt"`
	PublicKey string `json:"publicKey"`
	Signature string `json:"signature"`
}
type Response struct {
	TenantID  string `json:"tenantId"`
	OwnerID   string `json:"ownerId"`
	ServerID  string `json:"serverId"`
	StackID   string `json:"stackId"`
	Nonce     string `json:"nonce"`
	ExpiresAt int64  `json:"expiresAt"`
}

func ValidateResponse(request Request, response Response, now time.Time) error {
	if response.Nonce != request.Nonce || response.TenantID != request.TenantID || response.ServerID != request.ServerID ||
		!identityPattern.MatchString(response.OwnerID) || !identityPattern.MatchString(response.StackID) ||
		response.ExpiresAt <= now.Unix() || response.ExpiresAt > request.IssuedAt+30 {
		return fmt.Errorf("host identity response invalid")
	}
	return nil
}

func message(r Request) []byte {
	return []byte(strings.Join([]string{"kombify.ril-host-identity.v1", r.TenantID, r.ServerID, r.Nonce, strconv.FormatInt(r.IssuedAt, 10)}, "\n"))
}
func NewRequest(tenant, server string, signer ssh.Signer, now time.Time) (Request, error) {
	var r Request
	if signer == nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 || !identityPattern.MatchString(tenant) || !identityPattern.MatchString(server) {
		return r, fmt.Errorf("host identity input invalid")
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return r, err
	}
	r = Request{TenantID: tenant, ServerID: server, Nonce: base64.RawURLEncoding.EncodeToString(nonce), IssuedAt: now.Unix(), PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))}
	sig, err := signer.Sign(rand.Reader, message(r))
	if err != nil {
		return Request{}, err
	}
	r.Signature = base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig))
	return r, nil
}
func Verify(r Request, now time.Time) (string, error) {
	if !identityPattern.MatchString(r.TenantID) || !identityPattern.MatchString(r.ServerID) || len(r.Nonce) != 43 || r.IssuedAt > now.Unix()+5 || r.IssuedAt < now.Unix()-30 {
		return "", fmt.Errorf("host identity request invalid")
	}
	n, err := base64.RawURLEncoding.DecodeString(r.Nonce)
	if err != nil || len(n) != 32 || base64.RawURLEncoding.EncodeToString(n) != r.Nonce {
		return "", fmt.Errorf("nonce invalid")
	}
	k, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(r.PublicKey))
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 || k.Type() != ssh.KeyAlgoED25519 {
		return "", fmt.Errorf("host public key invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(r.Signature)
	if err != nil || len(raw) > 256 {
		return "", fmt.Errorf("host signature invalid")
	}
	var sig ssh.Signature
	if ssh.Unmarshal(raw, &sig) != nil || sig.Format != ssh.KeyAlgoED25519 || k.Verify(message(r), &sig) != nil {
		return "", fmt.Errorf("host signature invalid")
	}
	return ssh.FingerprintSHA256(k), nil
}
