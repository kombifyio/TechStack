package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// ManagedBackupRecipient is a node recipient key the control plane admitted for
// sealing. It is opaque to the job layer. Statement and Challenge are the
// node's statement and the challenge it answers, which the kombify Wallet
// verifies again before it seals in Wallet mode.
type ManagedBackupRecipient struct {
	Public      []byte
	Fingerprint string
	Generation  uint64
	SiteRef     string
	NodeRef     string
	Statement   []byte
	Challenge   string
	// AgentID is the server's Agent, under which the Wallet records the
	// delivery so a detach withdraws the node.
	AgentID string
}

// ManagedBackupNodeBinding holds the references the node issued for its own
// backup custody.
type ManagedBackupNodeBinding struct {
	BackupTargetRef       string
	CustodyAttestationRef string
}

// ManagedBackupTargetDelivery moves the managed off-site backup credentials to
// the node that owns them. The node proves its recipient key, opens a delivery
// sealed to it into its own encrypted custody and issues its own backup-target
// binding. The control plane builds no binding and sends no credential in the
// clear: everything it dispatches is ciphertext or public.
type ManagedBackupTargetDelivery interface {
	NewChallenge() (string, error)
	// DeliveryAuthority is the kombify Wallet's public delivering key the node
	// pins before a delivery when the Wallet delivers (Wallet mode); both are
	// empty when Techstack seals itself. In Wallet mode without a configured
	// Wallet it fails closed.
	DeliveryAuthority() (key, keyID string, err error)
	AdmitRecipient(ctx context.Context, tenantID, stackID, challenge string, statement []byte) (ManagedBackupRecipient, error)
	// Seal returns the sealed envelope for backup_target_import or
	// backup_target_rebind; ownerID is the stack owner whose Wallet holds the
	// target in Wallet mode.
	Seal(ctx context.Context, tenantID, stackID, ownerID string, recipient ManagedBackupRecipient, operation string) ([]byte, error)
	RecordNodeBinding(ctx context.Context, tenantID, stackID string, binding ManagedBackupNodeBinding) error
	NodeBinding(ctx context.Context, tenantID, stackID string) (ManagedBackupNodeBinding, error)
	// AttestTarget verifies the provisioned target read/write/delete with the
	// credentials Core holds and returns the opaque reference of that fresh
	// observation.
	AttestTarget(ctx context.Context, tenantID, stackID string) (string, error)
}

// ManagedBackupDeliveryProvider is implemented by an Inventory authority that
// can also deliver the backup credentials to the node. It returns nil when the
// control plane has no backup custody configured.
type ManagedBackupDeliveryProvider interface {
	BackupDelivery() ManagedBackupTargetDelivery
}

// managedBackupDeliveryOf returns the delivery authority behind the Inventory
// authority, or nil when there is none.
func managedBackupDeliveryOf(builder ManagedStackKitInventoryBuilder) ManagedBackupTargetDelivery {
	provider, ok := builder.(ManagedBackupDeliveryProvider)
	if !ok {
		return nil
	}
	return provider.BackupDelivery()
}

// deliverManagedBackupTarget runs one sealed delivery against the node: prove
// the recipient key, seal the provisioned credentials to it, let the node open
// them into its own custody, and record the binding the node issued. operation
// is StackKitLifecycleBackupTargetImport or StackKitLifecycleBackupTargetRebind.
// A rebind must reproduce the references already recorded: the node refuses
// different credentials, and a different reference would mean it custodies a
// different target than the one Core provisioned.
func deliverManagedBackupTarget(ctx context.Context, sender StackKitCommandSender, jobID string, base StackKitLifecycleRequest, release stackkitrelease.Release, delivery ManagedBackupTargetDelivery, operation string) (ManagedBackupNodeBinding, error) {
	if delivery == nil {
		return ManagedBackupNodeBinding{}, errors.New("managed backup delivery authority is not configured")
	}
	challenge, err := delivery.NewChallenge()
	if err != nil {
		return ManagedBackupNodeBinding{}, err
	}
	authorityKey, authorityKeyID, err := delivery.DeliveryAuthority()
	if err != nil {
		return ManagedBackupNodeBinding{}, &typedStackKitOperationError{Operation: operation, CommandID: jobID + "-" + operation, Err: err}
	}
	if authorityKey != "" {
		// The Wallet signs what it delivers and seals only to a node that
		// pinned its key. Pinning the same key again is a no-op on the node.
		if version := release.Receipt().Version; !stackkitcommand.ReleaseAtLeast(version, stackkitcommand.MinWalletAIProviderRelease) {
			return ManagedBackupNodeBinding{}, &typedStackKitOperationError{Operation: operation, CommandID: jobID + "-" + operation,
				Err: fmt.Errorf("managed backup delivery through the kombify Wallet needs StackKits %s or newer; this installation pins %s", stackkitcommand.MinWalletAIProviderRelease, version)}
		}
		pin := deliveryRequest(base, stackKitStepHostDeliveryRecipient)
		pin.RecipientChallenge, pin.DeliveryAuthorityKey, pin.DeliveryAuthorityKeyID = challenge, authorityKey, authorityKeyID
		var pinCommand *agentpb.StackKitCommand
		pinResult, pinCommandID, err := dispatchManagedDelivery(ctx, sender, jobID, pin, release, &pinCommand)
		if err != nil {
			return ManagedBackupNodeBinding{}, err
		}
		if _, err := stackkitcommand.ParseHostDeliveryStatement(pinCommand, pinResult); err != nil {
			return ManagedBackupNodeBinding{}, &typedStackKitOperationError{Operation: stackKitStepHostDeliveryRecipient, CommandID: pinCommandID, Err: err}
		}
	}
	recipientRequest := deliveryRequest(base, StackKitLifecycleBackupTargetRecipient)
	recipientRequest.RecipientChallenge = challenge
	var recipientCommand *agentpb.StackKitCommand
	result, commandID, err := dispatchManagedDelivery(ctx, sender, jobID, recipientRequest, release, &recipientCommand)
	if err != nil {
		return ManagedBackupNodeBinding{}, err
	}
	statement, err := stackkitcommand.ParseBackupTargetRecipientEvidence(recipientCommand, result)
	if err != nil {
		return ManagedBackupNodeBinding{}, &typedStackKitOperationError{Operation: StackKitLifecycleBackupTargetRecipient, CommandID: commandID, Err: err}
	}
	recipient, err := delivery.AdmitRecipient(ctx, base.TenantID, base.StackID, challenge, statement.Statement)
	if err != nil {
		return ManagedBackupNodeBinding{}, &typedStackKitOperationError{Operation: StackKitLifecycleBackupTargetRecipient, CommandID: commandID, Err: err}
	}
	recipient.AgentID = base.AgentID
	sealed, err := delivery.Seal(ctx, base.TenantID, base.StackID, base.OwnerID, recipient, operation)
	if err != nil {
		return ManagedBackupNodeBinding{}, &typedStackKitOperationError{Operation: operation, CommandID: jobID + "-" + operation, Err: err}
	}
	importRequest := deliveryRequest(base, operation)
	importRequest.SealedDelivery, importRequest.DeliveryTenantRef = sealed, stackkitcommand.DeliveryTenantRef(base.TenantID)
	var importCommand *agentpb.StackKitCommand
	result, commandID, err = dispatchManagedDelivery(ctx, sender, jobID, importRequest, release, &importCommand)
	if err != nil {
		return ManagedBackupNodeBinding{}, err
	}
	issued, err := stackkitcommand.ParseBackupTargetImportEvidence(importCommand, result)
	if err != nil {
		return ManagedBackupNodeBinding{}, &typedStackKitOperationError{Operation: operation, CommandID: commandID, Err: err}
	}
	binding := ManagedBackupNodeBinding{BackupTargetRef: issued.BackupTargetRef, CustodyAttestationRef: issued.CustodyAttestationRef}
	if err := delivery.RecordNodeBinding(ctx, base.TenantID, base.StackID, binding); err != nil {
		return ManagedBackupNodeBinding{}, &typedStackKitOperationError{Operation: operation, CommandID: commandID, Err: fmt.Errorf("the node-issued backup binding does not match the recorded one: %w", err)}
	}
	return binding, nil
}

// deliveryRequest is a delivery operation for the same stack: Owner-approved,
// bound to the generated spec, and carrying no Inventory because the node
// issues its own binding.
func deliveryRequest(base StackKitLifecycleRequest, operation string) StackKitLifecycleRequest {
	request := base
	request.Operation = operation
	request.OwnerApproved = true
	request.ExpectedSpecHash = ""
	request.InventoryJSON = nil
	request.CandidateSpecJSON = nil
	if request.BoundSpecPath != "" {
		request.SpecPath = request.BoundSpecPath
	}
	return request
}

func dispatchManagedDelivery(ctx context.Context, sender StackKitCommandSender, jobID string, request StackKitLifecycleRequest, release stackkitrelease.Release, dispatched **agentpb.StackKitCommand) (*agentpb.StackKitResult, string, error) {
	normalized, err := NormalizeStackKitLifecycleRequest(request)
	if err != nil {
		return nil, "", err
	}
	return dispatchTypedStackKitOperation(ctx, sender, jobID, normalized, release, func(command *agentpb.StackKitCommand) { *dispatched = command })
}
