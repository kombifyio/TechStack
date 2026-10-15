package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const (
	modernHomeNodeRef  = "home-main"
	modernCloudNodeRef = "cloud-edge"

	// modernCloudBringUpTimeout is the extra pre-apply budget the Cloud node
	// needs on top of the Home sequence: nine custody and binding steps and the
	// Cloud node's own generate and plan. Its Apply is not part of it: it keeps
	// its own apply allowance.
	modernCloudBringUpTimeout = 9*managedStackKitPlanTimeout + managedStackKitGenerateTimeout + managedStackKitPlanTimeout + 4*managedStackKitResultGrace
	// modernStackKitSequenceTimeout bounds the whole managed Modern rollout:
	// the Home sequence, the Cloud node's bring-up and the Cloud node's apply.
	modernStackKitSequenceTimeout = typedStackKitSequenceTimeout + modernCloudBringUpTimeout + managedStackKitApplyAllowance

	federationLinkBindingMissing = "external-federation-link-binding-missing"
)

// StackKitNodeAgentResolver resolves the enrolled agent that owns one node of a
// stack. Core binds each agent to the node its host name spells; a node with no
// approved agent, or with more than one, is an error and never a guess.
type StackKitNodeAgentResolver func(ctx context.Context, tenantID, ownerID, stackID, nodeRef string) (string, error)

// FederationBindingRequest carries what the fabric authority needs to author
// the opaque external binding for the node's own inter-site link requirement.
type FederationBindingRequest struct {
	TenantID         string
	StackID          string
	Requirement      []byte
	StackKitsVersion string
	CandidateDigest  string
}

// FederationFabric is the external fabric authority of the inter-site link.
// Techstack is that authority for every managed Modern deployment; StackKits
// only validates, Owner-signs and adopts what it returns.
type FederationFabric interface {
	IssueBinding(context.Context, FederationBindingRequest) ([]byte, error)
}

// modernRolloutSeam is set only by the managed Modern rollout. It is not an
// operator input and never travels in a job payload.
type modernRolloutSeam struct {
	CloudAgentID     string
	Fabric           FederationFabric
	StackKitsVersion string
	CandidateDigest  string
}

// modernCloudNode is the Cloud member after bring-up: its agent and the plan
// hash it computed from the Inventory Home admitted.
type modernCloudNode struct {
	request  StackKitLifecycleRequest
	planHash string
}

// bringUpModernCloudNode drives the Cloud node through the pinned StackKits CLI
// in the documented order (ADR-0045, docs 12, 13 and 21):
//
//  1. Federation fabric: Home's own link requirement, the external binding the
//     fabric authority issues for it, and its adoption on Home, then a new
//     generate and plan. Adoption changes the Inventory, so it comes before the
//     admission that binds the Inventory hash.
//  2. Member custody, issued by Home: admit-member, then the Cloud host joins,
//     Home certifies its evidence key, the Cloud host imports it. A certified
//     member must exist before Home applies; otherwise Home would run the Cloud
//     node's local owners in-process on Home.
//  3. The Cloud node generates and plans from the admitted StackSpec and
//     Inventory and must compute Home's plan hash.
//
// Every document that crosses a node is public. The Home key id the member
// compares comes from Home's own result, not from the admission document.
func bringUpModernCloudNode(
	ctx context.Context,
	sender StackKitCommandSender,
	jobID string,
	home StackKitLifecycleRequest,
	release stackkitrelease.Release,
	homePlanHash string,
) (*modernCloudNode, string, error) {
	seam := home.modern
	if seam == nil || seam.CloudAgentID == "" {
		return nil, "", errors.New("the Modern Cloud node has no enrolled agent")
	}
	cloud := home
	cloud.AgentID, cloud.NodeRef = seam.CloudAgentID, modernCloudNodeRef
	cloud.modern = nil
	cloud.CandidateSpecJSON, cloud.InventoryJSON = nil, nil
	cloud.AdvancedTrustBundle = nil
	cloud.ExpectedSpecHash = ""
	// `fleet join` persists the admitted StackSpec at the conventional path.
	cloud.SpecPath, cloud.BoundSpecPath = defaultStackKitSpecPath, ""

	step := func(role string, request StackKitLifecycleRequest, name, ref, ref2 string, document []byte) (stackkitcommand.HandoffResult, error) {
		request.Operation = StackKitLifecycleFederationHandoff
		request.HandoffStep, request.HandoffRef, request.HandoffRef2, request.HandoffDocument = name, ref, ref2, document
		request.OwnerApproved = true
		return dispatchModernHandoff(ctx, sender, jobID+"-"+role+"-"+name, request, release)
	}

	var err error
	if seam.Fabric != nil {
		if homePlanHash, err = adoptModernFederationBinding(ctx, sender, jobID, home, release, seam, step); err != nil {
			return nil, "", err
		}
	}
	admission, err := step("home", home, stackkitcommand.HandoffMemberAdmit, modernCloudNodeRef, "", nil)
	if err != nil {
		return nil, "", err
	}
	joined, err := step("cloud", cloud, stackkitcommand.HandoffMemberJoin, admission.HomeKeyID, "", admission.Document)
	if err != nil {
		return nil, "", err
	}
	certificate, err := step("home", home, stackkitcommand.HandoffMemberCertify, modernCloudNodeRef, "", joined.Document)
	if err != nil {
		return nil, "", err
	}
	if _, err = step("cloud", cloud, stackkitcommand.HandoffMemberImport, "", "", certificate.Document); err != nil {
		return nil, "", err
	}

	cloudPlanHash := ""
	for _, operation := range []string{StackKitLifecycleGenerate, StackKitLifecyclePlan} {
		request := cloud
		request.Operation = operation
		request, err = NormalizeStackKitLifecycleRequest(request)
		if err != nil {
			return nil, "", err
		}
		result, commandID, dispatchErr := dispatchTypedStackKitOperation(ctx, sender, jobID+"-cloud", request, release, nil)
		if dispatchErr != nil {
			return nil, "", dispatchErr
		}
		if operation == StackKitLifecyclePlan {
			if cloudPlanHash, err = typedStackKitPlanHash(result); err != nil {
				return nil, "", &typedStackKitOperationError{Operation: operation, CommandID: commandID, Err: err}
			}
			if blockers := typedStackKitApplyBlockers(result); len(blockers) != 0 {
				return nil, "", &typedStackKitOperationError{Operation: StackKitLifecycleApply, CommandID: commandID, Err: modernApplyBlockedError(blockers, seam)}
			}
		}
	}
	if cloudPlanHash != homePlanHash {
		return nil, "", &typedStackKitOperationError{Operation: StackKitLifecyclePlan, CommandID: jobID + "-cloud-" + StackKitLifecyclePlan,
			Err: fmt.Errorf("the Cloud member computed plan %s, not the Home plan %s it was admitted under", cloudPlanHash, homePlanHash)}
	}
	cloud.AdvancedTrustBundle = nil
	return &modernCloudNode{request: cloud, planHash: cloudPlanHash}, homePlanHash, nil
}

type modernHandoffStep func(role string, request StackKitLifecycleRequest, name, ref, ref2 string, document []byte) (stackkitcommand.HandoffResult, error)

// adoptModernFederationBinding has the fabric authority issue the binding for
// Home's own requirement, adopts it on Home, and returns Home's new plan hash.
func adoptModernFederationBinding(
	ctx context.Context,
	sender StackKitCommandSender,
	jobID string,
	home StackKitLifecycleRequest,
	release stackkitrelease.Release,
	seam *modernRolloutSeam,
	step modernHandoffStep,
) (string, error) {
	requirement, err := step("home", home, stackkitcommand.HandoffLinkRequirement, "", "", nil)
	if err != nil {
		return "", err
	}
	binding, err := seam.Fabric.IssueBinding(ctx, FederationBindingRequest{
		TenantID: home.TenantID, StackID: home.StackID, Requirement: requirement.Document,
		StackKitsVersion: seam.StackKitsVersion, CandidateDigest: seam.CandidateDigest,
	})
	if err != nil {
		return "", &typedStackKitOperationError{Operation: StackKitLifecycleFederationHandoff, CommandID: jobID + "-fabric", Err: fmt.Errorf("issue the federation link binding: %w", err)}
	}
	if _, err := step("home", home, stackkitcommand.HandoffBindingAdopt, "", "", binding); err != nil {
		return "", err
	}
	planHash := ""
	for _, operation := range []string{StackKitLifecycleGenerate, StackKitLifecyclePlan} {
		request := home
		request.Operation = operation
		request.ExpectedSpecHash = ""
		if request.BoundSpecPath != "" {
			request.SpecPath = request.BoundSpecPath
		}
		if request, err = NormalizeStackKitLifecycleRequest(request); err != nil {
			return "", err
		}
		result, commandID, dispatchErr := dispatchTypedStackKitOperation(ctx, sender, jobID+"-home-adopted", request, release, nil)
		if dispatchErr != nil {
			return "", dispatchErr
		}
		if operation == StackKitLifecyclePlan {
			if planHash, err = typedStackKitPlanHash(result); err != nil {
				return "", &typedStackKitOperationError{Operation: operation, CommandID: commandID, Err: err}
			}
		}
	}
	return planHash, nil
}

// dispatchModernHandoff sends one hand-off step and returns its verified
// result. The dispatched command is read back so the result is checked against
// the step it answers.
func dispatchModernHandoff(ctx context.Context, sender StackKitCommandSender, jobID string, request StackKitLifecycleRequest, release stackkitrelease.Release) (stackkitcommand.HandoffResult, error) {
	request, err := NormalizeStackKitLifecycleRequest(request)
	if err != nil {
		return stackkitcommand.HandoffResult{}, err
	}
	var dispatched *agentpb.StackKitCommand
	result, commandID, err := dispatchTypedStackKitOperation(ctx, sender, jobID, request, release, func(command *agentpb.StackKitCommand) { dispatched = command })
	if err != nil {
		return stackkitcommand.HandoffResult{}, err
	}
	handoff, err := stackkitcommand.ParseHandoffResult(dispatched, result)
	if err != nil {
		return stackkitcommand.HandoffResult{}, &typedStackKitOperationError{Operation: request.Operation, CommandID: commandID, Err: err}
	}
	return handoff, nil
}

// apply runs the Cloud member's Apply under the plan hash it computed.
func (node *modernCloudNode) apply(ctx context.Context, sender StackKitCommandSender, jobID string, release stackkitrelease.Release) (map[string]interface{}, error) {
	request := node.request
	request.Operation = StackKitLifecycleApply
	request, err := NormalizeStackKitLifecycleRequest(request)
	if err != nil {
		return nil, err
	}
	result, _, err := dispatchTypedStackKitOperation(ctx, sender, jobID+"-cloud", request, release, func(command *agentpb.StackKitCommand) {
		command.ExpectedPlanHash = node.planHash
	})
	if result == nil {
		return nil, err
	}
	normalized, normErr := normalizeStackKitLifecycleResult(request, result)
	if normErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, normErr
	}
	return normalized, err
}

// typedStackKitApplyBlockers lists the codes of the Apply readiness blockers a
// plan reported.
func typedStackKitApplyBlockers(result *agentpb.StackKitResult) []string {
	var envelope struct {
		Data struct {
			Output string `json:"output"`
		} `json:"data"`
	}
	if result == nil || json.Unmarshal(result.CommandResultJson, &envelope) != nil {
		return nil
	}
	var inspection struct {
		Readiness struct {
			Apply struct {
				Blockers []struct {
					Code string `json:"code"`
				} `json:"blockers"`
			} `json:"apply"`
		} `json:"readiness"`
	}
	if json.Unmarshal([]byte(envelope.Data.Output), &inspection) != nil {
		return nil
	}
	codes := make([]string, 0, len(inspection.Readiness.Apply.Blockers))
	for _, blocker := range inspection.Readiness.Apply.Blockers {
		codes = append(codes, blocker.Code)
	}
	return codes
}

func modernApplyBlockedError(blockers []string, seam *modernRolloutSeam) error {
	message := "the Modern plan is not apply-ready: " + strings.Join(blockers, ", ")
	for _, code := range blockers {
		if code == federationLinkBindingMissing && (seam == nil || seam.Fabric == nil) {
			message += " (no federation fabric authority is configured for this Core, so no external link binding can be issued)"
		}
	}
	return errors.New(message)
}
