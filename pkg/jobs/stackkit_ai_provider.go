package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// AIProviderResultField holds the secret-free outcome of ai_provider.
const AIProviderResultField = "ai_provider"

// walletHostDeliveryLifetime bounds the Wallet's sealed delivery; the Wallet
// allows at most 15 minutes and the node opens it within the import step.
const walletHostDeliveryLifetime = 10 * time.Minute

// WalletHostTarget is the host target a Wallet item must list before the
// Wallet seals it to the node. It is public: Site and node references, the
// tenant the delivery is addressed to, and the node Owner's public key.
type WalletHostTarget struct {
	SiteRef        string
	NodeRef        string
	TenantRef      string
	OwnerPublicKey string
	// AgentID is the server's Agent; the Wallet records the delivery under
	// it so a detach withdraws the node. It is not part of the host target.
	AgentID string
}

// WalletHostEnvelope is the Wallet's sealed delivery of one item version. It
// is ciphertext only: only the node's recipient key opens it.
type WalletHostEnvelope struct {
	Envelope []byte
	Version  string
}

// WalletHostDelivery is the kombify Wallet as ai_provider sees it: the public
// delivering key every node pins, and the sealing of one item to the node
// whose owner-signed recipient statement answers challenge.
type WalletHostDelivery interface {
	Authority() (key, keyID string, err error)
	DeliverItem(ctx context.Context, owner, itemID string, rotate bool, target WalletHostTarget, statement []byte, challenge string, expiresAt time.Time) (WalletHostEnvelope, error)
}

// IsAIProviderStep reports the internal steps of ai_provider, which no caller
// enqueues on their own.
func IsAIProviderStep(operation string) bool {
	switch operation {
	case stackKitStepHostDeliveryRecipient, stackKitStepWalletItemImport, stackKitStepAIProviderSetup:
		return true
	default:
		return false
	}
}

func validateAIProviderRequest(req *StackKitLifecycleRequest) error {
	if !req.OwnerApproved {
		return fmt.Errorf("%s requires explicit Owner approval", req.Operation)
	}
	if req.DryRun || req.Offline || len(req.CandidateSpecJSON) != 0 {
		return fmt.Errorf("%s takes no dry run, offline mode or candidate StackSpec", req.Operation)
	}
	if req.AIProviderDisable {
		if req.AIProviderBaseURL != "" || req.WalletItemID != "" || req.WalletItemRotate {
			return fmt.Errorf("removing the AI provider takes no provider URL or Wallet item")
		}
		return nil
	}
	baseURL, err := stackkitcommand.NormalizeAIProviderBaseURL(req.AIProviderBaseURL)
	if err != nil {
		return err
	}
	req.AIProviderBaseURL = baseURL
	if !stackkitcommand.ValidWalletItemRef(req.WalletItemID) {
		return fmt.Errorf("%s requires the Wallet item id (itm_...) of the provider's API key", req.Operation)
	}
	return nil
}

// runAIProviderJob runs ai_provider: deliver the Wallet item sealed to the
// node, bind it as the provider key, and reconcile so Open WebUI uses it.
func runAIProviderJob(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, q *Queue, req StackKitLifecycleRequest, release stackkitrelease.Release) error {
	summary := map[string]interface{}{
		"schema_version": "techstack.stackkit-lifecycle-result/v1", "techstack_id": req.StackID,
		stackKitInstanceIDField: req.StackKitInstanceID, "operation": req.Operation, "agent_id": req.AgentID,
	}
	outcome, err := configureAIProvider(ctx, cfg, job, q, req, release)
	summary[AIProviderResultField] = outcome
	if err != nil {
		summary["success"] = false
		job.replaceResult(summary)
		var config *aiProviderConfigError
		if errors.As(err, &config) {
			return NewConfigError(config.err)
		}
		return NewPermanentError(err)
	}
	job.setStep("stackkit_advanced_" + StackKitLifecycleDriftReconcile)
	q.UpdateProgress(job.ID, 70, "Reconciling the stack so Private AI uses the provider setting")
	reconcile := req
	reconcile.Operation = StackKitLifecycleDriftReconcile
	reconcile.AIProviderBaseURL, reconcile.WalletItemID, reconcile.WalletItemRotate, reconcile.AIProviderDisable = "", "", false, false
	reconciled, err := runAdvancedStackKitLifecycle(ctx, cfg, release, job.ID+"-reconcile", reconcile)
	summary[StackKitLifecycleDriftReconcile] = reconciled
	summary["success"] = err == nil
	job.replaceResult(summary)
	if err != nil {
		return NewPermanentError(fmt.Errorf("the provider setting is saved on the node, but reconciling the stack failed: %w", err))
	}
	q.UpdateProgress(job.ID, 100, "Private AI provider setting applied")
	return nil
}

type aiProviderConfigError struct{ err error }

func (e *aiProviderConfigError) Error() string { return e.err.Error() }

func configureAIProvider(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, q *Queue, req StackKitLifecycleRequest, release stackkitrelease.Release) (map[string]interface{}, error) {
	outcome := map[string]interface{}{"disable": req.AIProviderDisable}
	if version := release.Receipt().Version; !stackkitcommand.ReleaseAtLeast(version, stackkitcommand.MinWalletAIProviderRelease) {
		outcome["reason_code"] = "stackkits_release_too_old"
		return outcome, fmt.Errorf("the external AI provider needs StackKits %s or newer; this installation pins %s", stackkitcommand.MinWalletAIProviderRelease, version)
	}
	if cfg.AdvancedIssuer == nil {
		// The setting only takes effect through the Advanced reconcile; check
		// that before the Wallet discloses anything to the node.
		return outcome, &aiProviderConfigError{fmt.Errorf("the Advanced capability issuer is not configured")}
	}
	setup := deliveryRequest(req, stackKitStepAIProviderSetup)
	if !req.AIProviderDisable {
		target, version, err := deliverWalletItem(ctx, cfg, job, q, req, release, outcome)
		if err != nil {
			return outcome, err
		}
		outcome["host_target"] = hostTargetMap(target)
		outcome["item_version"] = version
	}
	job.setStep("stackkit_" + stackKitStepAIProviderSetup)
	q.UpdateProgress(job.ID, 55, "Setting the Private AI provider on the node")
	var command *agentpb.StackKitCommand
	result, commandID, err := dispatchManagedDelivery(ctx, cfg.Sender, job.ID, setup, release, &command)
	if err != nil {
		return outcome, err
	}
	evidence, err := stackkitcommand.ParseAIProviderEvidence(command, result)
	if err != nil {
		return outcome, &typedStackKitOperationError{Operation: stackKitStepAIProviderSetup, CommandID: commandID, Err: err}
	}
	outcome["enabled"], outcome["spec_hash"] = evidence.Enabled, evidence.SpecHash
	if evidence.Enabled {
		outcome["base_url"], outcome["wallet_item_id"] = evidence.BaseURL, evidence.ItemRef
	}
	return outcome, nil
}

// deliverWalletItem proves the node's recipient key with the Wallet's
// delivering key pinned, has the Wallet seal the item to it, and lets the node
// open it into its Wallet item custody. The item must list the node as a host
// target; when it does not, outcome names the target to add.
func deliverWalletItem(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, q *Queue, req StackKitLifecycleRequest, release stackkitrelease.Release, outcome map[string]interface{}) (WalletHostTarget, string, error) {
	if cfg.WalletHostDelivery == nil {
		return WalletHostTarget{}, "", &aiProviderConfigError{fmt.Errorf("kombify Wallet host delivery is not configured")}
	}
	key, keyID, err := cfg.WalletHostDelivery.Authority()
	if err != nil {
		return WalletHostTarget{}, "", &aiProviderConfigError{err}
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return WalletHostTarget{}, "", errors.New("cannot draw a recipient challenge")
	}
	challenge := hex.EncodeToString(random)

	job.setStep("stackkit_" + stackKitStepHostDeliveryRecipient)
	q.UpdateProgress(job.ID, 15, "Asking the node for its delivery recipient key")
	recipient := walletDeliveryStep(req, stackKitStepHostDeliveryRecipient)
	recipient.RecipientChallenge, recipient.DeliveryAuthorityKey, recipient.DeliveryAuthorityKeyID = challenge, key, keyID
	var recipientCommand *agentpb.StackKitCommand
	result, commandID, err := dispatchManagedDelivery(ctx, cfg.Sender, job.ID, recipient, release, &recipientCommand)
	if err != nil {
		return WalletHostTarget{}, "", err
	}
	statement, err := stackkitcommand.ParseHostDeliveryStatement(recipientCommand, result)
	if err != nil {
		return WalletHostTarget{}, "", &typedStackKitOperationError{Operation: stackKitStepHostDeliveryRecipient, CommandID: commandID, Err: err}
	}
	target := WalletHostTarget{
		SiteRef: statement.SiteRef, NodeRef: statement.NodeRef,
		TenantRef: stackkitcommand.DeliveryTenantRef(req.TenantID), OwnerPublicKey: statement.OwnerPublicKey,
		AgentID: req.AgentID,
	}

	job.setStep("wallet_host_delivery")
	q.UpdateProgress(job.ID, 30, "Having the kombify Wallet seal the item to the node")
	sealed, err := cfg.WalletHostDelivery.DeliverItem(ctx, req.OwnerID, req.WalletItemID, req.WalletItemRotate, target, statement.Statement, challenge, time.Now().Add(walletHostDeliveryLifetime))
	if err != nil {
		// The Wallet names its reason; the host target is what the owner
		// lists on the item so the Wallet admits this node.
		outcome["host_target"] = hostTargetMap(target)
		return target, "", fmt.Errorf("the kombify Wallet did not deliver item %s to this node: %w", req.WalletItemID, err)
	}

	job.setStep("stackkit_" + stackKitStepWalletItemImport)
	q.UpdateProgress(job.ID, 45, "Opening the sealed Wallet item on the node")
	imported := walletDeliveryStep(req, stackKitStepWalletItemImport)
	imported.SealedDelivery, imported.DeliveryTenantRef = sealed.Envelope, target.TenantRef
	var importCommand *agentpb.StackKitCommand
	result, commandID, err = dispatchManagedDelivery(ctx, cfg.Sender, job.ID, imported, release, &importCommand)
	if err != nil {
		return target, "", err
	}
	receipt, err := stackkitcommand.ParseWalletItemImportEvidence(importCommand, result)
	if err == nil && receipt.ItemRef != req.WalletItemID {
		err = fmt.Errorf("the node imported Wallet item %s instead of %s", receipt.ItemRef, req.WalletItemID)
	}
	if err != nil {
		return target, "", &typedStackKitOperationError{Operation: stackKitStepWalletItemImport, CommandID: commandID, Err: err}
	}
	return target, sealed.Version, nil
}

// walletDeliveryStep is one delivery step of ai_provider: the node's own
// Owner-approved command, carrying only that step's inputs.
func walletDeliveryStep(req StackKitLifecycleRequest, step string) StackKitLifecycleRequest {
	request := deliveryRequest(req, step)
	request.AIProviderBaseURL, request.WalletItemID, request.WalletItemRotate, request.AIProviderDisable = "", "", false, false
	return request
}

func hostTargetMap(target WalletHostTarget) map[string]interface{} {
	return map[string]interface{}{
		"site_ref": target.SiteRef, "node_ref": target.NodeRef,
		"tenant_ref": target.TenantRef, "owner_public_key": target.OwnerPublicKey,
	}
}
