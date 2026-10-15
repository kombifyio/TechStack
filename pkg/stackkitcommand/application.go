package stackkitcommand

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"slices"
	"strings"
	"time"
)

const ApplicationAdoptionCapability = "stackkit.application-adoption.v1"
const HomeAssistantAdoptionProfile = "home-assistant-container-in-place/v1"

type ApplicationAuthority struct {
	PlanHash              string `json:"planHash"`
	LifecycleContractHash string `json:"lifecycleContractHash"`
	LifecycleVersion      string `json:"lifecycleVersion"`
	PackageRef            string `json:"packageRef"`
}
type ApplicationSource struct {
	ContainerID         string `json:"containerId"`
	ImageID             string `json:"imageId"`
	ImageDigest         string `json:"imageDigest"`
	Revision            string `json:"revision"`
	ConfigMount         string `json:"configMount"`
	ConfigurationDigest string `json:"configurationDigest"`
	AccountsDigest      string `json:"accountsDigest"`
}

// ApplicationEvidence is the producer's source-bound lifecycle assessment or
// verified receipt. No credentials or application configuration cross Guard.
type ApplicationEvidence struct {
	APIVersion         string               `json:"apiVersion"`
	Authority          ApplicationAuthority `json:"authority"`
	WorkloadRef        string               `json:"workloadRef"`
	ProfileRef         string               `json:"profileRef"`
	Mode               string               `json:"mode"`
	Source             ApplicationSource    `json:"source"`
	PlanDigest         string               `json:"planDigest"`
	OperationID        string               `json:"operationId,omitempty"`
	OwnedFields        []string             `json:"ownedFields"`
	PreservedFields    []string             `json:"preservedFields"`
	AdmittedOperations []string             `json:"admittedOperations"`
	VerifiedOwnerRef   string               `json:"verifiedOwnerRef"`
	Status             string               `json:"status,omitempty"`
	VerifiedAt         time.Time            `json:"verifiedAt,omitempty"`
	Action             string               `json:"action,omitempty"`
	PowerState         string               `json:"powerState,omitempty"`
}

func IsApplicationOperation(op agentpb.StackKitOperation) bool {
	return op >= agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT && op <= agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONTROL
}
func ApplicationOperationName(op agentpb.StackKitOperation) string {
	switch op {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT:
		return "application.inspect"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_ADOPT:
		return "application.adopt"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_VERIFY:
		return "application.verify"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_RELEASE:
		return "application.release"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONTROL:
		return "application.control"
	}
	return ""
}
func applicationCommandName(op agentpb.StackKitOperation) string {
	name := ApplicationOperationName(op)
	if name == "" {
		return ""
	}
	return "stackkit " + strings.ReplaceAll(name, ".", " ")
}
func validateApplicationFields(c *agentpb.StackKitCommand) error {
	if !IsApplicationOperation(c.Operation) && !IsApplicationConnectorOperation(c.Operation) && (c.ApplicationContainerId != "" || c.ApplicationPlanDigest != "" || c.ApplicationOperationId != "" || c.ApplicationAction != "") {
		return fmt.Errorf("application fields require an application operation")
	}
	return nil
}
func validateApplicationCommand(c *agentpb.StackKitCommand) error {
	if c.ServiceKey != "smart-home" || c.StackkitInstanceId == "" {
		return fmt.Errorf("application adoption requires the supported smart-home lifecycle and exact workspace identity")
	}
	if !sha256Pattern.MatchString(c.ApplicationContainerId) {
		return fmt.Errorf("application requires a full immutable Docker container ID")
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT || c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_VERIFY {
		if c.OwnerApproved || c.ApplicationPlanDigest != "" || c.ApplicationOperationId != "" || c.ApplicationAction != "" {
			return fmt.Errorf("application read cannot carry mutation fields")
		}
		return nil
	}
	if !c.OwnerApproved || !commandIDPattern.MatchString(c.ApplicationOperationId) {
		return fmt.Errorf("application mutation requires Owner approval and stable operation identity")
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_ADOPT {
		if !specHashPattern.MatchString(c.ApplicationPlanDigest) {
			return fmt.Errorf("application adopt requires the reviewed source plan digest")
		}
	} else if c.ApplicationPlanDigest != "" {
		return fmt.Errorf("application plan digest is only valid for adopt")
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONTROL {
		if !slices.Contains([]string{"start", "stop", "restart"}, c.ApplicationAction) {
			return fmt.Errorf("application action must be start, stop, or restart")
		}
	} else if c.ApplicationAction != "" {
		return fmt.Errorf("application action is only valid for control")
	}
	return nil
}

func ParseApplicationEvidence(c *agentpb.StackKitCommand, r *agentpb.StackKitResult) (ApplicationEvidence, error) {
	var e ApplicationEvidence
	if c == nil || r == nil || !r.Success || !IsApplicationOperation(c.Operation) {
		return e, fmt.Errorf("successful application evidence is required")
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(r.CommandResultJson, &envelope); err != nil {
		return e, err
	}
	raw := envelope.Data
	var currentSource *ApplicationSource
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_VERIFY {
		var verified struct {
			APIVersion        string            `json:"apiVersion"`
			SignatureVerified bool              `json:"signatureVerified"`
			Receipt           json.RawMessage   `json:"receipt"`
			CurrentSource     ApplicationSource `json:"currentSource"`
		}
		if json.Unmarshal(raw, &verified) != nil || verified.APIVersion != "stackkit.application-adoption-verification/v1" || !verified.SignatureVerified {
			return e, fmt.Errorf("application verification lacks local signature verification")
		}
		raw = verified.Receipt
		currentSource = &verified.CurrentSource
	}
	if c.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT {
		var signed struct {
			Result    json.RawMessage `json:"result"`
			Signature struct {
				OwnerRef string `json:"ownerRef"`
				KeyID    string `json:"keyId"`
				Value    string `json:"value"`
			} `json:"signature"`
		}
		if err := json.Unmarshal(raw, &signed); err != nil {
			return e, err
		}
		digest := sha256.Sum256(r.CommandResultJson)
		if !r.PinnedCliEvidenceVerified || r.PinnedCliEvidenceSha256 != "sha256:"+hex.EncodeToString(digest[:]) || signed.Signature.OwnerRef == "" || signed.Signature.KeyID == "" || signed.Signature.Value == "" {
			return e, fmt.Errorf("application receipt lacks authenticated pinned-CLI custody verification")
		}
		raw = signed.Result
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, err
	}
	wantVersion := "stackkit.application-adoption-result/v1"
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT {
		wantVersion = "stackkit.application-adoption-plan/v1"
	}
	if e.APIVersion != wantVersion || e.ProfileRef != HomeAssistantAdoptionProfile || e.Mode != "adopt_in_place" || e.Source.ContainerID != c.ApplicationContainerId || !specHashPattern.MatchString(e.PlanDigest) || !specHashPattern.MatchString(e.Source.Revision) || !specHashPattern.MatchString(e.Source.ConfigurationDigest) || !specHashPattern.MatchString(e.Source.AccountsDigest) || e.Source.ConfigMount == "" || e.Source.ImageDigest == "" || e.VerifiedOwnerRef == "" || e.WorkloadRef == "" || e.Authority.PackageRef == "" || !specHashPattern.MatchString(e.Authority.PlanHash) || !specHashPattern.MatchString(e.Authority.LifecycleContractHash) || e.Authority.LifecycleVersion == "" {
		return e, fmt.Errorf("application evidence does not bind the supported source and lifecycle")
	}
	for _, field := range []string{"accounts", "configuration", "persistentData"} {
		if !slices.Contains(e.PreservedFields, field) {
			return e, fmt.Errorf("application preservation contract is incomplete")
		}
	}
	if !slices.Equal(e.OwnedFields, []string{"container.power-state"}) {
		return e, fmt.Errorf("application receipt does not limit ownership to supported runtime control")
	}
	for _, op := range e.AdmittedOperations {
		if !slices.Contains([]string{"verify", "start", "stop", "restart", "release"}, op) {
			return e, fmt.Errorf("application receipt admits an unsupported operation")
		}
	}
	if !slices.Contains(e.AdmittedOperations, "verify") || !slices.Contains(e.AdmittedOperations, "release") {
		return e, fmt.Errorf("application receipt has no verification/release binding")
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_ADOPT && (e.PlanDigest != c.ApplicationPlanDigest || e.OperationID != c.ApplicationOperationId) {
		return e, fmt.Errorf("application receipt differs from approved plan or operation")
	}
	if e.WorkloadRef != "smart-home" {
		return e, fmt.Errorf("application evidence differs from the supported workload")
	}
	if currentSource != nil && *currentSource != e.Source {
		return e, fmt.Errorf("application current runtime no longer matches its signed binding")
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_RELEASE || c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONTROL {
		if e.OperationID != c.ApplicationOperationId {
			return e, fmt.Errorf("application result differs from dispatched operation identity")
		}
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONTROL {
		want := "running"
		if c.ApplicationAction == "stop" {
			want = "stopped"
		}
		if e.Action != c.ApplicationAction || e.PowerState != want {
			return e, fmt.Errorf("application control has no matching measured power outcome")
		}
	}
	if c.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT {
		wantStatus := "verified"
		if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_RELEASE {
			wantStatus = "released"
		}
		if e.Status != wantStatus || e.VerifiedAt.IsZero() || e.VerifiedAt.After(time.Now().Add(time.Minute)) {
			return e, fmt.Errorf("application receipt has no verified outcome")
		}
	}
	return e, nil
}

func ApplicationEvidenceMap(e ApplicationEvidence) map[string]any {
	raw, _ := json.Marshal(e)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}
func SameApplicationSource(a, b ApplicationEvidence) bool {
	return a.Source == b.Source && a.Authority == b.Authority && a.WorkloadRef == b.WorkloadRef && a.PlanDigest == b.PlanDigest && a.VerifiedOwnerRef == b.VerifiedOwnerRef && strings.Join(a.PreservedFields, "\x00") == strings.Join(b.PreservedFields, "\x00")
}
