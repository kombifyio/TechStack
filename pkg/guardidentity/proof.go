package guardidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// ProofHeader is the request header that carries the proof (RFC 9449).
	ProofHeader = "DPoP"
	// ProofType is the JOSE typ of a Guard proof.
	ProofType = "dpop+jwt"
	// ProofAlgorithm is the only accepted proof signature algorithm.
	ProofAlgorithm = "EdDSA"
	// ProofWindow is the explicit freshness bound for a proof's iat, in both
	// directions. A jti is remembered until its iat leaves this window, so a
	// captured proof cannot be replayed while it is still acceptable.
	ProofWindow = 60 * time.Second
	// ReplayRetention is how long past the window a jti stays recorded. It
	// absorbs clock differences between control-plane replicas, which each
	// judge the window with their own clock.
	ReplayRetention = 30 * time.Second

	maxProofBytes = 4096
	minJTILength  = 16
	maxJTILength  = 128
)

type proofClaims struct {
	jwt.RegisteredClaims
	HTM string `json:"htm"`
	HTU string `json:"htu"`
	ATH string `json:"ath,omitempty"`
}

// ProofInput describes one request a Guard signs.
type ProofInput struct {
	// Method is the HTTP method of the request (htm).
	Method string
	// URL is the absolute request URL; query and fragment are not signed.
	URL string
	// AccessToken, when set, binds the proof to that token through ath.
	AccessToken string
	// Now defaults to the current time; JTI defaults to 128 random bits.
	Now time.Time
	JTI string
}

// SignProof creates a DPoP-shaped proof JWT for one request.
func SignProof(key ed25519.PrivateKey, in ProofInput) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", errors.New("guard identity key is not available")
	}
	htu, err := NormalizeHTU(in.URL)
	if err != nil {
		return "", err
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		return "", errors.New("proof method is required")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	jti := in.JTI
	if jti == "" {
		var raw [16]byte
		if _, readErr := rand.Read(raw[:]); readErr != nil {
			return "", fmt.Errorf("create proof identifier: %w", readErr)
		}
		jti = base64.RawURLEncoding.EncodeToString(raw[:])
	}
	claims := proofClaims{
		RegisteredClaims: jwt.RegisteredClaims{ID: jti, IssuedAt: jwt.NewNumericDate(now.UTC())},
		HTM:              method,
		HTU:              htu,
	}
	if in.AccessToken != "" {
		claims.ATH = AccessTokenHash(in.AccessToken)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["typ"] = ProofType
	token.Header["jwk"] = PublicJWK(key)
	signed, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign proof: %w", err)
	}
	return signed, nil
}

// NormalizeHTU reduces a request URL to the RFC 9449 htu form: lowercase
// scheme and host, default port removed, no query and no fragment.
func NormalizeHTU(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("proof target must be an absolute URL without credentials")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", errors.New("proof target must use http or https")
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" && !(scheme == "https" && port == "443") && !(scheme == "http" && port == "80") {
		host += ":" + port
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path, nil
}

// ProofExpectation is what a verifier requires of one request's proof.
type ProofExpectation struct {
	// Method is the actual request method.
	Method string
	// URLs are the acceptable absolute request URLs. A proxy-terminated
	// deployment passes both the forwarded public URL and the origin URL.
	URLs []string
	// AccessToken, when set, must be the token the proof is bound to.
	AccessToken string
	// Now is the verification time; it defaults to the current time.
	Now time.Time
}

// VerifiedProof is the outcome of a valid proof. The caller still has to
// compare Thumbprint with the key it expects and record the JTI until
// ReplayUntil to reject replays.
type VerifiedProof struct {
	JWK         JWK
	Thumbprint  string
	JTI         string
	IssuedAt    time.Time
	ReplayUntil time.Time
}

// VerifyProof validates one proof JWT. Every failure is a *Error whose Code is
// CodeProofInvalid; the Reason names the invariant that failed.
func VerifyProof(raw string, expect ProofExpectation) (*VerifiedProof, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, Denied(CodeProofInvalid, ReasonProofRequired)
	}
	if len(raw) > maxProofBytes {
		return nil, Denied(CodeProofInvalid, ReasonProofMalformed)
	}
	var embedded JWK
	claims := &proofClaims{}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{ProofAlgorithm}), jwt.WithoutClaimsValidation())
	_, err := parser.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if typ, _ := token.Header["typ"].(string); !strings.EqualFold(typ, ProofType) {
			return nil, errors.New("unsupported proof type")
		}
		rawJWK, ok := token.Header["jwk"]
		if !ok {
			return nil, errors.New("proof carries no key")
		}
		encoded, marshalErr := json.Marshal(rawJWK)
		if marshalErr != nil {
			return nil, errors.New("proof key is malformed")
		}
		parsed, parseErr := ParseJWK(encoded)
		if parseErr != nil {
			return nil, parseErr
		}
		embedded = parsed
		return parsed.PublicKey()
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			return nil, Denied(CodeProofInvalid, ReasonProofSignature)
		}
		return nil, Denied(CodeProofInvalid, ReasonProofMalformed)
	}
	if !strings.EqualFold(strings.TrimSpace(claims.HTM), strings.TrimSpace(expect.Method)) || claims.HTM == "" {
		return nil, Denied(CodeProofInvalid, ReasonProofMethod)
	}
	if !htuMatches(claims.HTU, expect.URLs) {
		return nil, Denied(CodeProofInvalid, ReasonProofURI)
	}
	if len(claims.ID) < minJTILength || len(claims.ID) > maxJTILength || claims.IssuedAt == nil {
		return nil, Denied(CodeProofInvalid, ReasonProofMalformed)
	}
	now := expect.Now
	if now.IsZero() {
		now = time.Now()
	}
	issuedAt := claims.IssuedAt.Time.UTC()
	if delta := now.UTC().Sub(issuedAt); delta > ProofWindow || delta < -ProofWindow {
		return nil, Denied(CodeProofInvalid, ReasonProofClock)
	}
	if expect.AccessToken != "" {
		want := AccessTokenHash(expect.AccessToken)
		if subtle.ConstantTimeCompare([]byte(claims.ATH), []byte(want)) != 1 {
			return nil, Denied(CodeProofInvalid, ReasonProofTokenBinding)
		}
	}
	thumbprint, err := embedded.Thumbprint()
	if err != nil {
		return nil, Denied(CodeProofInvalid, ReasonProofMalformed)
	}
	return &VerifiedProof{
		JWK: embedded, Thumbprint: thumbprint, JTI: claims.ID, IssuedAt: issuedAt,
		ReplayUntil: issuedAt.Add(ProofWindow + ReplayRetention),
	}, nil
}

func htuMatches(claimed string, acceptable []string) bool {
	normalized, err := NormalizeHTU(claimed)
	if err != nil {
		return false
	}
	for _, candidate := range acceptable {
		want, candidateErr := NormalizeHTU(candidate)
		if candidateErr == nil && subtle.ConstantTimeCompare([]byte(normalized), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

// AccessTokenHash is the RFC 9449 ath value: base64url SHA-256 of the token.
func AccessTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
