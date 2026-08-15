package jobs

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/core"
)

const (
	stackSpecProposalAPIVersion = "techstack.kombify.io/v1alpha1"
	stackSpecProposalKind       = "StackSpecProposal"
)

// buildReviewableRequirements describes the operator proposal without
// validating, resolving, defaulting, or rendering StackKits-owned state.
func buildReviewableRequirements(spec *core.KombinationSpec) (*core.RequirementsSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("StackSpec proposal is required")
	}
	kit := strings.TrimSpace(spec.Kit)
	description := "Review StackSpec proposal"
	if kit != "" {
		description += " for " + kit
	}
	description += "; final validation and resolution are owned by the pinned StackKits CLI"

	return &core.RequirementsSpec{
		APIVersion:      stackSpecProposalAPIVersion,
		Kind:            "StackSpecProposalReview",
		StackKit:        kit,
		IntentName:      strings.TrimSpace(spec.Name),
		AppliedDefaults: map[string]any{},
		Description:     description,
	}, nil
}

// buildReviewableStackSpecProposal preserves the reviewed operator choices in
// the existing compatibility envelope. Resolved fields intentionally remain
// empty: only StackKits may create the canonical ResolvedPlan.
func buildReviewableStackSpecProposal(spec *core.KombinationSpec) (*core.UnifiedSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("StackSpec proposal is required")
	}
	copy := copyKombinationSpec(spec)
	if copy.Metadata == nil {
		copy.Metadata = map[string]string{}
	}
	copy.Metadata["techstack_spec_role"] = "review-proposal"
	copy.Metadata["techstack_final_authority"] = "pinned-stackkit-cli"
	return &core.UnifiedSpec{
		APIVersion:      stackSpecProposalAPIVersion,
		Kind:            stackSpecProposalKind,
		KombinationSpec: copy,
		StackKit:        strings.TrimSpace(copy.Kit),
	}, nil
}

func copyKombinationSpec(spec *core.KombinationSpec) core.KombinationSpec {
	copy := core.KombinationSpec{
		Name:    spec.Name,
		Version: spec.Version,
		Kit:     spec.Kit,
		Network: spec.Network,
	}
	if spec.Metadata != nil {
		copy.Metadata = make(map[string]string, len(spec.Metadata))
		for key, value := range spec.Metadata {
			copy.Metadata[key] = value
		}
	}
	if spec.Nodes != nil {
		copy.Nodes = make([]core.NodeSpec, len(spec.Nodes))
		for i, node := range spec.Nodes {
			copy.Nodes[i] = node
			if node.Tags != nil {
				copy.Nodes[i].Tags = make(map[string]string, len(node.Tags))
				for key, value := range node.Tags {
					copy.Nodes[i].Tags[key] = value
				}
			}
			if node.SSH != nil {
				ssh := *node.SSH
				copy.Nodes[i].SSH = &ssh
			}
		}
	}
	if spec.Services != nil {
		copy.Services = make([]core.ServiceSpec, len(spec.Services))
		for i, service := range spec.Services {
			copy.Services[i] = service
			copy.Services[i].Needs = append([]string(nil), service.Needs...)
		}
	}
	return copy
}
