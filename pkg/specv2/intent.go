// Package specv2 projects wizard intent onto pinned StackKits Architecture v2
// kit seeds. The kit-authoring contract (#KitAuthoringOverrideV2) only permits
// metadata.name and network.domain.base at init time, so every other wizard
// decision is applied Techstack-side on the materialized seed and re-validated
// by the pinned `stackkit validate` — the CLI stays the acceptance authority.
package specv2

import (
	"fmt"
	"strings"
)

// WizardIntentSchema is the wire schema identifier for WizardIntent payloads.
const WizardIntentSchema = "techstack.wizard-intent/v1"

// Run kinds: the first run founds the homelab; every later run expands it
// (ADR-0036, one flow with dynamic questions).
const (
	RunKindFirstRun  = "first-run"
	RunKindExpansion = "expansion"
)

// Kit assignment modes for the server being onboarded.
const (
	KitAssignmentFound = "found" // found a new kit deployment (new spec)
	KitAssignmentJoin  = "join"  // join an existing kit deployment (append node)
)

// Architecture v2 node roles (#NodeRoleV2). The legacy wizard vocabulary
// (foundation/standalone/main) is normalized by NormalizeRole.
const (
	RoleController = "controller"
	RoleWorker     = "worker"
	RoleStorage    = "storage"
	RoleEdge       = "edge"
)

// Installable Architecture v2 kit slugs.
const (
	KitSlugBasement = "basement-kit"
	KitSlugCloud    = "cloud-kit"
	KitSlugModern   = "modern-homelab"
)

// Server transports: how the run's server is provisioned and reached. The
// vocabulary matches the stack config's server_provisioning_mode values.
const (
	TransportInstallCommand = "install-command"
	TransportConnectRemote  = "connect-remote"
	TransportKombifyCloud   = "kombify-cloud"
)

// KnownKitSlugs are the installable Architecture v2 kits.
var KnownKitSlugs = map[string]bool{
	KitSlugBasement: true,
	KitSlugCloud:    true,
	KitSlugModern:   true,
}

// WizardIntent is the compact request the wizard client sends; the backend
// owns the projection onto the kit seed. Owner identity deliberately never
// travels here — it is a separate custody handoff.
type WizardIntent struct {
	Schema        string        `json:"schema"`
	RunKind       string        `json:"run_kind"`
	HomelabID     string        `json:"homelab_id,omitempty"`
	Name          string        `json:"name"`
	DomainBase    string        `json:"domain_base,omitempty"`
	Goals         []string      `json:"goals,omitempty"`
	Server        ServerIntent  `json:"server"`
	KitAssignment KitAssignment `json:"kit_assignment"`
}

// ServerIntent describes the server being onboarded by this run.
type ServerIntent struct {
	// Roles uses Architecture v2 vocabulary; empty defaults to the seed's
	// own first node on "found" and to a worker on "join".
	Roles []string `json:"roles,omitempty"`
	// Purpose is the user-facing special-purpose marker (e.g. "backup");
	// recorded as intent only until the matching StackKits capability ships.
	Purpose string `json:"purpose,omitempty"`
	// Transport selects the provisioning lane; empty defaults to
	// install-command (self-host agent one-liner).
	Transport string `json:"transport,omitempty"`
}

// KitAssignment selects between founding a new kit deployment and joining an
// existing one.
type KitAssignment struct {
	Mode            string `json:"mode"`
	KitSlug         string `json:"kit_slug,omitempty"`
	KitDeploymentID string `json:"kit_deployment_id,omitempty"`
}

// Validate enforces the closed intent contract before any projection work.
func (intent WizardIntent) Validate() error {
	if intent.Schema != WizardIntentSchema {
		return fmt.Errorf("specv2: schema must be %q", WizardIntentSchema)
	}
	switch intent.RunKind {
	case RunKindFirstRun, RunKindExpansion:
	default:
		return fmt.Errorf("specv2: run_kind must be %q or %q", RunKindFirstRun, RunKindExpansion)
	}
	if strings.TrimSpace(intent.Name) == "" {
		return fmt.Errorf("specv2: name required")
	}
	switch intent.KitAssignment.Mode {
	case KitAssignmentFound:
		if !KnownKitSlugs[intent.KitAssignment.KitSlug] {
			return fmt.Errorf("specv2: kit_slug %q is not an installable kit", intent.KitAssignment.KitSlug)
		}
	case KitAssignmentJoin:
		if strings.TrimSpace(intent.KitAssignment.KitDeploymentID) == "" {
			return fmt.Errorf("specv2: kit_deployment_id required for join")
		}
	default:
		return fmt.Errorf("specv2: kit_assignment.mode must be %q or %q", KitAssignmentFound, KitAssignmentJoin)
	}
	for _, role := range intent.Server.Roles {
		if NormalizeRole(role) == "" {
			return fmt.Errorf("specv2: unknown server role %q", role)
		}
	}
	switch strings.TrimSpace(intent.Server.Transport) {
	case "", TransportInstallCommand, TransportConnectRemote, TransportKombifyCloud:
	default:
		return fmt.Errorf("specv2: server transport must be %q, %q, or %q", TransportInstallCommand, TransportConnectRemote, TransportKombifyCloud)
	}
	return nil
}

// NormalizeRole maps wizard and legacy wire vocabulary onto #NodeRoleV2.
// Unknown values return "".
func NormalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case RoleController, "foundation", "standalone", "main", "control-plane":
		return RoleController
	case RoleWorker:
		return RoleWorker
	case RoleStorage:
		return RoleStorage
	case RoleEdge:
		return RoleEdge
	default:
		return ""
	}
}
