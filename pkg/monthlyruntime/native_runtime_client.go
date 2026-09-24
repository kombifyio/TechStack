package monthlyruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

var (
	ErrNativeRuntimeObservationUnavailable = errors.New("monthlyruntime: native runtime observation unavailable")
	ErrNativeRuntimeActionUnsupported      = errors.New("monthlyruntime: native runtime action unsupported")
)

const NativeRuntimeActionUnsupportedErrorCode = "managed_runtime_action_unsupported"

type NativeRuntimeActionUnsupportedError struct {
	Action serverruntime.RuntimeAction
}

func (e *NativeRuntimeActionUnsupportedError) Error() string {
	return fmt.Sprintf("%s: %s", ErrNativeRuntimeActionUnsupported, e.Action)
}

func (e *NativeRuntimeActionUnsupportedError) Unwrap() error {
	return ErrNativeRuntimeActionUnsupported
}

// NativeRuntimeActionUnsupportedDetails preserves actionable, stable guidance
// when an action has no configured native executor in this deployment. Start,
// stop and SSH enable/disable are served by the provider-control power
// controller and the execution-channel SSH controller; this envelope is only
// reached when that composition is absent, so nothing was dispatched.
func NativeRuntimeActionUnsupportedDetails(err error) map[string]any {
	action := serverruntime.RuntimeAction("")
	var unsupported *NativeRuntimeActionUnsupportedError
	if errors.As(err, &unsupported) && unsupported != nil {
		action = unsupported.Action
	}
	reasonCode := "native_action_not_implemented"
	title := "This managed-server action is not available"
	body := "Techstack did not dispatch a provider mutation. Refresh the server state or choose a supported lifecycle action."
	retryable := false
	nextSteps := []string{
		"Refresh the managed server to see its current provider and Guard state.",
		"Use decommission only when you intend to permanently remove the managed server.",
	}
	switch action {
	case serverruntime.RuntimeActionStop, serverruntime.RuntimeActionStart:
		reasonCode = "managed_power_control_unavailable"
		title = "Managed power control is not available"
		body = "This Techstack deployment has no provider-control power executor configured. No provider or node change was made."
	case serverruntime.RuntimeActionEnableSSH, serverruntime.RuntimeActionDisableSSH:
		reasonCode = "managed_ssh_access_unavailable"
		title = "Managed SSH access control is not available"
		body = "This Techstack deployment has no execution-channel SSH controller configured. No node change was made."
	case serverruntime.RuntimeActionSSHInfo:
		reasonCode = "managed_ssh_custody_pending"
		title = "Managed SSH access is not ready"
		body = "The exact lease has no provider-controlled SSH target in custody yet. No credential or provider mutation was exposed."
		retryable = true
		nextSteps = []string{
			"Refresh the managed server after provider provisioning and Guard inventory have converged.",
			"If access remains unavailable, use the lease ID to inspect its provider operation and credential-custody evidence.",
		}
	}
	return map[string]any{
		"error_code":  NativeRuntimeActionUnsupportedErrorCode,
		"reason_code": reasonCode,
		"action":      string(action),
		"retryable":   retryable,
		"user_guidance": map[string]any{
			"title":      title,
			"body":       body,
			"next_steps": nextSteps,
		},
	}
}

type NativeRuntimeClient struct {
	Servers controlplane.ServerRuntimeStore
}

func (client *NativeRuntimeClient) validateRuntimeAction(action serverruntime.RuntimeAction) error {
	if action != serverruntime.RuntimeActionStatus {
		return &NativeRuntimeActionUnsupportedError{Action: action}
	}
	return nil
}

func (client *NativeRuntimeClient) enrollmentAuthoritative() bool {
	return client != nil && client.Servers != nil
}

// RuntimeAction serves status from the native provider-bound server aggregate.
// It never mutates: start/stop and SSH changes are served by the Service's
// power and SSH controllers, which the Service dispatches before this client.
func (client *NativeRuntimeClient) RuntimeAction(ctx context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	if err := client.validateRuntimeAction(req.Action); err != nil {
		return nil, err
	}
	if client == nil || client.Servers == nil {
		return nil, fmt.Errorf("%w: server registry is not configured", ErrNativeRuntimeObservationUnavailable)
	}
	tenantID := strings.TrimSpace(req.TenantID)
	leaseID := strings.TrimSpace(req.LeaseID)
	if tenantID == "" || leaseID == "" {
		return nil, fmt.Errorf("%w: tenant and lease are required", ErrNativeRuntimeObservationUnavailable)
	}
	serverID := runtimeidentity.LeaseServerID(leaseID)
	server, err := client.Servers.GetServerRuntime(ctx, tenantID, serverID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNativeRuntimeObservationUnavailable, err)
	}
	if server == nil || server.TenantID != tenantID || server.LeaseID != leaseID || server.ID != serverID {
		return nil, fmt.Errorf("%w: server identity does not match the lease", ErrNativeRuntimeObservationUnavailable)
	}
	providerID := normalizeNativeRuntimeProvider(server.ProviderRef)
	if providerID == "" {
		return nil, fmt.Errorf("%w: provider projection is unsupported", ErrNativeRuntimeObservationUnavailable)
	}
	if ownerID := strings.TrimSpace(req.OwnerID); ownerID != "" && strings.TrimSpace(server.OwnerSubjectID) != ownerID {
		return nil, fmt.Errorf("%w: owner projection does not match the lease", ErrNativeRuntimeObservationUnavailable)
	}

	observedState, reasonCode := nativeObservedState(*server)
	publicIP := firstNativeRuntimeMetadata(req.Metadata, "runtime_public_ip", "public_ip", "ssh_host")
	return &serverruntime.LeaseRuntimeActionResponse{
		SSHEnabled: ownerSSHAccessStateEnabled(server.Metadata),
		TenantID:   tenantID, LeaseID: leaseID, Action: req.Action, OfferingID: req.OfferingID,
		ProviderID: providerID, DesiredState: strings.TrimSpace(server.DesiredState),
		ObservedState: observedState, LeaseState: strings.TrimSpace(server.LifecycleState),
		Status: &serverruntime.NodeStatus{
			ID: server.ID, State: observedState, PublicIP: publicIP,
			UpdatedAt: server.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		},
		Metadata: map[string]string{
			"runtime_reason_code": reasonCode,
			"connection_state":    strings.TrimSpace(server.ConnectionState),
			"health_state":        strings.TrimSpace(server.HealthState),
			"inventory_revision":  fmt.Sprintf("%d", server.InventoryRevision),
		},
	}, nil
}

func normalizeNativeRuntimeProvider(providerRef string) string {
	providerRef = strings.ToLower(strings.TrimSpace(providerRef))
	if index := strings.IndexByte(providerRef, ':'); index >= 0 {
		providerRef = providerRef[:index]
	}
	switch providerRef {
	case "ionos", "ionos-managed":
		return "ionos"
	case "centron", "centron-managed":
		return "centron"
	default:
		return ""
	}
}

func nativeObservedState(server controlplane.ServerRuntime) (string, string) {
	lifecycle := strings.ToLower(strings.TrimSpace(server.LifecycleState))
	connection := strings.ToLower(strings.TrimSpace(server.ConnectionState))
	health := strings.ToLower(strings.TrimSpace(server.HealthState))
	if lifecycle == "decommissioned" || lifecycle == "absent" {
		return runtimeObservedStateNotFound, "provider_resource_absent"
	}
	if connection == "connected" && health == "healthy" && (lifecycle == "active" || lifecycle == "running") {
		return "running", "provider_and_guard_healthy"
	}
	if connection == "connected" && health != "" && health != "unknown" {
		return health, "guard_health_" + health
	}
	if connection != "" && connection != "unknown" {
		return connection, "guard_connection_" + connection
	}
	if lifecycle != "" {
		return lifecycle, "provider_lifecycle_" + lifecycle
	}
	return "unknown", "runtime_observation_unknown"
}

func firstNativeRuntimeMetadata(metadata map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			return value
		}
	}
	return ""
}
