// Package stackkitcapability issues the short-lived, secret-free capabilities
// which let StackKits admit explicitly approved advanced operations.
package stackkitcapability

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	SchemaVersion = "stackkit.advanced-capability/v1"
	Audience      = "stackkit"

	maxDocumentBytes = 64 * 1024
	maxLifetime      = 30 * 24 * time.Hour
	futureTolerance  = 5 * time.Minute
)

const (
	OperationDriftReconcileAdvanced   = "drift.reconcile.advanced"
	OperationRestoreDrill             = "restore.drill"
	OperationRollbackCoordinated      = "rollback.coordinated"
	OperationTerramateChangeSetApply  = "terramate.change-set.apply"
	OperationTerramateChangeSetCreate = "terramate.change-set.create"
)

var (
	ErrInvalidRequest = errors.New("stackkit capability: invalid issue request")
	ErrSigning        = errors.New("stackkit capability: signing failed")

	uuidV7Pattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	stackIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	ownerRefPattern = regexp.MustCompile(
		`^owner/local/[0-9a-f]{32}$`,
	)
	issuerIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	windowsPathPattern = regexp.MustCompile(
		`^[A-Za-z]:[/\\]`,
	)
)

var allowedOperationSet = map[string]struct{}{
	OperationDriftReconcileAdvanced:   {},
	OperationRestoreDrill:             {},
	OperationRollbackCoordinated:      {},
	OperationTerramateChangeSetApply:  {},
	OperationTerramateChangeSetCreate: {},
}

// Request is the complete input to Issue. Signer is injected by the
// composition root and may be backed by a secret store or hardware key. This
// package never discovers keys through environment variables or files.
type Request struct {
	CapabilityID      string
	IssuerID          string
	StackID           string
	OwnerRef          string
	AllowedOperations []string
	UIManagerRef      string
	RILRef            string
	IssuedAt          time.Time
	ExpiresAt         time.Time
	Now               time.Time
	Signer            crypto.Signer
}

type unsignedEnvelope struct {
	CapabilityID      string
	IssuerID          string
	StackID           string
	OwnerRef          string
	AllowedOperations []string
	UIManagerRef      string
	RILRef            string
	IssuedAt          string
	ExpiresAt         string
	KeyID             string
}

// Issue validates and signs one stackkit.advanced-capability/v1 document.
// The returned JSON is RFC 8785/JCS canonical and contains no credentials.
func Issue(request Request) ([]byte, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}

	publicKey, ok := request.Signer.Public().(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, invalid("signer", "must expose a 32-byte Ed25519 public key")
	}
	publicKey = slices.Clone(publicKey)
	publicKeyDigest := sha256.Sum256(publicKey)

	envelope := unsignedEnvelope{
		CapabilityID:      request.CapabilityID,
		IssuerID:          request.IssuerID,
		StackID:           request.StackID,
		OwnerRef:          request.OwnerRef,
		AllowedOperations: slices.Clone(request.AllowedOperations),
		UIManagerRef:      request.UIManagerRef,
		RILRef:            request.RILRef,
		IssuedAt:          request.IssuedAt.Format(time.RFC3339),
		ExpiresAt:         request.ExpiresAt.Format(time.RFC3339),
		KeyID:             "ed25519://sha256/" + hex.EncodeToString(publicKeyDigest[:]),
	}

	unsigned, err := canonicalUnsigned(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize unsigned document: %v", ErrInvalidRequest, err)
	}
	domainSeparated := make([]byte, 0, len(SchemaVersion)+1+len(unsigned))
	domainSeparated = append(domainSeparated, SchemaVersion...)
	domainSeparated = append(domainSeparated, 0)
	domainSeparated = append(domainSeparated, unsigned...)
	digest := sha256.Sum256(domainSeparated)

	signature, err := request.Signer.Sign(rand.Reader, digest[:], crypto.Hash(0))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSigning, err)
	}
	if len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: Ed25519 signer returned %d bytes, want %d", ErrSigning, len(signature), ed25519.SignatureSize)
	}
	if !ed25519.Verify(publicKey, digest[:], signature) {
		return nil, fmt.Errorf("%w: signer output does not match its public key", ErrSigning)
	}

	document, err := canonicalDocument(envelope, base64.RawStdEncoding.EncodeToString(signature))
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize signed document: %v", ErrInvalidRequest, err)
	}
	if len(document) > maxDocumentBytes {
		return nil, invalid("document", "exceeds 65536 bytes")
	}
	return document, nil
}

func validateRequest(request Request) error {
	if request.Signer == nil {
		return invalid("signer", "is required")
	}
	if !uuidV7Pattern.MatchString(request.CapabilityID) {
		return invalid("capabilityId", "must be a lowercase UUIDv7")
	}
	if !issuerIDPattern.MatchString(request.IssuerID) {
		return invalid("issuerId", "must match [a-z0-9][a-z0-9._-]{0,127}")
	}
	if !stackIDPattern.MatchString(request.StackID) {
		return invalid("stackId", "must match [a-z0-9][a-z0-9._-]{0,127}")
	}
	if !ownerRefPattern.MatchString(request.OwnerRef) {
		return invalid("ownerRef", "must match owner/local/[0-9a-f]{32}")
	}
	if len(request.AllowedOperations) < 1 || len(request.AllowedOperations) > 5 {
		return invalid("allowedOperations", "must contain 1 to 5 operations")
	}
	if !slices.IsSorted(request.AllowedOperations) {
		return invalid("allowedOperations", "must be lexicographically sorted")
	}
	for index, operation := range request.AllowedOperations {
		if _, ok := allowedOperationSet[operation]; !ok {
			return invalid("allowedOperations", fmt.Sprintf("operation %q is not permitted", operation))
		}
		if index > 0 && request.AllowedOperations[index-1] == operation {
			return invalid("allowedOperations", "must not contain duplicates")
		}
	}
	if err := validateLogicalReference("uiManagerRef", request.UIManagerRef); err != nil {
		return err
	}
	if err := validateLogicalReference("rilRef", request.RILRef); err != nil {
		return err
	}
	if request.IssuedAt.Location() != time.UTC || request.IssuedAt.Nanosecond() != 0 {
		return invalid("issuedAt", "must be UTC with exact-second precision")
	}
	if request.ExpiresAt.Location() != time.UTC || request.ExpiresAt.Nanosecond() != 0 {
		return invalid("expiresAt", "must be UTC with exact-second precision")
	}
	if request.Now.Location() != time.UTC || request.Now.Nanosecond() != 0 {
		return invalid("now", "trusted issue time must be UTC with exact-second precision")
	}
	lifetime := request.ExpiresAt.Sub(request.IssuedAt)
	if lifetime <= 0 || lifetime > maxLifetime {
		return invalid("expiresAt", "must be after issuedAt and no more than 30 days later")
	}
	if request.IssuedAt.After(request.Now.Add(futureTolerance)) {
		return invalid("issuedAt", "must not be more than 5 minutes in the future")
	}
	if !request.ExpiresAt.After(request.Now) {
		return invalid("expiresAt", "must be strictly later than the trusted issue time")
	}
	return nil
}

func validateLogicalReference(field, value string) error {
	if len(value) == 0 || len(value) > 256 {
		return invalid(field, "must contain 1 to 256 bytes")
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return invalid(field, "must contain printable ASCII without whitespace")
		}
	}
	if windowsPathPattern.MatchString(value) || strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, `\`) || strings.HasPrefix(value, "./") ||
		strings.HasPrefix(value, "../") || strings.Contains(value, `\`) {
		return invalid(field, "must be a logical URI reference, not a local path")
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return invalid(field, "must be a valid URI reference")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.User != nil {
		return invalid(field, "must not contain a query, fragment, or credentials")
	}
	if parsed.Host != "" {
		return invalid(field, "must not identify a network endpoint")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "file", "ftp", "http", "https", "ssh", "ws", "wss":
		return invalid(field, "must not identify a network or local-file endpoint")
	}

	unescaped, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return invalid(field, "contains invalid percent encoding")
	}
	for _, segment := range strings.Split(strings.ReplaceAll(unescaped, `\`, "/"), "/") {
		if segment == "." || segment == ".." {
			return invalid(field, "must not contain local path traversal")
		}
	}
	return nil
}

func invalid(field, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidRequest, field, reason)
}
