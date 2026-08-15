// Package runtimeaction contains Techstack's frozen pre-StackAction adapter.
// New product execution uses the generated stackaction contract and the pinned
// StackKits CLI. These definitions retain only local persisted/job compatibility.
package runtimeaction

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	TargetStackKits                   = "stackkits"
	TargetSimulate                    = "simulate"
	PathPrefix                        = "/api/v1/internal/runtime-actions"
	PathSimulateUpdate                = PathPrefix + "/simulate-update"
	PathStackKitRollout               = PathPrefix + "/stackkit-rollout"
	PathStackKitVerify                = PathPrefix + "/stackkit-verify"
	PathRestoreDrill                  = PathPrefix + "/restore-drill"
	ArchitectureV2PathPrefix          = "/api/v2/internal/runtime-actions"
	ArchitectureV2PathStackKitRollout = ArchitectureV2PathPrefix + "/stackkit-rollout"
	ArchitectureV2PathStackKitVerify  = ArchitectureV2PathPrefix + "/stackkit-verify"
)

type APIVersion string

const RuntimeActionAPIVersionV2Alpha1 APIVersion = "stackkit.runtime-action/v2alpha1"

type Action string

const (
	ActionSimulateUpdate  Action = "simulate_update"
	ActionStackKitRollout Action = "stackkit_rollout"
	ActionVerifyRollout   Action = "verify_rollout"
	ActionRestoreDrill    Action = "restore_drill"
)

type Mode string

const (
	ModeDryRun Mode = "dry-run"
	ModeApply  Mode = "apply"
)

type Status string

const (
	StatusAccepted          Status = "accepted"
	StatusReady             Status = "ready"
	StatusApplied           Status = "applied"
	StatusVerified          Status = "verified"
	StatusCompletedDegraded Status = "completed_degraded"
	StatusSkipped           Status = "skipped"
	StatusFailed            Status = "failed"
)

type RuntimeTarget struct {
	Host             string `json:"host,omitempty"`
	PublicIP         string `json:"public_ip,omitempty"`
	PrivateIP        string `json:"private_ip,omitempty"`
	User             string `json:"user,omitempty"`
	Port             int    `json:"port,omitempty"`
	DockerHost       string `json:"docker_host,omitempty"`
	KeyPath          string `json:"key_path,omitempty"`
	PrivateKey       string `json:"private_key,omitempty"`
	ClientPrivateKey string `json:"client_private_key,omitempty"`
	Password         string `json:"password,omitempty"`
}
type CheckStatus string
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
}
type StackKitOutputs map[string]any

type PlatformNode struct {
	Name      string             `json:"name,omitempty"`
	Role      string             `json:"role,omitempty"`
	IP        string             `json:"ip,omitempty"`
	Host      string             `json:"host,omitempty"`
	Services  []string           `json:"services,omitempty"`
	Platform  NodePlatformTarget `json:"platform,omitempty"`
	Bootstrap *NodeBootstrap     `json:"bootstrap,omitempty"`
}
type NodePlatformTarget struct {
	ServerID        string `json:"serverId,omitempty"`
	DestinationUUID string `json:"destinationUuid,omitempty"`
	EnvironmentID   string `json:"environmentId,omitempty"`
	ProjectUUID     string `json:"projectUuid,omitempty"`
	EnvironmentUUID string `json:"environmentUuid,omitempty"`
}
type NodeBootstrap struct {
	KomodoCoreAddress   string        `json:"komodo_core_address,omitempty"`
	KomodoOnboardingKey string        `json:"komodo_onboarding_key,omitempty"`
	SSH                 *SSHBootstrap `json:"ssh,omitempty"`
}
type SSHBootstrap struct {
	Host             string `json:"host,omitempty"`
	User             string `json:"user,omitempty"`
	Port             int    `json:"port,omitempty"`
	KeyPath          string `json:"key_path,omitempty"`
	KeyPEM           string `json:"key_pem,omitempty"`
	PrivateKey       string `json:"private_key,omitempty"`
	ClientPrivateKey string `json:"client_private_key,omitempty"`
	ProxyJump        string `json:"proxy_jump,omitempty"`
}

type OwnerSpecBootstrap struct {
	Endpoint  string   `json:"endpoint"`
	Token     string   `json:"token"`
	ExpiresAt string   `json:"expires_at"`
	Scopes    []string `json:"scopes,omitempty"`
}
type PreviewPolicy struct {
	Required          bool   `json:"required,omitempty"`
	Runtime           string `json:"runtime,omitempty"`
	Audience          string `json:"audience,omitempty"`
	Visibility        string `json:"visibility,omitempty"`
	TTLSeconds        int    `json:"ttl_seconds,omitempty"`
	StaffOnly         bool   `json:"staff_only,omitempty"`
	PublicBetaPreview bool   `json:"public_beta_preview,omitempty"`
}

type Request struct {
	APIVersion         APIVersion          `json:"api_version,omitempty"`
	Action             Action              `json:"action"`
	StackID            string              `json:"stack_id"`
	StackName          string              `json:"stack_name,omitempty"`
	StackKit           string              `json:"stackkit,omitempty"`
	TenantID           string              `json:"tenant_id,omitempty"`
	OwnerID            string              `json:"owner_id,omitempty"`
	StackSpec          json.RawMessage     `json:"stack_spec,omitempty"`
	StackSpecPath      string              `json:"stack_spec_path,omitempty"`
	TofuDir            string              `json:"tofu_dir,omitempty"`
	UnifiedPath        string              `json:"unified_path,omitempty"`
	OwnerSpecBootstrap *OwnerSpecBootstrap `json:"owner_spec_bootstrap,omitempty"`
	RuntimeTarget      *RuntimeTarget      `json:"runtime_target,omitempty"`
	PlatformNodes      []PlatformNode      `json:"platform_nodes,omitempty"`
	PreviewPolicy      *PreviewPolicy      `json:"preview_policy,omitempty"`
}

type Response struct {
	Status          Status           `json:"status"`
	Action          Action           `json:"action"`
	StackID         string           `json:"stack_id"`
	StackName       string           `json:"stack_name,omitempty"`
	StackKit        string           `json:"stackkit,omitempty"`
	TenantID        string           `json:"tenant_id,omitempty"`
	OwnerID         string           `json:"owner_id,omitempty"`
	TofuDir         string           `json:"tofu_dir,omitempty"`
	UnifiedPath     string           `json:"unified_path,omitempty"`
	Mode            Mode             `json:"mode"`
	SimulationID    string           `json:"simulation_id,omitempty"`
	DeploymentID    string           `json:"deployment_id,omitempty"`
	NodeIDs         []string         `json:"node_ids,omitempty"`
	PreviewURL      string           `json:"preview_url,omitempty"`
	ExpiresAt       string           `json:"expires_at,omitempty"`
	Checks          []Check          `json:"checks,omitempty"`
	StackKitOutputs *StackKitOutputs `json:"stackkit_outputs,omitempty"`
}

type ArchitectureV2Operation string

const (
	ArchitectureV2OperationRollout ArchitectureV2Operation = "stackkit_rollout"
	ArchitectureV2OperationVerify  ArchitectureV2Operation = "verify_rollout"
)

type ArchitectureV2Request struct {
	APIVersion       APIVersion              `json:"api_version"`
	Action           ArchitectureV2Operation `json:"action"`
	StackID          string                  `json:"stack_id"`
	TenantID         string                  `json:"tenant_id,omitempty"`
	OwnerID          string                  `json:"owner_id,omitempty"`
	StackSpec        json.RawMessage         `json:"stack_spec"`
	Inventory        json.RawMessage         `json:"inventory"`
	ExpectedPlanHash string                  `json:"expected_plan_hash"`
}
type ArchitectureV2ExecutionRequest struct {
	ArchitectureV2Request
	TofuDir       string         `json:"tofu_dir,omitempty"`
	RuntimeTarget *RuntimeTarget `json:"runtime_target,omitempty"`
	PlatformNodes []PlatformNode `json:"platform_nodes,omitempty"`
}

func NormalizeAction(action string) Action {
	return Action(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(action)), "-", "_"))
}

func ValidateArchitectureV2ExecutionRequest(request ArchitectureV2ExecutionRequest) error {
	if request.APIVersion != RuntimeActionAPIVersionV2Alpha1 || request.StackID == "" || len(request.StackSpec) == 0 || request.ExpectedPlanHash == "" {
		return fmt.Errorf("runtimeaction: invalid architecture v2 execution request")
	}
	if request.Action != ArchitectureV2OperationRollout && request.Action != ArchitectureV2OperationVerify {
		return fmt.Errorf("runtimeaction: unsupported architecture v2 action")
	}
	return nil
}
