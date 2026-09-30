package unifier

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

// The declared experience answer is the most trusted techiness source: it
// must decide the operator band of a recommendation, and without it the
// observed channel baseline stays in charge.
func TestSelfDisclosureExperienceDecidesRecommendationOperatorBand(t *testing.T) {
	authority := NewWizardRecommendationAuthority(nil)
	request := core.WizardRecommendationRequest{Goals: []string{"photos"}, DeploymentLane: "self-hosted", Surface: "easy"}

	cases := []struct {
		experience string
		band       string
	}{
		{core.SelfDisclosureExperienceHandsOff, "oneclick"},
		{core.SelfDisclosureExperienceGuided, "guided"},
		{core.SelfDisclosureExperienceCurious, "curious-admin"},
		{core.SelfDisclosureExperienceTechie, "techie"},
		{core.SelfDisclosureExperienceExpert, "expert"},
	}
	for _, tc := range cases {
		response := authority.Recommend(WizardRecommendationEvaluation{
			Request:                 request,
			TenantID:                "tenant-1",
			OwnerID:                 "owner-1",
			SelfDisclosure:          &core.OperatorSelfDisclosure{Experience: tc.experience, Placement: "home"},
			SelfDisclosureUpdatedAt: time.Now(),
		})
		operator := response.DecisionContext.Operator
		if operator == nil || operator.Band != tc.band || operator.Source != selfDisclosureSource {
			t.Fatalf("experience %q produced operator %#v, want band %q from self-disclosure", tc.experience, operator, tc.band)
		}
	}

	baseline := authority.Recommend(WizardRecommendationEvaluation{
		Request:        request,
		SelfDisclosure: &core.OperatorSelfDisclosure{Placement: "home"},
	})
	if operator := baseline.DecisionContext.Operator; operator == nil || operator.Source == selfDisclosureSource {
		t.Fatalf("disclosure without an experience answer must keep the observed baseline, got %#v", operator)
	}
}
