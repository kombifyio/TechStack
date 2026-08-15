// Package core provides the central domain model for kombify-TechStack.
//
// This package is the single source of truth for all spec types and interfaces
// used across the two architectural pillars:
//
//   - Pillar 1 (Unifier Engine): IntentSpec, RequirementsSpec, UnifiedSpec, ServicePlacement
//   - Pillar 2 (Runtime Intelligence Layer): Worker types, agent capabilities, health states
//
// CONTRACT: See CONTRACT.md in this directory for authoritative constraints.
// ARCHITECTURE: See docs/architecture/ARCHITECTURE_V2.md
package core

import (
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Unifier is the main interface for spec-driven infrastructure provisioning.
// It validates user specs against CUE schemas, resolves dependencies,
// and generates OpenTofu configurations.
//
// Architecture: Pillar 1 — Unifier Engine (6-Phase Pipeline)
//
//	Phase 1: IntentSpec parsing and pre-validation
//	Phase 2: IntentSpec → RequirementsSpec (StackKit, Add-Ons, workers)
//	Phase 3+4: Worker Registry + System-Info (agents report capabilities)
//	Phase 5: RequirementsSpec + Workers → UnifiedSpec (placement engine)
//	Phase 6: UnifiedSpec → IaC generation (tfvars + HCL)
//
// See: docs/architecture/ARCHITECTURE_V2.md Section 3
// See: pkg/unifier/CONTRACT.md
type Unifier interface {
	// Analyze examines an IntentSpec and produces RequirementsSpec.
	// This is Phase 1: determine StackKit, required workers, credentials, pre-checks.
	// Returns RequirementsSpec even with incomplete input (robustness principle).
	Analyze(spec *KombinationSpec) (*RequirementsSpec, error)

	// Validate checks a spec against CUE schemas and policies.
	// Returns validation errors without executing any infrastructure changes.
	Validate(spec *KombinationSpec) (*ValidationResult, error)

	// Unify merges user spec with system policies and resolves all computed values.
	// Returns a fully resolved configuration ready for OpenTofu generation.
	// This is Phase 2: requires workers to be registered for service placement.
	Unify(spec *KombinationSpec, workers []Worker) (*UnifiedSpec, error)

	// Provision creates or updates infrastructure based on a unified spec.
	Provision(unified *UnifiedSpec) error

	// Destroy tears down infrastructure for a stack.
	Destroy(stackID string) error

	// Status returns the current status of a stack.
	Status(stackID string) (*StackStatus, error)
}

// ValidationResult contains the outcome of spec validation.
type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationError `json:"errors,omitempty"`
}

// ValidationError represents a single validation error.
type ValidationError struct {
	Path    string `json:"path"`    // JSON path to the invalid field (e.g., "nodes[0].provider")
	Code    string `json:"code"`    // Error code (e.g., "required", "invalid_value")
	Message string `json:"message"` // Human-readable error message
}

// InputSpec represents raw/partial user input.
//
// Architecture note:
//   - InputSpec is intentionally permissive (all optional) to support the Easy-Path
//     and to allow agent-driven node discovery/registration later.
//   - The Unifier pipeline normalizes InputSpec into a KombinationSpec and only then
//     produces a UnifiedSpec.
type InputSpec struct {
	Name            *string           `json:"name,omitempty" yaml:"name,omitempty"`
	Version         *string           `json:"version,omitempty" yaml:"version,omitempty"`
	Kit             *string           `json:"kit,omitempty" yaml:"kit,omitempty"`
	StackKit        *string           `json:"stackkit,omitempty" yaml:"stackkit,omitempty"`
	Mode            *string           `json:"mode,omitempty" yaml:"mode,omitempty"`
	Runtime         *string           `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Context         *string           `json:"context,omitempty" yaml:"context,omitempty"`
	Domain          *string           `json:"domain,omitempty" yaml:"domain,omitempty"`
	SubdomainPrefix *string           `json:"subdomainPrefix,omitempty" yaml:"subdomainPrefix,omitempty"`
	Email           *string           `json:"email,omitempty" yaml:"email,omitempty"`
	AdminEmail      *string           `json:"adminEmail,omitempty" yaml:"adminEmail,omitempty"`
	Compute         *InputComputeSpec `json:"compute,omitempty" yaml:"compute,omitempty"`
	PAAS            *string           `json:"paas,omitempty" yaml:"paas,omitempty"`
	Nodes           []InputNodeSpec   `json:"nodes,omitempty" yaml:"nodes,omitempty"`
	Services        InputServiceSpecs `json:"services,omitempty" yaml:"services,omitempty"`
	Network         *InputNetworkSpec `json:"network,omitempty" yaml:"network,omitempty"`
	SSH             *InputSSHConfig   `json:"ssh,omitempty" yaml:"ssh,omitempty"`
	VPN             *InputVPNSpec     `json:"vpn,omitempty" yaml:"vpn,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// InputComputeSpec is the raw/partial StackKits compute section.
type InputComputeSpec struct {
	Tier *string `json:"tier,omitempty" yaml:"tier,omitempty"`
}

// InputNodeSpec is the raw/partial form of NodeSpec.
type InputNodeSpec struct {
	Name     *string           `json:"name,omitempty" yaml:"name,omitempty"`
	Type     *string           `json:"type,omitempty" yaml:"type,omitempty"`
	Role     *string           `json:"role,omitempty" yaml:"role,omitempty"`
	Provider *string           `json:"provider,omitempty" yaml:"provider,omitempty"`
	IP       *string           `json:"ip,omitempty" yaml:"ip,omitempty"`
	Host     *string           `json:"host,omitempty" yaml:"host,omitempty"`
	Services []string          `json:"services,omitempty" yaml:"services,omitempty"`
	SSH      *InputSSHConfig   `json:"ssh,omitempty" yaml:"ssh,omitempty"`
	Tags     map[string]string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

// InputSSHConfig is the raw/partial form of SSHConfig.
type InputSSHConfig struct {
	Host    *string `json:"host,omitempty" yaml:"host,omitempty"`
	Port    *int    `json:"port,omitempty" yaml:"port,omitempty"`
	User    *string `json:"user,omitempty" yaml:"user,omitempty"`
	KeyPath *string `json:"key_path,omitempty" yaml:"key_path,omitempty"`
}

// InputServiceSpec is the raw/partial form of ServiceSpec.
type InputServiceSpec struct {
	Name  *string  `json:"name,omitempty" yaml:"name,omitempty"`
	Type  *string  `json:"type,omitempty" yaml:"type,omitempty"`
	Node  *string  `json:"node,omitempty" yaml:"node,omitempty"`
	Needs []string `json:"needs,omitempty" yaml:"needs,omitempty"`
}

// InputServiceSpecs accepts both TechStack's legacy service array and the
// StackKits stack-spec service map:
//
//	services:
//	  immich:
//	    enabled: true
type InputServiceSpecs []InputServiceSpec

//nolint:gocyclo // YAML compatibility supports legacy map/list service shapes in one decoder.
func (s *InputServiceSpecs) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	switch value.Kind {
	case yaml.SequenceNode:
		var items []InputServiceSpec
		if err := value.Decode(&items); err != nil {
			return err
		}
		*s = items
		return nil
	case yaml.MappingNode:
		items := make([]InputServiceSpec, 0, len(value.Content)/2)
		for i := 0; i+1 < len(value.Content); i += 2 {
			name := strings.TrimSpace(value.Content[i].Value)
			if name == "" {
				continue
			}
			if value.Content[i+1].Kind == yaml.ScalarNode {
				var enabled bool
				if err := value.Content[i+1].Decode(&enabled); err == nil {
					if enabled {
						items = append(items, InputServiceSpec{Name: &name})
					}
					continue
				}
			}
			var raw struct {
				Enabled *bool    `yaml:"enabled,omitempty"`
				Type    *string  `yaml:"type,omitempty"`
				Kind    *string  `yaml:"kind,omitempty"`
				Node    *string  `yaml:"node,omitempty"`
				Needs   []string `yaml:"needs,omitempty"`
			}
			if err := value.Content[i+1].Decode(&raw); err != nil {
				return err
			}
			if raw.Enabled != nil && !*raw.Enabled {
				continue
			}
			spec := InputServiceSpec{Name: &name, Needs: raw.Needs}
			if raw.Type != nil && strings.TrimSpace(*raw.Type) != "" {
				spec.Type = raw.Type
			} else if raw.Kind != nil && strings.TrimSpace(*raw.Kind) != "" {
				spec.Type = raw.Kind
			}
			if raw.Node != nil && strings.TrimSpace(*raw.Node) != "" {
				spec.Node = raw.Node
			}
			items = append(items, spec)
		}
		*s = items
		return nil
	default:
		var items []InputServiceSpec
		if err := value.Decode(&items); err != nil {
			return err
		}
		*s = items
		return nil
	}
}

// InputNetworkSpec is the raw/partial form of NetworkSpec.
type InputNetworkSpec struct {
	VPN    *string `json:"vpn,omitempty" yaml:"vpn,omitempty"`
	Domain *string `json:"domain,omitempty" yaml:"domain,omitempty"`
	Mode   *string `json:"mode,omitempty" yaml:"mode,omitempty"`
	Subnet *string `json:"subnet,omitempty" yaml:"subnet,omitempty"`
}

// InputVPNSpec is the raw/partial StackKits VPN section.
type InputVPNSpec struct {
	Enabled *bool   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Type    *string `json:"type,omitempty" yaml:"type,omitempty"`
}

// =============================================================================
// IntentSpec: Immutable User Intent
// =============================================================================

// IntentSpec represents the user's original intent from kombination.yaml.
// This spec is IMMUTABLE - it is never modified after initial creation.
// The UI Wizard generates this, and the Unifier reads it without modification.
type IntentSpec struct {
	APIVersion string `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty" yaml:"kind,omitempty"` // "IntentSpec"

	Name    string         `json:"name" yaml:"name"`
	Version string         `json:"version,omitempty" yaml:"version,omitempty"`
	Kit     string         `json:"kit,omitempty" yaml:"kit,omitempty"`
	Goals   *GoalsSpec     `json:"goals,omitempty" yaml:"goals,omitempty"`
	Network *IntentNetwork `json:"network,omitempty" yaml:"network,omitempty"`
	Variant string         `json:"variant,omitempty" yaml:"variant,omitempty"`

	// Power-user overrides
	Overrides *IntentOverrides `json:"overrides,omitempty" yaml:"overrides,omitempty"`
}

// GoalsSpec defines what the user wants to achieve.
type GoalsSpec struct {
	Storage     bool `json:"storage,omitempty" yaml:"storage,omitempty"`
	Media       bool `json:"media,omitempty" yaml:"media,omitempty"`
	Development bool `json:"development,omitempty" yaml:"development,omitempty"`
	Monitoring  bool `json:"monitoring,omitempty" yaml:"monitoring,omitempty"`
}

// IntentNetwork defines network preferences in the intent.
type IntentNetwork struct {
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"` // "local" | "public"
	VPN  string `json:"vpn,omitempty" yaml:"vpn,omitempty"`   // "headscale" | "wireguard" | "none"
}

// IntentOverrides allows power-users to force specific choices.
type IntentOverrides struct {
	Kit      string   `json:"kit,omitempty" yaml:"kit,omitempty"`
	Services []string `json:"services,omitempty" yaml:"services,omitempty"`
}

// =============================================================================
// Spec Metadata: Persistence and Traceability
// =============================================================================

// SpecMetadata contains common metadata for all spec types.
type SpecMetadata struct {
	GeneratedAt    time.Time `json:"generatedAt,omitempty" yaml:"generatedAt,omitempty"`
	SourceFile     string    `json:"sourceFile,omitempty" yaml:"sourceFile,omitempty"`
	SourceHash     string    `json:"sourceHash,omitempty" yaml:"sourceHash,omitempty"`
	UnifierVersion string    `json:"unifierVersion,omitempty" yaml:"unifierVersion,omitempty"`
}

// RequirementsMetadata extends SpecMetadata for RequirementsSpec.
type RequirementsMetadata struct {
	SpecMetadata `json:",inline" yaml:",inline"`
}

// UnifiedMetadata extends SpecMetadata with hash chain.
type UnifiedMetadata struct {
	SpecMetadata           `json:",inline" yaml:",inline"`
	SourceRequirementsFile string `json:"sourceRequirementsFile,omitempty" yaml:"sourceRequirementsFile,omitempty"`
	SourceRequirementsHash string `json:"sourceRequirementsHash,omitempty" yaml:"sourceRequirementsHash,omitempty"`
}

// KombinationSpec represents the user's desired state from kombination.yaml.
// This is the user-owned "Intent" before unification with system policies.
//
// IMPORTANT:
// - `kombination.yaml` may be generated by the Easy/Tech Wizard OR imported/hand-written by the user.
// - kombifyTechstack must NEVER automatically rewrite or normalize the user's kombination.yaml.
// - All automated/derived state lives in separate persisted files (e.g. requirements-spec.yaml, unified-spec.yaml).
type KombinationSpec struct {
	Name     string            `json:"name" yaml:"name"`
	Version  string            `json:"version,omitempty" yaml:"version,omitempty"`
	Kit      string            `json:"kit" yaml:"kit"` // StackKit reference (e.g., "basement-kit" or "cloud-kit")
	Nodes    []NodeSpec        `json:"nodes" yaml:"nodes"`
	Services []ServiceSpec     `json:"services,omitempty" yaml:"services,omitempty"`
	Network  NetworkSpec       `json:"network,omitempty" yaml:"network,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// NodeSpec defines a node in the user's spec (before resolution).
type NodeSpec struct {
	Name     string            `json:"name" yaml:"name"`
	Type     string            `json:"type" yaml:"type"`         // "main", "worker"
	Provider string            `json:"provider" yaml:"provider"` // "local", "hetzner", "docker"
	SSH      *SSHConfig        `json:"ssh,omitempty" yaml:"ssh,omitempty"`
	Tags     map[string]string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

// SSHConfig holds SSH connection details for a node.
type SSHConfig struct {
	Host    string `json:"host" yaml:"host"`
	Port    int    `json:"port,omitempty" yaml:"port,omitempty"`
	User    string `json:"user" yaml:"user"`
	KeyPath string `json:"key_path,omitempty" yaml:"key_path,omitempty"`
}

// ServiceSpec defines a service in the user's spec.
type ServiceSpec struct {
	Name  string   `json:"name" yaml:"name"`
	Type  string   `json:"type" yaml:"type"`                       // "reverse-proxy", "database", "docker", etc.
	Node  string   `json:"node" yaml:"node"`                       // Target node name
	Needs []string `json:"needs,omitempty" yaml:"needs,omitempty"` // Dependencies
}

// NetworkSpec defines network configuration in the user's spec.
type NetworkSpec struct {
	VPN    string `json:"vpn,omitempty" yaml:"vpn,omitempty"`       // "headscale", "wireguard", ""
	Domain string `json:"domain,omitempty" yaml:"domain,omitempty"` // Internal domain
}

// UnifiedSpec is the result of unifying user spec with system policies.
// Contains all computed values and is ready for OpenTofu generation.
type UnifiedSpec struct {
	APIVersion   string          `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind         string          `json:"kind,omitempty" yaml:"kind,omitempty"` // "UnifiedSpec"
	PipelineInfo UnifiedMetadata `json:"pipelineInfo,omitempty" yaml:"pipelineInfo,omitempty"`

	KombinationSpec

	DecisionContext     *DecisionContext `json:"decisionContext,omitempty" yaml:"decisionContext,omitempty"`
	DecisionContextHash string           `json:"decisionContextHash,omitempty" yaml:"decisionContextHash,omitempty"`
	DecisionTrace       *DecisionTrace   `json:"decisionTrace,omitempty" yaml:"decisionTrace,omitempty"`
	DecisionTraceHash   string           `json:"decisionTraceHash,omitempty" yaml:"decisionTraceHash,omitempty"`

	// Computed values added by the Unifier
	ResolvedNodes    []ResolvedNode    `json:"resolved_nodes"`
	ResolvedServices []ResolvedService `json:"resolved_services"`
	ResolvedNetwork  ResolvedNetwork   `json:"resolved_network"`
	StackKit         string            `json:"stackkit"` // Selected StackKit template (e.g., "basement-kit" or "cloud-kit")

	// Phase 2: Service-to-Worker Placement
	Placements       []ServicePlacement `json:"placements,omitempty"`        // Service placement decisions
	PlacementQuality int                `json:"placement_quality,omitempty"` // Overall placement quality score (0-100)
}

// ResolvedNode is a node with all computed values filled in.
type ResolvedNode struct {
	NodeSpec
	PrivateIP string `json:"private_ip,omitempty"`
	PublicIP  string `json:"public_ip,omitempty"`
	Arch      string `json:"arch"` // "amd64", "arm64"
	OS        string `json:"os"`   // "linux", "darwin"
}

// ResolvedService is a service with all computed values filled in.
type ResolvedService struct {
	ServiceSpec
	Image       string            `json:"image,omitempty"`
	Port        int               `json:"port,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Status      string            `json:"status,omitempty"` // "pending", "running", "stopped", "error"
}

// ResolvedNetwork contains computed network configuration.
type ResolvedNetwork struct {
	NetworkSpec
	VPNType     string   `json:"vpn_type,omitempty"`
	Subnet      string   `json:"subnet,omitempty"`
	Gateway     string   `json:"gateway,omitempty"`
	Nameservers []string `json:"nameservers,omitempty"`
	Domain      string   `json:"domain,omitempty"`
}

// StackStatus represents the current state of a stack.
type StackStatus struct {
	ID          string          `json:"id"`
	State       string          `json:"state"` // "provisioning", "running", "failed", "destroyed"
	Nodes       []NodeStatus    `json:"nodes"`
	Services    []ServiceStatus `json:"services"`
	LastUpdated string          `json:"last_updated"`
	Errors      []string        `json:"errors,omitempty"`
}

// NodeStatus represents the status of a single node.
type NodeStatus struct {
	Name       string `json:"name"`
	State      string `json:"state"` // "pending", "running", "stopped", "error"
	PublicIP   string `json:"public_ip,omitempty"`
	PrivateIP  string `json:"private_ip,omitempty"`
	AgentState string `json:"agent_state,omitempty"` // "connected", "disconnected"
}

// ServiceStatus represents the status of a deployed service.
type ServiceStatus struct {
	Name    string `json:"name"`
	State   string `json:"state"` // "starting", "running", "stopped", "error"
	Healthy bool   `json:"healthy"`
	URL     string `json:"url,omitempty"`
}

// =============================================================================
// Two-Phase Unifier: RequirementsSpec
// =============================================================================
// RequirementsSpec is the output of Phase 2 (Unifier.Analyze).
// It bridges IntentSpec → UnifiedSpec by calculating what the user needs
// before actual deployment planning.

// RequirementsSpec contains the Unifier's analysis of what's needed.
// This is produced BEFORE worker registration and UnifiedSpec generation.
type RequirementsSpec struct {
	APIVersion string               `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string               `json:"kind,omitempty" yaml:"kind,omitempty"` // "RequirementsSpec"
	Metadata   RequirementsMetadata `json:"metadata,omitempty" yaml:"metadata,omitempty"`

	DecisionContext     *DecisionContext `json:"decisionContext,omitempty" yaml:"decisionContext,omitempty"`
	DecisionContextHash string           `json:"decisionContextHash,omitempty" yaml:"decisionContextHash,omitempty"`
	DecisionTrace       *DecisionTrace   `json:"decisionTrace,omitempty" yaml:"decisionTrace,omitempty"`
	DecisionTraceHash   string           `json:"decisionTraceHash,omitempty" yaml:"decisionTraceHash,omitempty"`

	// StackKit chosen by Unifier (or explicit from IntentSpec.Kit)
	StackKit string `json:"stackKit"`

	// Auto-detected Add-ons based on goals/services
	DetectedAddons []string `json:"detectedAddons,omitempty"`

	// Worker requirements (min specs, count)
	RequiredWorkers WorkerRequirements `json:"requiredWorkers"`

	// Credentials that need to be configured (API keys, etc.)
	RequiredCredentials []CredentialRequirement `json:"requiredCredentials,omitempty"`

	// Pre-checks that must pass before deployment
	RequiredPreChecks []PreCheckDefinition `json:"requiredPreChecks,omitempty"`

	// Environment gaps that must be resolved or acknowledged before rollout.
	EnvironmentGaps []EnvironmentGap `json:"environmentGaps,omitempty" yaml:"environmentGaps,omitempty"`

	// Capability-guided UI / CLI guidance derived from OperatorCapabilityProfile.
	Guidance []DecisionGuidance `json:"guidance,omitempty" yaml:"guidance,omitempty"`

	// Defaults applied by Unifier (from StackKit + policies)
	AppliedDefaults map[string]any `json:"appliedDefaults,omitempty"`

	// Human-readable summary for UI display
	Description string `json:"description"`

	// Source IntentSpec reference (for traceability)
	IntentName string `json:"intentName,omitempty"`
}

// DecisionContext is the immutable input bundle used by the Unifier decision plane.
// It keeps user intent separate from environment reality and operator capability.
type DecisionContext struct {
	APIVersion string `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty" yaml:"kind,omitempty"`

	IntentRef   IntentReference            `json:"intentRef,omitempty" yaml:"intentRef,omitempty"`
	Channel     string                     `json:"channel,omitempty" yaml:"channel,omitempty"`
	Source      string                     `json:"source,omitempty" yaml:"source,omitempty"`
	Constraints []DecisionConstraint       `json:"constraints,omitempty" yaml:"constraints,omitempty"`
	Environment *EnvironmentInventory      `json:"environment,omitempty" yaml:"environment,omitempty"`
	Operator    *OperatorCapabilityProfile `json:"operator,omitempty" yaml:"operator,omitempty"`
}

// IntentReference points at the user-owned intent without embedding or rewriting it.
type IntentReference struct {
	Name       string `json:"name,omitempty" yaml:"name,omitempty"`
	StackID    string `json:"stackId,omitempty" yaml:"stackId,omitempty"`
	SourceFile string `json:"sourceFile,omitempty" yaml:"sourceFile,omitempty"`
	SourceHash string `json:"sourceHash,omitempty" yaml:"sourceHash,omitempty"`
}

// DecisionConstraint captures a hard or soft decision input supplied by UI, CLI, or policy.
type DecisionConstraint struct {
	Key      string `json:"key" yaml:"key"`
	Value    string `json:"value,omitempty" yaml:"value,omitempty"`
	Source   string `json:"source,omitempty" yaml:"source,omitempty"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
}

// EnvironmentInventory is the current or planned Homelab reality known to TechStack.
type EnvironmentInventory struct {
	SnapshotID      string                   `json:"snapshotId,omitempty" yaml:"snapshotId,omitempty"`
	GeneratedAt     time.Time                `json:"generatedAt,omitempty" yaml:"generatedAt,omitempty"`
	ComputeNodes    []EnvironmentComputeNode `json:"computeNodes,omitempty" yaml:"computeNodes,omitempty"`
	AccessEndpoints []AccessEndpoint         `json:"accessEndpoints,omitempty" yaml:"accessEndpoints,omitempty"`
	Assets          []EnvironmentAsset       `json:"assets,omitempty" yaml:"assets,omitempty"`
	NetworkSignals  []EnvironmentSignal      `json:"networkSignals,omitempty" yaml:"networkSignals,omitempty"`
	TLSSignals      []EnvironmentSignal      `json:"tlsSignals,omitempty" yaml:"tlsSignals,omitempty"`
	Reachability    []EnvironmentSignal      `json:"reachability,omitempty" yaml:"reachability,omitempty"`
	PreCheckResults []PreCheckResult         `json:"preCheckResults,omitempty" yaml:"preCheckResults,omitempty"`
	ObservedAt      time.Time                `json:"observedAt,omitempty" yaml:"observedAt,omitempty"`
	Source          string                   `json:"source,omitempty" yaml:"source,omitempty"`
}

// EnvironmentComputeNode is a normalized compute node from user spec, registry, or lease state.
type EnvironmentComputeNode struct {
	ID       string            `json:"id,omitempty" yaml:"id,omitempty"`
	Name     string            `json:"name,omitempty" yaml:"name,omitempty"`
	Role     string            `json:"role,omitempty" yaml:"role,omitempty"`
	Provider string            `json:"provider,omitempty" yaml:"provider,omitempty"`
	Status   string            `json:"status,omitempty" yaml:"status,omitempty"`
	CPU      int               `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	RAMMB    int               `json:"ramMb,omitempty" yaml:"ramMb,omitempty"`
	DiskGB   int               `json:"diskGb,omitempty" yaml:"diskGb,omitempty"`
	Arch     string            `json:"arch,omitempty" yaml:"arch,omitempty"`
	OS       string            `json:"os,omitempty" yaml:"os,omitempty"`
	Source   string            `json:"source,omitempty" yaml:"source,omitempty"`
	Tags     map[string]string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

// AccessEndpoint describes a domain, subdomain, mail address, auth endpoint, or service URL.
type AccessEndpoint struct {
	ID       string `json:"id,omitempty" yaml:"id,omitempty"`
	Kind     string `json:"kind,omitempty" yaml:"kind,omitempty"`
	Value    string `json:"value,omitempty" yaml:"value,omitempty"`
	Source   string `json:"source,omitempty" yaml:"source,omitempty"`
	Verified bool   `json:"verified,omitempty" yaml:"verified,omitempty"`
}

// EnvironmentAsset describes non-compute resources that affect rollout decisions.
type EnvironmentAsset struct {
	ID     string `json:"id,omitempty" yaml:"id,omitempty"`
	Kind   string `json:"kind,omitempty" yaml:"kind,omitempty"`
	Name   string `json:"name,omitempty" yaml:"name,omitempty"`
	Value  string `json:"value,omitempty" yaml:"value,omitempty"`
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
	Ready  bool   `json:"ready,omitempty" yaml:"ready,omitempty"`
}

// EnvironmentSignal captures lower-level environment evidence used by the decision plane.
type EnvironmentSignal struct {
	Key        string `json:"key" yaml:"key"`
	Value      string `json:"value,omitempty" yaml:"value,omitempty"`
	Confidence string `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	Source     string `json:"source,omitempty" yaml:"source,omitempty"`
}

// OperatorCapabilityProfile describes how much technical control the operator can handle.
type OperatorCapabilityProfile struct {
	Score      int                          `json:"score" yaml:"score"`
	Band       string                       `json:"band" yaml:"band"`
	Confidence float64                      `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	Source     string                       `json:"source,omitempty" yaml:"source,omitempty"`
	Evidence   []OperatorCapabilityEvidence `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	UpdatedAt  time.Time                    `json:"updatedAt,omitempty" yaml:"updatedAt,omitempty"`
}

// OperatorCapabilityEvidence records why a capability score was chosen.
type OperatorCapabilityEvidence struct {
	Signal string `json:"signal" yaml:"signal"`
	Weight int    `json:"weight,omitempty" yaml:"weight,omitempty"`
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
}

// DecisionTrace is the explainable output of the decision plane.
type DecisionTrace struct {
	SelectedStackKit    string             `json:"selectedStackKit,omitempty" yaml:"selectedStackKit,omitempty"`
	StackKitProfile     string             `json:"stackKitProfile,omitempty" yaml:"stackKitProfile,omitempty"`
	Foundation          string             `json:"foundation,omitempty" yaml:"foundation,omitempty"`
	Addons              []string           `json:"addons,omitempty" yaml:"addons,omitempty"`
	DeploymentMode      string             `json:"deploymentMode,omitempty" yaml:"deploymentMode,omitempty"`
	ComputeTier         string             `json:"computeTier,omitempty" yaml:"computeTier,omitempty"`
	PAAS                string             `json:"paas,omitempty" yaml:"paas,omitempty"`
	Reasons             []DecisionReason   `json:"reasons,omitempty" yaml:"reasons,omitempty"`
	BlockingGaps        []EnvironmentGap   `json:"blockingGaps,omitempty" yaml:"blockingGaps,omitempty"`
	Guidance            []DecisionGuidance `json:"guidance,omitempty" yaml:"guidance,omitempty"`
	DecisionContextHash string             `json:"decisionContextHash,omitempty" yaml:"decisionContextHash,omitempty"`
}

// DecisionReason explains a concrete resolver choice.
type DecisionReason struct {
	Code    string `json:"code" yaml:"code"`
	Message string `json:"message" yaml:"message"`
	Source  string `json:"source,omitempty" yaml:"source,omitempty"`
}

// EnvironmentGap is a missing or misaligned environment capability.
type EnvironmentGap struct {
	Code     string `json:"code" yaml:"code"`
	Message  string `json:"message" yaml:"message"`
	Severity string `json:"severity,omitempty" yaml:"severity,omitempty"`
	Blocking bool   `json:"blocking,omitempty" yaml:"blocking,omitempty"`
	Source   string `json:"source,omitempty" yaml:"source,omitempty"`
}

// DecisionGuidance is UI/CLI guidance derived from the operator capability profile.
type DecisionGuidance struct {
	Code    string `json:"code" yaml:"code"`
	Level   string `json:"level,omitempty" yaml:"level,omitempty"`
	Message string `json:"message" yaml:"message"`
	Surface string `json:"surface,omitempty" yaml:"surface,omitempty"`
}

// WorkerRequirements defines minimum worker specifications.
type WorkerRequirements struct {
	// Minimum number of cloud-hosted servers (Hetzner, etc.)
	MinCloudServers int `json:"minCloudServers"`

	// Minimum number of local/on-prem servers
	MinLocalServers int `json:"minLocalServers"`

	// Minimum total RAM in MB across all workers
	MinRAM int `json:"minRAM"`

	// Minimum total CPU cores across all workers
	MinCPU int `json:"minCPU"`

	// Special requirements as human-readable hints
	SpecialRequirements []string `json:"specialRequirements,omitempty"`
}

// CredentialRequirement defines a credential the user must provide.
type CredentialRequirement struct {
	// Unique key for this credential (e.g., "cloudflare_api_token")
	Key string `json:"key"`

	// Human-readable label
	Label string `json:"label"`

	// Description of what this credential is for
	Description string `json:"description"`

	// Whether this is required or optional
	Required bool `json:"required"`

	// Type hint for UI (e.g., "api_key", "password", "oauth")
	Type string `json:"type"`

	// URL to docs/setup instructions
	HelpURL string `json:"helpUrl,omitempty"`
}

// PreCheckDefinition defines a check that must pass before deployment.
type PreCheckDefinition struct {
	// Type of check (e.g., "gpu_detection", "docker_version")
	Type string `json:"type"`

	// Human-readable description
	Description string `json:"description"`

	// Minimum version if applicable
	MinVersion string `json:"minVersion,omitempty"`

	// Whether failure is blocking or just a warning
	Blocking bool `json:"blocking"`
}

// PreCheckResult is the outcome of running a PreCheckDefinition.
type PreCheckResult struct {
	// Reference to the original check
	Type string `json:"type"`

	// Status: "passed", "failed", "skipped", "warning"
	Status string `json:"status"`

	// Details about the result
	Details map[string]any `json:"details,omitempty"`

	// Error message if failed
	Error string `json:"error,omitempty"`
}

// =============================================================================
// Phase 2: Worker Registration & Matching
// =============================================================================

// Worker represents a registered node with capabilities.
// Used by Phase 2 (Unify) for service-to-worker matching.
type Worker struct {
	// Unique identifier (agent ID or node name)
	ID string `json:"id"`

	// Display name
	Name string `json:"name"`

	// Node type: "main", "worker", "storage"
	Type string `json:"type"`

	// Provider: "local", "hetzner", "aws", etc.
	Provider string `json:"provider"`

	// Connection status: "online", "offline", "unknown"
	Status string `json:"status"`

	// Hardware capabilities
	Capabilities WorkerCapabilities `json:"capabilities"`

	// Current resource usage (for load balancing)
	Usage WorkerUsage `json:"usage"`

	// Tags for affinity rules
	Tags map[string]string `json:"tags,omitempty"`
}

// WorkerCapabilities defines hardware and software capabilities.
type WorkerCapabilities struct {
	// Total CPU cores
	CPU int `json:"cpu"`

	// Total RAM in MB
	RAM int `json:"ram"`

	// Disk space in GB
	Disk int `json:"disk"`

	// GPU info if available
	GPU *GPUInfo `json:"gpu,omitempty"`

	// Architecture: "amd64", "arm64"
	Arch string `json:"arch"`

	// OS: "linux", "darwin"
	OS string `json:"os"`

	// Docker version if installed
	DockerVersion string `json:"dockerVersion,omitempty"`

	// Special hardware flags
	HasNVMe        bool `json:"hasNVMe,omitempty"`
	HasHWTranscode bool `json:"hasHWTranscode,omitempty"`
}

// GPUInfo contains GPU details for AI workloads.
type GPUInfo struct {
	// Vendor: "nvidia", "amd", "intel"
	Vendor string `json:"vendor"`

	// Model name
	Model string `json:"model"`

	// VRAM in MB
	VRAM int `json:"vram"`

	// Compute capability for CUDA
	ComputeCapability string `json:"computeCapability,omitempty"`
}

// WorkerUsage tracks current resource allocation.
type WorkerUsage struct {
	// Allocated CPU cores
	CPU int `json:"cpu"`

	// Allocated RAM in MB
	RAM int `json:"ram"`

	// Allocated disk in GB
	Disk int `json:"disk"`

	// Number of services currently assigned
	ServiceCount int `json:"serviceCount"`
}

// ServicePlacement is the result of matching a service to a worker.
type ServicePlacement struct {
	// Service being placed
	Service ServiceSpec `json:"service"`

	// Target worker ID
	WorkerID string `json:"workerId"`

	// Worker name for display
	WorkerName string `json:"workerName"`

	// Placement score (higher = better match)
	Score int `json:"score"`

	// Warnings if placement is suboptimal
	Warnings []string `json:"warnings,omitempty"`
}

// =============================================================================
// Type Aliases for Backward Compatibility
// =============================================================================

// KombinationSpecAlias is an alias for backward compatibility.
// Deprecated: Use IntentSpec for new code.
// type KombinationSpecAlias = KombinationSpec
