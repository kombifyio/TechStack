package detection

import (
	"context"
	"fmt"

	"github.com/kombifyio/techstack/pkg/ril/actions"
)

const (
	cpuWarningThreshold   = 85.0
	cpuCriticalThreshold  = 95.0
	memWarningThreshold   = 85.0
	memCriticalThreshold  = 95.0
	diskWarningThreshold  = 85.0
	diskCriticalThreshold = 95.0
)

// ResourceMonitor checks CPU, memory, and disk usage against configurable
// thresholds and produces action cards when limits are exceeded.
type ResourceMonitor struct{}

func NewResourceMonitor() *ResourceMonitor { return &ResourceMonitor{} }

func (e *ResourceMonitor) Name() string { return "resource-monitor" }

func (e *ResourceMonitor) Evaluate(_ context.Context, input *EvaluationInput) ([]*actions.ActionCard, error) {
	if input == nil || input.ResourceMetrics == nil {
		return nil, nil
	}

	m := input.ResourceMetrics
	var cards []*actions.ActionCard

	if card := e.checkResource(input, "CPU", m.CPUPercent, cpuWarningThreshold, cpuCriticalThreshold); card != nil {
		cards = append(cards, card)
	}
	if card := e.checkResource(input, "Memory", m.MemPercent, memWarningThreshold, memCriticalThreshold); card != nil {
		cards = append(cards, card)
	}
	if card := e.checkResource(input, "Disk", m.DiskPercent, diskWarningThreshold, diskCriticalThreshold); card != nil {
		cards = append(cards, card)
	}

	return cards, nil
}

func (e *ResourceMonitor) checkResource(input *EvaluationInput, resource string, value, warnThresh, critThresh float64) *actions.ActionCard {
	if value < warnThresh {
		return nil
	}

	severity := actions.SeverityWarning
	if value >= critThresh {
		severity = actions.SeverityCritical
	}

	return &actions.ActionCard{
		ServerID:    input.ServerID,
		OwnerID:     input.OwnerID,
		Type:        actions.TypeResource,
		Severity:    severity,
		Title:       fmt.Sprintf("%s usage critical: %.1f%%", resource, value),
		Description: fmt.Sprintf("%s usage at %.1f%% (warning: %.0f%%, critical: %.0f%%)", resource, value, warnThresh, critThresh),
		Engine:      e.Name(),
	}
}
