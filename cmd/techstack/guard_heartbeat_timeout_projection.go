package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/outcome"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const guardHeartbeatTimeoutSource = "provision-guard-timeout"

type guardHeartbeatTimeoutStore interface {
	ListServerRuntimesByTenant(ctx context.Context, tenantID, stackID string) ([]controlplane.ServerRuntime, error)
	GetServerRuntime(ctx context.Context, tenantID, serverID string) (*controlplane.ServerRuntime, error)
	ApplyServerEvent(ctx context.Context, event controlplane.ServerEvent) (*controlplane.ServerEventResult, error)
}

// guardHeartbeatTimeoutProjector ends the enrollment of a stack's servers when
// its provision job failed because the created server's Guard never sent a
// heartbeat. Without it the server stays "enrolling" forever although nothing
// waits for it any more. A Guard that connects later still promotes it.
type guardHeartbeatTimeoutProjector struct {
	store guardHeartbeatTimeoutStore
}

func (p guardHeartbeatTimeoutProjector) ObserveTerminalJob(ctx context.Context, job jobs.JobSnapshot) error {
	if p.store == nil || job.Type != jobs.JobTypeProvision || job.State != jobs.JobStateFailed {
		return nil
	}
	if timedOut, _ := job.Result[jobs.GuardHeartbeatTimeoutReason].(bool); !timedOut {
		return nil
	}
	tenantID, stackID := stackKitNotificationString(job, "tenant_id"), strings.TrimSpace(job.TargetID)
	if tenantID == "" || stackID == "" {
		return nil
	}
	servers, err := p.store.ListServerRuntimesByTenant(ctx, tenantID, stackID)
	if err != nil {
		return err
	}
	for _, server := range servers {
		if !awaitingGuardHeartbeat(server) {
			continue
		}
		if err := p.failEnrollment(ctx, server, job.ID); err != nil {
			return err
		}
	}
	return nil
}

func awaitingGuardHeartbeat(server controlplane.ServerRuntime) bool {
	return serverregistry.LifecycleState(server.LifecycleState) == serverregistry.LifecycleEnrolling &&
		strings.TrimSpace(server.LifecycleReasonCode) == "awaiting_guard_heartbeat"
}

func (p guardHeartbeatTimeoutProjector) failEnrollment(ctx context.Context, server controlplane.ServerRuntime, jobID string) error {
	for attempt := 0; attempt < 3; attempt++ {
		_, err := p.store.ApplyServerEvent(ctx, controlplane.ServerEvent{
			TenantID: server.TenantID, ServerID: server.ID,
			ExpectedRevision: server.Revision, Generation: server.Generation,
			Authority: controlplane.ServerEventAuthorityControlPlane,
			Source:    guardHeartbeatTimeoutSource, SourceID: jobID, ObservedAt: time.Now().UTC(),
			Outcome:  guardHeartbeatTimeoutOutcome(server.ProviderRef, jobID),
			Evidence: map[string]any{"provision_job_id": jobID},
			Runtime: controlplane.ServerRuntime{
				LifecycleState:      string(serverregistry.LifecycleFailed),
				LifecycleReasonCode: jobs.GuardHeartbeatTimeoutReason,
			},
		})
		if err == nil || !errors.Is(err, controlplane.ErrConflict) {
			return err
		}
		// A concurrent write moved the head; act only if it still waits.
		current, getErr := p.store.GetServerRuntime(ctx, server.TenantID, server.ID)
		if getErr != nil {
			return getErr
		}
		if current == nil || !awaitingGuardHeartbeat(*current) {
			return nil
		}
		server = *current
	}
	return nil
}

func guardHeartbeatTimeoutOutcome(providerID, jobID string) *outcome.Decision {
	return &outcome.Decision{
		Status: outcome.StatusFailed, ErrorCode: "GUARD_HEARTBEAT_TIMEOUT", ReasonCode: jobs.GuardHeartbeatTimeoutReason,
		Capability: "techstack.server.connect", ProviderID: strings.TrimSpace(providerID), Retryable: true,
		UserGuidance: &outcome.Guidance{
			Title: "The server's Guard never connected",
			Body:  "The server exists, but its kombify Guard sent no authenticated heartbeat within the enrollment window. It keeps running and costing money until it connects or is decommissioned.",
			NextSteps: []outcome.Step{
				{ID: "check-guard", Label: "Check that the server is online and can reach kombify", Kind: "connect"},
				{ID: "decommission-server", Label: "Decommission the server if it is not needed", Kind: "handoff"},
			},
		},
		SupportContext: map[string]any{"provision_job_id": jobID},
	}
}
