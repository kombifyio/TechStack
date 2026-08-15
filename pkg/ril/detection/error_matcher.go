package detection

import (
	"context"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/ril/actions"
)

const errorCountThreshold = 10

// ErrorMatcher detects recurring error patterns in service logs and
// produces action cards with suggested fixes.
type ErrorMatcher struct{}

func NewErrorMatcher() *ErrorMatcher { return &ErrorMatcher{} }

func (e *ErrorMatcher) Name() string { return "error-pattern-matcher" }

func (e *ErrorMatcher) Evaluate(_ context.Context, input *EvaluationInput) ([]*actions.ActionCard, error) {
	if input == nil || len(input.ErrorPatterns) == 0 {
		return nil, nil
	}

	var cards []*actions.ActionCard

	for service, patterns := range input.ErrorPatterns {
		for _, p := range patterns {
			if p.Count < errorCountThreshold {
				continue
			}

			severity := actions.SeverityWarning
			if p.Count >= 50 {
				severity = actions.SeverityCritical
			}

			cards = append(cards, &actions.ActionCard{
				ServerID:    input.ServerID,
				OwnerID:     input.OwnerID,
				Type:        actions.TypeErrorFix,
				Severity:    severity,
				Title:       fmt.Sprintf("Recurring error in %s (%dx)", service, p.Count),
				Description: fmt.Sprintf("Pattern: %s\nFirst seen: %s\nLast seen: %s", p.Pattern, p.FirstSeen, p.LastSeen),
				Engine:      e.Name(),
				SuggestedPlan: &actions.ActionPlan{
					Steps: []actions.ActionStep{
						{
							Description:   fmt.Sprintf("Investigate and fix recurring error in %s", service),
							TargetService: service,
						},
						{
							Description:   fmt.Sprintf("Restart %s after fix", service),
							Command:       fmt.Sprintf("systemctl restart %s", strings.ReplaceAll(service, " ", "-")),
							TargetService: service,
						},
					},
					EstDuration: "5m",
				},
			})
		}
	}

	return cards, nil
}
