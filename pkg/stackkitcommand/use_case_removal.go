package stackkitcommand

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

type UseCaseRemovalKind string

const (
	UseCaseRemovalWorkload UseCaseRemovalKind = "workload"
	UseCaseRemovalGroup    UseCaseRemovalKind = "use_case"
)

// Members are bound by the released catalog, never accepted from the HTTP body.
// Both variants author retained-data candidates; neither can request deletion.
type UseCaseRemovalMember struct {
	WorkloadRef string `json:"workload_ref"`
	ModuleRef   string `json:"module_ref"`
}
type UseCaseRemoval struct {
	Kind             UseCaseRemovalKind     `json:"selector_kind"`
	Ref              string                 `json:"selector_ref"`
	ExpectedSpecHash string                 `json:"expected_spec_hash"`
	Members          []UseCaseRemovalMember `json:"members"`
}

func DecodeUseCaseRemoval(raw []byte) (UseCaseRemoval, error) {
	var removal UseCaseRemoval
	if len(raw) == 0 || len(raw) > MaxUseCaseConfigurationBytes {
		return removal, fmt.Errorf("removal intent is not bounded")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&removal); err != nil {
		return removal, fmt.Errorf("invalid typed removal intent: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return removal, fmt.Errorf("removal requires one object")
	}
	if err := removal.Validate(); err != nil {
		return removal, err
	}
	return removal, nil
}

func (r UseCaseRemoval) Validate() error {
	if r.Kind != UseCaseRemovalWorkload && r.Kind != UseCaseRemovalGroup {
		return fmt.Errorf("select a workload or a catalog use case")
	}
	if !serviceKeyPattern.MatchString(r.Ref) || !NativeSpecHashValid(r.ExpectedSpecHash) || len(r.Members) == 0 || len(r.Members) > 64 {
		return fmt.Errorf("removal requires an exact catalog selection and native baseline")
	}
	seen := map[string]bool{}
	for _, member := range r.Members {
		if !serviceKeyPattern.MatchString(member.WorkloadRef) || !serviceKeyPattern.MatchString(member.ModuleRef) || seen[member.WorkloadRef] {
			return fmt.Errorf("removal catalog members are invalid or duplicated")
		}
		seen[member.WorkloadRef] = true
	}
	if r.Kind == UseCaseRemovalWorkload && (len(r.Members) != 1 || r.Members[0].WorkloadRef != r.Ref) {
		return fmt.Errorf("single-workload removal must contain only its exact workload")
	}
	return nil
}

func (r UseCaseRemoval) WorkloadRefs() []string {
	refs := make([]string, 0, len(r.Members))
	for _, member := range r.Members {
		refs = append(refs, member.WorkloadRef)
	}
	sort.Strings(refs)
	return refs
}

func ValidateUseCaseRemovalFields(c *agentpb.StackKitCommand) error {
	if c == nil {
		return fmt.Errorf("removal command is required")
	}
	if c.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_USE_CASE_REMOVE {
		if len(c.UseCaseRemovalJson) != 0 {
			return fmt.Errorf("removal intent belongs only to its typed authoring operation")
		}
		return nil
	}
	if c.StackkitInstanceId == "" || c.SpecPath == "" || len(c.CandidateSpecJson) != 0 || len(c.InventoryJson) != 0 || c.DryRun || c.Offline || c.TargetRelease != "" || c.OwnerApproved {
		return fmt.Errorf("removal authors an instance-scoped candidate without runtime approval")
	}
	_, err := DecodeUseCaseRemoval(c.UseCaseRemovalJson)
	return err
}

type UseCaseRemovalEvidence = NativeIntentCandidateEvidence

func ParseUseCaseRemovalEvidence(c *agentpb.StackKitCommand, result *agentpb.StackKitResult) (UseCaseRemovalEvidence, error) {
	var evidence UseCaseRemovalEvidence
	removal, err := DecodeUseCaseRemoval(c.UseCaseRemovalJson)
	if err != nil {
		return evidence, err
	}
	evidence, err = ParseNativeIntentCandidateEvidence(c, result, removal.ExpectedSpecHash)
	if err != nil {
		return evidence, err
	}
	if evidence.SpecHash == evidence.PreviousSpecHash {
		return evidence, fmt.Errorf("removal candidate retired no installed workload")
	}
	if err := validateUseCaseRemovalDelta(evidence.PreviousSpec, evidence.CandidateSpec, removal); err != nil {
		return evidence, err
	}
	return evidence, nil
}

// The native graph resolver owns dependency admission. At transport ingress,
// ensure the compiler retired exactly the released catalog members and their
// module-owned routes, preserving every other canonical field and all data.
func validateUseCaseRemovalDelta(before, after map[string]any, removal UseCaseRemoval) error {
	expected := cloneConfigurationObject(before)
	workloads, ok := expected["workloads"].(map[string]any)
	if !ok {
		return fmt.Errorf("removal baseline has no workloads")
	}
	modules, _ := expected["modules"].(map[string]any)
	retiredModules := map[string]bool{}
	for _, member := range removal.Members {
		if _, exists := workloads[member.WorkloadRef]; !exists {
			return fmt.Errorf("removal member is not installed")
		}
		delete(workloads, member.WorkloadRef)
		delete(modules, member.ModuleRef)
		retiredModules[member.ModuleRef] = true
	}
	routes, _ := expected["routes"].(map[string]any)
	for id, raw := range routes {
		route, _ := raw.(map[string]any)
		module, _ := route["moduleRef"].(string)
		if retiredModules[module] {
			delete(routes, id)
		}
	}
	if !reflect.DeepEqual(expected, after) {
		return fmt.Errorf("removal changed unrelated Foundation, workloads, routes, data or server intent")
	}
	return nil
}
