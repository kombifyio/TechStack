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

	"github.com/kombifyio/techstack/internal/guardbootstrap"
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
	Host                  string
	PublicIP              string
	PrivateIP             string
	SSHUser               string
	SSHPort               int
	SSHKeyPath            string
	SSHPrivateKey         string
	SSHClientPrivateKey   string
	SSHPassword           string
	SSHProviderPrivateKey string
	// SSHHostKey is provider/runtime custody evidence, never a key discovered
	// opportunistically by a browser session. Interactive access fails closed
	// when this value is absent.
	SSHHostKey string
	DockerHost string
	Source     string
}

type ManagedRuntimeTargetResolver interface {
	ResolveManagedRuntimeTarget(ctx context.Context, req ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error)
}

type StackKitArtifactGenerateRequest struct {
	StackID string
	// TenantID and OwnerID bind the stack's managed kombify.me addresses to
	// their owner; the free-text StackName is display-only.
	TenantID      string
	OwnerID       string
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
		SSHUser:   firstNonEmpty(os.Getenv("TECHSTACK_DEV_MONTHLY_RUNTIME_TARGET_SSH_USER"), guardbootstrap.ExecutionChannelUser),
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
	TenantID       string
	LeaseID        string
	OperationID    string
	ServerID       string
	RuntimeAgentID string
	Provider       string
	TargetKind     string
	RuntimeTarget  *RuntimeActionTarget
	ActionEndpoint RuntimeActionDescriptor
	Elapsed        time.Duration
	Err            error
}

type RuntimeDiagnosticsBinding struct {
	JobID          string
	StackID        string
	TenantID       string
	LeaseID        string
	OperationID    string
	ServerID       string
	RuntimeAgentID string
	Provider       string
}

type RuntimeDiagnosticsBundle struct {
	Status      string
	Reason      string
	Action      string
	Binding     RuntimeDiagnosticsBinding
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
	// BackupRunner dispatches one backup_run to the node. It is separate from
	// RestoreDrill because a drill proves a repository can be restored while a
	// run is the cost-bearing action the entitlement gate guards.
	BackupRunner RuntimeActionRunner
	// BackupStatus reads the node's view of the repository. It never admits or
	// denies: the billing meter is the control-plane object-store sum, and the
	// node reports the customer-facing protected-data figure only.
	BackupStatus         RuntimeActionRunner
	DiagnosticsCollector RuntimeDiagnosticsCollector
}

type ManagedLeaseMetadataUpdater interface {
	UpdateLeaseMetadata(ctx context.Context, tenantID, leaseID string, metadata map[string]string) error
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
	ready, metadataFallback, err := r.resolvePersistedManagedRuntimeTarget(ctx, tenantID, leaseID, req.Provider)
	if err != nil || ready != nil {
		return ready, err
	}
	ready, metadataFallback, err = r.resolvePrimedManagedRuntimeTarget(
		ctx, tenantID, strings.TrimSpace(req.OwnerID), leaseID, req.Provider, metadataFallback,
	)
	if err != nil || ready != nil {
		return ready, err
	}
	return r.resolveManagedRuntimeSSHInfo(ctx, tenantID, leaseID, req, metadataFallback)
}

func (r *MonthlyRuntimeTargetResolver) resolvePersistedManagedRuntimeTarget(
	ctx context.Context,
	tenantID, leaseID string,
	provider string,
) (*ManagedRuntimeTarget, *ManagedRuntimeTarget, error) {
	metadata, pendingErr := r.leaseMetadataTargetOrPending(ctx, tenantID, leaseID)
	if metadata != nil {
		candidate, ready := r.prepareManagedRuntimeTargetCandidate(ctx, tenantID, leaseID, metadata, provider)
		if ready {
			return candidate, nil, nil
		}
		return nil, candidate, nil
	}
	canonical, err := r.canonicalServerTarget(ctx, tenantID, leaseID)
	if err != nil {
		return nil, nil, err
	}
	if canonical != nil {
		candidate, ready := r.prepareManagedRuntimeTargetCandidate(ctx, tenantID, leaseID, canonical, provider)
		if ready {
			return candidate, nil, nil
		}
		return nil, candidate, nil
	}
	if pendingErr != nil {
		return nil, nil, pendingErr
	}
	return nil, nil, nil
}

func (r *MonthlyRuntimeTargetResolver) resolvePrimedManagedRuntimeTarget(
	ctx context.Context,
	tenantID, ownerID, leaseID string,
	provider string,
	fallback *ManagedRuntimeTarget,
) (*ManagedRuntimeTarget, *ManagedRuntimeTarget, error) {
	if err := r.primeManagedRuntimeAddress(ctx, tenantID, ownerID, leaseID); err != nil {
		return nil, fallback, nil
	}
	target, pendingErr := r.leaseMetadataTargetOrPending(ctx, tenantID, leaseID)
	if target == nil && pendingErr == nil {
		return nil, fallback, nil
	}
	if target == nil {
		return nil, nil, pendingErr
	}
	candidate, ready := r.prepareManagedRuntimeTargetCandidate(ctx, tenantID, leaseID, target, provider)
	if ready {
		return candidate, nil, nil
	}
	return nil, candidate, nil
}

func (r *MonthlyRuntimeTargetResolver) prepareManagedRuntimeTargetCandidate(
	ctx context.Context,
	tenantID, leaseID string,
	target *ManagedRuntimeTarget,
	provider string,
) (*ManagedRuntimeTarget, bool) {
	target = attachManagedProviderCredential(target, provider)
	target = r.overlayObservedPublicIP(ctx, tenantID, leaseID, target)
	return target, managedRuntimeTargetHasRuntimeActionCredential(target)
}

func (r *MonthlyRuntimeTargetResolver) resolveManagedRuntimeSSHInfo(
	ctx context.Context,
	tenantID, leaseID string,
	req ManagedRuntimeTargetRequest,
	metadataFallback *ManagedRuntimeTarget,
) (*ManagedRuntimeTarget, error) {
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
		target, ready := r.prepareManagedRuntimeTargetCandidate(ctx, tenantID, leaseID, target, req.Provider)
		if !ready {
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

func (r *MonthlyRuntimeTargetResolver) overlayObservedPublicIP(ctx context.Context, tenantID, leaseID string, target *ManagedRuntimeTarget) *ManagedRuntimeTarget {
	return applyObservedPublicIP(target, r.observedGuardPublicIP(ctx, tenantID, leaseID))
}

func (r *MonthlyRuntimeTargetResolver) observedGuardPublicIP(ctx context.Context, tenantID, leaseID string) string {
	if r == nil || r.Servers == nil {
		return ""
	}
	server, err := r.Servers.GetServerRuntime(ctx, tenantID, runtimeidentity.LeaseServerID(leaseID))
	if err != nil || server == nil {
		return ""
	}
	return observedPublicIPFromServerMetadata(server.Metadata)
}

func observedPublicIPFromServerMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	host := mapFromInterface(metadata["host"])
	return firstNonEmpty(stringFromMap(host, "public_ip"), stringFromMap(metadata, "public_ip"))
}

func applyObservedPublicIP(target *ManagedRuntimeTarget, observedIP string) *ManagedRuntimeTarget {
	target = cloneManagedRuntimeTarget(target)
	observedIP = strings.TrimSpace(observedIP)
	if target == nil || observedIP == "" {
		return target
	}
	if target.Host == observedIP && (target.PublicIP == "" || target.PublicIP == observedIP) {
		return target
	}
	target.Host = observedIP
	target.PublicIP = observedIP
	if !strings.Contains(target.Source, "guard-inventory") {
		target.Source = firstNonEmpty(target.Source, "lease-metadata") + "+guard-inventory"
	}
	return normalizeManagedRuntimeTarget(target)
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
	if target == nil {
		return nil
	}
	privateKey, err := managedProviderCredentialResolver(strings.TrimSpace(provider), time.Now().UTC())
	if err != nil {
		return normalizeManagedRuntimeTarget(target)
	}
	privateKey = strings.TrimSpace(privateKey)
	if privateKey == "" {
		return normalizeManagedRuntimeTarget(target)
	}
	if target.SSHPrivateKey == "" {
		target.SSHPrivateKey = privateKey
		return normalizeManagedRuntimeTarget(target)
	}
	if target.SSHPrivateKey != privateKey {
		target.SSHProviderPrivateKey = privateKey
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
		SSHHostKey: firstNonEmpty(metadata["runtime_ssh_host_key"], metadata["ssh_host_key"]),
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
		target.SSHHostKey = strings.TrimSpace(resp.SSH.HostKey)
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
	target.SSHProviderPrivateKey = strings.TrimSpace(target.SSHProviderPrivateKey)
	target.SSHHostKey = strings.TrimSpace(target.SSHHostKey)
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
	target.SSHUser = managedCloudExecutionSSHUser(target.SSHUser)
	if target.Host == "" {
		return nil
	}
	return target
}

// managedCloudExecutionSSHUser fills an empty login with the Cloud
// execution-channel account. An explicit root login is left in place so the
// first bootstrap attempt can still use it on a host that has not yet been
// hardened; later attempts continue as kombify and ubuntu.
func managedCloudExecutionSSHUser(user string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		return guardbootstrap.ExecutionChannelUser
	}
	return user
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
		Host:               target.Host,
		PublicIP:           target.PublicIP,
		PrivateIP:          target.PrivateIP,
		User:               target.SSHUser,
		Port:               target.SSHPort,
		KeyPath:            keyPath,
		PrivateKey:         target.SSHPrivateKey,
		ClientPrivateKey:   target.SSHClientPrivateKey,
		Password:           target.SSHPassword,
		ProviderPrivateKey: target.SSHProviderPrivateKey,
		DockerHost:         target.DockerHost,
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
	normalized.ProviderPrivateKey = strings.TrimSpace(normalized.ProviderPrivateKey)
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
	return target != nil && firstNonEmpty(target.DockerHost, target.SSHClientPrivateKey, target.SSHPrivateKey, target.SSHProviderPrivateKey, target.SSHPassword) != ""
}

func managedRuntimeTargetHasSSHCredential(target *ManagedRuntimeTarget) bool {
	target = normalizeManagedRuntimeTarget(target)
	return target != nil && firstNonEmpty(target.SSHClientPrivateKey, target.SSHPrivateKey, target.SSHProviderPrivateKey, target.SSHPassword) != ""
}

func runtimeActionTargetHasSSHCredential(target *RuntimeActionTarget) bool {
	target = normalizeRuntimeActionTarget(target)
	return target != nil && firstNonEmpty(target.ClientPrivateKey, target.PrivateKey, target.ProviderPrivateKey, target.Password, target.KeyPath) != ""
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
