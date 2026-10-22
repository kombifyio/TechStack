package routes

import (
	"slices"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/executionchannel"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
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
//	reboot             POST /api/v1/servers/{id}/actions {"action":"reboot"}
//	os_update_plan     POST /api/v1/servers/{id}/actions {"action":"os_update_plan"}
//	os_update          POST /api/v1/servers/{id}/actions {"action":"os_update"}
//	re_pair            POST /api/v1/servers/{id}/binding-repair
//
// The three maintenance ids come from serverMaintenanceHandlers.advertisedActions
// and are offered only when the maintenance route is wired, the node is an
// active, bound and connected non-substrate that is not the control-plane host,
// and its agent advertises servermaintenance.AgentCapability.
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
	serverActionRePair            = "re_pair"
)

// Stack action vocabulary. All of these route to
// POST /api/v1/stacks/{id}/stackkit/operations and are scoped to the StackKit
// deployment on this node, never to the node itself.
const (
	serverStackActionPlan            = "plan"
	serverStackActionApply           = "apply"
	serverStackActionVerify          = "verify"
	serverStackActionUpgrade         = "upgrade"
	serverStackActionDriftDetect     = "drift_detect"
	serverStackActionDriftReconcile  = "drift_reconcile"
	serverStackActionBackupStatus    = "backup_status"
	serverStackActionBackupRestore   = "backup_restore"
	serverStackActionRestoreActivate = "backup_restore_activate"
	serverStackActionRestoreRecover  = "backup_restore_recover"
	serverStackActionBackupRun       = "backup_run"
	serverStackActionBackupConfigure = "backup_configure"
	serverStackActionAdvancedChange  = "advanced_change_set"
	serverStackActionRollback        = "rollback"
	serverStackActionRestoreDrill    = "restore_drill"
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
	// Direct backup operations are admitted only for Basement by the existing
	// orchestrator. Cloud and other managed kits retain their schedule authority.
	if jobs.NormalizeStackKitRef(stringFromAnyMap(server.Metadata, "stackkit")) == jobs.DefaultBasementKitRef {
		actions = append(actions, serverStackActionBackupStatus, serverStackActionBackupRestore)
	}
	sort.Strings(actions)
	return actions
}

// Day-2 actions require the authenticated node transport and the exact runtime
// release it has converged. The dispatcher still performs operation admission,
// capability issuance, service-lock checks and owner approval before dispatch.
func (h serverRuntimeHandlers) applyStackKitActions(item *serverRuntimeResponse, server controlplane.ServerRuntime) {
	if item.Capabilities == nil || item.Capabilities.IdentityBinding != serverIdentityKeyBound ||
		!slices.Contains(item.StackActions, serverStackActionApply) {
		return
	}
	capabilities := append([]string(nil), item.Capabilities.stackKitCapabilities...)
	if h.currentMTLSCapabilities != nil {
		if current, present := h.currentMTLSCapabilities(server); present {
			capabilities = current
		}
	}
	configured, err := jobs.CachedTargetStackKitRelease()
	if err != nil || configured == nil {
		return
	}
	// A missing catalog leaves only the non-Advanced actions admitted by the
	// pinned receipt. Release verification remains with its owning resolver.
	catalog, _ := configured.AdvancedOperations()
	item.StackActions = append(item.StackActions, stackKitDay2Actions(item, capabilities, configured.Receipt(), catalog)...)
	sort.Strings(item.StackActions)
}

// applyBindingRepair removes mutating StackKit actions while this server has
// no pinned binding key, and advertises re-pair only when that route is wired.
// An action this process does not know is treated as mutating.
func (h serverRuntimeHandlers) applyBindingRepair(item *serverRuntimeResponse) {
	if item == nil || item.Capabilities == nil || item.Capabilities.BindingRepair == nil || !item.Capabilities.BindingRepair.Required {
		return
	}
	kept := make([]string, 0, len(item.StackActions))
	for _, action := range item.StackActions {
		if !stackActionMutates(action) {
			kept = append(kept, action)
		}
	}
	item.StackActions = kept
	if h.bindingRepair == nil || slices.Contains(item.AllowedActions, serverActionRePair) {
		return
	}
	actions := make([]string, len(item.AllowedActions), len(item.AllowedActions)+1)
	copy(actions, item.AllowedActions)
	item.AllowedActions = append(actions, serverActionRePair)
	sort.Strings(item.AllowedActions)
}

func stackActionMutates(action string) bool {
	switch action {
	case serverStackActionPlan, serverStackActionVerify, serverStackActionDriftDetect, serverStackActionBackupStatus:
		return false
	default:
		return true
	}
}

// stackKitDay2Actions classifies already verified release and node observations.
// It opens no database and grants no command authority: the central dispatcher
// repeats the tenant, entitlement, release, approval and execution admission.
func stackKitDay2Actions(item *serverRuntimeResponse, capabilities []string, configured stackkitrelease.Receipt, catalog stackkitrelease.AdvancedOperationsCatalog) []string {
	if item.Capabilities == nil || item.Capabilities.IdentityBinding != serverIdentityKeyBound ||
		!slices.Contains(item.StackActions, serverStackActionApply) || !stackKitRuntimeConverged(item, configured) {
		return nil
	}
	for _, required := range []string{"stackkit", stackkitcommand.ExpectedPlanHashCapability, stackkitcommand.WorkspaceInstanceCapability} {
		if !slices.Contains(capabilities, required) {
			return nil
		}
	}
	actions := []string{}
	// Direct backup custody remains Basement-only, as admitted by the base
	// projection. Other managed kits retain their existing schedule authority.
	if slices.Contains(item.StackActions, serverStackActionBackupStatus) {
		actions = append(actions, serverStackActionBackupRun, serverStackActionBackupConfigure)
	}
	if restoreStackActionsSupported(item, capabilities, configured) {
		actions = append(actions, serverStackActionRestoreActivate, serverStackActionRestoreRecover)
	}
	if slices.Contains(capabilities, stackkitcommand.AdvancedOperationsCapability) {
		actions = append(actions, advancedStackActions(catalog)...)
	}
	return actions
}

// Both phases must be present in the pinned catalog before the change-set
// action is offered. A partial or unavailable operation remains unadvertised.
func advancedStackActions(catalog stackkitrelease.AdvancedOperationsCatalog) []string {
	actions := []string{}
	if catalog.Dispatchable("terramate.change-set.create") == nil && catalog.Dispatchable("terramate.change-set.apply") == nil {
		actions = append(actions, serverStackActionAdvancedChange)
	}
	for action, operation := range map[string]string{
		serverStackActionRollback:     "rollback.coordinated",
		serverStackActionRestoreDrill: "restore.drill",
	} {
		if catalog.Dispatchable(operation) == nil {
			actions = append(actions, action)
		}
	}
	return actions
}

// This classifies already verified release and observation data. It grants no
// mutation authority; the typed dispatcher still enforces the exact pin.
func restoreStackActionsSupported(item *serverRuntimeResponse, capabilities []string, configured stackkitrelease.Receipt) bool {
	if item.Capabilities == nil || item.Capabilities.IdentityBinding != serverIdentityKeyBound || !slices.Contains(item.StackActions, serverStackActionBackupRestore) ||
		!stackkitcommand.ReleaseAtLeast(configured.Version, stackkitcommand.MinRestoreActivationRelease) {
		return false
	}
	for _, required := range []string{"stackkit", stackkitcommand.ExpectedPlanHashCapability, stackkitcommand.WorkspaceInstanceCapability, stackkitcommand.RestoreActivationCapability} {
		if !slices.Contains(capabilities, required) {
			return false
		}
	}
	return stackKitRuntimeConverged(item, configured)
}

func stackKitRuntimeConverged(item *serverRuntimeResponse, configured stackkitrelease.Receipt) bool {
	if item.Capabilities == nil {
		return false
	}
	observed := item.Capabilities.runtimeConvergence
	if observed == nil || observed.State != runtimeconvergence.StateReady {
		return false
	}
	for _, component := range observed.Components {
		if component.Name == runtimeconvergence.StackKitsRuntimeComponent && component.State == runtimeconvergence.ComponentReady &&
			strings.TrimPrefix(component.Version, "v") == strings.TrimPrefix(configured.Version, "v") &&
			component.ArtifactSHA256 == configured.ArchiveSHA256 {
			return true
		}
	}
	return false
}
