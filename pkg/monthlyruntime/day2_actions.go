package monthlyruntime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

// OwnerSSHAccessMetadataKey holds the generation-bound owner SSH grant on
// the canonical RuntimeServer aggregate metadata.
const OwnerSSHAccessMetadataKey = "owner_ssh_access"

// OperationStatusReconnected records a reconnect that restarted Guard and
// re-proved its control-plane connection.
const OperationStatusReconnected = "reconnected"

// ReconnectOutcome reports what a reconnect did.
type ReconnectOutcome struct {
	AgentRestarted  bool      `json:"agent_restarted"`
	ConnectionState string    `json:"connection_state,omitempty"`
	ObservedAt      time.Time `json:"observed_at"`
}

// Reconnect observation bounds. Guard re-registers within seconds of a
// restart; guard restart plus the window stay below the 100s edge timeout.
var (
	reconnectObservationWindow = 60 * time.Second
	reconnectPollInterval      = 5 * time.Second
)

func (s *Service) day2ActionConfigured(action serverruntime.RuntimeAction) bool {
	if s == nil {
		return false
	}
	switch action {
	case serverruntime.RuntimeActionStart, serverruntime.RuntimeActionStop:
		return s.Power != nil
	case serverruntime.RuntimeActionEnableSSH, serverruntime.RuntimeActionDisableSSH:
		return s.SSHAccess != nil
	default:
		return false
	}
}

// day2Action serves start/stop and SSH enable/disable through their native
// controllers. Authorization, lease validation, entitlement and demo guards
// already ran in prepareAction.
func (s *Service) day2Action(ctx context.Context, req ActionRequest, prepared *preparedRuntimeAction) (*RuntimeResponse, bool, error) {
	if !s.day2ActionConfigured(req.Action) {
		return nil, false, nil
	}
	if req.Action == serverruntime.RuntimeActionStop && !req.Internal && demoguard.IsProtectedLease(string(prepared.lease.ID)) {
		return nil, true, ErrDemoRestricted
	}
	switch req.Action {
	case serverruntime.RuntimeActionStart, serverruntime.RuntimeActionStop:
		resp, err := s.executePowerAction(ctx, req, prepared)
		return resp, true, err
	default:
		resp, err := s.executeSSHAccessAction(ctx, req, prepared)
		return resp, true, err
	}
}

func (s *Service) executePowerAction(ctx context.Context, req ActionRequest, prepared *preparedRuntimeAction) (*RuntimeResponse, error) {
	state := PowerStateRunning
	if req.Action == serverruntime.RuntimeActionStop {
		state = PowerStateStopped
	}
	actor := strings.TrimSpace(req.UserID)
	lease := prepared.lease
	operation, err := s.Power.RequestPower(ctx, PowerRequest{
		TenantID: prepared.tenantID, OwnerID: strings.TrimSpace(lease.Subject.ID),
		LeaseID: string(lease.ID), State: state,
	})
	if err != nil {
		s.recordDay2Operation(ctx, prepared.tenantID, lease.ID, actor, vmleases.OperationStatusFailed, err.Error())
		return nil, err
	}
	// Power intent lives in the provider-control ledger and the canonical
	// server, never in the lease desired state: the rental, its capacity and
	// billing continue while the server is off, and a lease that is not
	// running leaves active provider-control custody, which would refuse
	// status and the next start.
	status := vmleases.OperationStatusPending
	detail := ""
	switch {
	case operation.Converged() && state == PowerStateRunning:
		status = vmleases.OperationStatusStarted
	case operation.Converged():
		status = vmleases.OperationStatusStopped
	case operation.Failed():
		status, detail = vmleases.OperationStatusFailed, "power reconcile failed: "+operation.ReasonCode
	}
	s.recordDay2Operation(ctx, prepared.tenantID, lease.ID, actor, status, detail)
	logRuntimeAction(prepared.tenantID, lease.ID, actor, req.Action, nil, nil)
	return &RuntimeResponse{
		TenantID: prepared.tenantID, LeaseID: string(lease.ID), Action: req.Action,
		RuntimeOfferingID: prepared.offeringID, DesiredState: string(state),
		ObservedState: powerObservedState(operation), LeaseState: "valid",
		EnrollmentStatus: s.day2EnrollmentStatus(*lease),
		Power:            &operation,
	}, nil
}

// runtimePowerStopped reports whether the durable power ledger holds the
// runtime stopped or stopping.
func (s *Service) runtimePowerStopped(ctx context.Context, tenantID string, lease vmlease.Lease) bool {
	if lease.DesiredState == vmlease.DesiredStateStopped {
		return true
	}
	if s == nil || s.Power == nil {
		return false
	}
	operation, err := s.Power.LatestPower(ctx, tenantID, string(lease.ID))
	return err == nil && operation != nil && operation.DesiredPowerState == PowerStateStopped && !operation.Failed()
}

// day2EnrollmentStatus reports enrollment for a Day-2 response. The power and
// SSH controllers admit only active canonical servers, and a native lease's
// legacy enrollment metadata is frozen at cutover.
func (s *Service) day2EnrollmentStatus(lease vmlease.Lease) string {
	if s.canonicalEnrollment() {
		return enrollmentStatusEnrolled
	}
	return strings.TrimSpace(lease.Metadata["runtime_enrollment_status"])
}

func powerObservedState(operation PowerOperation) string {
	switch {
	case operation.Converged():
		return string(operation.DesiredPowerState)
	case operation.Failed():
		return "power_failed"
	case operation.DesiredPowerState == PowerStateStopped:
		return "stopping"
	default:
		return "starting"
	}
}

func (s *Service) executeSSHAccessAction(ctx context.Context, req ActionRequest, prepared *preparedRuntimeAction) (*RuntimeResponse, error) {
	lease := prepared.lease
	if s.runtimePowerStopped(ctx, prepared.tenantID, *lease) {
		return nil, ErrRuntimeStopped
	}
	enabled := req.Action == serverruntime.RuntimeActionEnableSSH
	actor := strings.TrimSpace(req.UserID)
	result, err := s.SSHAccess.SetOwnerSSHAccess(ctx, SSHAccessRequest{
		TenantID: prepared.tenantID, OwnerID: strings.TrimSpace(lease.Subject.ID), LeaseID: string(lease.ID),
		ResourceGenerationID: vmleases.ResourceGenerationID(*lease), Actor: actor, Enabled: enabled,
	})
	if err != nil {
		s.recordDay2Operation(ctx, prepared.tenantID, lease.ID, actor, vmleases.OperationStatusFailed, err.Error())
		return nil, err
	}
	status := vmleases.OperationStatusSSHDisabled
	if enabled {
		status = vmleases.OperationStatusSSHEnabled
	}
	s.recordDay2Operation(ctx, prepared.tenantID, lease.ID, actor, status, "")
	return &RuntimeResponse{
		TenantID: prepared.tenantID, LeaseID: string(lease.ID), Action: req.Action,
		RuntimeOfferingID: prepared.offeringID, DesiredState: string(lease.DesiredState),
		ObservedState: firstNonEmpty(lease.Metadata["runtime_observed_state"], "running"), LeaseState: "valid",
		EnrollmentStatus: s.day2EnrollmentStatus(*lease),
		SSHEnabled:       result.Enabled, SSHAccess: &result,
	}, nil
}

func (s *Service) recordDay2Operation(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, actor, status, detail string) {
	recorder, ok := s.Leases.(OperationRecorder)
	if !ok {
		return
	}
	_ = recorder.RecordOperation(ctx, vmleases.OperationEvent{
		TenantID: tenantID, LeaseID: leaseID, EventType: vmleases.OperationEventRuntimeAction,
		Status: status, Actor: actor, Error: detail,
	})
}

func (s *Service) attachLatestPower(ctx context.Context, tenantID string, resp *RuntimeResponse) {
	if s == nil || s.Power == nil || resp == nil {
		return
	}
	operation, err := s.Power.LatestPower(ctx, tenantID, resp.LeaseID)
	if err != nil || operation == nil {
		return
	}
	resp.Power = operation
	// An intentionally stopped or transitioning server reports its power
	// state; Guard's connection only describes a server that should run.
	if !operation.Failed() && (operation.DesiredPowerState == PowerStateStopped || !operation.Converged()) {
		resp.ObservedState = powerObservedState(*operation)
		if resp.Status != nil {
			resp.Status.State = resp.ObservedState
		}
	}
}

// ownerSSHAccessEnabled reads the generation-bound grant from the canonical
// server. A missing record keeps the historical default of enabled.
func (s *Service) ownerSSHAccessEnabled(ctx context.Context, tenantID, leaseID string) bool {
	native, ok := s.Runtime.(*NativeRuntimeClient)
	if !ok || native == nil || native.Servers == nil {
		return true
	}
	server, err := native.Servers.GetServerRuntime(ctx, tenantID, runtimeidentity.LeaseServerID(leaseID))
	if err != nil || server == nil {
		return true
	}
	return ownerSSHAccessStateEnabled(server.Metadata)
}

func ownerSSHAccessStateEnabled(metadata map[string]any) bool {
	raw, ok := metadata[OwnerSSHAccessMetadataKey]
	if !ok || raw == nil {
		return true
	}
	var grant struct {
		State string `json:"state"`
	}
	switch value := raw.(type) {
	case string:
		_ = json.Unmarshal([]byte(value), &grant)
	case []byte:
		_ = json.Unmarshal(value, &grant)
	default:
		if encoded, err := json.Marshal(value); err == nil {
			_ = json.Unmarshal(encoded, &grant)
		}
	}
	return grant.State != "disabled"
}

// awaitReconnect re-probes the aggregate until Guard proves a connection or
// the bounded observation window closes.
func (s *Service) awaitReconnect(ctx context.Context, probe serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	deadline := time.Now().Add(reconnectObservationWindow)
	for {
		timer := time.NewTimer(reconnectPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		resp, err := s.Runtime.RuntimeAction(ctx, probe)
		if err == nil && reconnectProvesEnrollment(resp) {
			return resp, nil
		}
		if !time.Now().Before(deadline) {
			return nil, ErrEnrollmentPending
		}
	}
}
