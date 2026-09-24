// Package manifest is the Go source of truth for the Kombify
// ClientUpdateManifest v1 wire contract
// (kombify-Core/standards/client-update-manifest.v1.schema.json). It parses
// manifests fail-closed and verifies their detached signature against public
// keys embedded in the client binary.
//
// Signing payload: the canonical newline-joined string
//
//	kombify.windows-update.v1\n<version>\n<channel>\n<package_url>\n
//	<package_sha256>\n<package_size>\n<published_at>\n<key_id>\n
//
// Ed25519 signs the raw payload bytes (target algorithm per
// DESKTOP-CLIENT-DISTRIBUTION-STANDARD section 4). RS256 (RSASSA-PKCS1-v1_5
// over SHA-256) remains valid for the transitional PowerShell producer.
// Signature values are unpadded base64url.
package manifest

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Signature algorithms accepted by v1.
const (
	AlgorithmEd25519 = "Ed25519"
	AlgorithmRS256   = "RS256"
)

// PayloadPrefix is the first line of the canonical signing payload.
const PayloadPrefix = "kombify.windows-update.v1"

// Errors returned by this package.
var (
	ErrInvalidManifest   = errors.New("manifest: invalid update manifest")
	ErrSignatureInvalid  = errors.New("manifest: signature verification failed")
	ErrUnknownKey        = errors.New("manifest: no embedded key for manifest key id")
	ErrUnsupportedScheme = errors.New("manifest: unsupported signature algorithm")
)

// Manifest is the parsed ClientUpdateManifest v1 document.
type Manifest struct {
	SchemaVersion string    `json:"schema_version"`
	Version       string    `json:"version"`
	Channel       string    `json:"channel"`
	PackageURL    string    `json:"package_url"`
	PackageSHA256 string    `json:"package_sha256"`
	PackageSize   int64     `json:"package_size"`
	PublishedAt   string    `json:"published_at"`
	Signature     Signature `json:"signature"`
}

// Signature is the detached signature block.
type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Value     string `json:"value"`
}

// Keyring holds the public keys embedded in a client binary, indexed by key
// id. A consumer rejects any manifest whose key id it does not carry.
type Keyring struct {
	Ed25519 map[string]ed25519.PublicKey
	RSA     map[string]*rsa.PublicKey
}

var (
	semverPattern  = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	channelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)
	sha256Pattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	keyIDPattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{2,63}$`)
	// Signature charset; length bounds (16..8192 per the JSON schema) are
	// checked separately because Go's regexp caps repeat counts at 1000.
	signatureCharset = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func validSignatureValue(value string) bool {
	return len(value) >= 16 && len(value) <= 8192 && signatureCharset.MatchString(value)
}

var allowedTopLevel = map[string]bool{
	"schema_version": true, "version": true, "channel": true,
	"package_url": true, "package_sha256": true, "package_size": true,
	"published_at": true, "signature": true,
}

var allowedSignature = map[string]bool{"algorithm": true, "key_id": true, "value": true}

// Parse validates raw against the full v1 contract. Unknown fields anywhere
// reject the document (fail-closed, matching the schema's
// additionalProperties: false).
func Parse(raw []byte) (*Manifest, error) {
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("%w: not valid JSON: %v", ErrInvalidManifest, err)
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: manifest must be a JSON object", ErrInvalidManifest)
	}

	violations := collectViolations(root)
	if len(violations) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidManifest, strings.Join(violations, "; "))
	}
	parsed := &Manifest{}
	if err := json.Unmarshal(raw, parsed); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	return parsed, nil
}

func collectViolations(root map[string]any) []string {
	var violations []string
	for key := range root {
		if !allowedTopLevel[key] {
			violations = append(violations, "manifest."+key+": unknown field")
		}
	}
	for key := range allowedTopLevel {
		if _, present := root[key]; !present {
			violations = append(violations, "manifest."+key+": required")
		}
	}
	if version, _ := root["schema_version"].(string); version != "1" {
		violations = append(violations, `manifest.schema_version: must equal "1"`)
	}
	if version, _ := root["version"].(string); !semverPattern.MatchString(version) {
		violations = append(violations, "manifest.version: SemVer required")
	}
	if channel, _ := root["channel"].(string); !channelPattern.MatchString(channel) {
		violations = append(violations, "manifest.channel: invalid channel")
	}
	validatePackageURL(root["package_url"], &violations)
	if digest, _ := root["package_sha256"].(string); !sha256Pattern.MatchString(digest) {
		violations = append(violations, "manifest.package_sha256: lowercase hex SHA-256 required")
	}
	validatePackageSize(root["package_size"], &violations)
	validatePublishedAt(root, &violations)
	validateSignature(root["signature"], &violations)
	return violations
}

func validatePublishedAt(root map[string]any, violations *[]string) {
	published, isString := root["published_at"].(string)
	if _, present := root["published_at"]; !present {
		return // covered by the required-keys check
	}
	if !isString || published == "" {
		*violations = append(*violations, "manifest.published_at: RFC3339 timestamp required")
		return
	}
	if _, err := time.Parse(time.RFC3339, published); err != nil {
		*violations = append(*violations, "manifest.published_at: RFC3339 timestamp required")
	}
}

func validatePackageURL(value any, violations *[]string) {
	raw, ok := value.(string)
	if !ok || raw == "" {
		*violations = append(*violations, "manifest.package_url: HTTPS URL required")
		return
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		*violations = append(*violations, "manifest.package_url: HTTPS without user-info or fragment required")
	}
}

func validatePackageSize(value any, violations *[]string) {
	size, ok := value.(float64)
	if !ok || size < 1 || size != float64(int64(size)) {
		*violations = append(*violations, "manifest.package_size: positive integer required")
	}
}

func validateSignature(value any, violations *[]string) {
	signature, ok := value.(map[string]any)
	if !ok {
		*violations = append(*violations, "manifest.signature: must be an object")
		return
	}
	for key := range signature {
		if !allowedSignature[key] {
			*violations = append(*violations, "manifest.signature."+key+": unknown field")
		}
	}
	for key := range allowedSignature {
		if _, present := signature[key]; !present {
			*violations = append(*violations, "manifest.signature."+key+": required")
		}
	}
	if algorithm, _ := signature["algorithm"].(string); algorithm != AlgorithmEd25519 && algorithm != AlgorithmRS256 {
		*violations = append(*violations, "manifest.signature.algorithm: must be Ed25519 or RS256")
	}
	if keyID, _ := signature["key_id"].(string); !keyIDPattern.MatchString(keyID) {
		*violations = append(*violations, "manifest.signature.key_id: invalid key id")
	}
	if value, _ := signature["value"].(string); !validSignatureValue(value) {
		*violations = append(*violations, "manifest.signature.value: base64url signature required")
	}
}

// CanonicalPayload returns the exact byte sequence the signature covers.
func (m *Manifest) CanonicalPayload() []byte {
	payload := strings.Join([]string{
		PayloadPrefix,
		m.Version,
		m.Channel,
		m.PackageURL,
		m.PackageSHA256,
		strconv.FormatInt(m.PackageSize, 10),
		m.PublishedAt,
		m.Signature.KeyID,
	}, "\n")
	return []byte(payload + "\n")
}

// VerifySignature checks the manifest signature against the keyring. It fails
// closed on unknown key ids and on algorithm/key-type mismatches.
func (m *Manifest) VerifySignature(keys Keyring) error {
	signature, err := base64.RawURLEncoding.DecodeString(m.Signature.Value)
	if err != nil {
		return fmt.Errorf("%w: decode: %v", ErrSignatureInvalid, err)
	}
	payload := m.CanonicalPayload()
	switch m.Signature.Algorithm {
	case AlgorithmEd25519:
		key, ok := keys.Ed25519[m.Signature.KeyID]
		if !ok {
			return fmt.Errorf("%w: %q (Ed25519)", ErrUnknownKey, m.Signature.KeyID)
		}
		if !ed25519.Verify(key, payload, signature) {
			return ErrSignatureInvalid
		}
		return nil
	case AlgorithmRS256:
		key, ok := keys.RSA[m.Signature.KeyID]
		if !ok {
			return fmt.Errorf("%w: %q (RS256)", ErrUnknownKey, m.Signature.KeyID)
		}
		digest := sha256.Sum256(payload)
		if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
			return fmt.Errorf("%w: %v", ErrSignatureInvalid, err)
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedScheme, m.Signature.Algorithm)
	}
}

// ParseEd25519PublicKeyPEM parses a PEM-encoded Ed25519 public key (SubjectPublicKeyInfo),
// the format `openssl pkey -pubout` produces for the update keypair.
func ParseEd25519PublicKeyPEM(pemBytes []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block", ErrSignatureInvalid)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSignatureInvalid, err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: not an Ed25519 key", ErrSignatureInvalid)
	}
	return key, nil
}

// ParseRSAPublicKeyPEM parses a PEM-encoded RSA public key
// (SubjectPublicKeyInfo), matching the transitional PowerShell verifier.
func ParseRSAPublicKeyPEM(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block", ErrSignatureInvalid)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSignatureInvalid, err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: not an RSA key", ErrSignatureInvalid)
	}
	return key, nil
}
