package stacks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/specv2"
)

const wizardGoalActivationTopic = "product.news"

// loadStoredWizardGoals folds the durable homelab goal backlog into every
// expansion before the exact pinned StackKits projector runs. Techstack keeps
// no goal-to-workload mapping of its own.
func (h wizardRunHandlers) loadStoredWizardGoals(ctx context.Context, run *wizardRunState) error {
	homelab, err := h.crud.homelabStore.GetHomelabByOwner(ctx, run.tenantID, run.ownerID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return nil
		}
		return err
	}
	run.homelab = homelab
	wizard, _ := homelab.Intent["wizard"].(map[string]any)
	storedGoals := wizardStringValues(wizard["goals"])
	run.priorUnmappedGoals = wizardStringValues(wizard["unmapped_goals"])
	run.request.Intent.Goals = mergeWizardGoals(storedGoals, run.priorUnmappedGoals, run.request.Intent.Goals)
	return nil
}

// captureGoalActivations compares the release projection with the durable
// unmapped backlog. Only a goal that the exact pinned release stopped
// returning as unmapped is considered newly activated.
func (run *wizardRunState) captureGoalActivations(projection *specv2.Projection) {
	if projection == nil || len(run.priorUnmappedGoals) == 0 {
		return
	}
	stillUnmapped := make(map[string]struct{}, len(projection.UnmappedGoals))
	for _, goal := range projection.UnmappedGoals {
		stillUnmapped[strings.ToLower(strings.TrimSpace(goal))] = struct{}{}
	}
	for _, goal := range run.priorUnmappedGoals {
		normalized := strings.ToLower(strings.TrimSpace(goal))
		if normalized == "" {
			continue
		}
		if _, remains := stillUnmapped[normalized]; !remains {
			run.activatedGoals = append(run.activatedGoals, normalized)
		}
	}
	run.activatedGoals = mergeWizardGoals(run.activatedGoals)
}

func (h wizardRunHandlers) enqueueWizardGoalActivationNotifications(ctx context.Context, run *wizardRunState, stackID string) {
	if h.cfg.NotificationOutbox == nil || len(run.activatedGoals) == 0 {
		return
	}
	release := strings.TrimSpace(h.cfg.ReleaseVersion)
	for _, goal := range run.activatedGoals {
		key := fmt.Sprintf("techstack-wizard-goal-activated:%s:%s:%s:%s", run.tenantID, run.homelab.ID, release, goal)
		err := h.cfg.NotificationOutbox.Enqueue(ctx, productnotifications.ProductEvent{
			Topic:          wizardGoalActivationTopic,
			Channel:        "in_app",
			Auth0UserID:    run.ownerID,
			OrganizationID: run.tenantID,
			IdempotencyKey: key,
			Payload: map[string]any{
				"subject":    "A saved Wizard goal is now available",
				"body":       fmt.Sprintf("Your saved %s goal is now included in this StackKit deployment.", goal),
				"severity":   "info",
				"goal":       goal,
				"release":    release,
				"homelab_id": run.homelab.ID,
				"stack_id":   stackID,
				"source_app": "techstack",
				"event_key":  "wizard.goal_activated",
				"link_url":   "/dashboard",
			},
		})
		if err != nil {
			logger.Default().Warn("wizard_goal_activation_notification_enqueue_failed",
				"tenant_id", run.tenantID, "homelab_id", run.homelab.ID,
				"goal", goal, "release", release, "error", err)
		}
	}
}

func wizardStringValues(raw any) []string {
	values := []string{}
	switch typed := raw.(type) {
	case []string:
		values = typed
	case []any:
		for _, value := range typed {
			if text, ok := value.(string); ok {
				values = append(values, text)
			}
		}
	}
	return mergeWizardGoals(values)
}

func mergeWizardGoals(groups ...[]string) []string {
	unique := map[string]struct{}{}
	for _, group := range groups {
		for _, goal := range group {
			normalized := strings.ToLower(strings.TrimSpace(goal))
			if normalized != "" {
				unique[normalized] = struct{}{}
			}
		}
	}
	goals := make([]string, 0, len(unique))
	for goal := range unique {
		goals = append(goals, goal)
	}
	sort.Strings(goals)
	return goals
}
