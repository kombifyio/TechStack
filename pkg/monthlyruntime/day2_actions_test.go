package monthlyruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

type countingPowerController struct {
	requests []PowerRequest
	latest   *PowerOperation
}

func (c *countingPowerController) RequestPower(_ context.Context, req PowerRequest) (PowerOperation, error) {
	c.requests = append(c.requests, req)
	operation := PowerOperation{OperationID: "op-" + string(req.State), DesiredPowerState: req.State, Status: "pending", Phase: "resources_bound"}
	c.latest = &operation
	return operation, nil
}

func (c *countingPowerController) LatestPower(context.Context, string, string) (*PowerOperation, error) {
	return c.latest, nil
}

type countingSSHAccess struct{ requests []SSHAccessRequest }

func (c *countingSSHAccess) SetOwnerSSHAccess(_ context.Context, req SSHAccessRequest) (SSHAccessResult, error) {
	c.requests = append(c.requests, req)
	return SSHAccessResult{Enabled: req.Enabled}, nil
}

type countingReconnector struct{ restarts int }

func (c *countingReconnector) RestartAgent(context.Context, AgentReconnectRequest) error {
	c.restarts++
	return nil
}

// Start/stop and SSH changes are cost-bearing or access-changing provider and
// node mutations: without positive entitlement they must be refused before
// the power or SSH controller is reached and before the lease changes.
func TestServiceDay2MutationsFailClosedWithoutEntitlement(t *testing.T) {
	for _, action := range []serverruntime.RuntimeAction{
		serverruntime.RuntimeActionStart, serverruntime.RuntimeActionStop,
		serverruntime.RuntimeActionEnableSSH, serverruntime.RuntimeActionDisableSSH,
	} {
		t.Run(string(action), func(t *testing.T) {
			leases := newLeaseServiceWith(t, time.Now().UTC(), EnrollmentStatusEnrolled)
			power, ssh := &countingPowerController{}, &countingSSHAccess{}
			svc := &Service{
				Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{},
				Features: fakeFeatureChecker{enabled: false}, Power: power, SSHAccess: ssh,
			}
			_, err := svc.Action(t.Context(), ActionRequest{
				TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: action,
			})
			if !errors.Is(err, ErrFeatureDisabled) {
				t.Fatalf("Action error = %v, want entitlement denial", err)
			}
			if len(power.requests) != 0 || len(ssh.requests) != 0 {
				t.Fatalf("denied %s reached a controller: power=%v ssh=%v", action, power.requests, ssh.requests)
			}
			stored, err := leases.Get(t.Context(), "org-1", "lease-1")
			if err != nil || stored.DesiredState != vmlease.DesiredStateRunning {
				t.Fatalf("denied %s changed the lease: %+v err=%v", action, stored, err)
			}
		})
	}
}

// A stopped managed runtime stays under provider-control custody: the owner
// still reads its status and can start it again. Live on 0.19.2 the stop
// moved the lease out of custody, so status and start answered 409.
func TestServiceStoppedRuntimeStaysReadableAndStartable(t *testing.T) {
	leases := newLeaseServiceWith(t, time.Now().UTC(), EnrollmentStatusEnrolled)
	power := &countingPowerController{}
	svc := &Service{
		Leases: nativeLeaseService(leases), Runtime: &fakeRuntimeClient{},
		Features: fakeFeatureChecker{enabled: true}, Power: power,
	}
	act := func(action serverruntime.RuntimeAction) *RuntimeResponse {
		t.Helper()
		resp, err := svc.Action(t.Context(), ActionRequest{
			TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1", Action: action,
		})
		if err != nil {
			t.Fatalf("%s after stop: %v", action, err)
		}
		return resp
	}
	if stop := act(serverruntime.RuntimeActionStop); stop.Power == nil || stop.ObservedState != "stopping" {
		t.Fatalf("stop response = %+v", stop)
	}
	if status := act(serverruntime.RuntimeActionStatus); status.Power == nil || status.ObservedState != "stopping" {
		t.Fatalf("status while stopping = %+v", status)
	}
	act(serverruntime.RuntimeActionStart)
	if len(power.requests) != 2 || power.requests[0].State != PowerStateStopped || power.requests[1].State != PowerStateRunning {
		t.Fatalf("power requests = %+v", power.requests)
	}
}

// Reconnect restarts Guard only for an entitled owner of a running server and
// only when the aggregate cannot already prove a connection.
func TestServiceReconnectRestartsGuardOnlyWhenNeededAndAdmitted(t *testing.T) {
	previousWindow, previousInterval := reconnectObservationWindow, reconnectPollInterval
	reconnectObservationWindow, reconnectPollInterval = 50*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { reconnectObservationWindow, reconnectPollInterval = previousWindow, previousInterval })

	offline := &serverruntime.LeaseRuntimeActionResponse{ObservedState: "offline", Metadata: map[string]string{"connection_state": "offline"}}
	connected := &serverruntime.LeaseRuntimeActionResponse{ObservedState: "running", Metadata: map[string]string{"connection_state": "connected"}}
	for _, test := range []struct {
		name         string
		entitled     bool
		stopped      bool
		wantErr      error
		wantRestarts int
	}{
		{name: "entitled offline runtime is restarted and re-proven", entitled: true, wantRestarts: 1},
		{name: "unentitled owner is refused before the node", entitled: false, wantErr: ErrFeatureDisabled},
		{name: "stopped runtime is refused before the node", entitled: true, stopped: true, wantErr: ErrRuntimeStopped},
	} {
		t.Run(test.name, func(t *testing.T) {
			leases := newLeaseServiceWith(t, time.Now().UTC(), EnrollmentStatusEnrolled)
			power := &countingPowerController{}
			if test.stopped {
				power.latest = &PowerOperation{DesiredPowerState: PowerStateStopped, Status: "succeeded"}
			}
			reconnector := &countingReconnector{}
			probes := 0
			runtime := &fakeRuntimeClient{response: offline}
			runtime.onAction = func(serverruntime.LeaseRuntimeActionRequest) error {
				probes++
				if reconnector.restarts > 0 {
					runtime.response = connected
				}
				return nil
			}
			svc := &Service{
				Leases: nativeLeaseService(leases), Runtime: runtime,
				Features: fakeFeatureChecker{enabled: test.entitled}, Reconnector: reconnector, Power: power,
			}
			resp, err := svc.Reconnect(t.Context(), ActionRequest{TenantID: "org-1", UserID: "user-1", LeaseID: "lease-1"})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Reconnect error = %v, want %v", err, test.wantErr)
			}
			if reconnector.restarts != test.wantRestarts {
				t.Fatalf("guard restarts = %d, want %d", reconnector.restarts, test.wantRestarts)
			}
			if test.wantErr == nil && (resp.Reconnect == nil || !resp.Reconnect.AgentRestarted || probes < 2) {
				t.Fatalf("reconnect response = %+v probes=%d", resp, probes)
			}
		})
	}
}
