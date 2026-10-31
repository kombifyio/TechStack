// Package advancedissuer is Techstack's Advanced capability issuer. One
// Ed25519 key per Techstack installation signs short-lived
// stackkit.advanced-capability/v1 documents, each scoped to one managed
// deployment's StackKit stack id, local Owner reference and operation set.
// The pinned StackKits CLI verifies them offline against the
// stackkit.advanced-trust-bundle/v1 that every managed rollout imports after
// INIT.
//
// runtime-contracts-go owns the wire, canonical bytes and signing domain.
// Techstack owns key custody, issuance admission and recording; StackKits owns
// offline trust and local operation admission.
package advancedissuer

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	wire "github.com/kombifyio/stackkits/contracts/advancedcapability"
)

const (
	CapabilitySchemaVersion  = wire.SchemaVersion
	TrustBundleSchemaVersion = wire.TrustBundleSchemaVersion
	Audience                 = wire.Audience

	// DefaultTTL keeps a capability valid for one dispatched operation.
	DefaultTTL = 15 * time.Minute
	// MaxTTL is the StackKits verifier's lifetime limit.
	MaxTTL = wire.MaxLifetimeSeconds * time.Second
)

// Advanced operation identifiers accepted by the StackKits verifier. A new
// capability-gated StackKitCommand operation binds to one of these.
const (
	OperationApplicationTransferRetire = wire.OperationApplicationTransferRetire
	OperationDriftReconcileAdvanced    = wire.OperationDriftReconcileAdvanced
	OperationOriginCertificateInstall  = wire.OperationOriginCertificateInstall
	OperationOriginCertificateRequest  = wire.OperationOriginCertificateRequest
	OperationRestoreDrill              = wire.OperationRestoreDrill
	OperationRollbackCoordinated       = wire.OperationRollbackCoordinated
	OperationTerramateChangeSetApply   = wire.OperationTerramateChangeSetApply
	OperationTerramateChangeSetCreate  = wire.OperationTerramateChangeSetCreate
)

var (
	// Patterns mirror the StackKits verifier so an issued document is never
	// one the verifier would reject as malformed.
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
	if !wire.IsTechstackIssuerID(cfg.IssuerID) {
		return nil, fmt.Errorf("advanced issuer id %q must name a Techstack issuer", cfg.IssuerID)
	}
	if len(cfg.PrivateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("advanced issuer key must be one Ed25519 private key")
	}
	if cfg.Recorder == nil {
		return nil, errors.New("advanced issuer requires an issuance recorder")
	}
	publicKey, _ := cfg.PrivateKey.Public().(ed25519.PublicKey)
	keyID := wire.KeyID(publicKey)
	trust := wire.TrustBundle{SchemaVersion: wire.TrustBundleSchemaVersion, Keys: []wire.TrustBundleKey{wire.NewTrustBundleKey(cfg.IssuerID, publicKey)}}
	if err := trust.Validate(); err != nil {
		return nil, err
	}
	bundle := trust.Canonical()
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Issuer{
		issuerID:  cfg.IssuerID,
		key:       slices.Clone(cfg.PrivateKey),
		keyID:     keyID,
		bundle:    bundle,
		bundleSHA: trust.Digest(),
		recorder:  cfg.Recorder,
		now:       now,
	}, nil
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
	// TransferRetirement comes from the admitted existing transfer job and its
	// signed node result; the canonical wire binds the exact retirement scope.
	TransferRetirement *wire.TransferRetirement
	BackupRenewal      *wire.BackupRenewal
	TenantID           string
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
	id, err := uuid.NewV7()
	if err != nil {
		return Capability{}, fmt.Errorf("allocate advanced capability id: %w", err)
	}
	return issuer.issue(ctx, request, id)
}

// IssueBoundRequest binds durable issuance to an already authenticated caller
// request. Execution's receiver verifies principal, body and deployment first.
func (issuer *Issuer) IssueBoundRequest(ctx context.Context, request Request, requestID uuid.UUID) (Capability, error) {
	if requestID == uuid.Nil {
		return Capability{}, errors.New("advanced issuer requires an authenticated request id")
	}
	return issuer.issue(ctx, request, requestID)
}

// issue uses the authenticated request id at the process seam. The existing
// tenant/capability primary key is the durable replay fence, including restart.
func (issuer *Issuer) issue(ctx context.Context, request Request, id uuid.UUID) (Capability, error) {
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
	document := wire.Capability{
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
	if request.TransferRetirement != nil {
		scope := *request.TransferRetirement
		document.TransferRetirement = &scope
	}
	if err := document.ValidateClaims(); err != nil {
		return Capability{}, err
	}
	document.Sign(issuer.key)
	raw := document.Canonical()

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
		if !wire.IsOperation(operation) {
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
