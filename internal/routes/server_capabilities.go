package routes

import (
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/executionchannel"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// Node action vocabulary of the canonical server read model. Every id maps to
// exactly one endpoint that exists today:
//
//	connect            POST /v1/ril/servers/connect
//	terminal           POST /api/v1/servers/{id}/terminal-sessions
//	rotate_credentials POST /v1/ril/servers/{id}/credential/rotate
//	detach             POST /api/v1/servers/{id}/detach
//	reconnect          POST /api/v1/monthly-runtimes/{leaseId}/reconnect
//	start              POST /api/v1/monthly-runtimes/{leaseId}/start
//	stop               POST /api/v1/monthly-runtimes/{leaseId}/stop
//	ssh_enable         POST /api/v1/monthly-runtimes/{leaseId}/enable-ssh
//	ssh_disable        POST /api/v1/monthly-runtimes/{leaseId}/disable-ssh
//	decommission       POST /api/v1/monthly-runtimes/{leaseId}/decommission
//	resolve_custody    POST /api/v1/monthly-runtimes/{leaseId}/resolve-custody
//
// There is deliberately no `logs` id: no node-log endpoint exists, and a read
// model that advertised one would be lying. The lease-scoped ids target
// `provider.lease_id`, which the same response already carries.
const (
	serverActionConnect           = "connect"
	serverActionTerminal          = "terminal"
	serverActionReconnect         = "reconnect"
	serverActionStart             = "start"
	serverActionStop              = "stop"
	serverActionRotateCredentials = "rotate_credentials"
	serverActionSSHEnable         = "ssh_enable"
	serverActionSSHDisable        = "ssh_disable"
	serverActionDetach            = "detach"
	serverActionDecommission      = "decommission"
	serverActionResolveCustody    = "resolve_custody"
)

// Stack action vocabulary. All of these route to
// POST /api/v1/stacks/{id}/stackkit/operations and are scoped to the StackKit
// deployment on this node, never to the node itself.
const (
	serverStackActionPlan           = "plan"
	serverStackActionApply          = "apply"
	serverStackActionVerify         = "verify"
	serverStackActionUpgrade        = "upgrade"
	serverStackActionDriftDetect    = "drift_detect"
	serverStackActionDriftReconcile = "drift_reconcile"
)

// serverNodeActions is what an authorized owner may currently invoke against
// this node. It replaces the dashboard-side heuristics: the backend owns which
// operations its own state admits, and an unknown state advertises nothing
// rather than guessing that a mutation is safe.
//
// detachable reflects a route-level dependency (the detacher is optional in the
// composition root), so an action whose handler is not even registered is never
// advertised.
func serverNodeActions(server controlplane.ServerRuntime, detachable bool) []string {
	lifecycle := serverregistry.LifecycleState(strings.TrimSpace(server.LifecycleState))
	connected := serverregistry.MutationsAllowed(server.ConnectionState)
	bound := strings.TrimSpace(server.WorkerID) != ""
	lease := strings.TrimSpace(server.LeaseID) != ""
	actions := make([]string, 0, 8)

	switch lifecycle {
	case serverregistry.LifecyclePlanned, serverregistry.LifecycleProvisioning, serverregistry.LifecycleEnrolling:
		// The node is still being brought up. Enrolment is the only thing an
		// owner can usefully do to it.
		if !bound {
			actions = append(actions, serverActionConnect)
		}
	case serverregistry.LifecycleActive:
		stopped := serverregistry.DesiredState(strings.TrimSpace(server.DesiredState)) == serverregistry.DesiredStopped
		if !bound {
			actions = append(actions, serverActionConnect)
		}
		if bound && connected && !stopped {
			actions = append(actions, serverActionTerminal)
		}
		if bound {
			actions = append(actions, serverActionRotateCredentials)
		}
		if lease && !connected && !stopped {
			actions = append(actions, serverActionReconnect)
		}
		managed := lease && managedPowerProvider(server.ProviderRef)
		if lease && stopped && !managed {
			actions = append(actions, serverActionDecommission)
		}
		if managed && stopped {
			// An intentionally stopped managed server can be started or
			// removed; node-channel actions need it running.
			actions = append(actions, serverActionStart, serverActionDecommission)
		}
		if lease && !stopped && !managed {
			actions = append(actions, serverActionDecommission)
		}
		if managed && !stopped {
			// The durable owner SSH grant selects the one direction that
			// changes something; both require the node to be reachable.
			if connected {
				if executionchannel.OwnerSSHAccessFromMetadata(server.Metadata).Enabled() {
					actions = append(actions, serverActionSSHDisable)
				} else {
					actions = append(actions, serverActionSSHEnable)
				}
			}
			actions = append(actions, serverActionStop, serverActionDecommission)
		}
	case serverregistry.LifecycleFailed:
		if lease {
			actions = append(actions, serverActionDecommission)
		}
	case serverregistry.LifecycleDecommissioning, serverregistry.LifecycleDecommissioned:
		// Retirement is under way. Only custody resolution moves it forward;
		// offering terminal or credential rotation here would be a dead button.
		if lease {
			actions = append(actions, serverActionResolveCustody)
		}
	}
	if detachable && bound && controlplane.SelfOwnedServerDetachAllowed(server) {
		switch lifecycle {
		case serverregistry.LifecyclePlanned, serverregistry.LifecycleProvisioning,
			serverregistry.LifecycleEnrolling, serverregistry.LifecycleActive,
			serverregistry.LifecycleFailed:
			actions = append(actions, serverActionDetach)
		}
	}
	sort.Strings(actions)
	return actions
}

// managedPowerProvider reports whether the server's provider has native
// start/stop and SSH-grant executors (the managed IONOS and centron lanes).
func managedPowerProvider(providerRef string) bool {
	provider := strings.ToLower(strings.TrimSpace(providerRef))
	if index := strings.IndexByte(provider, ':'); index >= 0 {
		provider = provider[:index]
	}
	switch provider {
	case "ionos", "ionos-managed", "centron", "centron-managed":
		return true
	default:
		return false
	}
}

// serverStackActions is what the StackKit deployment on this node admits. It
// reproduces the contract the dashboard heuristic expressed, moved to the
// authority that actually knows the state: without an observed kit only the
// read-only preview is offered, and a node that cannot execute anything offers
// nothing at all.
func serverStackActions(server controlplane.ServerRuntime) []string {
	if stringFromAnyMap(server.Metadata, "server_node_role") == "substrate" {
		return []string{}
	}
	lifecycle := serverregistry.LifecycleState(strings.TrimSpace(server.LifecycleState))
	if lifecycle != serverregistry.LifecycleActive ||
		strings.TrimSpace(server.WorkerID) == "" ||
		!serverregistry.MutationsAllowed(server.ConnectionState) {
		return []string{}
	}
	if strings.TrimSpace(stringFromAnyMap(server.Metadata, "stackkit")) == "" {
		// A node with no observed kit can be planned against, nothing more.
		return []string{serverStackActionPlan}
	}
	actions := []string{
		serverStackActionPlan, serverStackActionVerify, serverStackActionApply,
		serverStackActionUpgrade, serverStackActionDriftDetect, serverStackActionDriftReconcile,
	}
	sort.Strings(actions)
	return actions
}
