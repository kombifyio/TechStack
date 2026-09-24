package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
)

const stackKitLifecycleNotificationTopic = "jobs.completed"

type stackKitLifecycleNotificationOutbox interface {
	Enqueue(context.Context, productnotifications.ProductEvent) error
}

// stackKitLifecycleNotifier projects only the canonical terminal job snapshot.
// Delivery, device selection, suppression, and preferences remain owned by the
// central Notifications service and the existing Techstack outbox.
type stackKitLifecycleNotifier struct {
	outbox stackKitLifecycleNotificationOutbox
}

func (n stackKitLifecycleNotifier) ObserveTerminalJob(ctx context.Context, job jobs.JobSnapshot) error {
	if n.outbox == nil {
		return nil
	}
	eventKey, operation, state, relevant := stackKitLifecycleNotificationIdentity(job)
	if !relevant {
		return nil
	}
	tenantID := stackKitNotificationString(job, "tenant_id")
	ownerID := stackKitNotificationString(job, "owner_id")
	stackKitID := firstStackKitNotificationString(job, "stackkit_id", "stackkit", "stackkit_catalog_ref", "catalog_ref")
	nodeID := stackKitNotificationNodeID(job)
	kitDeploymentID := strings.TrimSpace(job.TargetID)
	if tenantID == "" || ownerID == "" || stackKitID == "" || kitDeploymentID == "" || nodeID == "" {
		logger.Get().Warn("stackkit_lifecycle_notification_context_incomplete",
			"job_id", job.ID, "tenant_id", tenantID, "kit_deployment_id", kitDeploymentID,
			"has_owner", ownerID != "", "has_stackkit_id", stackKitID != "", "has_node_id", nodeID != "")
		return nil
	}
	deepLink := "/stacks/" + url.PathEscape(kitDeploymentID)
	completedAt := time.Now().UTC()
	if job.CompletedAt != nil {
		completedAt = job.CompletedAt.UTC()
	}
	subject, body := stackKitLifecycleNotificationCopy(stackKitID, operation, state)
	payload := map[string]any{
		"subject": subject, "body": body, "link_url": deepLink,
		"stackkit_id": stackKitID, "kit_deployment_id": kitDeploymentID,
		"techstack_id": kitDeploymentID, "node_id": nodeID,
		"stack_name": strings.TrimSpace(job.TargetName), "job_id": strings.TrimSpace(job.ID),
		"operation": operation, "state": state,
		"occurred_at": completedAt.Format(time.RFC3339Nano),
	}
	priority := "normal"
	if state == string(jobs.JobStateFailed) {
		priority = "high"
	}
	for _, channel := range []string{"in_app", "push"} {
		if err := n.outbox.Enqueue(ctx, productnotifications.ProductEvent{
			Topic: stackKitLifecycleNotificationTopic, Channel: channel,
			Auth0UserID: ownerID, OrganizationID: tenantID,
			IdempotencyKey: fmt.Sprintf("techstack:stackkit:%s:%s:%s", job.ID, eventKey, channel),
			Payload:        payload, SourceApp: notificationSourceApp, EventKey: eventKey,
			SubjectRef: "stackkit:" + stackKitID, DeepLink: deepLink,
			GroupKey: "kit_deployment:" + kitDeploymentID, Priority: priority,
		}); err != nil {
			return err
		}
	}
	return nil
}

func stackKitLifecycleNotificationIdentity(job jobs.JobSnapshot) (eventKey, operation, state string, relevant bool) {
	switch job.State {
	case jobs.JobStateCompleted, jobs.JobStateFailed:
		state = string(job.State)
	default:
		return "", "", "", false
	}
	switch job.Type {
	case jobs.JobTypeDeploy:
		operation = "install"
	case jobs.JobTypeStackKitLifecycle:
		switch strings.ToLower(stackKitNotificationString(job, "operation")) {
		case jobs.StackKitLifecycleApply, jobs.StackKitLifecycleUpgrade:
			operation = "update"
		default:
			return "", "", "", false
		}
	default:
		return "", "", "", false
	}
	return "stackkit." + operation + "." + state, operation, state, true
}

func stackKitLifecycleNotificationCopy(stackKitID, operation, state string) (string, string) {
	if operation == "install" && state == string(jobs.JobStateCompleted) {
		return "StackKit installed", fmt.Sprintf("StackKit %s was installed successfully.", stackKitID)
	}
	if operation == "install" {
		return "StackKit installation failed", fmt.Sprintf("StackKit %s could not be installed.", stackKitID)
	}
	if state == string(jobs.JobStateCompleted) {
		return "StackKit updated", fmt.Sprintf("StackKit %s was updated successfully.", stackKitID)
	}
	return "StackKit update failed", fmt.Sprintf("StackKit %s could not be updated.", stackKitID)
}

func firstStackKitNotificationString(job jobs.JobSnapshot, keys ...string) string {
	for _, key := range keys {
		if value := stackKitNotificationString(job, key); value != "" {
			return value
		}
	}
	return ""
}

func stackKitNotificationString(job jobs.JobSnapshot, key string) string {
	for _, source := range []map[string]any{job.Payload, job.Result} {
		if value, ok := source[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stackKitNotificationNodeID(job jobs.JobSnapshot) string {
	if value := firstStackKitNotificationString(job, "node_id", "server_id", "runtime_server_id"); value != "" {
		return value
	}
	workers, _ := job.Payload["workers"].([]any)
	for _, raw := range workers {
		worker, _ := raw.(map[string]any)
		for _, key := range []string{"node_id", "server_id"} {
			if value, ok := worker[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	if workers, ok := job.Payload["workers"].([]map[string]any); ok {
		for _, worker := range workers {
			for _, key := range []string{"node_id", "server_id"} {
				if value, ok := worker[key].(string); ok && strings.TrimSpace(value) != "" {
					return strings.TrimSpace(value)
				}
			}
		}
	}
	return ""
}
