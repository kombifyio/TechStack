package routes

import (
	"slices"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// TestServerNodeActionsAdvertiseOnlyWhatTheStateAdmits is the capability
// contract of the canonical server read model. Each row is one real node state;
// the expectation is the exact advertised set, because "advertised" and
// "invocable" have to be the same thing.
func TestServerNodeActionsAdvertiseOnlyWhatTheStateAdmits(t *testing.T) {
	for _, test := range []struct {
		name       string
		server     controlplane.ServerRuntime
		detachable bool
		want       []string
	}{
		{
			name: "enrolling node without an agent can only be connected",
			server: controlplane.ServerRuntime{
				LifecycleState: string(serverregistry.LifecycleEnrolling),
			},
			detachable: true,
			want:       []string{serverActionConnect},
		},
		{
			name: "active managed node offers the full operable set",
			server: controlplane.ServerRuntime{
				LifecycleState:  string(serverregistry.LifecycleActive),
				ConnectionState: string(serverregistry.ConnectionConnected),
				WorkerID:        "agent-1", LeaseID: "lease-1", ProviderRef: "ionos",
			},
			detachable: true,
			want: []string{
				serverActionDecommission, serverActionRotateCredentials,
				serverActionSSHDisable, serverActionStop, serverActionTerminal,
			},
		},
		{
			name: "managed node with SSH turned off offers only enabling it",
			server: controlplane.ServerRuntime{
				LifecycleState:  string(serverregistry.LifecycleActive),
				ConnectionState: string(serverregistry.ConnectionConnected),
				WorkerID:        "agent-1", LeaseID: "lease-1", ProviderRef: "centron",
				Metadata: map[string]any{"owner_ssh_access": map[string]any{"state": "disabled"}},
			},
			want: []string{
				serverActionDecommission, serverActionRotateCredentials,
				serverActionSSHEnable, serverActionStop, serverActionTerminal,
			},
		},
		{
			name: "stopped managed node offers start and decommission only",
			server: controlplane.ServerRuntime{
				LifecycleState:  string(serverregistry.LifecycleActive),
				DesiredState:    string(serverregistry.DesiredStopped),
				ConnectionState: string(serverregistry.ConnectionOffline),
				WorkerID:        "agent-1", LeaseID: "lease-1", ProviderRef: "ionos",
			},
			want: []string{serverActionDecommission, serverActionRotateCredentials, serverActionStart},
		},
		{
			name: "active customer-operated Hostinger VPS offers detach, not decommission",
			server: controlplane.ServerRuntime{
				LifecycleState:  string(serverregistry.LifecycleActive),
				ConnectionState: string(serverregistry.ConnectionConnected),
				WorkerID:        "agent-1",
				ProviderRef:     "hostinger-vps:srv1161760",
				RuntimeTarget: func() serverregistry.RuntimeTarget {
					target, ok := serverregistry.HostingerExternalVPSTarget(
						"hostinger-vps:srv1161760",
						time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC),
					)
					if !ok {
						panic("hostinger target")
					}
					return target
				}(),
			},
			detachable: true,
			want: []string{
				serverActionDetach, serverActionRotateCredentials, serverActionTerminal,
			},
		},
		{
			name: "active but disconnected node offers reconnect instead of a terminal",
			server: controlplane.ServerRuntime{
				LifecycleState:  string(serverregistry.LifecycleActive),
				ConnectionState: string(serverregistry.ConnectionOffline),
				WorkerID:        "agent-1", LeaseID: "lease-1", ProviderRef: "centron",
			},
			want: []string{
				serverActionDecommission, serverActionReconnect, serverActionRotateCredentials,
				serverActionStop,
			},
		},
		{
			name: "a node whose detach handler is not registered never advertises detach",
			server: controlplane.ServerRuntime{
				LifecycleState:  string(serverregistry.LifecycleActive),
				ConnectionState: string(serverregistry.ConnectionConnected),
				WorkerID:        "agent-1",
			},
			want: []string{serverActionRotateCredentials, serverActionTerminal},
		},
		{
			name: "decommissioning node only offers custody resolution",
			server: controlplane.ServerRuntime{
				LifecycleState:  string(serverregistry.LifecycleDecommissioning),
				ConnectionState: string(serverregistry.ConnectionConnected),
				WorkerID:        "agent-1", LeaseID: "lease-1",
			},
			detachable: true,
			want:       []string{serverActionResolveCustody},
		},
		{
			name: "an unknown lifecycle advertises nothing rather than guessing",
			server: controlplane.ServerRuntime{
				LifecycleState:  "quiescing",
				ConnectionState: string(serverregistry.ConnectionConnected),
				WorkerID:        "agent-1", LeaseID: "lease-1",
			},
			detachable: true,
			want:       []string{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := serverNodeActions(test.server, test.detachable)
			if !slices.Equal(got, test.want) {
				t.Fatalf("allowed_actions = %#v, want %#v", got, test.want)
			}
			if slices.Contains(got, "logs") {
				t.Fatal("node logs were advertised without an endpoint behind them")
			}
		})
	}
}

// TestServerStackActionsFollowTheObservedKit keeps the StackKit scope separate
// from the node scope and refuses to offer a kit operation on a node that
// cannot execute one.
func TestServerStackActionsFollowTheObservedKit(t *testing.T) {
	operable := controlplane.ServerRuntime{
		LifecycleState:  string(serverregistry.LifecycleActive),
		ConnectionState: string(serverregistry.ConnectionConnected),
		WorkerID:        "agent-1",
	}
	if got := serverStackActions(operable); !slices.Equal(got, []string{serverStackActionPlan}) {
		t.Fatalf("kit-less node stack_actions = %#v, want plan only", got)
	}

	withKit := operable
	withKit.Metadata = map[string]any{"stackkit": "family-lab"}
	got := serverStackActions(withKit)
	for _, want := range []string{
		serverStackActionPlan, serverStackActionVerify, serverStackActionApply,
		serverStackActionUpgrade, serverStackActionDriftDetect, serverStackActionDriftReconcile,
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("stack_actions = %#v, missing %q", got, want)
		}
	}

	disconnected := withKit
	disconnected.ConnectionState = string(serverregistry.ConnectionOffline)
	if got := serverStackActions(disconnected); len(got) != 0 {
		t.Fatalf("disconnected node stack_actions = %#v, want none", got)
	}
}
