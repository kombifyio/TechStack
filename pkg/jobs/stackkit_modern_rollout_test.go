package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// modernLab is a two-node StackKits lab behind the command seam. It answers
// each hand-off step the way the pinned CLI does and records which agent ran
// what, in order.
type modernLab struct {
	steps     []string
	blockers  []string
	planHash  map[string]string
	adopted   bool
	certified bool
}

const (
	labHomeAgent  = "agent-home"
	labCloudAgent = "agent-cloud"
)

func (lab *modernLab) SendStackKitCommand(_ context.Context, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	role := map[string]string{labHomeAgent: "home", labCloudAgent: "cloud"}[agentID]
	name := strings.TrimPrefix(command.Operation.String(), "STACKKIT_OPERATION_")
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_FEDERATION_HANDOFF {
		name += ":" + command.HandoffStep
	}
	lab.steps = append(lab.steps, role+" "+name)
	switch command.HandoffStep {
	case stackkitcommand.HandoffBindingAdopt:
		lab.adopted = true
	case stackkitcommand.HandoffMemberCertify:
		lab.certified = true
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_FEDERATION_HANDOFF:
		return lab.handoff(command), nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN:
		hash := lab.planHash["adopted"]
		if !lab.adopted {
			hash = lab.planHash["initial"]
		}
		return &agentpb.StackKitResult{Success: true, Release: command.Release, CommandResultJson: modernPlanResult(hash, lab.blockers)}, nil
	}
	return &agentpb.StackKitResult{Success: true, Release: command.Release, CommandResultJson: typedStackKitFakeSuccessResult(command)}, nil
}

func (lab *modernLab) handoff(command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	documents := map[string]string{
		stackkitcommand.HandoffMemberAdmit: `{"kind":"MemberAdmission"}`, stackkitcommand.HandoffMemberJoin: `{"kind":"EvidenceKeyRequest"}`,
		stackkitcommand.HandoffMemberCertify: `{"kind":"EvidenceKeyCertificate"}`, stackkitcommand.HandoffLinkRequirement: `{"kind":"FederationLinkRequirement"}`,
	}
	data := map[string]any{"schemaVersion": stackkitcommand.HandoffResultVersion, "step": command.HandoffStep}
	document := []byte(documents[command.HandoffStep])
	if len(document) != 0 {
		digest := sha256.Sum256(document)
		data["documentSha256"] = "sha256:" + hex.EncodeToString(digest[:])
	}
	if command.HandoffStep == stackkitcommand.HandoffMemberAdmit {
		data["homeKeyId"] = "ed25519://sha256/" + strings.Repeat("a", 64)
	}
	envelope, _ := json.Marshal(map[string]any{"schemaVersion": "stackkit.command-result/v1", "command": stackkitcommand.ResultCommandName(command.Operation), "status": "success", "data": data})
	return &agentpb.StackKitResult{Success: true, Release: command.Release, CommandResultJson: envelope, HandoffDocument: document}
}

func modernPlanResult(planHash string, blockers []string) []byte {
	apply := []map[string]string{}
	for _, code := range blockers {
		apply = append(apply, map[string]string{"code": code})
	}
	inspection, _ := json.Marshal(map[string]any{
		"apiVersion": "stackkit.plan-inspection/v1", "kind": "PlanInspection", "verifiedPhase": "generation", "executorInvoked": false,
		"binding":   map[string]any{"planHash": planHash},
		"readiness": map[string]any{"generation": map[string]any{"status": "ready", "blockers": []string{}}, "apply": map[string]any{"blockers": apply}},
	})
	receipt, _ := json.Marshal(map[string]any{"schemaVersion": "stackkit.command-result/v1", "command": "stackkit plan", "status": "success", "data": map[string]any{"output": string(inspection)}})
	return receipt
}

type issuingFabric struct{ requirement []byte }

func (fabric *issuingFabric) IssueBinding(_ context.Context, request FederationBindingRequest) ([]byte, error) {
	fabric.requirement = request.Requirement
	return []byte(`{"kind":"ExternalFederationLinkBinding"}`), nil
}

func modernRequest(fabric FederationFabric) StackKitLifecycleRequest {
	request := typedStackKitApplyRequest()
	request.StackKit, request.StackName, request.AgentID, request.NodeRef = "modern-homelab", "modern-stack", labHomeAgent, modernHomeNodeRef
	request.CandidateSpecJSON = []byte(`{"apiVersion":"stackkit/v2alpha2","metadata":{"name":"modern-stack"},"kit":{"slug":"modern-homelab"}}`)
	request.modern = &modernRolloutSeam{CloudAgentID: labCloudAgent, Fabric: fabric, StackKitsVersion: "v0.50.9", CandidateDigest: "sha256:" + strings.Repeat("c", 64)}
	return request
}

// The Cloud node is certified by Home before Home applies, the binding is
// adopted before the admission that pins the Inventory, and each step runs on
// the agent that owns its node.
func TestModernRolloutBringsUpTheCloudNodeBeforeHomeApplies(t *testing.T) {
	lab := &modernLab{planHash: map[string]string{"initial": testResolvedPlanHash, "adopted": "sha256:" + strings.Repeat("9", 64)}}
	fabric := &issuingFabric{}
	result, err := runTypedStackKitApplySequence(context.Background(), lab, "job-1", modernRequest(fabric), releaseWithAdvancedCatalog(t))
	if err != nil {
		t.Fatalf("Modern rollout failed: %v", err)
	}
	want := []string{
		"home INIT", "home ADVANCED_TRUST_IMPORT", "home GENERATE", "home PLAN",
		"home FEDERATION_HANDOFF:link-requirement", "home FEDERATION_HANDOFF:binding-adopt", "home GENERATE", "home PLAN",
		"home FEDERATION_HANDOFF:member-admit", "cloud FEDERATION_HANDOFF:member-join",
		"home FEDERATION_HANDOFF:member-certify", "cloud FEDERATION_HANDOFF:member-import",
		"cloud GENERATE", "cloud PLAN", "home APPLY", "cloud APPLY",
	}
	if !slices.Equal(lab.steps, want) {
		t.Fatalf("steps =\n%s\nwant\n%s", strings.Join(lab.steps, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(string(fabric.requirement), "FederationLinkRequirement") {
		t.Fatalf("the fabric authority was not given Home's own requirement: %s", fabric.requirement)
	}
	if _, ok := result["modern_cloud_node_apply"]; !ok {
		t.Fatalf("the Cloud node's Apply is missing from the result: %v", result)
	}
}

// Without a fabric authority the plan stays blocked on the link binding. The
// rollout must stop before either node applies and say why, instead of letting
// Home's Apply refuse on its own.
func TestModernRolloutStopsBeforeApplyWhenTheLinkBindingCannotBeIssued(t *testing.T) {
	lab := &modernLab{planHash: map[string]string{"initial": testResolvedPlanHash, "adopted": testResolvedPlanHash}, blockers: []string{federationLinkBindingMissing}}
	_, err := runTypedStackKitApplySequence(context.Background(), lab, "job-1", modernRequest(nil), releaseWithAdvancedCatalog(t))
	if err == nil || !strings.Contains(err.Error(), federationLinkBindingMissing) || !strings.Contains(err.Error(), "federation fabric") {
		t.Fatalf("error = %v, want the missing link binding named", err)
	}
	for _, step := range lab.steps {
		if strings.HasSuffix(step, "APPLY") {
			t.Fatalf("a node applied although the plan was blocked: %v", lab.steps)
		}
	}
	if !lab.certified {
		t.Fatal("the Cloud member custody did not complete before the stop")
	}
}
