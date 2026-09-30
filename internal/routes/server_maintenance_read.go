package routes

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/denial"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
)

// serverMaintenanceJobView is GET /api/v1/servers/{serverId}/actions/{jobId}.
// state is the ledger state: queued, running, waiting (an OS update whose
// install unit outlived the CLI's wait, reason_code apply_still_running),
// awaiting_node_return (a reboot waiting for its node), completed, failed or
// cancelled.
type serverMaintenanceJobView struct {
	JobID       string                       `json:"job_id"`
	ServerID    string                       `json:"server_id"`
	Action      string                       `json:"action"`
	State       string                       `json:"state"`
	Status      string                       `json:"status"`
	ReasonCode  string                       `json:"reason_code,omitempty"`
	PlanDigest  string                       `json:"plan_digest,omitempty"`
	Failure     *serverMaintenanceFailure    `json:"failure,omitempty"`
	Result      map[string]any               `json:"result"`
	Reboot      *serverMaintenanceRebootView `json:"reboot,omitempty"`
	DeadlineAt  *time.Time                   `json:"deadline_at,omitempty"`
	CreatedAt   time.Time                    `json:"created_at"`
	UpdatedAt   time.Time                    `json:"updated_at"`
	CompletedAt *time.Time                   `json:"completed_at,omitempty"`
}

type serverMaintenanceFailure struct {
	Code         string              `json:"code"`
	Retryable    bool                `json:"retryable"`
	UserGuidance denial.UserGuidance `json:"user_guidance"`
}

// serverMaintenanceRebootView is present for reboot jobs once the node
// acknowledged the reboot. boot_id_changed is true once the node reports a
// boot id other than the one before the reboot, which is the only proof that
// it rebooted.
type serverMaintenanceRebootView struct {
	ScheduledAt    *time.Time `json:"scheduled_at,omitempty"`
	ReturnDeadline *time.Time `json:"return_deadline,omitempty"`
	BootIDChanged  bool       `json:"boot_id_changed"`
}

// serverMaintenanceResultKeys are the job result fields a client may read.
// Everything else the runner keeps (such as the boot id) stays internal.
var serverMaintenanceResultKeys = []string{
	// os_update_plan
	"plan_digest", "pending_count", "security_count", "reboot_likely", "hold_scope", "held",
	"dpkg_problems", "packages", "packages_truncated",
	// os_update
	"outcome", "reboot_required", "unit", "changed", "remaining_pending_count", "plan_digest_after", "not_installed",
}

func (h serverRuntimeHandlers) maintenanceJob(e *httpx.Event) error {
	m := h.maintenance
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, ServerMaintenanceEntitlement)
	if tenantErr != nil {
		return tenantErr
	}
	serverID := strings.TrimSpace(e.Request.PathValue("serverId"))
	jobID := strings.TrimSpace(e.Request.PathValue("jobId"))
	if serverID == "" || jobID == "" {
		return httpx.BadRequest(e, "Server ID and job ID are required", nil)
	}
	ctx := e.Request.Context()
	if !m.entitlements.SignedGrant(ctx, ServerMaintenanceEntitlement) {
		return writeServerMaintenanceDenial(e, http.StatusForbidden, maintenanceReasonEntitlement, nil)
	}
	server, err := h.store.GetServerRuntime(ctx, tenantID, serverID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && !serverRuntimeOwnedBy(*server, ownerID)) {
		return httpx.NotFound(e, "Maintenance job not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	job, err := m.jobs.GetServerMaintenanceJob(ctx, tenantID, jobID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && (job.ServerID != server.ID || job.OwnerSubjectID != ownerID)) {
		return httpx.NotFound(e, "Maintenance job not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server maintenance is unavailable", nil)
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, serverMaintenanceJobResponse(*job, *server))
}

func serverMaintenanceJobResponse(job controlplane.ServerMaintenanceJob, server controlplane.ServerRuntime) serverMaintenanceJobView {
	view := serverMaintenanceJobView{
		JobID: job.ID, ServerID: job.ServerID, Action: job.Action, State: job.State,
		Status: serverMaintenanceStatus(job.State), ReasonCode: job.ReasonCode, PlanDigest: job.PlanDigest,
		Result: map[string]any{}, DeadlineAt: job.DeadlineAt,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt, CompletedAt: job.CompletedAt,
	}
	for _, key := range serverMaintenanceResultKeys {
		if value, ok := job.Result[key]; ok {
			view.Result[key] = value
		}
	}
	if job.State == controlplane.ServerMaintenanceStateFailed || job.State == controlplane.ServerMaintenanceStateCancelled {
		view.Failure = serverMaintenanceFailureFor(firstNonEmptyString(job.ReasonCode, servermaintenance.ReasonDispatchFailed))
	}
	if job.Action == controlplane.ServerMaintenanceActionReboot {
		view.Reboot = serverMaintenanceRebootFor(job, server)
	}
	return view
}

func serverMaintenanceRebootFor(job controlplane.ServerMaintenanceJob, server controlplane.ServerRuntime) *serverMaintenanceRebootView {
	before, _ := job.Result["boot_id_before"].(string)
	scheduledRaw, _ := job.Result["scheduled_at"].(string)
	scheduledAt, err := time.Parse(time.RFC3339Nano, scheduledRaw)
	if strings.TrimSpace(before) == "" || err != nil {
		return nil
	}
	scheduledAt = scheduledAt.UTC()
	view := &serverMaintenanceRebootView{ScheduledAt: &scheduledAt}
	if job.State == controlplane.ServerMaintenanceStateAwaitingNodeReturn {
		view.ReturnDeadline = job.DeadlineAt
	}
	current := strings.TrimSpace(stringFromAnyMap(server.Metadata, servermaintenance.MetadataBootID))
	view.BootIDChanged = job.ReasonCode == servermaintenance.ReasonNodeReturned ||
		(current != "" && current != strings.TrimSpace(before))
	return view
}

// serverMaintenanceFailureGuidance explains the stable failure and refusal
// codes a maintenance job can end with: the runner's own reasons and the
// stackkit.host-maintenance/v1 refusal and failure codes.
var serverMaintenanceFailureGuidance = map[string]maintenanceGuidance{
	servermaintenance.ReasonNodeDidNotReturn: {
		title: "The server did not come back",
		body:  "The server went offline for the reboot and did not reconnect within 10 minutes.",
		steps: []string{"Check the server's console or provider panel.", "Reconnect the server once it is running."},
	},
	servermaintenance.ReasonRebootAbandoned: {
		title: "The reboot did not happen",
		body:  "The server stayed busy with package operations, so the reboot was skipped to protect it. Nothing was changed.",
		steps: []string{"Retry the reboot later."}, retryable: true,
	},
	servermaintenance.ReasonDeadlineExceeded: {
		title: "Maintenance took too long",
		body:  "The job did not finish in time and was stopped. The server may still be busy.",
		steps: []string{"Check the server, then retry."}, retryable: true,
	},
	servermaintenance.ReasonApplyPartial: {
		title: "Some updates were not installed",
		body:  "The update finished, but some packages of the approved plan are still at their old versions.",
		steps: []string{"Create a new update plan and approve it."}, retryable: true,
	},
	servermaintenance.ReasonServerRemoved: {
		title: "The server was removed",
		body:  "The server was removed while the maintenance job was running.",
		steps: []string{"No action is needed."},
	},
	servermaintenance.ReasonDispatchInterrupted: {
		title: "The request never reached the server",
		body:  "Techstack stopped before the server's agent picked up the command. Nothing ran on the server.",
		steps: []string{"Retry the request."}, retryable: true,
	},
	servermaintenance.RefusalPackageManagerBusy: {
		title: "The server was busy",
		body:  "Another package operation or maintenance job was running on the server. Nothing was changed.",
		steps: []string{"Retry in a few minutes."}, retryable: true,
	},
	"plan_stale": {
		title: "The update plan was out of date",
		body:  "The packages available on the server changed after the plan. Nothing was installed.",
		steps: []string{"Create a new update plan and approve it."}, retryable: true,
	},
	"control_plane_host": {
		title: "This server runs the control plane",
		body:  "The server refused the reboot because it runs Techstack itself.",
		steps: []string{"Maintain this host from its own console."},
	},
	"unattended_reboot_unsupported": {
		title: "This server cannot reboot unattended",
		body:  "The server needs someone at the console to boot, for example to unlock an encrypted disk.",
		steps: []string{"Reboot the server where you can unlock it."},
	},
	"unsupported_package_manager": {
		title: "OS updates are not supported here",
		body:  "kombify updates Debian and Ubuntu servers that use apt.",
		steps: []string{"Update this server with its own tools."},
	},
	"unsupported_os": {
		title: "OS updates are not supported here",
		body:  "kombify updates Debian and Ubuntu servers that use apt.",
		steps: []string{"Update this server with its own tools."},
	},
	"dpkg_broken": {
		title: "The package database needs repair",
		body:  "The server reports interrupted or broken package installs, so no update was started.",
		steps: []string{"Repair the package database on the server, then create a new plan."},
	},
	"install_failed": {
		title: "The update did not install",
		body:  "apt reported an error while installing the planned updates.",
		steps: []string{"Create a new update plan and retry.", "Contact support with this job if it fails again."}, retryable: true,
	},
}

func serverMaintenanceFailureFor(code string) *serverMaintenanceFailure {
	guidance, ok := serverMaintenanceFailureGuidance[code]
	if !ok {
		guidance = maintenanceGuidance{
			title:     "Maintenance did not finish",
			body:      "The server could not complete the maintenance job. Nothing more will run for this job.",
			steps:     []string{"Check that the server is online, then retry.", "Contact support with this job if it fails again."},
			retryable: true,
		}
	}
	return &serverMaintenanceFailure{
		Code: code, Retryable: guidance.retryable,
		UserGuidance: denial.UserGuidance{Title: guidance.title, Body: guidance.body, NextSteps: guidance.steps},
	}
}
