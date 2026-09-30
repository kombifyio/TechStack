package unifier

import (
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

const (
	selfDisclosureSource = "self-disclosure"
	// Voluntary self-disclosure is the most trusted techiness source
	// (CREATION-EXPERIENCE-STANDARD §4), so it starts above the inferred
	// baseline confidence of 0.55.
	selfDisclosureConfidence = 0.85
)

// selfDisclosureExperienceScores places the five experience answers on the
// ten-level capability scale. Each answer lands on the upper anchor of its
// band, so the band a user picked is the band the profile reports.
var selfDisclosureExperienceScores = map[string]int{
	core.SelfDisclosureExperienceHandsOff: 2,
	core.SelfDisclosureExperienceGuided:   4,
	core.SelfDisclosureExperienceCurious:  6,
	core.SelfDisclosureExperienceTechie:   8,
	core.SelfDisclosureExperienceExpert:   10,
}

// OperatorCapabilityFromSelfDisclosure derives the capability profile from a
// voluntary self-disclosure. It returns nil when the operator did not answer
// the experience question: the other answers are context, not techiness, and
// the observed channel baseline stays in charge.
func OperatorCapabilityFromSelfDisclosure(disclosure *core.OperatorSelfDisclosure, updatedAt time.Time) *core.OperatorCapabilityProfile {
	if disclosure == nil {
		return nil
	}
	experience := strings.ToLower(strings.TrimSpace(disclosure.Experience))
	score, ok := selfDisclosureExperienceScores[experience]
	if !ok {
		return nil
	}
	profile := &core.OperatorCapabilityProfile{
		Score:      score,
		Confidence: selfDisclosureConfidence,
		Source:     selfDisclosureSource,
		Evidence: []core.OperatorCapabilityEvidence{{
			Signal: "self-disclosure:experience:" + experience,
			Weight: score,
			Source: selfDisclosureSource,
		}},
		UpdatedAt: updatedAt.UTC(),
	}
	normalizeOperatorCapability(profile)
	return profile
}

// selfDisclosureConstraints projects the non-techiness answers into soft
// decision constraints. They inform guidance and ranking context but never
// override an explicit wizard choice, so none of them is required.
func selfDisclosureConstraints(disclosure *core.OperatorSelfDisclosure) []core.DecisionConstraint {
	if disclosure == nil {
		return nil
	}
	constraints := make([]core.DecisionConstraint, 0, 8)
	add := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			constraints = append(constraints, core.DecisionConstraint{Key: key, Value: value, Source: selfDisclosureSource})
		}
	}
	add("self_disclosure.placement", disclosure.Placement)
	add("self_disclosure.household", disclosure.Household)
	for _, hardware := range disclosure.Hardware {
		add("self_disclosure.hardware", hardware)
	}
	for _, motivation := range disclosure.Motivations {
		add("self_disclosure.motivation", motivation)
	}
	for _, goal := range disclosure.Goals {
		add("self_disclosure.goal", goal)
	}
	return constraints
}
