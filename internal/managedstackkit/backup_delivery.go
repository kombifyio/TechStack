package managedstackkit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/kombifyio/stackkits/pkg/backupbinding"
	"github.com/kombifyio/stackkits/pkg/hostdelivery"
)

const (
	// recipientStatementMaxAge bounds how stale a recipient statement may be.
	recipientStatementMaxAge = 2 * time.Minute
	// deliveryLifetime is how long one sealed delivery stays openable.
	deliveryLifetime = 5 * time.Minute
	// backupTargetItemVersion is the version of the sealed backup target payload.
	backupTargetItemVersion = 1
	// backupTargetRegion is the S3 region the managed object store addresses.
	backupTargetRegion = "auto"
)

// rolloutCustodian provisions the per-stack bucket and scoped token.
type rolloutCustodian interface {
	Ensure(context.Context, string, string) (backupstore.CustodyEvidence, error)
}

// DeliveryCustody is the durable state host delivery needs: the credentials
// Core provisioned, the node's pinned recipient and the binding the node issued.
type DeliveryCustody interface {
	Get(context.Context, string, string) (backupstore.Credentials, error)
	Evidence(context.Context, string, string) (backupstore.CustodyEvidence, error)
	RecordAttestation(context.Context, string, string, backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error)
	PinRecipient(context.Context, backupstore.RecipientPin) error
	Recipient(context.Context, string, string) (backupstore.RecipientPin, bool, error)
	RecordNodeBinding(context.Context, string, string, backupstore.NodeBinding) error
	NodeBinding(context.Context, string, string) (backupstore.NodeBinding, error)
}

// BackupDelivery moves the managed off-site backup credentials Core provisioned
// to the node that owns them. It seals to a recipient key the node proved, hands
// the sealed bytes to the transport, and records the references the node issued.
// It never builds a backup-target binding: the node issues its own.
type BackupDelivery struct {
	custodian rolloutCustodian
	custody   DeliveryCustody
	attest    targetAttestor
	now       func() time.Time
}

func NewBackupDelivery(custodian rolloutCustodian, custody DeliveryCustody) (*BackupDelivery, error) {
	if custodian == nil || custody == nil {
		return nil, errors.New("managed backup delivery requires provider custody and the durable delivery state")
	}
	return &BackupDelivery{
		custodian: custodian, custody: custody, now: time.Now,
		attest: func(ctx context.Context, credentials backupstore.Credentials, evidence backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
			verifier, err := backupstore.NewTargetVerifier(ctx, credentials)
			if err != nil {
				return backupstore.CustodyEvidence{}, err
			}
			evidence.AttestationEvidence = nil
			return verifier.Verify(ctx, credentials, evidence)
		},
	}, nil
}

// DeliveryScope names the stack a delivery is for. The Site and node are the
// managed Cloud node the rollout initializes.
type DeliveryScope struct {
	TenantID string
	StackID  string
}

// Recipient is a node recipient key admitted for sealing.
type Recipient struct {
	Public      []byte
	Fingerprint string
	Generation  uint64
	SiteRef     string
	NodeRef     string
}

// NewChallenge returns the fresh value the node must echo in its statement.
func (d *BackupDelivery) NewChallenge() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", errors.New("managed backup delivery cannot draw a challenge")
	}
	return hex.EncodeToString(random), nil
}

// AdmitRecipient provisions the stack's custody if needed, then admits the
// node's recipient key. At first enrollment the owner key that signed the
// statement is pinned; afterwards only a statement signed by the pinned owner
// key, for the pinned recipient and generation, is accepted. A substituted key
// is refused here and never sealed to.
func (d *BackupDelivery) AdmitRecipient(ctx context.Context, scope DeliveryScope, challenge string, statementJSON []byte) (Recipient, error) {
	scope.TenantID, scope.StackID = strings.TrimSpace(scope.TenantID), strings.TrimSpace(scope.StackID)
	if d == nil || scope.TenantID == "" || scope.StackID == "" || challenge == "" {
		return Recipient{}, errors.New("managed backup delivery requires the exact tenant, stack and challenge")
	}
	if _, err := d.custodian.Ensure(ctx, scope.TenantID, scope.StackID); err != nil {
		return Recipient{}, fmt.Errorf("ensure managed backup custody: %w", err)
	}
	var statement hostdelivery.Statement
	if err := json.Unmarshal(statementJSON, &statement); err != nil {
		return Recipient{}, errors.New("managed backup recipient statement is invalid")
	}
	if statement.SiteRef != cloudOperationsSite || statement.NodeRef != cloudOperationsNode {
		return Recipient{}, errors.New("managed backup recipient statement names a different Site or node")
	}
	pin, pinned, err := d.custody.Recipient(ctx, scope.TenantID, scope.StackID)
	if err != nil {
		return Recipient{}, err
	}
	ownerKey := statement.OwnerPublic
	if pinned {
		ownerKey = pin.OwnerKey
	}
	owner, err := base64.RawStdEncoding.Strict().DecodeString(ownerKey)
	if err != nil || len(owner) != ed25519.PublicKeySize {
		return Recipient{}, errors.New("managed backup recipient owner key is invalid")
	}
	if err := hostdelivery.VerifyStatement(statement, ed25519.PublicKey(owner), challenge, d.now(), recipientStatementMaxAge); err != nil {
		return Recipient{}, err
	}
	if pinned && (pin.Fingerprint != statement.Fingerprint || pin.Generation != statement.Generation || pin.SiteRef != statement.SiteRef || pin.NodeRef != statement.NodeRef) {
		return Recipient{}, backupstore.ErrRecipientSubstituted
	}
	if err := d.custody.PinRecipient(ctx, backupstore.RecipientPin{
		TenantID: scope.TenantID, StackID: scope.StackID, OwnerKey: statement.OwnerPublic, OwnerKeyID: statement.OwnerKeyID,
		Fingerprint: statement.Fingerprint, Generation: statement.Generation, SiteRef: statement.SiteRef, NodeRef: statement.NodeRef,
	}); err != nil {
		return Recipient{}, err
	}
	public, err := base64.RawStdEncoding.Strict().DecodeString(statement.RecipientKey)
	if err != nil {
		return Recipient{}, errors.New("managed backup recipient key is invalid")
	}
	return Recipient{Public: public, Fingerprint: statement.Fingerprint, Generation: statement.Generation, SiteRef: statement.SiteRef, NodeRef: statement.NodeRef}, nil
}

// Seal verifies the provisioned target is usable, then seals its credentials to
// the admitted recipient for one operation (backup-target-import or -rebind).
// The result is ciphertext: it is the only form that leaves this process.
func (d *BackupDelivery) Seal(ctx context.Context, scope DeliveryScope, recipient Recipient, operation string) ([]byte, error) {
	if d == nil || len(recipient.Public) == 0 {
		return nil, errors.New("managed backup delivery requires an admitted recipient")
	}
	if operation != hostdelivery.OperationBackupTargetImport && operation != hostdelivery.OperationBackupTargetRebind {
		return nil, errors.New("managed backup delivery supports only target import and rebind")
	}
	credentials, err := d.custody.Get(ctx, scope.TenantID, scope.StackID)
	if err != nil {
		return nil, fmt.Errorf("load managed backup custody: %w", err)
	}
	evidence, err := d.custodian.Ensure(ctx, scope.TenantID, scope.StackID)
	if err != nil {
		return nil, fmt.Errorf("ensure managed backup custody: %w", err)
	}
	verified, err := d.attest(ctx, credentials, evidence)
	if err != nil {
		return nil, fmt.Errorf("attest managed backup target: %w", err)
	}
	if _, err := d.custody.RecordAttestation(ctx, scope.TenantID, scope.StackID, verified); err != nil {
		return nil, fmt.Errorf("persist managed backup target attestation: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"endpoint": credentials.Endpoint, "bucket": credentials.Bucket, "prefix": "", "region": backupTargetRegion,
		"accessKeyId": credentials.AccessKeyID, "secretAccessKey": credentials.SecretAccessKey, "passphrase": credentials.RepositoryPassword,
	})
	if err != nil {
		return nil, errors.New("encode managed backup target material")
	}
	now := d.now().UTC()
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, errors.New("managed backup delivery cannot draw an operation id")
	}
	return hostdelivery.Seal(recipient.Public, hostdelivery.Context{
		TenantRef: stackkitcommand.DeliveryTenantRef(scope.TenantID), DeliveryRef: "backup-target/" + scope.StackID, ItemVersion: backupTargetItemVersion,
		SiteRef: recipient.SiteRef, NodeRef: recipient.NodeRef, BindingGeneration: recipient.Generation, Operation: operation,
		RecipientFingerprint: recipient.Fingerprint, OperationID: hex.EncodeToString(random),
		IssuedAt: now.Format(time.RFC3339Nano), NotAfter: now.Add(deliveryLifetime).Format(time.RFC3339Nano),
	}, payload, now)
}

// RecordNodeBinding stores the references the node issued after an import, or
// checks that a rebind reproduced exactly the ones already recorded.
func (d *BackupDelivery) RecordNodeBinding(ctx context.Context, scope DeliveryScope, binding backupstore.NodeBinding) error {
	return d.custody.RecordNodeBinding(ctx, scope.TenantID, scope.StackID, binding)
}

// NodeBinding returns the references the node issued for its own custody.
func (d *BackupDelivery) NodeBinding(ctx context.Context, scope DeliveryScope) (backupstore.NodeBinding, error) {
	return d.custody.NodeBinding(ctx, scope.TenantID, scope.StackID)
}

// AttestTarget re-verifies the provisioned target with the credentials Core
// holds and returns the opaque reference of the fresh observation. The custody
// must not change while it runs.
func (d *BackupDelivery) AttestTarget(ctx context.Context, tenantID, stackID string) (string, error) {
	credentials, err := d.custody.Get(ctx, tenantID, stackID)
	if err != nil {
		return "", err
	}
	evidence, err := d.custody.Evidence(ctx, tenantID, stackID)
	if err != nil {
		return "", err
	}
	input := evidence
	input.AttestationEvidence = nil
	fresh, err := d.attest(ctx, credentials, input)
	if err != nil {
		return "", fmt.Errorf("managed backup target attestation failed")
	}
	if fresh.ObservedAt.IsZero() || d.now().Sub(fresh.ObservedAt) > time.Minute || fresh.ObservedAt.After(d.now().Add(time.Minute)) ||
		!bytes.Equal(fresh.TargetEvidence, evidence.TargetEvidence) || !bytes.Equal(fresh.BindingEvidence, evidence.BindingEvidence) {
		return "", errors.New("managed backup target evidence differs")
	}
	after, err := d.custody.Evidence(ctx, tenantID, stackID)
	if err != nil || !bytes.Equal(after.TargetEvidence, evidence.TargetEvidence) || !bytes.Equal(after.BindingEvidence, evidence.BindingEvidence) {
		return "", errors.New("managed backup custody changed during attestation")
	}
	return backupbinding.OpaqueReference("backup-custody-attestation", fresh.AttestationEvidence)
}
