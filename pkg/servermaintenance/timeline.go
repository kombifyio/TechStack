package servermaintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/outcome"
)

// Server timeline events of owner maintenance. The request itself is
// recorded by the route (owner_reboot_requested, os_update_requested).
const (
	EventRebootAcknowledged = "reboot_acknowledged"
	EventRebootObserved     = "reboot_observed"
	EventOSUpdateApplied    = "os_update_applied"
)

// maintenanceCapability is the entitlement the outcome is attributed to.
const maintenanceCapability = "techstack.servers.maintenance"

// recordTimeline writes the server event for a reboot or OS update that
// changed state: the pending outcome of the request moves on, becomes an
// available outcome on success, or becomes a failed outcome. The job row stays the durable record;
// a lost timeline write is logged and never undoes a job transition.
func (r Runner) recordTimeline(ctx context.Context, job controlplane.ServerMaintenanceJob) {
	if r.Events == nil || r.Servers == nil || job.Action == controlplane.ServerMaintenanceActionPlan {
		return
	}
	event := controlplane.ServerEvent{
		TenantID: job.TenantID, ServerID: job.ServerID, Authority: controlplane.ServerEventAuthorityControlPlane,
		Source: "server-maintenance", SourceID: job.ID, ObservedAt: r.now(),
		Evidence: map[string]any{"job_id": job.ID, "action": job.Action, "state": job.State, "reason_code": job.ReasonCode},
	}
	switch {
	case job.State == controlplane.ServerMaintenanceStateAwaitingNodeReturn:
		event.Outcome = &outcome.Decision{
			Status: outcome.StatusPending, ReasonCode: EventRebootAcknowledged, Capability: maintenanceCapability,
			UserGuidance: &outcome.Guidance{
				Title: "Reboot scheduled",
				Body:  "The server accepted the reboot. It goes offline briefly and must come back within 10 minutes.",
				NextSteps: []outcome.Step{
					{ID: "follow-maintenance-job", Label: "Follow the maintenance job", Kind: "note"},
				},
			},
			SupportContext: map[string]any{"job_id": job.ID},
		}
		event.Evidence["boot_id_before"] = stringValue(job.Result, "boot_id_before")
		event.Evidence["scheduled_at"] = stringValue(job.Result, "scheduled_at")
	case job.State == controlplane.ServerMaintenanceStateCompleted:
		// Success is an explicit available outcome, not a clear: a heartbeat
		// may already have cleared the pending outcome, and a clear of nothing
		// records no timeline transition.
		reason := EventOSUpdateApplied
		if job.Action == controlplane.ServerMaintenanceActionReboot {
			reason = EventRebootObserved
			event.Evidence["boot_id_before"] = stringValue(job.Result, "boot_id_before")
		}
		event.Outcome = &outcome.Decision{
			Status: outcome.StatusAvailable, ReasonCode: reason, Capability: maintenanceCapability,
			SupportContext: map[string]any{"job_id": job.ID},
		}
	case job.State == controlplane.ServerMaintenanceStateFailed || job.State == controlplane.ServerMaintenanceStateCancelled:
		reason := firstNonEmpty(job.ReasonCode, "maintenance_failed")
		event.Outcome = &outcome.Decision{
			Status: outcome.StatusFailed, ReasonCode: reason, Capability: maintenanceCapability,
			UserGuidance: &outcome.Guidance{
				Title: "Server maintenance did not finish",
				Body:  "The reboot or OS update did not complete. The server is unchanged or needs a look.",
				NextSteps: []outcome.Step{
					{ID: "review-maintenance-job", Label: "Review the maintenance job", Kind: "note"},
				},
			},
			SupportContext: map[string]any{"job_id": job.ID, "reason_code": reason},
		}
	default:
		return
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		server, err := r.Servers.GetServerRuntime(ctx, job.TenantID, job.ServerID)
		if err != nil {
			lastErr = err
			break
		}
		event.ExpectedRevision, event.Generation = server.Revision, server.Generation
		_, lastErr = r.Events.ApplyServerEvent(ctx, event)
		if lastErr == nil {
			return
		}
		if !errors.Is(lastErr, controlplane.ErrConflict) {
			break
		}
	}
	slog.WarnContext(ctx, "server_maintenance_timeline_write_failed", "server_id", job.ServerID, "job_id", job.ID, "state", job.State, "error", fmt.Sprint(lastErr))
}
