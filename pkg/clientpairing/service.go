// Package clientpairing implements the short-lived, tenant-bound capability
// used to bind a second Kombify client to a TechStack instance.
//
// It is deliberately separate from the worker-enrollment pairing-token domain:
// client pairing may only hand off into the normal account/OIDC authority and
// must never mint a worker credential or duplicate the local owner identity.
package clientpairing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Version         = "1"
	RedeemPath      = "/api/v1/client-pairing/redeem"
	DefaultLifetime = 5 * time.Minute
	MaxLifetime     = 10 * time.Minute

	codeTenantLengthWidth = 3
	codeEntropyBytes      = 32
	maxTenantIDBytes      = 128
)

var (
	ErrInvalidRequest  = errors.New("client pairing: invalid request")
	ErrInvalidCode     = errors.New("client pairing: invalid code")
	ErrExpired         = errors.New("client pairing: code expired")
	ErrAlreadyConsumed = errors.New("client pairing: code already consumed")
	ErrBindingMismatch = errors.New("client pairing: binding mismatch")
	ErrConflict        = errors.New("client pairing: conflict")

	instanceIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,127}$`)
	fingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// Envelope is the exact Kombify Core ClientPairingEnvelope v1 wire shape.
// It contains the only raw copy of the one-time code.
type Envelope struct {
	Version              string    `json:"version"`
	Endpoint             string    `json:"endpoint"`
	InstanceID           string    `json:"instance_id"`
	TLSFingerprintSHA256 string    `json:"tls_fingerprint_sha256"`
	OneTimeCode          string    `json:"one_time_code"`
	IssuedAt             time.Time `json:"issued_at"`
	ExpiresAt            time.Time `json:"expires_at"`
}

// Code is the persisted, non-secret representation of an issued envelope.
// CodeHash is SHA-256 over the raw one-time code; the raw code is never stored.
type Code struct {
	ID                   string
	TenantID             string
	InstanceID           string
	CodeHash             string
	TLSFingerprintSHA256 string
	IssuedBySubjectID    string
	IssuedAt             time.Time
	ExpiresAt            time.Time
	ConsumedAt           *time.Time
}

// ConsumeRequest contains every authoritative binding used by the atomic
// consume operation.
type ConsumeRequest struct {
	TenantID             string
	CodeHash             string
	InstanceID           string
	TLSFingerprintSHA256 string
	Now                  time.Time
}

// Store is the narrow persistence boundary required by the pairing service.
type Store interface {
	Create(ctx context.Context, code Code) error
	Consume(ctx context.Context, req ConsumeRequest) (*Code, error)
}

type IssueRequest struct {
	TenantID             string
	InstanceID           string
	IssuedBySubjectID    string
	Endpoint             string
	TLSFingerprintSHA256 string
	Lifetime             time.Duration
}

type RedeemRequest struct {
	OneTimeCode          string
	ExpectedTenantID     string
	InstanceID           string
	TLSFingerprintSHA256 string
}

type Service struct {
	store   Store
	now     func() time.Time
	entropy io.Reader
}

type Option func(*Service)

func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

func WithEntropy(entropy io.Reader) Option {
	return func(s *Service) {
		if entropy != nil {
			s.entropy = entropy
		}
	}
}

func New(store Store, options ...Option) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: store is required", ErrInvalidRequest)
	}
	service := &Service{store: store, now: time.Now, entropy: rand.Reader}
	for _, option := range options {
		option(service)
	}
	return service, nil
}

// Issue creates a Core-conforming envelope and persists only its SHA-256 hash.
func (s *Service) Issue(ctx context.Context, req IssueRequest) (Envelope, error) {
	if s == nil || s.store == nil {
		return Envelope{}, fmt.Errorf("%w: service is not configured", ErrInvalidRequest)
	}
	tenantID, err := validateTenantID(req.TenantID)
	if err != nil {
		return Envelope{}, err
	}
	instanceID := strings.TrimSpace(req.InstanceID)
	fingerprint := strings.TrimSpace(req.TLSFingerprintSHA256)
	if err := ValidateBinding(instanceID, fingerprint); err != nil {
		return Envelope{}, err
	}
	subjectID := strings.TrimSpace(req.IssuedBySubjectID)
	if subjectID == "" || len(subjectID) > 512 {
		return Envelope{}, fmt.Errorf("%w: issuing subject is required", ErrInvalidRequest)
	}
	endpoint, err := validateRedemptionEndpoint(req.Endpoint)
	if err != nil {
		return Envelope{}, err
	}
	lifetime := req.Lifetime
	if lifetime == 0 {
		lifetime = DefaultLifetime
	}
	if lifetime <= 0 || lifetime > MaxLifetime {
		return Envelope{}, fmt.Errorf("%w: lifetime must be at most ten minutes", ErrInvalidRequest)
	}

	rawCode, codeHash, err := generateCode(s.entropy, tenantID)
	if err != nil {
		return Envelope{}, fmt.Errorf("generate one-time code: %w", err)
	}
	issuedAt := s.now().UTC()
	expiresAt := issuedAt.Add(lifetime)
	record := Code{
		ID:                   "client_pair_" + codeHash[:24],
		TenantID:             tenantID,
		InstanceID:           instanceID,
		CodeHash:             codeHash,
		TLSFingerprintSHA256: fingerprint,
		IssuedBySubjectID:    subjectID,
		IssuedAt:             issuedAt,
		ExpiresAt:            expiresAt,
	}
	if err := s.store.Create(ctx, record); err != nil {
		return Envelope{}, err
	}

	return Envelope{
		Version:              Version,
		Endpoint:             endpoint,
		InstanceID:           instanceID,
		TLSFingerprintSHA256: fingerprint,
		OneTimeCode:          rawCode,
		IssuedAt:             issuedAt,
		ExpiresAt:            expiresAt,
	}, nil
}

// Redeem verifies the envelope bindings and atomically consumes the code.
// The caller must perform the normal OIDC/account handoff after this succeeds;
// this service never mints a user session itself.
func (s *Service) Redeem(ctx context.Context, req RedeemRequest) (*Code, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("%w: service is not configured", ErrInvalidRequest)
	}
	rawCode := req.OneTimeCode
	if rawCode == "" || strings.TrimSpace(rawCode) != rawCode {
		return nil, ErrInvalidCode
	}
	tenantID, err := tenantFromCode(rawCode)
	if err != nil {
		return nil, ErrInvalidCode
	}
	expectedTenantID, err := validateTenantID(req.ExpectedTenantID)
	if err != nil {
		return nil, err
	}
	if tenantID != expectedTenantID {
		return nil, ErrBindingMismatch
	}
	instanceID := strings.TrimSpace(req.InstanceID)
	fingerprint := strings.TrimSpace(req.TLSFingerprintSHA256)
	if err := ValidateBinding(instanceID, fingerprint); err != nil {
		return nil, err
	}
	codeHash := hashCode(rawCode)
	return s.store.Consume(ctx, ConsumeRequest{
		TenantID:             tenantID,
		CodeHash:             codeHash,
		InstanceID:           instanceID,
		TLSFingerprintSHA256: fingerprint,
		Now:                  s.now().UTC(),
	})
}

// ValidateBinding verifies the public instance and TLS identifiers shared by
// issue and redeem handlers without exposing the package's regex ownership.
func ValidateBinding(instanceID, fingerprint string) error {
	if !instanceIDPattern.MatchString(strings.TrimSpace(instanceID)) {
		return fmt.Errorf("%w: invalid instance id", ErrInvalidRequest)
	}
	if !fingerprintPattern.MatchString(strings.TrimSpace(fingerprint)) {
		return fmt.Errorf("%w: invalid TLS fingerprint", ErrInvalidRequest)
	}
	return nil
}

func validateRedemptionEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", fmt.Errorf("%w: HTTPS redemption endpoint required", ErrInvalidRequest)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != RedeemPath {
		return "", fmt.Errorf("%w: invalid redemption endpoint", ErrInvalidRequest)
	}
	return parsed.String(), nil
}

func validateTenantID(raw string) (string, error) {
	tenantID := strings.TrimSpace(raw)
	if tenantID == "" || tenantID != raw || !utf8.ValidString(tenantID) || len([]byte(tenantID)) > maxTenantIDBytes {
		return "", fmt.Errorf("%w: invalid tenant id", ErrInvalidRequest)
	}
	return tenantID, nil
}

func generateCode(entropy io.Reader, tenantID string) (string, string, error) {
	tenantBytes := []byte(tenantID)
	if _, err := validateTenantID(tenantID); err != nil {
		return "", "", err
	}
	randomBytes := make([]byte, codeEntropyBytes)
	if _, err := io.ReadFull(entropy, randomBytes); err != nil {
		return "", "", err
	}
	tenantPart := base64.RawURLEncoding.EncodeToString(tenantBytes)
	randomPart := base64.RawURLEncoding.EncodeToString(randomBytes)
	raw := fmt.Sprintf("%03x_%s_%s", len(tenantBytes), tenantPart, randomPart)
	if len(raw) < 16 || len(raw) > 256 {
		return "", "", fmt.Errorf("%w: generated code length is invalid", ErrInvalidRequest)
	}
	return raw, hashCode(raw), nil
}

func tenantFromCode(raw string) (string, error) {
	if len(raw) < codeTenantLengthWidth+3 || len(raw) > 256 || raw[codeTenantLengthWidth] != '_' {
		return "", ErrInvalidCode
	}
	length, err := strconv.ParseUint(raw[:codeTenantLengthWidth], 16, 16)
	if err != nil || length == 0 || length > maxTenantIDBytes {
		return "", ErrInvalidCode
	}
	encodedLength := base64.RawURLEncoding.EncodedLen(int(length))
	tenantStart := codeTenantLengthWidth + 1
	tenantEnd := tenantStart + encodedLength
	if tenantEnd >= len(raw) || raw[tenantEnd] != '_' || len(raw[tenantEnd+1:]) < 32 {
		return "", ErrInvalidCode
	}
	tenantBytes, err := base64.RawURLEncoding.DecodeString(raw[tenantStart:tenantEnd])
	if err != nil || len(tenantBytes) != int(length) {
		return "", ErrInvalidCode
	}
	tenantID, err := validateTenantID(string(tenantBytes))
	if err != nil {
		return "", ErrInvalidCode
	}
	return tenantID, nil
}

func hashCode(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
