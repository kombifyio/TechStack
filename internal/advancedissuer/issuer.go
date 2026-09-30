// Package advancedissuer is Techstack's Advanced capability issuer. One
// Ed25519 key per Techstack installation signs short-lived
// stackkit.advanced-capability/v1 documents, each scoped to one managed
// deployment's StackKit stack id, local Owner reference and operation set.
// The pinned StackKits CLI verifies them offline against the
// stackkit.advanced-trust-bundle/v1 that every managed rollout imports after
// INIT.
//
// StackKits owns the wire contract: internal/advancedcapability (capability
// canonical bytes, signature domain and verifier) and internal/advancedtrust
// (trust bundle) in the pinned StackKits release. This package only produces
// documents those verifiers accept; it never widens their rules.
package advancedissuer

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	CapabilitySchemaVersion  = "stackkit.advanced-capability/v1"
	TrustBundleSchemaVersion = "stackkit.advanced-trust-bundle/v1"
	Audience                 = "stackkit"

	// DefaultTTL keeps a capability valid for one dispatched operation.
	DefaultTTL = 15 * time.Minute
	// MaxTTL is the StackKits verifier's lifetime limit.
	MaxTTL = 30 * 24 * time.Hour
)

// Advanced operation identifiers accepted by the StackKits verifier. A new
// capability-gated StackKitCommand operation binds to one of these.
const (
	OperationDriftReconcileAdvanced   = "drift.reconcile.advanced"
	OperationRestoreDrill             = "restore.drill"
	OperationRollbackCoordinated      = "rollback.coordinated"
	OperationTerramateChangeSetApply  = "terramate.change-set.apply"
	OperationTerramateChangeSetCreate = "terramate.change-set.create"
)

var knownOperations = map[string]struct{}{
	OperationDriftReconcileAdvanced:   {},
	OperationRestoreDrill:             {},
	OperationRollbackCoordinated:      {},
	OperationTerramateChangeSetApply:  {},
	OperationTerramateChangeSetCreate: {},
}

var (
	// Patterns mirror the StackKits verifier so an issued document is never
	// one the verifier would reject as malformed.
	issuerIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	stackIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	ownerRefPattern     = regexp.MustCompile(`^owner/local/[0-9a-f]{32}$`)
	deploymentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	bundleDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ErrUnavailable reports that no issuer is configured for this installation.
var ErrUnavailable = errors.New("advanced capability issuer is unavailable")

// Issuance is the audit record written before a capability leaves the issuer.
type Issuance struct {
	TenantID     string
	DeploymentID string
	CapabilityID string
	StackID      string
	OwnerRef     string
	Operations   []string
	IssuerID     string
	KeyID        string
	IssuedAt     time.Time
	ExpiresAt    time.Time
}

// TrustBinding records that a managed deployment's host imported this
// installation's trust bundle, and the local Owner reference StackKits bound
// it to. Capabilities for the deployment are scoped to that Owner.
type TrustBinding struct {
	TenantID     string
	DeploymentID string
	StackID      string
	OwnerRef     string
	BundleSHA256 string
	IssuerID     string
	KeyID        string
	ImportedAt   time.Time
}

// Recorder persists issuance and trust-binding records per managed deployment.
type Recorder interface {
	RecordIssuance(context.Context, Issuance) error
	RecordTrustBinding(context.Context, TrustBinding) error
	LookupTrustBinding(ctx context.Context, tenantID, deploymentID string) (TrustBinding, error)
}

// Config builds an Issuer from an installation key.
type Config struct {
	IssuerID   string
	PrivateKey ed25519.PrivateKey
	Recorder   Recorder
	Now        func() time.Time
}

// Issuer signs capabilities with the installation key.
type Issuer struct {
	issuerID  string
	key       ed25519.PrivateKey
	keyID     string
	bundle    []byte
	bundleSHA string
	recorder  Recorder
	now       func() time.Time
}

// New validates the installation key and renders its trust bundle once.
func New(cfg Config) (*Issuer, error) {
	if !issuerIDPattern.MatchString(cfg.IssuerID) {
		return nil, fmt.Errorf("advanced issuer id %q is not canonical", cfg.IssuerID)
	}
	if len(cfg.PrivateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("advanced issuer key must be one Ed25519 private key")
	}
	if cfg.Recorder == nil {
		return nil, errors.New("advanced issuer requires an issuance recorder")
	}
	publicKey, _ := cfg.PrivateKey.Public().(ed25519.PublicKey)
	keyID := KeyID(publicKey)
	bundle := canonicalTrustBundle(cfg.IssuerID, keyID, base64.RawStdEncoding.EncodeToString(publicKey))
	digest := sha256.Sum256(bundle)
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Issuer{
		issuerID:  cfg.IssuerID,
		key:       slices.Clone(cfg.PrivateKey),
		keyID:     keyID,
		bundle:    bundle,
		bundleSHA: "sha256:" + hex.EncodeToString(digest[:]),
		recorder:  cfg.Recorder,
		now:       now,
	}, nil
}

// KeyID derives the StackKits key id bound to a raw Ed25519 public key.
func KeyID(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return "ed25519://sha256/" + hex.EncodeToString(digest[:])
}

func (issuer *Issuer) IssuerID() string { return issuer.issuerID }
func (issuer *Issuer) KeyID() string    { return issuer.keyID }

// TrustBundle returns the exact canonical stackkit.advanced-trust-bundle/v1
// bytes. They contain public key material only.
func (issuer *Issuer) TrustBundle() []byte { return slices.Clone(issuer.bundle) }

// TrustBundleSHA256 is the sha256:<hex> pin StackKits records on import.
func (issuer *Issuer) TrustBundleSHA256() string { return issuer.bundleSHA }

// Request scopes one capability to one managed deployment.
type Request struct {
	BackupRenewal *BackupRenewal
	TenantID      string
	// DeploymentID is the Techstack stack (managed deployment) id.
	DeploymentID string
	// StackID is the StackKits StackSpec/ResolvedPlan stackId on the host.
	StackID string
	// OwnerRef is the host's local Owner reference (owner/local/<32 hex>).
	OwnerRef   string
	Operations []string
	// TTL defaults to DefaultTTL and may not exceed MaxTTL.
	TTL time.Duration
}

// Capability is one issued, recorded, canonical capability document.
type Capability struct {
	ID         string
	IssuerID   string
	KeyID      string
	StackID    string
	OwnerRef   string
	Operations []string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	// Raw is the exact document the StackKits CLI reads from --capability.
	Raw []byte
}

// Issue signs one capability and records it before returning it. A capability
// whose issuance cannot be recorded is never released.
func (issuer *Issuer) Issue(ctx context.Context, request Request) (Capability, error) {
	if issuer == nil {
		return Capability{}, ErrUnavailable
	}
	request.TenantID = strings.TrimSpace(request.TenantID)
	if request.TenantID == "" {
		return Capability{}, errors.New("advanced capability requires a tenant")
	}
	if !deploymentIDPattern.MatchString(request.DeploymentID) {
		return Capability{}, errors.New("advanced capability deployment id is not canonical")
	}
	if !stackIDPattern.MatchString(request.StackID) {
		return Capability{}, errors.New("advanced capability stack id is not canonical")
	}
	if !ownerRefPattern.MatchString(request.OwnerRef) {
		return Capability{}, errors.New("advanced capability owner reference is not a local owner reference")
	}
	operations, err := normalizeOperations(request.Operations)
	if err != nil {
		return Capability{}, err
	}
	ttl := request.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < time.Second || ttl > MaxTTL {
		return Capability{}, fmt.Errorf("advanced capability ttl must be between 1s and %s", MaxTTL)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Capability{}, fmt.Errorf("allocate advanced capability id: %w", err)
	}
	issuedAt := issuer.now().UTC().Truncate(time.Second)
	expiresAt := issuedAt.Add(ttl).Truncate(time.Second)
	if request.BackupRenewal != nil {
		if len(operations) != 1 || operations[0] != OperationRestoreDrill || ttl > 15*time.Minute || request.BackupRenewal.TenantID != request.TenantID || request.BackupRenewal.DeploymentID != request.DeploymentID {
			return Capability{}, errors.New("backup renewal must match one approved restore drill")
		}
		if err := request.BackupRenewal.Validate(issuedAt); err != nil {
			return Capability{}, err
		}
	}
	document := envelope{
		BackupRenewal:     request.BackupRenewal,
		AllowedOperations: operations,
		Audience:          Audience,
		CapabilityID:      id.String(),
		ExpiresAt:         expiresAt.Format(time.RFC3339),
		IssuedAt:          issuedAt.Format(time.RFC3339),
		IssuerID:          issuer.issuerID,
		KeyID:             issuer.keyID,
		OwnerRef:          request.OwnerRef,
		// Both references are logical, secret-free URNs: the approving
		// surface is the managed deployment, the decision is this capability.
		RILRef:        "urn:kombify:techstack:ril:capability:" + id.String(),
		SchemaVersion: CapabilitySchemaVersion,
		StackID:       request.StackID,
		UIManagerRef:  "urn:kombify:techstack:deployment:" + request.DeploymentID,
	}
	document.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(issuer.key, SigningDigest(canonicalUnsigned(document))))
	raw := canonicalDocument(document)

	issuance := Issuance{
		TenantID: request.TenantID, DeploymentID: request.DeploymentID, CapabilityID: document.CapabilityID,
		StackID: request.StackID, OwnerRef: request.OwnerRef, Operations: slices.Clone(operations),
		IssuerID: issuer.issuerID, KeyID: issuer.keyID, IssuedAt: issuedAt, ExpiresAt: expiresAt,
	}
	if err := issuer.recorder.RecordIssuance(ctx, issuance); err != nil {
		return Capability{}, fmt.Errorf("record advanced capability issuance: %w", err)
	}
	return Capability{
		ID: document.CapabilityID, IssuerID: issuer.issuerID, KeyID: issuer.keyID,
		StackID: request.StackID, OwnerRef: request.OwnerRef, Operations: operations,
		IssuedAt: issuedAt, ExpiresAt: expiresAt, Raw: raw,
	}, nil
}

// RecordTrustBinding admits the evidence a host returned for importing this
// installation's exact bundle and records the Owner it is bound to.
func (issuer *Issuer) RecordTrustBinding(ctx context.Context, binding TrustBinding) error {
	if issuer == nil {
		return ErrUnavailable
	}
	binding.IssuerID, binding.KeyID = issuer.issuerID, issuer.keyID
	if strings.TrimSpace(binding.TenantID) == "" || !deploymentIDPattern.MatchString(binding.DeploymentID) {
		return errors.New("advanced trust binding requires tenant and deployment")
	}
	if binding.StackID != "" && !stackIDPattern.MatchString(binding.StackID) {
		return errors.New("advanced trust binding stack id is not canonical")
	}
	if !ownerRefPattern.MatchString(binding.OwnerRef) {
		return errors.New("advanced trust binding owner reference is not a local owner reference")
	}
	if !bundleDigestPattern.MatchString(binding.BundleSHA256) || binding.BundleSHA256 != issuer.bundleSHA {
		return errors.New("advanced trust binding does not pin this installation's trust bundle")
	}
	if binding.ImportedAt.IsZero() {
		binding.ImportedAt = issuer.now()
	}
	binding.ImportedAt = binding.ImportedAt.UTC().Truncate(time.Second)
	return issuer.recorder.RecordTrustBinding(ctx, binding)
}

// TrustBindingFor returns the recorded trust binding of a managed deployment.
func (issuer *Issuer) TrustBindingFor(ctx context.Context, tenantID, deploymentID string) (TrustBinding, error) {
	if issuer == nil {
		return TrustBinding{}, ErrUnavailable
	}
	return issuer.recorder.LookupTrustBinding(ctx, tenantID, deploymentID)
}

func normalizeOperations(operations []string) ([]string, error) {
	normalized := make([]string, 0, len(operations))
	for _, operation := range operations {
		operation = strings.TrimSpace(operation)
		if _, ok := knownOperations[operation]; !ok {
			return nil, fmt.Errorf("advanced capability operation %q is not an Advanced operation", operation)
		}
		if !slices.Contains(normalized, operation) {
			normalized = append(normalized, operation)
		}
	}
	if len(normalized) == 0 {
		return nil, errors.New("advanced capability requires at least one operation")
	}
	slices.Sort(normalized)
	return normalized, nil
}

// SigningDigest is the domain-separated digest StackKits verifies:
// SHA-256(schemaVersion || 0x00 || canonical unsigned document).
func SigningDigest(unsigned []byte) []byte {
	message := make([]byte, 0, len(CapabilitySchemaVersion)+1+len(unsigned))
	message = append(message, CapabilitySchemaVersion...)
	message = append(message, 0)
	message = append(message, unsigned...)
	digest := sha256.Sum256(message)
	return digest[:]
}

type envelope struct {
	BackupRenewal     *BackupRenewal
	AllowedOperations []string
	Audience          string
	CapabilityID      string
	ExpiresAt         string
	IssuedAt          string
	IssuerID          string
	KeyID             string
	OwnerRef          string
	RILRef            string
	SchemaVersion     string
	Signature         string
	StackID           string
	UIManagerRef      string
}

// canonicalUnsigned renders RFC 8785 (JCS) bytes without the signature. Every
// value is validated ASCII, so Go quoting equals JCS string serialization.
func canonicalUnsigned(document envelope) []byte {
	return canonical(document, false)
}

func canonicalDocument(document envelope) []byte {
	return canonical(document, true)
}

func canonical(document envelope, signed bool) []byte {
	out := make([]byte, 0, 1024)
	out = append(out, `{"allowedOperations":[`...)
	for index, operation := range document.AllowedOperations {
		if index > 0 {
			out = append(out, ',')
		}
		out = strconv.AppendQuote(out, operation)
	}
	out = append(out, ']')
	property := func(name, value string) {
		out = append(out, ',')
		out = strconv.AppendQuote(out, name)
		out = append(out, ':')
		out = strconv.AppendQuote(out, value)
	}
	property("audience", document.Audience)
	if document.BackupRenewal != nil {
		out = append(out, `,"backupRenewal":`...)
		out = append(out, document.BackupRenewal.canonical()...)
	}
	property("capabilityId", document.CapabilityID)
	property("expiresAt", document.ExpiresAt)
	property("issuedAt", document.IssuedAt)
	property("issuerId", document.IssuerID)
	property("keyId", document.KeyID)
	property("ownerRef", document.OwnerRef)
	property("rilRef", document.RILRef)
	property("schemaVersion", document.SchemaVersion)
	if signed {
		property("signature", document.Signature)
	}
	property("stackId", document.StackID)
	property("uiManagerRef", document.UIManagerRef)
	return append(out, '}')
}

func canonicalTrustBundle(issuerID, keyID, publicKey string) []byte {
	out := []byte(`{"keys":[{"issuerId":`)
	out = strconv.AppendQuote(out, issuerID)
	out = append(out, `,"keyId":`...)
	out = strconv.AppendQuote(out, keyID)
	out = append(out, `,"publicKey":`...)
	out = strconv.AppendQuote(out, publicKey)
	out = append(out, `}],"schemaVersion":`...)
	out = strconv.AppendQuote(out, TrustBundleSchemaVersion)
	return append(out, '}')
}
