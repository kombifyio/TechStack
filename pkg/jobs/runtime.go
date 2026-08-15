package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

const (
	DefaultMonthlyRuntimeProvider = providercatalog.ProviderCentron
	DefaultManagedLeaseProvider   = DefaultMonthlyRuntimeProvider
	DefaultBasementKitRef         = "basement-kit"
	DefaultBaseKitRef             = DefaultBasementKitRef // Deprecated symbol: external rollouts use basement-kit.
	DefaultCloudKitRef            = "cloud-kit"

	providerLocal = "local"
	providerCloud = "cloud"

	stackRoleMain = "main"

	metadataKeyStackID              = "stack_id"
	metadataKeyServerMode           = "server_mode"
	metadataKeyRuntimeLane          = "runtime_lane"
	metadataKeyRuntimeOfferingID    = "runtime_offering_id"
	metadataKeyProviderID           = providercatalog.ProviderIDField
	metadataKeyLeaseProvider        = providercatalog.LegacyLeaseProviderField
	metadataKeyProviderRegion       = "provider_region"
	metadataKeyIONOSDatacenter      = "ionos_datacenter"
	metadataKeySimulateProviderID   = providercatalog.LegacySimulateProviderIDField
	metadataKeySimulateLifecycle    = "simulate_node_lifecycle"
	metadataKeyDesiredState         = "desired_state"
	metadataKeyBillingMode          = "billing_mode"
	metadataKeyBillingCadence       = "billing_cadence"
	metadataKeyStackKitCatalogRef   = "stackkit_catalog_ref"
	metadataKeyStackKitOutputs      = "stackkit_outputs"
	metadataKeyVerificationStatus   = "verification_status"
	metadataKeyServerProvisionMode  = "server_provisioning_mode"
	metadataKeyServerConnectionMode = "server_connection_mode"
	metadataKeyRuntimePublicIP      = "runtime_public_ip"
	metadataKeyRuntimePrivateIP     = "runtime_private_ip"
	metadataKeyRuntimeSSHHost       = "runtime_ssh_host"
	metadataKeyRuntimeSSHUser       = "runtime_ssh_user"
	metadataKeyRuntimeSSHPort       = "runtime_ssh_port"
	metadataKeyRuntimeSSHPrivateKey = "runtime_ssh_private_key_enc"
	metadataKeyRuntimeClientKey     = "runtime_client_private_key_enc"
	metadataKeyRuntimeSSHPassword   = "runtime_ssh_password_enc" // #nosec G101 -- metadata key name, not a credential value.
	metadataKeyRuntimeEnrollState   = "runtime_enrollment_status"
	metadataKeyRuntimeEnrollError   = "runtime_enrollment_error"
	metadataKeyScenarioID           = "scenario_id"
	serverModeMonthlyRuntime        = serverruntime.RuntimeLaneMonthly
	serverModeManagedCloud          = "managed-cloud"
	serverModeUserOwned             = "user-owned"
	billingCadenceMonthly           = string(serverruntime.BillingCadenceMonthly)
	defaultRuntimeOfferingID        = string(serverruntime.RuntimeOfferingStandard)
	simulateLifecyclePVM            = string(serverruntime.NodeLifecyclePVM)
	desiredStateRunning             = "running"
	billingModeSubscription         = "subscription"
	verificationStatusPending       = "pending"
	runtimeEnrollmentStatusPending  = "pending"
	runtimeEnrollmentStatusFailed   = "failed"
	runtimeEnrollmentStatusRetrying = "retrying"
	serverProvisionModeKombifyCloud = "kombify-cloud"
	serverProvisionModeInstall      = "install-command"
	serverConnectionManagedSub      = "managed-subscription"
	defaultLeaseRegion              = "de-fra"
	defaultLeaseImage               = "ubuntu-24.04"
)

var (
	ErrManagedRuntimeEnrollmentFailed        = errors.New("managed runtime lease enrollment failed")
	ErrManagedRuntimeTargetCredentialFailed  = errors.New("managed runtime target credentials unavailable")
	ErrManagedLeaseDecommissionUnavailable   = errors.New("native managed-lease decommissioner unavailable")
	ErrManagedLeaseDecommissionProofRequired = errors.New("terminal managed-lease decommission proof required")
)

type RuntimePhase string

const (
	// PrimaryManagedLeaseOperationKey is the transport/workflow identity used by
	// the initial managed runtime admitted as part of stack creation. Durable
	// resource identity comes from RuntimeSlotKey, never from a retry key.
	PrimaryManagedLeaseOperationKey = "primary"
	// PrimaryManagedRuntimeSlotKey is the stable product identity of the first
	// server in a stack. It is deliberately independent of the server's role.
	PrimaryManagedRuntimeSlotKey = "primary"
	// PreparedManagedLeaseRequestPayloadKey carries the byte-equivalent native
	// admission request from the synchronous create boundary into the durable
	// provision job. The job must replay that request instead of reconstructing
	// a second provider intent from later StackKit projections.
	PreparedManagedLeaseRequestPayloadKey = "_prepared_managed_lease_request"
	// PreparedManagedLeaseRequestResultKey is the same immutable request carried
	// in the durable job result. Control-plane job persistence stores Result but
	// not the process-local Payload, so provider-wait rehydration uses this
	// checkpoint to restore the exact admission request after a restart.
	PreparedManagedLeaseRequestResultKey = PreparedManagedLeaseRequestPayloadKey

	RuntimePhasePrepared           RuntimePhase = "prepared"
	RuntimePhaseLeasePending       RuntimePhase = "lease_pending"
	RuntimePhaseLeaseReady         RuntimePhase = "lease_ready"
	RuntimePhaseRuntimeConnected   RuntimePhase = "runtime_connected"
	RuntimePhaseSimulationPassed   RuntimePhase = "simulation_passed"
	RuntimePhaseDeploying          RuntimePhase = "deploying"
	RuntimePhaseDeployed           RuntimePhase = "deployed"
	RuntimePhaseVerificationFailed RuntimePhase = "verification_failed"
	RuntimePhaseVerified           RuntimePhase = "verified"
)

type ManagedLeaseRequest struct {
	StackID               string            `json:"stack_id"`
	StackName             string            `json:"stack_name"`
	StackKit              string            `json:"stackkit"`
	TenantID              string            `json:"tenant_id"`
	OwnerID               string            `json:"owner_id"`
	Provider              string            `json:"provider"`
	OperationKey          string            `json:"operation_key"`
	RuntimeSlotKey        string            `json:"runtime_slot_key"`
	RuntimeSlotGeneration uint64            `json:"runtime_slot_generation"`
	NodeRole              string            `json:"node_role"`
	Services              []string          `json:"services"`
	Metadata              map[string]string `json:"metadata"`
}

func ManagedLeaseRequestPayload(req ManagedLeaseRequest) map[string]interface{} {
	payload := map[string]interface{}{}
	raw, err := json.Marshal(req)
	if err == nil {
		_ = json.Unmarshal(raw, &payload)
	}
	return payload
}

func managedLeaseRequestFromPayload(value interface{}) (*ManagedLeaseRequest, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var req ManagedLeaseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

type ManagedLeaseResult struct {
	RuntimeSlotKey        string
	RuntimeSlotID         string
	RuntimeSlotGeneration uint64
	LeaseID               string
	RuntimeServerID       string
	ResourceGenerationID  string
	OperationID           string
	Provider              string
	DesiredState          string
	BillingMode           string
	Phase                 RuntimePhase
	IdempotentReplay      bool
	Target                *ManagedRuntimeTarget
}

type ManagedLeaseManager interface {
	CreateOrBindLease(ctx context.Context, req ManagedLeaseRequest) (*ManagedLeaseResult, error)
}

// ManagedLeaseAdmissionPreflighter proves that a fresh managed-runtime request
// is currently eligible to reach durable job coordination. The check is
// read-only and never grants execution authority: CreateOrBindLease must repeat
// every mutable policy, capacity, and activation check transactionally.
type ManagedLeaseAdmissionPreflighter interface {
	PreflightCreateOrBindLease(ctx context.Context, req ManagedLeaseRequest) error
}

// ManagedRuntimeSlotGenerationRequest identifies one provider-neutral logical
// server slot without supplying provider handles or a lifecycle epoch.
type ManagedRuntimeSlotGenerationRequest struct {
	TenantID       string
	StackID        string
	RuntimeSlotKey string
}

// ManagedRuntimeSlotGeneration is the server-resolved lifecycle epoch used by
// all durable identities for one add-server attempt.
type ManagedRuntimeSlotGeneration struct {
	RuntimeSlotID      string
	GenerationOrdinal  uint64
	ExistingUnreleased bool
}

type ManagedRuntimeSlotGenerationResolver interface {
	ResolveManagedRuntimeSlotGeneration(
		context.Context,
		ManagedRuntimeSlotGenerationRequest,
	) (ManagedRuntimeSlotGeneration, error)
}

type ManagedLeaseDecommissionRequest struct {
	StackID                  string
	TenantID                 string
	OwnerID                  string
	LeaseID                  string
	ResourceGenerationDigest string
}

type ManagedLeaseDecommissionResult struct {
	LeaseIDs       []string
	Decommissioned int
	Skipped        int
	Proofs         []ManagedLeaseDecommissionProof
}

const (
	ManagedLeaseDecommissionObservedDecommissioned = "decommissioned"
	ManagedLeaseDecommissionObservedNotFound       = "not_found"
	ManagedRuntimeDecommissionRequiredField        = "managed_runtime_decommission_required"
)

// ManagedLeaseDecommissionProof is the exact terminal provider read-back that
// must precede any local stack/lease success marker. Counts, desired state, and
// a missing local workspace are never proof of provider cleanup.
type ManagedLeaseDecommissionProof struct {
	StackID                  string
	TenantID                 string
	LeaseID                  string
	ProviderID               string
	ResourceGenerationID     string
	ResourceGenerationDigest string
	ObservedState            string
	ReceiptRef               string
	ReceiptDigest            string
	VerifiedAt               time.Time
}

type ManagedLeaseDecommissioner interface {
	DecommissionManagedLeases(ctx context.Context, req ManagedLeaseDecommissionRequest) (*ManagedLeaseDecommissionResult, error)
}

type ManagedRuntimeTargetRequest struct {
	StackID   string
	StackName string
	StackKit  string
	TenantID  string
	OwnerID   string
	LeaseID   string
	Provider  string
	Metadata  map[string]string
}

type ManagedRuntimeTarget struct {
	Host                string
	PublicIP            string
	PrivateIP           string
	SSHUser             string
	SSHPort             int
	SSHKeyPath          string
	SSHPrivateKey       string
	SSHClientPrivateKey string
	SSHPassword         string
	DockerHost          string
	Source              string
}

type ManagedRuntimeTargetResolver interface {
	ResolveManagedRuntimeTarget(ctx context.Context, req ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error)
}

type StackKitArtifactGenerateRequest struct {
	StackID       string
	StackName     string
	StackKit      string
	WorkDir       string
	StackSpecPath string
	OutputDir     string
	RuntimeTarget *ManagedRuntimeTarget
}

type StackKitArtifactGenerateResult struct {
	StackSpecPath    string
	OutputDir        string
	ResolvedPlanPath string
	Metadata         map[string]string
}

type StackKitArtifactGenerator interface {
	GenerateStackKitArtifacts(ctx context.Context, req StackKitArtifactGenerateRequest) (*StackKitArtifactGenerateResult, error)
}

type StaticManagedRuntimeTargetResolver struct {
	Target ManagedRuntimeTarget
}

func NewStaticManagedRuntimeTargetResolver(target ManagedRuntimeTarget) *StaticManagedRuntimeTargetResolver {
	return &StaticManagedRuntimeTargetResolver{Target: target}
}

func NewStaticManagedRuntimeTargetResolverFromEnv() *StaticManagedRuntimeTargetResolver {
	host := firstNonEmpty(
		os.Getenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_HOST"),
		os.Getenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_PUBLIC_IP"),
		os.Getenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_PRIVATE_IP"),
	)
	if host == "" {
		return nil
	}
	port := 0
	if raw := strings.TrimSpace(os.Getenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_SSH_PORT")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			port = parsed
		}
	}
	return NewStaticManagedRuntimeTargetResolver(ManagedRuntimeTarget{
		Host:      host,
		PublicIP:  firstNonEmpty(os.Getenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_PUBLIC_IP"), host),
		PrivateIP: os.Getenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_PRIVATE_IP"),
		SSHUser:   firstNonEmpty(os.Getenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_SSH_USER"), "root"),
		SSHPort:   firstPositiveInt(port, 22),
		DockerHost: strings.TrimSpace(firstNonEmpty(
			os.Getenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_DOCKER_HOST"),
			os.Getenv("TECHSTACK_E2E_LOCAL_DOCKER_HOST"),
		)),
		Source: "dev-static-target",
	})
}

func (r *StaticManagedRuntimeTargetResolver) ResolveManagedRuntimeTarget(context.Context, ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error) {
	if r == nil {
		return nil, fmt.Errorf("static managed runtime target resolver is not configured")
	}
	target := r.Target
	if normalized := normalizeManagedRuntimeTarget(&target); normalized != nil {
		return normalized, nil
	}
	return nil, fmt.Errorf("static managed runtime target is missing a host")
}

type OwnerSpecBootstrap = runtimeaction.OwnerSpecBootstrap
type RuntimeActionResponse = runtimeaction.Response
type RuntimeActionTarget = runtimeaction.RuntimeTarget
type PlatformNode = runtimeaction.PlatformNode
type NodePlatformTarget = runtimeaction.NodePlatformTarget
type NodeBootstrap = runtimeaction.NodeBootstrap
type SSHBootstrap = runtimeaction.SSHBootstrap
type PreviewPolicy = runtimeaction.PreviewPolicy

type RuntimeActionRequest struct {
	Action              runtimeaction.Action `json:"action"`
	StackID             string               `json:"stack_id"`
	StackName           string               `json:"stack_name,omitempty"`
	StackKit            string               `json:"stackkit,omitempty"`
	Mode                string               `json:"mode,omitempty"`
	TenantID            string               `json:"tenant_id,omitempty"`
	OwnerID             string               `json:"owner_id,omitempty"`
	StackSpec           json.RawMessage      `json:"stack_spec,omitempty"`
	StackSpecPath       string               `json:"stack_spec_path,omitempty"`
	TofuDir             string               `json:"tofu_dir,omitempty"`
	UnifiedPath         string               `json:"unified_path,omitempty"`
	OwnerSpecBootstrap  *OwnerSpecBootstrap  `json:"owner_spec_bootstrap,omitempty"`
	RuntimeTarget       *RuntimeActionTarget `json:"runtime_target,omitempty"`
	PlatformNodes       []PlatformNode       `json:"platform_nodes,omitempty"`
	PreviewPolicy       *PreviewPolicy       `json:"preview_policy,omitempty"`
	TechStackEnrollment *TechStackEnrollment `json:"techstack_enrollment,omitempty"`
}

type TechStackEnrollment struct {
	TenantID         string         `json:"tenant_id,omitempty"`
	OwnerID          string         `json:"owner_id,omitempty"`
	StackID          string         `json:"stack_id,omitempty"`
	LeaseID          string         `json:"lease_id,omitempty"`
	ServerURL        string         `json:"server_url,omitempty"`
	ServerID         string         `json:"server_id"`
	RuntimeAgentID   string         `json:"runtime_agent_id"`
	AgentToken       string         `json:"agent_token,omitempty"`
	HeartbeatURL     string         `json:"heartbeat_url,omitempty"`
	InventoryURL     string         `json:"inventory_url,omitempty"`
	ControlURLs      []string       `json:"control_urls,omitempty"`
	ChannelBootstrap map[string]any `json:"channel_bootstrap,omitempty"`
}

type RuntimeActionRunner interface {
	Run(ctx context.Context, req RuntimeActionRequest) error
}

type RuntimeActionResultRunner interface {
	RunWithResult(ctx context.Context, req RuntimeActionRequest) (map[string]interface{}, error)
}

type RuntimeActionDescriptor struct {
	Action  string
	Target  string
	BaseURL string
	Path    string
}

type RuntimeActionDescriber interface {
	RuntimeActionDescriptor() RuntimeActionDescriptor
}

type RuntimeDiagnosticsRequest struct {
	Action         string
	Reason         string
	JobID          string
	StackID        string
	LeaseID        string
	Provider       string
	TargetKind     string
	RuntimeTarget  *RuntimeActionTarget
	ActionEndpoint RuntimeActionDescriptor
	Elapsed        time.Duration
	Err            error
}

type RuntimeDiagnosticsBundle struct {
	Status      string
	Reason      string
	Action      string
	Target      map[string]interface{}
	Endpoint    map[string]interface{}
	Commands    []RuntimeDiagnosticsCommand
	Error       string
	StartedAt   time.Time
	CompletedAt time.Time
	DurationMS  int64
}

type RuntimeDiagnosticsCommand struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	ExitStatus int    `json:"exit_status"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type RuntimeDiagnosticsCollector interface {
	CollectRuntimeDiagnostics(ctx context.Context, req RuntimeDiagnosticsRequest) (*RuntimeDiagnosticsBundle, error)
}

type RuntimeTargetBootstrapper interface {
	BootstrapRuntimeTarget(ctx context.Context, target *RuntimeActionTarget) (*RuntimeTargetBootstrapResult, error)
}

type StackKitPrepRunner interface {
	PrepareStackKitRuntimeTarget(ctx context.Context, req RuntimeActionRequest) (*RuntimeTargetBootstrapResult, error)
}

type RuntimeActions struct {
	LeaseManager          ManagedLeaseManager
	LeaseDecommissioner   ManagedLeaseDecommissioner
	RuntimeTargetResolver ManagedRuntimeTargetResolver
	StackKitPrepRunner    StackKitPrepRunner
	TargetBootstrapper    RuntimeTargetBootstrapper
	StackKitGenerator     StackKitArtifactGenerator
	SimulationGate        RuntimeActionRunner
	RolloutRunner         RuntimeActionRunner
	RolloutVerifier       RuntimeActionRunner
	RestoreDrill          RuntimeActionRunner
	DiagnosticsCollector  RuntimeDiagnosticsCollector
}

type VMLeaseAuthority interface {
	CreateOrUpdate(ctx context.Context, req vmleases.CreateRequest) (*vmlease.Lease, error)
}

type vmLeasePatcher interface {
	Patch(ctx context.Context, tenantID string, id vmlease.LeaseID, req vmleases.PatchRequest) (*vmlease.Lease, error)
}

type vmLeaseGetter interface {
	Get(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmlease.Lease, error)
}

type vmLeaseTenantLister interface {
	ListByTenant(ctx context.Context, tenantID string) ([]vmlease.Lease, error)
}

type vmLeaseDecommissionAuthority interface {
	vmLeaseGetter
	vmLeasePatcher
	vmLeaseTenantLister
}

type ManagedLeaseMetadataUpdater interface {
	UpdateLeaseMetadata(ctx context.Context, tenantID, leaseID string, metadata map[string]string) error
}

type VMLeaseManagerAdapter struct {
	Authority VMLeaseAuthority
	Runtime   monthlyruntime.RuntimeClient
	Now       func() time.Time
	ValidFor  time.Duration
}

func NewVMLeaseManagerAdapter(authority VMLeaseAuthority) *VMLeaseManagerAdapter {
	return &VMLeaseManagerAdapter{Authority: authority}
}

func (a *VMLeaseManagerAdapter) CreateOrBindLease(ctx context.Context, req ManagedLeaseRequest) (*ManagedLeaseResult, error) {
	if a.Authority == nil {
		return nil, vmleases.ErrEnrollmentRequired
	}
	now := time.Now().UTC()
	if a.Now != nil {
		now = a.Now().UTC()
	}
	validFor := a.ValidFor
	if validFor <= 0 {
		validFor = 30 * 24 * time.Hour
	}

	if err := providercatalog.ValidateNoLegacyProviderFields(
		req.Metadata[metadataKeyLeaseProvider],
		req.Metadata[metadataKeySimulateProviderID],
	); err != nil {
		return nil, fmt.Errorf("jobs: managed lease provider identity: %w", err)
	}
	provider, err := providercatalog.ResolveCanonicalProviderID(
		req.Provider,
		req.Metadata[metadataKeyProviderID],
	)
	if err != nil {
		return nil, fmt.Errorf("jobs: managed lease provider identity: %w", err)
	}
	stackID := strings.TrimSpace(req.StackID)
	stackName := strings.TrimSpace(req.StackName)
	if stackName == "" {
		stackName = stackID
	}
	ownerID := firstNonEmpty(req.OwnerID, "system")
	tenantID := firstNonEmpty(req.TenantID, ownerID, "self-hosted")
	stackKit := firstNonEmpty(req.StackKit, DefaultCloudKitRef)

	metadata := map[string]string{
		metadataKeyStackID:            stackID,
		"stack_name":                  stackName,
		"stackkit":                    stackKit,
		metadataKeyServerMode:         serverModeMonthlyRuntime,
		metadataKeyRuntimeLane:        serverModeMonthlyRuntime,
		metadataKeyProviderID:         provider,
		metadataKeySimulateLifecycle:  simulateLifecyclePVM,
		metadataKeyBillingCadence:     billingCadenceMonthly,
		metadataKeyVerificationStatus: verificationStatusPending,
		metadataKeyStackKitCatalogRef: stackKit,
		metadataKeyScenarioID:         strings.Join([]string{stackID, provider}, ":"),
	}
	for k, v := range req.Metadata {
		if strings.TrimSpace(k) != "" {
			metadata[k] = v
		}
	}
	metadata, err = normalizeMonthlyRuntimeMetadata(metadata, provider)
	if err != nil {
		return nil, err
	}
	monthlyruntime.StampEnrollmentStart(metadata, now)
	region := monthlyruntime.ProviderRegionFromMetadata(provider, metadata, defaultLeaseRegion)

	leaseID := vmlease.LeaseID(firstNonEmpty(metadata["lease_id"], "lease-"+sanitizeLeaseID(stackID)))
	lease := vmlease.Lease{
		ID:             leaseID,
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: ownerID, OrgID: tenantID},
		Resource:       vmlease.ResourceRef{ProviderID: provider, Region: region},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(validFor),
		RenewedAt:      now,
		Metadata:       metadata,
	}

	created, err := a.Authority.CreateOrUpdate(ctx, vmleases.CreateRequest{
		Lease:          lease,
		IdempotencyKey: strings.Join([]string{tenantID, stackID, stackRoleMain}, ":"),
	})
	if err != nil {
		return nil, err
	}
	return &ManagedLeaseResult{
		LeaseID:      string(created.ID),
		Provider:     created.Resource.ProviderID,
		DesiredState: string(created.DesiredState),
		BillingMode:  string(created.BillingMode),
		Phase:        RuntimePhaseLeaseReady,
		Target:       ManagedRuntimeTargetFromMetadata(created.Metadata),
	}, nil
}

func (a *VMLeaseManagerAdapter) UpdateLeaseMetadata(ctx context.Context, tenantID, leaseID string, metadata map[string]string) error {
	if a == nil || a.Authority == nil {
		return vmleases.ErrEnrollmentRequired
	}
	patcher, ok := a.Authority.(vmLeasePatcher)
	if !ok {
		return nil
	}
	tenantID = strings.TrimSpace(tenantID)
	leaseID = strings.TrimSpace(leaseID)
	if tenantID == "" || leaseID == "" || len(metadata) == 0 {
		return nil
	}
	_, err := patcher.Patch(ctx, tenantID, vmlease.LeaseID(leaseID), vmleases.PatchRequest{Metadata: metadata})
	return err
}

func (a *VMLeaseManagerAdapter) DecommissionManagedLeases(ctx context.Context, req ManagedLeaseDecommissionRequest) (*ManagedLeaseDecommissionResult, error) {
	result := &ManagedLeaseDecommissionResult{}
	if a == nil || a.Authority == nil {
		return result, vmleases.ErrEnrollmentRequired
	}
	authority, ok := a.Authority.(vmLeaseDecommissionAuthority)
	if !ok {
		return result, vmleases.ErrEnrollmentRequired
	}
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" {
		return result, vmleases.ErrTenantRequired
	}
	ownerID := strings.TrimSpace(req.OwnerID)
	if ownerID == "" {
		return result, monthlyruntime.ErrForbidden
	}
	candidates, err := managedLeaseDecommissionCandidates(ctx, authority, req)
	if err != nil {
		return result, err
	}
	if len(candidates) == 0 {
		return result, nil
	}
	if a.Runtime == nil {
		return result, monthlyruntime.ErrRuntimeClient
	}
	svc := &monthlyruntime.Service{Leases: authority, Runtime: a.Runtime}
	seen := map[vmlease.LeaseID]bool{}
	expectedDigest := strings.TrimSpace(req.ResourceGenerationDigest)
	for _, lease := range candidates {
		if seen[lease.ID] {
			continue
		}
		seen[lease.ID] = true
		if !monthlyruntime.IsMonthlyRuntimeMetadata(lease.Metadata) {
			result.Skipped++
			continue
		}
		reconcileClaimed := expectedDigest != ""
		if lease.CancelledAt != nil && !reconcileClaimed {
			result.Skipped++
			continue
		}
		if reconcileClaimed {
			currentDigest, digestErr := vmleases.ResourceGenerationDigest(tenantID, lease)
			if digestErr != nil {
				return result, digestErr
			}
			if currentDigest != expectedDigest || strings.TrimSpace(lease.Metadata[vmleases.MetadataKeyDecommissionClaimDigest]) != expectedDigest {
				return result, vmleases.ErrResourceGenerationSuperseded
			}
		}
		if !managedLeaseVisibleToOwner(lease, tenantID, ownerID) {
			if strings.TrimSpace(req.LeaseID) == "" {
				result.Skipped++
				continue
			}
			return result, monthlyruntime.ErrForbidden
		}
		if _, err := svc.Action(ctx, monthlyruntime.ActionRequest{
			TenantID:                         tenantID,
			UserID:                           ownerID,
			LeaseID:                          lease.ID,
			Action:                           serverruntime.RuntimeActionDecommission,
			Internal:                         reconcileClaimed,
			ExpectedResourceGenerationDigest: expectedDigest,
			ReconcileClaimedDecommission:     reconcileClaimed,
		}); err != nil {
			return result, err
		}
		result.Decommissioned++
		result.LeaseIDs = append(result.LeaseIDs, string(lease.ID))
	}
	return result, nil
}

func managedLeaseDecommissionCandidates(ctx context.Context, authority vmLeaseDecommissionAuthority, req ManagedLeaseDecommissionRequest) ([]vmlease.Lease, error) {
	tenantID := strings.TrimSpace(req.TenantID)
	stackID := strings.TrimSpace(req.StackID)
	out := []vmlease.Lease{}
	add := func(lease vmlease.Lease) {
		out = append(out, lease)
	}
	if leaseID := strings.TrimSpace(req.LeaseID); leaseID != "" {
		lease, err := authority.Get(ctx, tenantID, vmlease.LeaseID(leaseID))
		if errors.Is(err, vmleases.ErrNotFound) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		add(*lease)
		// An explicit lease id is the complete destructive allowlist. Never
		// widen it by enumerating other leases attached to the same stack.
		return out, nil
	}
	if stackID == "" {
		return out, nil
	}
	leases, err := authority.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, lease := range leases {
		if strings.TrimSpace(lease.Metadata[metadataKeyStackID]) == stackID {
			add(lease)
		}
	}
	return out, nil
}

func managedLeaseVisibleToOwner(lease vmlease.Lease, tenantID, ownerID string) bool {
	if strings.TrimSpace(lease.Subject.OrgID) != strings.TrimSpace(tenantID) {
		return false
	}
	if strings.TrimSpace(ownerID) == "" {
		return false
	}
	if strings.TrimSpace(lease.Subject.ID) == strings.TrimSpace(ownerID) {
		return true
	}
	return lease.Subject.Kind == vmlease.SubjectOrg
}

type MonthlyRuntimeTargetResolver struct {
	Service interface {
		Action(context.Context, monthlyruntime.ActionRequest) (*monthlyruntime.RuntimeResponse, error)
	}
	Leases interface {
		GetInventory(context.Context, string, vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error)
	}
	Servers interface {
		GetServerRuntime(context.Context, string, string) (*controlplane.ServerRuntime, error)
	}
	CredentialDecryptor func(string) (string, error)
}

func NewMonthlyRuntimeTargetResolver(service interface {
	Action(context.Context, monthlyruntime.ActionRequest) (*monthlyruntime.RuntimeResponse, error)
}, servers ...interface {
	GetServerRuntime(context.Context, string, string) (*controlplane.ServerRuntime, error)
}) *MonthlyRuntimeTargetResolver {
	resolver := &MonthlyRuntimeTargetResolver{Service: service, CredentialDecryptor: defaultManagedRuntimeCredentialDecryptor}
	if len(servers) > 0 {
		resolver.Servers = servers[0]
	}
	if svc, ok := service.(*monthlyruntime.Service); ok {
		if inventory, inventoryOK := svc.Leases.(interface {
			GetInventory(context.Context, string, vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error)
		}); inventoryOK {
			resolver.Leases = inventory
		}
	}
	return resolver
}

func (r *MonthlyRuntimeTargetResolver) ResolveManagedRuntimeTarget(ctx context.Context, req ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error) {
	if r == nil || r.Service == nil {
		return nil, fmt.Errorf("monthly runtime target resolver is not configured")
	}
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required to resolve managed runtime target")
	}
	leaseID := strings.TrimSpace(req.LeaseID)
	if leaseID == "" {
		return nil, fmt.Errorf("lease_id is required to resolve managed runtime target")
	}
	var metadataFallback *ManagedRuntimeTarget
	if target, pendingErr := r.leaseMetadataTargetOrPending(ctx, tenantID, leaseID); target != nil || pendingErr != nil {
		if target == nil {
			if canonical, canonicalErr := r.canonicalServerTarget(ctx, tenantID, leaseID); canonical != nil || canonicalErr != nil {
				if canonicalErr != nil {
					return nil, canonicalErr
				}
				canonical = attachManagedProviderCredential(canonical, req.Provider)
				if managedRuntimeTargetHasRuntimeActionCredential(canonical) {
					return canonical, nil
				}
				metadataFallback = canonical
			} else {
				return nil, pendingErr
			}
		} else {
			target = attachManagedProviderCredential(target, req.Provider)
			if managedRuntimeTargetHasRuntimeActionCredential(target) {
				return target, nil
			}
			metadataFallback = target
		}
	} else if canonical, canonicalErr := r.canonicalServerTarget(ctx, tenantID, leaseID); canonical != nil || canonicalErr != nil {
		if canonicalErr != nil {
			return nil, canonicalErr
		}
		canonical = attachManagedProviderCredential(canonical, req.Provider)
		if managedRuntimeTargetHasRuntimeActionCredential(canonical) {
			return canonical, nil
		}
		metadataFallback = canonical
	}
	if err := r.primeManagedRuntimeAddress(ctx, tenantID, strings.TrimSpace(req.OwnerID), leaseID); err == nil {
		if target, pendingErr := r.leaseMetadataTargetOrPending(ctx, tenantID, leaseID); target != nil || pendingErr != nil {
			if target == nil {
				return nil, pendingErr
			}
			target = attachManagedProviderCredential(target, req.Provider)
			if managedRuntimeTargetHasRuntimeActionCredential(target) {
				return target, nil
			}
			metadataFallback = target
		}
	}
	resp, err := r.Service.Action(ctx, monthlyruntime.ActionRequest{
		TenantID: tenantID,
		UserID:   strings.TrimSpace(req.OwnerID),
		LeaseID:  vmlease.LeaseID(leaseID),
		Action:   serverruntime.RuntimeActionSSHInfo,
		// Background rollout target resolution for an already-authorized lease:
		// entitlement was enforced at stack/lease creation and cannot be re-checked
		// here (no SaaS edge entitlement headers in the deploy job context).
		Internal: true,
	})
	if err != nil {
		if errors.Is(err, monthlyruntime.ErrEnrollmentPending) {
			if terminalErr := r.leaseEnrollmentFailure(ctx, tenantID, leaseID); terminalErr != nil {
				return nil, terminalErr
			}
			if pendingErr := r.leaseEnrollmentPendingCause(ctx, tenantID, leaseID, err); pendingErr != nil {
				return nil, pendingErr
			}
		}
		if metadataFallback != nil && managedRuntimeTargetCredentialLookupTimedOut(err) {
			return nil, managedRuntimeTargetCredentialUnavailableAfterRuntimeActionError(leaseID, metadataFallback, err)
		}
		return nil, err
	}
	target := ManagedRuntimeTargetFromRuntimeResponse(resp)
	if target != nil {
		target.Source = firstNonEmpty(target.Source, "monthly-runtime")
		target = attachManagedProviderCredential(target, req.Provider)
		if !managedRuntimeTargetHasRuntimeActionCredential(target) {
			return nil, managedRuntimeTargetCredentialUnavailableError(leaseID, target)
		}
		return target, nil
	}
	if metadataFallback != nil {
		return nil, managedRuntimeTargetCredentialUnavailableError(leaseID, metadataFallback)
	}
	return nil, nil
}

func (r *MonthlyRuntimeTargetResolver) canonicalServerTarget(ctx context.Context, tenantID, leaseID string) (*ManagedRuntimeTarget, error) {
	if r == nil || r.Servers == nil {
		return nil, nil
	}
	server, err := r.Servers.GetServerRuntime(ctx, tenantID, runtimeidentity.LeaseServerID(leaseID))
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	target := ManagedRuntimeTargetFromMetadata(stringMapFromAny(server.Metadata))
	if target == nil {
		return nil, nil
	}
	lease, err := r.nativeActiveLease(ctx, tenantID, leaseID)
	if err != nil {
		return nil, err
	}
	target, err = r.enrichTargetFromLeaseMetadata(target, lease.Metadata)
	if err != nil {
		return nil, err
	}
	target.Source = "canonical-server"
	return target, nil
}

func (r *MonthlyRuntimeTargetResolver) primeManagedRuntimeAddress(ctx context.Context, tenantID, ownerID, leaseID string) error {
	if r == nil || r.Service == nil {
		return nil
	}
	_, err := r.Service.Action(ctx, monthlyruntime.ActionRequest{
		TenantID: tenantID,
		UserID:   strings.TrimSpace(ownerID),
		LeaseID:  vmlease.LeaseID(strings.TrimSpace(leaseID)),
		Action:   serverruntime.RuntimeActionStatus,
		Internal: true,
	})
	return err
}

func (r *MonthlyRuntimeTargetResolver) leaseMetadataTargetOrPending(ctx context.Context, tenantID, leaseID string) (*ManagedRuntimeTarget, error) {
	if r == nil || r.Leases == nil {
		return nil, nil
	}
	lease, err := r.nativeActiveLease(ctx, tenantID, leaseID)
	if err != nil {
		return nil, err
	}
	if target := ManagedRuntimeTargetFromMetadata(lease.Metadata); target != nil {
		enriched, err := r.enrichTargetFromLeaseMetadata(target, lease.Metadata)
		if err != nil {
			return nil, err
		}
		target = enriched
		target.Source = firstNonEmpty(target.Source, "lease-metadata")
		return target, nil
	}
	status := strings.ToLower(strings.TrimSpace(lease.Metadata[metadataKeyRuntimeEnrollState]))
	switch status {
	case runtimeEnrollmentStatusFailed:
		return nil, r.leaseEnrollmentFailureFromLease(*lease, leaseID)
	case runtimeEnrollmentStatusPending, runtimeEnrollmentStatusRetrying:
		return nil, r.leaseEnrollmentPendingCauseFromLease(*lease, leaseID, monthlyruntime.ErrEnrollmentPending)
	default:
		return nil, nil
	}
}

func (r *MonthlyRuntimeTargetResolver) nativeActiveLease(ctx context.Context, tenantID, leaseID string) (*vmlease.Lease, error) {
	if r == nil || r.Leases == nil {
		return nil, vmleases.ErrLeaseInventoryUnavailable
	}
	record, err := r.Leases.GetInventory(ctx, strings.TrimSpace(tenantID), vmlease.LeaseID(strings.TrimSpace(leaseID)))
	if err != nil {
		return nil, err
	}
	if record == nil || !record.NativeActive() {
		return nil, monthlyruntime.ErrExecutionAuthorityInactive
	}
	lease := record.Lease
	return &lease, nil
}

func (r *MonthlyRuntimeTargetResolver) enrichTargetFromLeaseMetadata(target *ManagedRuntimeTarget, metadata map[string]string) (*ManagedRuntimeTarget, error) {
	target = cloneManagedRuntimeTarget(target)
	if target == nil || metadata == nil {
		return target, nil
	}
	var err error
	if target.SSHPrivateKey == "" {
		if target.SSHPrivateKey, err = r.decryptLeaseCredential(metadata, metadataKeyRuntimeSSHPrivateKey, "runtime_ssh_private_key"); err != nil {
			return nil, err
		}
	}
	if target.SSHClientPrivateKey == "" {
		if target.SSHClientPrivateKey, err = r.decryptLeaseCredential(metadata, metadataKeyRuntimeClientKey, "runtime_client_private_key"); err != nil {
			return nil, err
		}
	}
	if target.SSHPassword == "" {
		if target.SSHPassword, err = r.decryptLeaseCredential(metadata, metadataKeyRuntimeSSHPassword, "runtime_ssh_password"); err != nil {
			return nil, err
		}
	}
	return normalizeManagedRuntimeTarget(target), nil
}

var managedProviderCredentialResolver = func(string, time.Time) (string, error) {
	return "", nil
}

func attachManagedProviderCredential(target *ManagedRuntimeTarget, provider string) *ManagedRuntimeTarget {
	target = cloneManagedRuntimeTarget(target)
	if target == nil || target.SSHPrivateKey != "" {
		return target
	}
	privateKey, err := managedProviderCredentialResolver(strings.TrimSpace(provider), time.Now().UTC())
	if err == nil {
		target.SSHPrivateKey = privateKey
	}
	return normalizeManagedRuntimeTarget(target)
}

func (r *MonthlyRuntimeTargetResolver) decryptLeaseCredential(metadata map[string]string, keys ...string) (string, error) {
	for _, key := range keys {
		value := strings.TrimSpace(metadata[key])
		if value == "" {
			continue
		}
		if !auth.IsEncrypted(value) {
			return "", fmt.Errorf("%w: lease metadata %q must be encrypted for StackKits rollout handoff", ErrManagedRuntimeTargetCredentialFailed, key)
		}
		decrypt := defaultManagedRuntimeCredentialDecryptor
		if r != nil && r.CredentialDecryptor != nil {
			decrypt = r.CredentialDecryptor
		}
		plain, err := decrypt(value)
		if err != nil {
			return "", fmt.Errorf("%w: decrypt lease metadata %q: %v", ErrManagedRuntimeTargetCredentialFailed, key, err)
		}
		return strings.TrimSpace(plain), nil
	}
	return "", nil
}

func defaultManagedRuntimeCredentialDecryptor(value string) (string, error) {
	return auth.DecryptIfNeeded(auth.GetEncryptor(), value)
}

func (r *MonthlyRuntimeTargetResolver) leaseEnrollmentFailure(ctx context.Context, tenantID, leaseID string) error {
	if r == nil || r.Leases == nil {
		return nil
	}
	lease, err := r.nativeActiveLease(ctx, tenantID, leaseID)
	if err != nil {
		return err
	}
	return r.leaseEnrollmentFailureFromLease(*lease, leaseID)
}

func (r *MonthlyRuntimeTargetResolver) leaseEnrollmentFailureFromLease(lease vmlease.Lease, leaseID string) error {
	status := strings.ToLower(strings.TrimSpace(lease.Metadata[metadataKeyRuntimeEnrollState]))
	if status != runtimeEnrollmentStatusFailed {
		return nil
	}
	cause := strings.TrimSpace(lease.Metadata[metadataKeyRuntimeEnrollError])
	if cause == "" {
		cause = "runtime enrollment status is failed"
	}
	return fmt.Errorf("%w for lease %q: %s", ErrManagedRuntimeEnrollmentFailed, leaseID, cause)
}

func (r *MonthlyRuntimeTargetResolver) leaseEnrollmentPendingCause(ctx context.Context, tenantID, leaseID string, original error) error {
	if r == nil || r.Leases == nil {
		return nil
	}
	lease, err := r.nativeActiveLease(ctx, tenantID, leaseID)
	if err != nil {
		return err
	}
	return r.leaseEnrollmentPendingCauseFromLease(*lease, leaseID, original)
}

func (r *MonthlyRuntimeTargetResolver) leaseEnrollmentPendingCauseFromLease(lease vmlease.Lease, leaseID string, original error) error {
	status := strings.ToLower(strings.TrimSpace(lease.Metadata[metadataKeyRuntimeEnrollState]))
	switch status {
	case runtimeEnrollmentStatusFailed, runtimeEnrollmentStatusPending:
		if status == runtimeEnrollmentStatusFailed {
			return nil
		}
	case runtimeEnrollmentStatusRetrying:
	default:
		if status == "" {
			return nil
		}
	}
	cause := strings.TrimSpace(lease.Metadata[metadataKeyRuntimeEnrollError])
	if cause == "" {
		if status == runtimeEnrollmentStatusPending {
			return fmt.Errorf("managed runtime lease enrollment pending for lease %q: %w", leaseID, original)
		}
		return nil
	}
	return fmt.Errorf("managed runtime lease enrollment %s for lease %q: %s: %w", status, leaseID, cause, original)
}

func ManagedRuntimeTargetFromMetadata(metadata map[string]string) *ManagedRuntimeTarget {
	if metadata == nil {
		return nil
	}
	target := &ManagedRuntimeTarget{
		Host: firstNonEmpty(
			metadata[metadataKeyRuntimeSSHHost],
			metadata["ssh_host"],
			metadata["host"],
			metadata[metadataKeyRuntimePublicIP],
			metadata["node_public_ip"],
			metadata["public_ip"],
			metadata[metadataKeyRuntimePrivateIP],
			metadata["node_private_ip"],
			metadata["private_ip"],
		),
		PublicIP: firstNonEmpty(metadata[metadataKeyRuntimePublicIP], metadata["node_public_ip"], metadata["public_ip"]),
		PrivateIP: firstNonEmpty(
			metadata[metadataKeyRuntimePrivateIP],
			metadata["node_private_ip"],
			metadata["private_ip"],
		),
		SSHUser: firstNonEmpty(metadata[metadataKeyRuntimeSSHUser], metadata["ssh_user"]),
		SSHPort: firstPositiveInt(
			parseMetadataInt(metadata, metadataKeyRuntimeSSHPort),
			parseMetadataInt(metadata, "ssh_port"),
		),
		DockerHost: firstNonEmpty(metadata["runtime_docker_host"], metadata["docker_host"]),
		Source:     "lease-metadata",
	}
	if normalized := normalizeManagedRuntimeTarget(target); normalized != nil {
		return normalized
	}
	return nil
}

func ManagedRuntimeTargetFromRuntimeResponse(resp *monthlyruntime.RuntimeResponse) *ManagedRuntimeTarget {
	if resp == nil {
		return nil
	}
	target := &ManagedRuntimeTarget{Source: "runtime-response"}
	if resp.SSH != nil {
		target.Host = firstNonEmpty(resp.SSH.Host, resp.SSH.DisplayHost, resp.SSH.NodePublicIP, resp.SSH.NodePrivateIP)
		target.PublicIP = strings.TrimSpace(resp.SSH.NodePublicIP)
		target.PrivateIP = strings.TrimSpace(resp.SSH.NodePrivateIP)
		target.SSHUser = strings.TrimSpace(resp.SSH.User)
		target.SSHPort = resp.SSH.Port
		target.SSHKeyPath = strings.TrimSpace(resp.SSH.KeyPath)
		target.SSHPrivateKey = strings.TrimSpace(resp.SSH.PrivateKey)
		target.SSHClientPrivateKey = strings.TrimSpace(resp.SSH.ClientPrivateKey)
		target.SSHPassword = strings.TrimSpace(resp.SSH.Password)
	}
	if resp.Status != nil {
		target.PublicIP = firstNonEmpty(target.PublicIP, resp.Status.PublicIP)
		target.PrivateIP = firstNonEmpty(target.PrivateIP, resp.Status.PrivateIP)
		target.Host = firstNonEmpty(target.Host, resp.Status.PublicIP, resp.Status.PrivateIP)
	}
	if normalized := normalizeManagedRuntimeTarget(target); normalized != nil {
		return normalized
	}
	return nil
}

func normalizeManagedRuntimeTarget(target *ManagedRuntimeTarget) *ManagedRuntimeTarget {
	if target == nil {
		return nil
	}
	target.Host = strings.TrimSpace(target.Host)
	target.PublicIP = strings.TrimSpace(target.PublicIP)
	target.PrivateIP = strings.TrimSpace(target.PrivateIP)
	target.SSHUser = strings.TrimSpace(target.SSHUser)
	target.SSHKeyPath = strings.TrimSpace(target.SSHKeyPath)
	target.SSHPrivateKey = strings.TrimSpace(target.SSHPrivateKey)
	target.SSHClientPrivateKey = strings.TrimSpace(target.SSHClientPrivateKey)
	target.SSHPassword = strings.TrimSpace(target.SSHPassword)
	target.DockerHost = strings.TrimSpace(target.DockerHost)
	target.Source = strings.TrimSpace(target.Source)
	if target.Host == "" {
		target.Host = firstNonEmpty(target.PublicIP, target.PrivateIP)
	}
	if target.PublicIP == "" {
		target.PublicIP = target.Host
	}
	if target.SSHPort <= 0 {
		target.SSHPort = 22
	}
	if target.Host == "" {
		return nil
	}
	return target
}

func runtimeActionTargetFromManagedRuntimeTarget(target *ManagedRuntimeTarget) *RuntimeActionTarget {
	target = normalizeManagedRuntimeTarget(target)
	if target == nil {
		return nil
	}
	keyPath := target.SSHKeyPath
	if firstNonEmpty(target.SSHClientPrivateKey, target.SSHPrivateKey, target.SSHPassword) != "" {
		keyPath = ""
	}
	return normalizeRuntimeActionTarget(&RuntimeActionTarget{
		Host:             target.Host,
		PublicIP:         target.PublicIP,
		PrivateIP:        target.PrivateIP,
		User:             target.SSHUser,
		Port:             target.SSHPort,
		KeyPath:          keyPath,
		PrivateKey:       target.SSHPrivateKey,
		ClientPrivateKey: target.SSHClientPrivateKey,
		Password:         target.SSHPassword,
		DockerHost:       target.DockerHost,
	})
}

func stackKitsRuntimeActionTargetFromManagedRuntimeTarget(target *ManagedRuntimeTarget) *RuntimeActionTarget {
	target = normalizeManagedRuntimeTarget(target)
	if target == nil {
		return nil
	}
	if target.DockerHost != "" &&
		!managedRuntimeTargetHasSSHCredential(target) &&
		target.DockerHost == strings.TrimSpace(os.Getenv("DOCKER_HOST")) {
		return nil
	}
	return runtimeActionTargetFromManagedRuntimeTarget(target)
}

func normalizeRuntimeActionTarget(target *RuntimeActionTarget) *RuntimeActionTarget {
	if target == nil {
		return nil
	}
	normalized := *target
	normalized.Host = strings.TrimSpace(normalized.Host)
	normalized.PublicIP = strings.TrimSpace(normalized.PublicIP)
	normalized.PrivateIP = strings.TrimSpace(normalized.PrivateIP)
	normalized.User = strings.TrimSpace(normalized.User)
	normalized.DockerHost = strings.TrimSpace(normalized.DockerHost)
	normalized.KeyPath = strings.TrimSpace(normalized.KeyPath)
	normalized.PrivateKey = strings.TrimSpace(normalized.PrivateKey)
	normalized.ClientPrivateKey = strings.TrimSpace(normalized.ClientPrivateKey)
	normalized.Password = strings.TrimSpace(normalized.Password)
	if normalized.Host == "" {
		normalized.Host = firstNonEmpty(normalized.PublicIP, normalized.PrivateIP)
	}
	if normalized.PublicIP == "" {
		normalized.PublicIP = normalized.Host
	}
	if normalized.User == "" {
		normalized.User = "root"
	}
	if normalized.Port <= 0 {
		normalized.Port = 22
	}
	if normalized.Host == "" {
		return nil
	}
	return &normalized
}

func managedRuntimeTargetHasRuntimeActionCredential(target *ManagedRuntimeTarget) bool {
	target = normalizeManagedRuntimeTarget(target)
	// KeyPath is intentionally excluded: provider key paths are local to the
	// VM authority/Simulate container and cannot be dereferenced by TechStack or
	// the StackKits runtime action service in production.
	return target != nil && firstNonEmpty(target.DockerHost, target.SSHClientPrivateKey, target.SSHPrivateKey, target.SSHPassword) != ""
}

func managedRuntimeTargetHasSSHCredential(target *ManagedRuntimeTarget) bool {
	target = normalizeManagedRuntimeTarget(target)
	return target != nil && firstNonEmpty(target.SSHClientPrivateKey, target.SSHPrivateKey, target.SSHPassword) != ""
}

func runtimeActionTargetHasSSHCredential(target *RuntimeActionTarget) bool {
	target = normalizeRuntimeActionTarget(target)
	return target != nil && firstNonEmpty(target.ClientPrivateKey, target.PrivateKey, target.Password, target.KeyPath) != ""
}

func managedRuntimeTargetCredentialUnavailableError(leaseID string, target *ManagedRuntimeTarget) error {
	source := "managed-runtime"
	keyPathOnly := false
	if target != nil {
		source = firstNonEmpty(target.Source, source)
		keyPathOnly = strings.TrimSpace(target.SSHKeyPath) != "" &&
			firstNonEmpty(target.SSHClientPrivateKey, target.SSHPrivateKey, target.SSHPassword, target.DockerHost) == ""
	}
	detail := "RuntimeActionSSHInfo must provide private_key, client_private_key, password, or docker_host for StackKits rollout"
	if keyPathOnly {
		detail += "; provider-local key_path alone is not usable across TechStack/StackKits service boundaries"
	}
	if strings.TrimSpace(leaseID) == "" {
		return fmt.Errorf("%w: %s target has no transportable rollout credential: %s", ErrManagedRuntimeTargetCredentialFailed, source, detail)
	}
	return fmt.Errorf("%w for lease %q: %s target has no transportable rollout credential: %s", ErrManagedRuntimeTargetCredentialFailed, leaseID, source, detail)
}

func managedRuntimeTargetCredentialUnavailableAfterRuntimeActionError(leaseID string, target *ManagedRuntimeTarget, cause error) error {
	if managedRuntimeTargetCredentialLookupTimedOut(cause) {
		source := "managed-runtime"
		if target != nil {
			source = firstNonEmpty(target.Source, source)
		}
		detail := "RuntimeActionSSHInfo must provide private_key, client_private_key, password, or docker_host for StackKits rollout"
		if strings.TrimSpace(leaseID) == "" {
			return fmt.Errorf("managed runtime target credential lookup pending: %s target has no transportable rollout credential yet: %s; RuntimeActionSSHInfo failed before returning a transportable credential: %w", source, detail, cause)
		}
		return fmt.Errorf("managed runtime target credential lookup pending for lease %q: %s target has no transportable rollout credential yet: %s; RuntimeActionSSHInfo failed before returning a transportable credential: %w", leaseID, source, detail, cause)
	}
	err := managedRuntimeTargetCredentialUnavailableError(leaseID, target)
	if cause == nil {
		return err
	}
	return fmt.Errorf("%w; RuntimeActionSSHInfo failed before returning a transportable credential: %v", err, cause)
}

func managedRuntimeTargetCredentialLookupTimedOut(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "context deadline exceeded") ||
		strings.Contains(text, "timed out") ||
		strings.Contains(text, "timeout")
}

func parseMetadataInt(metadata map[string]string, key string) int {
	if metadata == nil {
		return 0
	}
	value := strings.TrimSpace(metadata[key])
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func normalizeMonthlyRuntimeMetadata(metadata map[string]string, provider string) (map[string]string, error) {
	if metadata == nil {
		metadata = map[string]string{}
	}
	if err := providercatalog.ValidateNoLegacyProviderFields(
		metadata[metadataKeyLeaseProvider],
		metadata[metadataKeySimulateProviderID],
	); err != nil {
		return nil, fmt.Errorf("jobs: managed runtime metadata: %w", err)
	}
	provider, err := providercatalog.ResolveCanonicalProviderID(provider, metadata[metadataKeyProviderID])
	if err != nil {
		return nil, fmt.Errorf("jobs: managed runtime metadata: %w", err)
	}
	metadata[metadataKeyProviderID] = provider
	metadata, err = monthlyruntime.NormalizeFreshMetadata(metadata, monthlyruntime.OfferingIDFromMetadata(metadata))
	if err != nil {
		return nil, err
	}
	if metadata[metadataKeyServerMode] == serverModeManagedCloud {
		metadata[metadataKeyServerMode] = serverModeMonthlyRuntime
	}
	if strings.TrimSpace(metadata[metadataKeyServerMode]) == "" {
		metadata[metadataKeyServerMode] = serverModeMonthlyRuntime
	}
	if strings.TrimSpace(metadata[metadataKeyRuntimeLane]) == "" {
		metadata[metadataKeyRuntimeLane] = serverModeMonthlyRuntime
	}
	if strings.TrimSpace(metadata[metadataKeySimulateLifecycle]) == "" {
		metadata[metadataKeySimulateLifecycle] = simulateLifecyclePVM
	}
	if strings.TrimSpace(metadata[metadataKeyBillingMode]) == "" {
		metadata[metadataKeyBillingMode] = billingModeSubscription
	}
	if strings.TrimSpace(metadata[metadataKeyBillingCadence]) == "" {
		metadata[metadataKeyBillingCadence] = billingCadenceMonthly
	}
	if strings.TrimSpace(metadata[metadataKeyRuntimeOfferingID]) == "" {
		metadata[metadataKeyRuntimeOfferingID] = defaultRuntimeOfferingID
	}
	return metadata, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func sanitizeLeaseID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "stack"
	}
	return out
}
