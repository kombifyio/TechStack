package guardidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The kombify Guard host proof is the Guard's Connect and relay credential
// (Gateway docs/connect/guard-host-profile.md, kombify-Me relay profile): one
// compact JWS signed with the Guard identity key. It proves possession of the
// key bound to a server; it authenticates no request by itself. The audience
// names the one verifier it may be presented to, so a proof minted for Connect
// cannot open the relay and the other way round.
const (
	// HostProofType is the JOSE typ of a Guard host proof.
	HostProofType = "kombify-guard-host+jwt"
	// HostProofMaxLifetime is the longest exp-iat either verifier accepts.
	HostProofMaxLifetime = 300 * time.Second
	// HostProofDefaultLifetime keeps a minted proof short; a proof is single
	// use and minted per connection or per registration.
	HostProofDefaultLifetime = 120 * time.Second
	// HostProofSkew is the clock tolerance of the verifiers (30 s) and of the
	// control plane when it checks a proof a Guard hands it.
	HostProofSkew = 30 * time.Second

	// AudienceConnect is the Connect origin, exact.
	AudienceConnect = "https://connect.kombify.io"
	// AudienceRelay is the kombify.me relay origin, exact.
	AudienceRelay = "https://kombify.me"

	maxHostProofBytes = 4096
)

var (
	hostProofJTIPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22,128}$`)
	hostProofIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// Reasons refining CodeProofInvalid for a host proof.
const (
	ReasonHostProofMalformed = "host_proof_malformed"
	ReasonHostProofHeader    = "host_proof_header"
	ReasonHostProofSignature = "host_proof_signature"
	ReasonHostProofClaims    = "host_proof_claims"
	ReasonHostProofAudience  = "host_proof_audience"
	ReasonHostProofLifetime  = "host_proof_lifetime"
	ReasonHostProofExpired   = "host_proof_expired"
	ReasonHostProofFuture    = "host_proof_not_yet_valid"
	ReasonHostProofMismatch  = "host_proof_mismatch"
)

type hostProofHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	JWK JWK    `json:"jwk"`
}

type hostProofClaims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	TID string `json:"tid"`
	Aud string `json:"aud"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	JTI string `json:"jti"`
	Gen int64  `json:"gen"`
}

// HostProofInput describes one proof a Guard signs.
type HostProofInput struct {
	// Issuer is the Techstack worker id (GuardPrincipal), Subject the server id.
	Issuer, Subject string
	// TenantID is the tenant the verifier acts for; the control plane names it
	// because only it knows how each verifier derives a tenant.
	TenantID string
	// Audience is the exact verifier origin (AudienceConnect, AudienceRelay).
	Audience string
	// Generation is the binding generation the control plane registered.
	Generation int64
	// Lifetime defaults to HostProofDefaultLifetime and is capped at
	// HostProofMaxLifetime. Now and JTI default to the clock and 128 random bits.
	Lifetime time.Duration
	Now      time.Time
	JTI      string
}

// SignHostProof mints a host proof. The private key only signs: neither the
// key nor anything derivable from it other than the public JWK and a signature
// appears in the result.
func SignHostProof(key ed25519.PrivateKey, in HostProofInput) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", errors.New("guard identity key is not available")
	}
	if !hostProofIDPattern.MatchString(in.Issuer) || !hostProofIDPattern.MatchString(in.Subject) {
		return "", errors.New("host proof needs a worker id and a server id")
	}
	if strings.TrimSpace(in.TenantID) == "" || len(in.TenantID) > 255 {
		return "", errors.New("host proof needs a tenant id")
	}
	if in.Audience != AudienceConnect && in.Audience != AudienceRelay {
		return "", errors.New("host proof audience is not a kombify Guard verifier")
	}
	if in.Generation < 1 {
		return "", errors.New("host proof needs a binding generation of at least 1")
	}
	lifetime := in.Lifetime
	if lifetime <= 0 {
		lifetime = HostProofDefaultLifetime
	}
	if lifetime > HostProofMaxLifetime {
		lifetime = HostProofMaxLifetime
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	jti := in.JTI
	if jti == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", fmt.Errorf("create host proof identifier: %w", err)
		}
		jti = base64.RawURLEncoding.EncodeToString(raw[:])
	}
	header, err := json.Marshal(hostProofHeader{Alg: ProofAlgorithm, Typ: HostProofType, JWK: PublicJWK(key)})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(hostProofClaims{
		Iss: in.Issuer, Sub: in.Subject, TID: in.TenantID, Aud: in.Audience,
		Iat: now.UTC().Unix(), Exp: now.UTC().Add(lifetime).Unix(), JTI: jti, Gen: in.Generation,
	})
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	signature := ed25519.Sign(key, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// HostProofExpectation is what the control plane requires of a proof a Guard
// hands it before it forwards the proof to Connect or the relay.
type HostProofExpectation struct {
	Issuer, Subject, TenantID, Audience string
	Generation                          int64
	// Now is the verification time; it defaults to the current time.
	Now time.Time
}

// VerifiedHostProof is the outcome of a valid host proof.
type VerifiedHostProof struct {
	JWK        JWK
	Thumbprint string
	JTI        string
	IssuedAt   time.Time
	ExpiresAt  time.Time
}

// VerifyHostProof validates a host proof the way both downstream verifiers do,
// so the control plane never forwards one that would be refused, and checks it
// against the request it answers. The caller still compares Thumbprint with
// the key bound to the worker.
func VerifyHostProof(raw string, expect HostProofExpectation) (*VerifiedHostProof, error) {
	deny := func(reason string) (*VerifiedHostProof, error) { return nil, Denied(CodeProofInvalid, reason) }
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxHostProofBytes {
		return deny(ReasonHostProofMalformed)
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return deny(ReasonHostProofMalformed)
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return deny(ReasonHostProofMalformed)
	}
	// Exactly alg, typ and a public jwk: crit, b64, jku, x5u, kid and a private
	// member are refused instead of ignored.
	var header hostProofHeader
	decoder := json.NewDecoder(bytesReader(headerBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&header) != nil || header.Alg != ProofAlgorithm || header.Typ != HostProofType {
		return deny(ReasonHostProofHeader)
	}
	var generic map[string]json.RawMessage
	if json.Unmarshal(headerBytes, &generic) != nil || len(generic) != 3 {
		return deny(ReasonHostProofHeader)
	}
	publicKey, err := header.JWK.PublicKey()
	if err != nil {
		return deny(ReasonHostProofHeader)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(publicKey, []byte(parts[0]+"."+parts[1]), signature) {
		return deny(ReasonHostProofSignature)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return deny(ReasonHostProofMalformed)
	}
	var claims hostProofClaims
	claimDecoder := json.NewDecoder(bytesReader(payload))
	claimDecoder.DisallowUnknownFields()
	if claimDecoder.Decode(&claims) != nil {
		return deny(ReasonHostProofClaims)
	}
	if !hostProofIDPattern.MatchString(claims.Iss) || !hostProofIDPattern.MatchString(claims.Sub) ||
		claims.TID == "" || len(claims.TID) > 255 || !hostProofJTIPattern.MatchString(claims.JTI) || claims.Gen < 1 {
		return deny(ReasonHostProofClaims)
	}
	if claims.Aud != expect.Audience {
		return deny(ReasonHostProofAudience)
	}
	if claims.Iss != expect.Issuer || claims.Sub != expect.Subject || claims.TID != expect.TenantID || claims.Gen != expect.Generation {
		return deny(ReasonHostProofMismatch)
	}
	lifetime := time.Duration(claims.Exp-claims.Iat) * time.Second
	if lifetime <= 0 || lifetime > HostProofMaxLifetime {
		return deny(ReasonHostProofLifetime)
	}
	now := expect.Now
	if now.IsZero() {
		now = time.Now()
	}
	issuedAt, expiresAt := time.Unix(claims.Iat, 0).UTC(), time.Unix(claims.Exp, 0).UTC()
	if expiresAt.Before(now.Add(-HostProofSkew)) {
		return deny(ReasonHostProofExpired)
	}
	if issuedAt.After(now.Add(HostProofSkew)) {
		return deny(ReasonHostProofFuture)
	}
	thumbprint, err := header.JWK.Thumbprint()
	if err != nil {
		return deny(ReasonHostProofHeader)
	}
	return &VerifiedHostProof{JWK: header.JWK, Thumbprint: thumbprint, JTI: claims.JTI, IssuedAt: issuedAt, ExpiresAt: expiresAt}, nil
}
