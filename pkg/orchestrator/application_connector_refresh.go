package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
)

// RefreshApplicationConnector creates a distinct read job from the immutable
// completed setup. The existing job reservation owns retries and unknown ACKs.
func (o *Orchestrator) RefreshApplicationConnector(ctx context.Context, tenantID, ownerID, serviceID string) (string, error) {
	if o == nil || o.jobStore == nil {
		return "", ErrStackKitLifecycleUnavailable
	}
	services, ok := o.cfg.StackStore.(interface {
		controlplane.ServiceRuntimeStore
		controlplane.ServiceEventReader
	})
	if !ok {
		return "", ErrStackKitLifecycleUnavailable
	}
	head, err := services.ReadServiceEventHead(ctx, tenantID, serviceID)
	if err != nil {
		return "", err
	}
	serviceJobs, ok := o.jobStore.(controlplane.ServiceOperationJobStore)
	if !ok {
		return "", ErrStackKitLifecycleUnavailable
	}
	originalID, _ := head.Service.Metadata["application_connector_job_id"].(string)
	original, err := serviceJobs.GetServiceOperationJob(ctx, tenantID, head.Service.StackID, serviceID, originalID)
	if err != nil {
		return "", err
	}
	req, err := jobs.ApplicationConnectorRefreshRequestForStack(ctx, original, head.Service)
	if err != nil {
		return "", err
	}
	if req.TenantID != tenantID || req.OwnerID != ownerID {
		return "", controlplane.ErrNotFound
	}
	if err = o.authorizeApplicationConnector(ctx, req); err != nil {
		return "", err
	}
	competing, err := serviceJobs.FindUnresolvedServiceJob(ctx, tenantID, req.StackID, serviceID, req.DurableJobID)
	if err != nil {
		return "", err
	}
	if competing != "" {
		return "", fmt.Errorf("%w: service operation remains fenced", controlplane.ErrConflict)
	}
	// Admitting the release digests the StackKits bundle; do it only for a
	// refresh that is actually enqueued.
	if _, err = jobs.ApplicationConnectorReleaseForStack(ctx, tenantID, req.StackID, false); err != nil {
		return "", err
	}
	return o.EnqueueStackKitLifecycle(ctx, req)
}

// ReconcileApplicationConnectors runs after the existing authenticated canonical
// inventory commit. It introduces no scheduler and cannot reissue setup. It
// runs on every Guard inventory, so it reads the cached release admission:
// resolving the release afresh costs seconds of CPU per call.
func (o *Orchestrator) ReconcileApplicationConnectors(ctx context.Context, tenantID, ownerID, stackID, serverID string) {
	if o == nil {
		return
	}
	if _, err := jobs.ApplicationConnectorReleaseForStack(ctx, tenantID, stackID, true); err != nil {
		return
	}
	services, ok := o.cfg.StackStore.(controlplane.ServiceRuntimeStore)
	if !ok {
		return
	}
	rows, err := services.ListServiceRuntimes(ctx, tenantID, stackID, serverID)
	if err != nil {
		return
	}
	for _, service := range rows {
		projection, _ := service.Metadata["application_connector"].(map[string]any)
		if projection["status"] != "active" {
			continue
		}
		var verified time.Time
		switch stamp := projection["verified_at"].(type) {
		case time.Time:
			verified = stamp
		case string:
			verified, _ = time.Parse(time.RFC3339Nano, stamp)
		}
		if !verified.IsZero() && !verified.After(time.Now().Add(time.Minute)) && time.Since(verified) <= runtimehealth.FreshHeartbeatWindow {
			continue
		}
		if _, err = o.RefreshApplicationConnector(ctx, tenantID, ownerID, service.ID); err != nil {
			o.log.Warn("application_connector_refresh_not_admitted", "service_id", service.ID)
		}
	}
}
