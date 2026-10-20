package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// The two halves of letting a server trust the owner's kombify Wallet SSH CA
// (Wallet `ssh-ca-trust` host delivery). The owner's client runs the Wallet
// part itself, because only the owner may ask the Wallet to seal the CA's
// trust to a server:
//
//  1. ssh_trust_recipient: the node proves its host delivery recipient key,
//     answering the owner's challenge with the Wallet's delivering key
//     pinned. The job result carries the owner-signed statement and the host
//     target the owner lists on the CA item.
//  2. The owner adds that host target to the CA item and asks the Wallet for
//     the `ssh-ca-trust` delivery (owner route, fresh second factor).
//  3. ssh_trust_apply: the node opens the sealed delivery and applies it to
//     sshd (`stackkit host ssh apply`): TrustedUserCAKeys plus one
//     AuthorizedPrincipalsFile per local user.
//
// Every SSH delivery carries public material only (the CA's public key and
// its principal map, a revocation or a host certificate), so the envelope may
// travel in the job payload until the node consumes it; the node refuses a
// replay and anything not signed by the delivering key it pinned.
const (
	StackKitLifecycleSSHTrustRecipient = "ssh_trust_recipient"
	StackKitLifecycleSSHTrustApply     = "ssh_trust_apply"
	// The node step of ssh_trust_apply. Internal, never an operator input.
	stackKitStepHostSSHApply = "host_ssh_apply"

	// SSHTrustResultField holds the secret-free outcome of both operations.
	SSHTrustResultField = "ssh_trust"
)

// The owner's challenge: random, URL-safe, long enough to be fresh.
var sshTrustChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// IsSSHTrustOperation reports the two operator operations of SSH CA trust.
func IsSSHTrustOperation(operation string) bool {
	return operation == StackKitLifecycleSSHTrustRecipient || operation == StackKitLifecycleSSHTrustApply
}

// IsSSHTrustStep reports the internal node step of ssh_trust_apply, which no
// caller enqueues on its own.
func IsSSHTrustStep(operation string) bool { return operation == stackKitStepHostSSHApply }

func validateSSHTrustRequest(req *StackKitLifecycleRequest) error {
	if !req.OwnerApproved {
		return fmt.Errorf("%s requires explicit Owner approval", req.Operation)
	}
	if req.DryRun || req.Offline || len(req.CandidateSpecJSON) != 0 {
		return fmt.Errorf("%s takes no dry run, offline mode or candidate StackSpec", req.Operation)
	}
	switch req.Operation {
	case StackKitLifecycleSSHTrustRecipient:
		if len(req.SealedDelivery) != 0 || !sshTrustChallengePattern.MatchString(req.RecipientChallenge) {
			return fmt.Errorf("ssh_trust_recipient requires one random recipient_challenge (16 to 128 URL-safe characters) and no sealed delivery")
		}
	case StackKitLifecycleSSHTrustApply:
		trimmed := bytes.TrimSpace(req.SealedDelivery)
		if req.RecipientChallenge != "" || len(trimmed) == 0 || len(trimmed) > stackkitcommand.MaxSealedDeliveryBytes ||
			trimmed[0] != '{' || !json.Valid(trimmed) {
			return fmt.Errorf("ssh_trust_apply requires the Wallet's sealed SSH delivery (one JSON envelope within %d bytes) and no challenge", stackkitcommand.MaxSealedDeliveryBytes)
		}
		req.SealedDelivery = trimmed
	}
	return nil
}

// runSSHTrustJob runs ssh_trust_recipient or ssh_trust_apply on the stack's
// node and records the secret-free outcome under SSHTrustResultField.
func runSSHTrustJob(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, q *Queue, req StackKitLifecycleRequest, release stackkitrelease.Release) error {
	summary := map[string]interface{}{
		"schema_version": "techstack.stackkit-lifecycle-result/v1", "techstack_id": req.StackID,
		stackKitInstanceIDField: req.StackKitInstanceID, "operation": req.Operation, "agent_id": req.AgentID,
	}
	outcome, err := sshTrust(ctx, cfg, job, q, req, release)
	summary[SSHTrustResultField] = outcome
	summary["success"] = err == nil
	job.replaceResult(summary)
	if err != nil {
		var config *aiProviderConfigError
		if errors.As(err, &config) {
			return NewConfigError(config.err)
		}
		return NewPermanentError(err)
	}
	q.UpdateProgress(job.ID, 100, "SSH CA trust step complete")
	return nil
}

func sshTrust(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, q *Queue, req StackKitLifecycleRequest, release stackkitrelease.Release) (map[string]interface{}, error) {
	outcome := map[string]interface{}{}
	if version := release.Receipt().Version; !stackkitcommand.ReleaseAtLeast(version, stackkitcommand.MinWalletAIProviderRelease) {
		outcome["reason_code"] = "stackkits_release_too_old"
		return outcome, fmt.Errorf("trusting a Wallet SSH CA needs StackKits %s or newer; this installation pins %s", stackkitcommand.MinWalletAIProviderRelease, version)
	}
	if req.Operation == StackKitLifecycleSSHTrustRecipient {
		if cfg.WalletHostDelivery == nil {
			return outcome, &aiProviderConfigError{fmt.Errorf("kombify Wallet host delivery is not configured")}
		}
		key, keyID, err := cfg.WalletHostDelivery.Authority()
		if err != nil {
			return outcome, &aiProviderConfigError{err}
		}
		job.setStep("stackkit_" + stackKitStepHostDeliveryRecipient)
		q.UpdateProgress(job.ID, 30, "Asking the node for its delivery recipient key")
		step := sshTrustStep(req, stackKitStepHostDeliveryRecipient)
		step.RecipientChallenge, step.DeliveryAuthorityKey, step.DeliveryAuthorityKeyID = req.RecipientChallenge, key, keyID
		var command *agentpb.StackKitCommand
		result, commandID, err := dispatchManagedDelivery(ctx, cfg.Sender, job.ID, step, release, &command)
		if err != nil {
			return outcome, err
		}
		statement, err := stackkitcommand.ParseHostDeliveryStatement(command, result)
		if err != nil {
			return outcome, &typedStackKitOperationError{Operation: stackKitStepHostDeliveryRecipient, CommandID: commandID, Err: err}
		}
		var document map[string]interface{}
		if err := json.Unmarshal(statement.Statement, &document); err != nil {
			return outcome, &typedStackKitOperationError{Operation: stackKitStepHostDeliveryRecipient, CommandID: commandID, Err: fmt.Errorf("the recipient statement is not a JSON object")}
		}
		outcome["host_target"] = hostTargetMap(WalletHostTarget{
			SiteRef: statement.SiteRef, NodeRef: statement.NodeRef,
			TenantRef: stackkitcommand.DeliveryTenantRef(req.TenantID), OwnerPublicKey: statement.OwnerPublicKey,
		})
		outcome["statement"], outcome["challenge"] = document, req.RecipientChallenge
		return outcome, nil
	}

	job.setStep("stackkit_" + stackKitStepHostSSHApply)
	q.UpdateProgress(job.ID, 40, "Applying the Wallet SSH CA trust to the server's sshd")
	step := sshTrustStep(req, stackKitStepHostSSHApply)
	step.SealedDelivery, step.DeliveryTenantRef = req.SealedDelivery, stackkitcommand.DeliveryTenantRef(req.TenantID)
	var command *agentpb.StackKitCommand
	result, commandID, err := dispatchManagedDelivery(ctx, cfg.Sender, job.ID, step, release, &command)
	if err != nil {
		return outcome, err
	}
	evidence, err := stackkitcommand.ParseSSHApplyEvidence(command, result)
	if err != nil {
		return outcome, &typedStackKitOperationError{Operation: stackKitStepHostSSHApply, CommandID: commandID, Err: err}
	}
	outcome["delivery"], outcome["state"], outcome["trusted_ca_keys"] = evidence.Operation, evidence.State, evidence.TrustedCAKeys
	outcome["users"] = append([]string{}, evidence.Users...)
	if len(evidence.CleanupPending) > 0 {
		outcome["cleanup_pending"] = evidence.CleanupPending
	}
	return outcome, nil
}

// sshTrustStep is one node step of SSH CA trust, carrying only its own inputs.
func sshTrustStep(req StackKitLifecycleRequest, step string) StackKitLifecycleRequest {
	request := walletDeliveryStep(req, step)
	request.RecipientChallenge, request.SealedDelivery = "", nil
	return request
}
