// Package specv2 projects wizard intent onto pinned StackKits Architecture v2
// kit seeds. The kit-authoring contract (#KitAuthoringOverrideV2) only permits
// metadata.name and network.domain.base at init time, so every other wizard
// decision is applied Techstack-side on the materialized seed and re-validated
// by the pinned `stackkit validate` — the CLI stays the acceptance authority.
package specv2

import (
	"fmt"
	"net/mail"
	"slices"
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
	TransportHypervisor     = "hypervisor"
	TransportKombifyCloud   = "kombify-cloud"
)

const (
	environmentClassLocal = "local"
	environmentClassCloud = "cloud"
)

// Access modes describe the default reachability of the Homelab. Public
// publication remains an explicit per-service decision.
const (
	AccessModeLocal         = "local"
	AccessModeRemotePrivate = "remote-private"
	AccessExposurePublic    = "public"
)

// Household profiles are planning intent. They never create identities or
// send invitations during the creation run.
const (
	HouseholdProfileSolo   = "solo"
	HouseholdProfileShared = "shared"

	maxPlannedHouseholdPeople = 50
)

// knownKitSlugs are the installable Architecture v2 kits.
var knownKitSlugs = map[string]bool{
	KitSlugBasement: true,
	KitSlugCloud:    true,
	KitSlugModern:   true,
}

// IsKnownKitSlug reports whether slug identifies an installable Architecture v2 kit.
func IsKnownKitSlug(slug string) bool {
	return knownKitSlugs[slug]
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
	// Access preserves the operator's reachability and explicit publication
	// decisions. Runtime activation is reported separately from this intent.
	Access AccessIntent `json:"access,omitempty"`
	// Household records optional invitation drafts. Drafts are not accounts
	// and never authorize invitation delivery during creation.
	Household HouseholdIntent `json:"household,omitempty"`
	// UseCaseSettings carries the operator's decisions per selected use case,
	// keyed by use-case slug then setting id, exactly as the StackKits catalog
	// declares them (#UseCaseSetting). Validated against that catalog by the
	// handler, because the contract is closed and the catalog is its authority.
	// Recorded against the homelab beside the goals; a setting whose
	// realization is "recorded" waits for a release that honours it.
	UseCaseSettings map[string]map[string]any `json:"use_case_settings,omitempty"`
}

// AccessIntent captures the default private reachability and explicit public
// service publications. An empty mode is the backwards-compatible local
// default for older clients.
type AccessIntent struct {
	Mode         string                     `json:"mode,omitempty"`
	Publications []ServicePublicationIntent `json:"publications,omitempty"`
}

// ServicePublicationIntent is an explicit request to publish one service.
// The backend retains the request even when no runtime adapter can activate it.
type ServicePublicationIntent struct {
	ServiceID string `json:"service_id"`
	Exposure  string `json:"exposure"`
}

// HouseholdIntent describes who the operator plans to invite after the owner
// becomes active. PlannedPeople never contain activation credentials.
type HouseholdIntent struct {
	Profile       string                `json:"profile,omitempty"`
	PlannedPeople []PlannedPersonIntent `json:"planned_people,omitempty"`
}

// PlannedPersonIntent is a browser-stable invitation draft. ClientRef is
// opaque to the backend and lets the client reconcile rows without using an
// email address as identity.
type PlannedPersonIntent struct {
	ClientRef string `json:"client_ref"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
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
	// EnvironmentClass is explicit placement evidence for self-owned targets.
	// Empty remains unclassified; managed provider custody supplies its own
	// cloud evidence independently of this declaration.
	EnvironmentClass string `json:"environment_class,omitempty"`
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
	if !slices.Contains([]string{RunKindFirstRun, RunKindExpansion}, intent.RunKind) {
		return fmt.Errorf("specv2: run_kind must be %q or %q", RunKindFirstRun, RunKindExpansion)
	}
	if strings.TrimSpace(intent.Name) == "" {
		return fmt.Errorf("specv2: name required")
	}
	switch intent.KitAssignment.Mode {
	case KitAssignmentFound:
		if !IsKnownKitSlug(intent.KitAssignment.KitSlug) {
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
	for slug, settings := range intent.UseCaseSettings {
		if strings.TrimSpace(slug) == "" || len(settings) == 0 {
			return fmt.Errorf("specv2: use_case_settings entries must name a use case and at least one setting")
		}
		if !slices.Contains(intent.Goals, slug) {
			return fmt.Errorf("specv2: use_case_settings for %q without that goal", slug)
		}
		for id := range settings {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("specv2: use_case_settings.%s names an empty setting", slug)
			}
		}
	}
	if !slices.Contains([]string{"", TransportInstallCommand, TransportConnectRemote, TransportKombifyCloud, TransportHypervisor}, strings.TrimSpace(intent.Server.Transport)) {
		return fmt.Errorf("specv2: unsupported server transport")
	}
	if !slices.Contains([]string{"", environmentClassLocal, environmentClassCloud}, strings.ToLower(strings.TrimSpace(intent.Server.EnvironmentClass))) {
		return fmt.Errorf("specv2: server environment_class must be %q or %q", environmentClassLocal, environmentClassCloud)
	}
	if err := intent.Access.validate(); err != nil {
		return err
	}
	if err := intent.Household.validate(); err != nil {
		return err
	}
	return nil
}

func (intent AccessIntent) validate() error {
	mode := strings.ToLower(strings.TrimSpace(intent.Mode))
	if !slices.Contains([]string{"", AccessModeLocal, AccessModeRemotePrivate}, mode) {
		return fmt.Errorf("specv2: access.mode must be %q or %q", AccessModeLocal, AccessModeRemotePrivate)
	}
	seen := make(map[string]struct{}, len(intent.Publications))
	for _, publication := range intent.Publications {
		serviceID := strings.TrimSpace(publication.ServiceID)
		if serviceID == "" {
			return fmt.Errorf("specv2: access publication service_id required")
		}
		if _, exists := seen[serviceID]; exists {
			return fmt.Errorf("specv2: access publication service_id %q is duplicated", serviceID)
		}
		seen[serviceID] = struct{}{}
		if strings.ToLower(strings.TrimSpace(publication.Exposure)) != AccessExposurePublic {
			return fmt.Errorf("specv2: access publication exposure must be %q", AccessExposurePublic)
		}
	}
	return nil
}

func (intent HouseholdIntent) validate() error {
	profile := strings.ToLower(strings.TrimSpace(intent.Profile))
	if !slices.Contains([]string{"", HouseholdProfileSolo, HouseholdProfileShared}, profile) {
		return fmt.Errorf("specv2: household.profile is unsupported")
	}
	if len(intent.PlannedPeople) > maxPlannedHouseholdPeople {
		return fmt.Errorf("specv2: household planned_people exceeds %d", maxPlannedHouseholdPeople)
	}
	if profile == HouseholdProfileSolo && len(intent.PlannedPeople) > 0 {
		return fmt.Errorf("specv2: solo household cannot carry planned people")
	}
	seen := make(map[string]struct{}, len(intent.PlannedPeople))
	for _, person := range intent.PlannedPeople {
		clientRef := strings.TrimSpace(person.ClientRef)
		if clientRef == "" {
			return fmt.Errorf("specv2: household planned person client_ref required")
		}
		if _, exists := seen[clientRef]; exists {
			return fmt.Errorf("specv2: household planned person client_ref %q is duplicated", clientRef)
		}
		seen[clientRef] = struct{}{}
		name := strings.TrimSpace(person.Name)
		email := strings.TrimSpace(person.Email)
		if name == "" && email == "" {
			return fmt.Errorf("specv2: household planned person requires a name or email")
		}
		if len(name) > 200 || len(email) > 320 {
			return fmt.Errorf("specv2: household planned person exceeds field limits")
		}
		if email != "" {
			address, err := mail.ParseAddress(email)
			if err != nil || !strings.EqualFold(strings.TrimSpace(address.Address), email) {
				return fmt.Errorf("specv2: household planned person email is invalid")
			}
		}
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
