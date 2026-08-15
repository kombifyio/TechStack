package detection

import (
	"context"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/ril/actions"
)

// DriftDetector adapts the existing Pillar 3 drift detection (pkg/drift/)
// into the detection engine interface. It produces action cards when
// config drift is detected between spec (UnifiedSpec/IaC) and actual.
type DriftDetector struct{}

func NewDriftDetector() *DriftDetector { return &DriftDetector{} }

func (e *DriftDetector) Name() string { return "config-drift-detector" }

func (e *DriftDetector) Evaluate(_ context.Context, input *EvaluationInput) ([]*actions.ActionCard, error) {
	if input == nil || len(input.DriftEntries) == 0 {
		return nil, nil
	}

	diffs := make([]string, 0, len(input.DriftEntries))
	steps := make([]actions.ActionStep, 0, len(input.DriftEntries))
	for _, d := range input.DriftEntries {
		diffs = append(diffs, fmt.Sprintf("%s: expected=%q actual=%q", d.Path, d.Expected, d.Actual))
		steps = append(steps, actions.ActionStep{
			Description:   fmt.Sprintf("Reconcile %s to expected value", d.Path),
			TargetService: d.Service,
		})
	}

	severity := actions.SeverityWarning
	if len(input.DriftEntries) > 5 {
		severity = actions.SeverityCritical
	}

	return []*actions.ActionCard{{
		ServerID:    input.ServerID,
		OwnerID:     input.OwnerID,
		Type:        actions.TypeDrift,
		Severity:    severity,
		Title:       fmt.Sprintf("Config drift detected (%d items)", len(input.DriftEntries)),
		Description: strings.Join(diffs, "\n"),
		Engine:      e.Name(),
		SuggestedPlan: &actions.ActionPlan{
			Steps:       steps,
			Rollback:    "Revert to previous config via git checkout",
			EstDuration: fmt.Sprintf("%dm", len(input.DriftEntries)),
		},
	}}, nil
}
