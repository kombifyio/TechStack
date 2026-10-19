package stacks

import (
	"context"
	"encoding/json"
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

// loadStoredWizardGoals folds canonical wishes into the effective intent before
// admission. Explicit incoming fields win, including false and empty answers.
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
	stored, err := wizardIntentFromDocument(wizard)
	if err != nil {
		return err
	}
	run.priorUnmappedGoals = wizardStringValues(wizard["unmapped_goals"])
	intent := &run.request.Intent
	intent.Goals = mergeWizardGoals(stored.Goals, run.priorUnmappedGoals, intent.Goals)
	intent.UseCaseSettings = mergeWizardDecisions(stored.UseCaseSettings, intent.UseCaseSettings)
	intent.UseCaseCapabilities = mergeWizardDecisions(stored.UseCaseCapabilities, intent.UseCaseCapabilities)
	intent.UseCaseSetupAnswers = mergeWizardDecisions(stored.UseCaseSetupAnswers, intent.UseCaseSetupAnswers)
	if intent.Access.Mode == "" && intent.Access.Publications == nil {
		intent.Access = stored.Access
	}
	if intent.Household.Profile == "" && intent.Household.PlannedPeople == nil {
		intent.Household = stored.Household
	}
	return nil
}

func wizardIntentFromDocument(wizard map[string]any) (specv2.WizardIntent, error) {
	var intent specv2.WizardIntent
	raw, err := json.Marshal(wizard)
	if err != nil {
		return intent, err
	}
	err = json.Unmarshal(raw, &intent)
	return intent, err
}

func mergeWizardDecisions[T any](stored, incoming map[string]map[string]T) map[string]map[string]T {
	if stored == nil && incoming == nil {
		return nil
	}
	merged := make(map[string]map[string]T)
	for _, layer := range []map[string]map[string]T{stored, incoming} {
		for slug, values := range layer {
			if merged[slug] == nil {
				merged[slug] = make(map[string]T)
			}
			for id, value := range values {
				merged[slug][id] = value
			}
		}
	}
	return merged
}

// captureGoalActivations compares the release projection with the durable
// unmapped backlog. Only a goal that the exact pinned release stopped
// returning as unmapped is considered newly activated.
func (run *wizardRunState) captureGoalActivations(projection *specv2.Projection) {
	if projection == nil {
		return
	}
	run.evaluatedGoals = mergeWizardGoals(run.request.Intent.Goals)
	if len(run.priorUnmappedGoals) == 0 {
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
