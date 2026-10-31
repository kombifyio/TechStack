package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/originca"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/kombifyio/stackkits/pkg/workloadremoval"
	"google.golang.org/protobuf/proto"

	wire "github.com/kombifyio/stackkits/contracts/advancedcapability"
)

const (
	StackKitLifecyclePlan = "plan"
	// Internal to admitted managed Generate. It is not an operator lifecycle
	// input: it may only acquire the retained generation's attested cache.
	stackKitStepPrepareSourceGeneration = "prepare_source_generation"
	StackKitLifecycleGenerate           = "generate"
	StackKitLifecycleApply              = "apply"
	StackKitLifecycleVerify             = "verify"
	StackKitLifecycleUpgrade            = "upgrade"
	StackKitLifecycleDriftDetect        = "drift_detect"
	StackKitLifecycleDriftReconcile     = "drift_reconcile"
	StackKitLifecycleInit               = "init"
	StackKitLifecycleServiceStart       = "service_start"
	StackKitLifecycleServiceStop        = "service_stop"
	StackKitLifecycleServiceRestart     = "service_restart"
	StackKitLifecycleServiceLogs        = "service_logs"
	StackKitLifecycleRemove             = "remove"
	StackKitLifecycleAddressBind        = "address_bind"
	// Home network move: network_rebind re-detects the node's LAN address,
	// re-issues the signed LAN DNS custody record and re-applies the workspace
	// (`stackkit network rebind`); network_status reads the node's network facts.
	StackKitLifecycleNetworkRebind = "network_rebind"
	StackKitLifecycleNetworkStatus = "network_status"
	// StackKitLifecycleAdvancedTrustImport installs this installation's
	// Advanced issuer trust bundle on the managed host. Every managed rollout
	// runs it after init; operators may re-run it to refresh the trust.
	StackKitLifecycleAdvancedTrustImport = "advanced_trust_import"
	// StackKitLifecycleFederationHandoff is one step of the Modern Cloud-node
	// bring-up: Core relays a public document between the Home node and the
	// Cloud node. Only the managed Modern rollout dispatches it.
	StackKitLifecycleFederationHandoff = "federation_handoff"
	// The native backup operations reuse the typed BACKUP_* commands the
	// managed rollout's restore drill runs. Each binds the node-local plan it
	// acts on; restore only stages a snapshot into StackKits' isolated area.
	StackKitLifecycleBackupRun             = "backup_run"
	StackKitLifecycleBackupStatus          = "backup_status"
	StackKitLifecycleBackupConfigure       = "backup_configure"
	StackKitLifecycleBackupRestore         = "backup_restore"
	StackKitLifecycleBackupRestoreActivate = "backup_restore_activate"
	StackKitLifecycleBackupRestoreRecover  = "backup_restore_recover"
	// Sealed host delivery of the managed off-site backup target. The node
	// proves its recipient key, opens a delivery sealed to it into its own
	// custody and issues its own backup-target binding; Core never builds
	// that binding. Internal to the managed rollout and the restore renewal.
	StackKitLifecycleBackupTargetRecipient = "backup_target_recipient"
	StackKitLifecycleBackupTargetImport    = "backup_target_import"
	StackKitLifecycleBackupTargetRebind    = "backup_target_rebind"
	// Advanced Mode operator operations (ADR-0045). Each dispatches
	// capability-gated StackKits Advanced operations from the pinned
	// release's catalog: advanced_change_set creates and applies a change set
	// for the candidate StackSpec in the request body, drift_reconcile
	// reconciles through a change set, rollback runs the coordinated rollback
	// to rollback_target_ref, and restore_drill runs the native restore drill.
	StackKitLifecycleAdvancedChangeSet = "advanced_change_set"
	StackKitLifecycleRollback          = "rollback"
	StackKitLifecycleRestoreDrill      = "restore_drill"
	// StackKitLifecyclePublicTLSRepair installs the managed kombify.me origin
	// certificate (ADR-0047) on the node of a rollout that completed with
	// public TLS degraded. Its hostnames come from that rollout, never from a
	// caller.
	StackKitLifecyclePublicTLSRepair = "public_tls_repair"
	// StackKitLifecycleAIProvider lets the Private AI use case offer an
	// external OpenAI-compatible provider whose API key is a kombify Wallet
	// item: the Wallet seals the item to the node, the node binds it, and a
	// drift reconcile applies the setting in Advanced Mode. With
	// ai_provider_disable it removes the provider again.
	StackKitLifecycleAIProvider = "ai_provider"
	// The steps of ai_provider. Internal to it, never an operator input.
	stackKitStepHostDeliveryRecipient = "host_delivery_recipient"
	stackKitStepWalletItemImport      = "wallet_item_import"
	stackKitStepAIProviderSetup       = "ai_provider_setup"
	// The steps of a workload relocation (JobTypeWorkloadRelocation). Each
	// runs on one of the move's two nodes; never an operator input.
	stackKitStepRelocatePlan    = "relocate_plan"
	stackKitStepRelocateCheck   = "relocate_check"
	stackKitStepRelocateDrain   = "relocate_drain"
	stackKitStepRelocateUndrain = "relocate_undrain"
	stackKitStepRelocateSend    = "relocate_send"
	stackKitStepRelocateReceive = "relocate_receive"
	stackKitStepRelocateStatus  = "relocate_status"
	stackKitStepRelocateAbort   = "relocate_abort"

	defaultStackKitNodeWorkspace = "/opt/stackkit"
	stackKitReadCommandTimeout   = 5 * time.Minute
	stackKitWriteCommandTimeout  = 12 * time.Minute
	stackKitBackupCommandTimeout = 15 * time.Minute
	// StackKits bounds the Advanced target at 15 minutes and gives a failed
	// target a separate 10-minute recovery context. Admission and checkpoint
	// also run inside this command, so the agent must outlive both phases.
	stackKitAdvancedMutationCommandTimeout = 30 * time.Minute
	// managedOriginCertificateBudget covers one CSR request, the Cloudflare
	// issuance and one install, each bounded by the result grace.
	managedOriginCertificateBudget = 2*managedStackKitResultGrace + 60*time.Second

	// Managed rollout commands share one product budget which must terminate,
	// collect target diagnostics, and leave persistence headroom before the
	// visible managed-provision acceptance deadline. The same execution budget
	// is sent to the agent, so a timed-out command cannot keep mutating after the
	// control-plane job has failed.
	managedStackKitInitTimeout     = 30 * time.Second
	managedStackKitGenerateTimeout = 60 * time.Second
	managedStackKitPlanTimeout     = 45 * time.Second
	managedStackKitResultGrace     = 10 * time.Second
	managedStackKitDispatchMargin  = 5 * time.Second
	stackKitCommandResultGrace     = 30 * time.Second

	// managedStackKitApplyBudget is what `stackkit apply` alone may run on the
	// node. A first apply on a cold node pulls every image of the Cloud core and
	// then waits for two readiness gates that StackKits bounds at five minutes
	// each, so the 4-minute share this replaced killed a healthy apply at seven
	// minutes, mid-change. It is the write-command timeout, so a retried apply
	// and the managed rollout grant the node the same time.
	managedStackKitApplyBudget = stackKitWriteCommandTimeout
	// managedStackKitApplyAllowance is one apply's own deadline: its budget and
	// result grace. Nothing before apply can shrink it; only a closer job
	// deadline does.
	managedStackKitApplyAllowance = managedStackKitApplyBudget + managedStackKitResultGrace
	// managedStackKitConvergenceWait is the longest the pre-apply phase waits
	// for the Guard's runtime to converge: every retry after the first attempt
	// waits one interval (typedStackKitRuntimePendingMaxAttempts).
	managedStackKitConvergenceWait = (typedStackKitRuntimePendingMaxAttempts - 1) * typedStackKitRuntimePendingRetryInterval
	// managedStackKitPreApplyTimeout is the ONE deadline the whole pre-apply
	// phase shares: init, trust import, address bind, generate and plan, their
	// convergence waits and retries. It covers the legitimate worst case, one
	// full convergence wait plus the init, generate and plan budgets with their
	// result graces, and no command may borrow from the apply allowance. It is
	// also the cap of any single pre-apply command, which is then bounded by
	// what is left of the shared deadline.
	managedStackKitPreApplyTimeout = managedStackKitConvergenceWait + managedStackKitInitTimeout + managedStackKitGenerateTimeout + managedStackKitPlanTimeout + 3*managedStackKitResultGrace + managedStackKitDispatchMargin
	// typedStackKitSequenceTimeout is the managed sequence: the shared pre-apply
	// deadline and one apply allowance.
	typedStackKitSequenceTimeout = managedStackKitPreApplyTimeout + managedStackKitApplyAllowance
	// managedStackKitAuthorityMargin is the least slack the apply command, with
	// its result grace, keeps inside its own command authority window.
	managedStackKitAuthorityMargin = 2 * time.Minute
)

// The apply command and its result grace must end inside its own authority
// window (agentcontrol.CommandAuthorityLifetime, counted from that command's
// enqueue) with the margin to spare. A budget that breaks this does not
// compile.
const _ = uint64(agentcontrol.CommandAuthorityLifetime - managedStackKitApplyBudget - managedStackKitResultGrace - managedStackKitAuthorityMargin)

// managedStackKitApplyAllowances is what every apply of a managed sequence
// keeps for itself: one for the node, and Modern adds the Cloud node's own.
func managedStackKitApplyAllowances(modern bool) time.Duration {
	if modern {
		return 2 * managedStackKitApplyAllowance
	}
	return managedStackKitApplyAllowance
}

// managedStackKitSequenceBudget is the whole managed sequence: Modern adds the
// Cloud node's bring-up and its own apply, and an origin certificate adds its
// own request, issuance and install. Subtracting managedStackKitApplyAllowances
// leaves the shared pre-apply deadline.
func managedStackKitSequenceBudget(modern, originCertificate bool) time.Duration {
	budget := typedStackKitSequenceTimeout
	if modern {
		budget = modernStackKitSequenceTimeout
	}
	if originCertificate {
		budget += managedOriginCertificateBudget
	}
	return budget
}

func managedStackKitOperationTimeout(operation string) time.Duration {
	switch operation {
	case StackKitLifecycleApply:
		return managedStackKitApplyBudget
	case StackKitLifecycleInit, StackKitLifecycleAdvancedTrustImport, StackKitLifecycleAddressBind,
		StackKitLifecycleGenerate, StackKitLifecyclePlan,
		StackKitLifecycleFederationHandoff, StackKitLifecycleBackupTargetRecipient, StackKitLifecycleBackupTargetImport,
		stackKitStepHostDeliveryRecipient, stackKitStepWalletItemImport, stackKitStepAIProviderSetup, stackKitStepHostSSHApply:
		// Pre-apply commands share one cap instead of per-command caps: those
		// made a healthy first-run init fail while it populated its release
		// cache. Each is bounded by what is left of the shared pre-apply
		// deadline, so a slow one cannot eat the apply allowance.
		return managedStackKitPreApplyTimeout
	case stackKitStepRelocatePlan, stackKitStepRelocateCheck:
		return stackKitReadCommandTimeout
	case stackKitStepRelocateDrain, stackKitStepRelocateUndrain:
		return stackKitRelocationDrainTimeout
	case stackKitStepRelocateSend, stackKitStepRelocateReceive:
		return stackKitRelocationTransferTimeout
	default:
		return 0
	}
}

// StackKitLifecycleRequest is the closed operator-facing lifecycle input.
// It deliberately has no argv, environment, binary path, or shell command.
type StackKitLifecycleRequest struct {
	Connector          *ApplicationConnectorBinding
	Application        *ApplicationLifecycleBinding
	BackupRenewal      *wire.BackupRenewal
	StackID            string
	StackKitInstanceID string
	TenantID           string
	OwnerID            string
	AgentID            string
	NodeID             string
	// NodeRef is the StackKits node the addressed agent owns. Only a
	// multi-node kit reads it; it picks that node's Site and channel.
	NodeRef string
	// ManagedRuntimeLeaseID is derived from the approved canonical server at
	// enqueue, never from an operator input. Generate revalidates this lease
	// before the managed Inventory producer may replace its process pin.
	ManagedRuntimeLeaseID  string
	ManagedRuntimeServerID string
	Operation              string
	TargetRelease          string
	DryRun                 bool
	Offline                bool
	OwnerApproved          bool
	WorkingDirectory       string
	// SpecPath is a relative canonical StackSpec path inside WorkingDirectory.
	// Deploy binds this to the v2 document already materialized by the pinned
	// generator; operator lifecycle calls default to the StackSpec the stack's
	// last managed rollout applied (ApplyStackKitRolloutDefaults).
	SpecPath          string
	StackName         string
	Domain            string
	ExpectedSpecHash  string
	CandidateSpecJSON []byte
	// OriginHosts are the certificate hostnames a public_tls_repair installs,
	// taken from the degraded rollout at enqueue.
	OriginHosts []string

	// managedBackupDelivery is set only by a managed rollout whose plan selects
	// the off-site backup capability. The node then issues its own binding: the
	// rollout runs generate with a channel-only Inventory, delivers the sealed
	// credentials, and generates again without an Inventory so the node's
	// binding survives. It is neither an operator input nor a wire field.
	managedBackupDelivery ManagedBackupTargetDelivery
	// originCertificate is set only by a managed kombify.me rollout: the
	// certificate is requested, issued and installed before apply (ADR-0047).
	// It is neither an operator input nor a wire field.
	originCertificate *managedOriginCertificate

	// inspectCurrentIntent is set only by an admitted exact deploy recovery.
	// It is not an operator input or a wire field: PLAN obtains the existing
	// document's CUE-normalized hash before INIT attempts a replacement.
	inspectCurrentIntent bool
	// sourceGenerationInspection is set only by managed Generate preparation;
	// operator inputs and persisted lifecycle payloads cannot set it.
	sourceGenerationInspection bool
	// modern is set only by the managed Modern rollout: the Cloud node's agent
	// and the fabric authority. It is not an operator input.
	modern *modernRolloutSeam
	// OwnerEmail is the stack Owner's signed-in account email, sent with init
	// only. StackKits requires it for a fresh local Owner (the PocketID owner).
	OwnerEmail string
	// StackKit selects the local execution binding. Apply refuses to guess it:
	// "Apply never infers that a planned target is this machine: the owner
	// names the exact Site, node, and channel this process owns, and anything
	// else stays unadmitted" (StackKits cmd/stackkit/commands/apply.go:71).
	StackKit      string
	InventoryJSON []byte
	ServiceKey    string
	LogTail       int32
	LogCursor     string
	ServiceID     string
	// DurableJobID and ServiceActionDigest contain only derived, non-secret
	// idempotency material. The raw HTTP key is never persisted.
	DurableJobID        string
	ServiceActionDigest string
	RILOperation        *RILOperationBinding
	WizardExtend        *WizardExtendBinding
	WorkloadRef         string
	AddressPrefix       string
	BoundSpecPath       string
	// SnapshotAnchorID names the snapshot a staged backup restore reads.
	SnapshotAnchorID            string
	RestoreResultID             string
	ActivationOperationID       string
	RestorePlanHash             string
	RestoreIdempotencyKeySHA256 string
	// SealedDelivery is one sealed host delivery envelope (ciphertext only) for
	// backup_target_import or backup_target_rebind. It is set at dispatch and
	// never persisted in a job payload.
	SealedDelivery []byte
	// DeliveryTenantRef is the tenant the sealed delivery is addressed to.
	DeliveryTenantRef string
	// RecipientChallenge is the fresh value backup_target_recipient and
	// host_delivery_recipient echo.
	RecipientChallenge string
	// DeliveryAuthorityKey and DeliveryAuthorityKeyID are the kombify
	// Wallet's public delivering key the node pins at host_delivery_recipient.
	// Set at dispatch, never persisted.
	DeliveryAuthorityKey   string
	DeliveryAuthorityKeyID string
	// AIProviderBaseURL and WalletItemID name the external AI provider and
	// the Wallet item holding its API key for ai_provider. WalletItemRotate
	// delivers a rotated item, which replaces the version the node holds;
	// AIProviderDisable removes the provider.
	AIProviderBaseURL string
	WalletItemID      string
	WalletItemRotate  bool
	AIProviderDisable bool
	// AdvancedTrustBundle is the installation's canonical public
	// stackkit.advanced-trust-bundle/v1, set at dispatch for
	// advanced_trust_import only and never persisted in a job payload.
	AdvancedTrustBundle []byte
	// RollbackTargetRef is the sha256 executor-state snapshot id or change-set
	// id a rollback returns to.
	RollbackTargetRef string
	// Handoff* carry one federation_handoff step: the closed step name, its
	// references, and the public document the previous step produced.
	HandoffStep     string
	HandoffRef      string
	HandoffRef2     string
	HandoffDocument []byte
	// Relocation* carry one step of a workload relocation: the Coolify
	// projects, the secret-free plan, the source's share address and the
	// destination's domain switches. Set by the relocation job only.
	RelocationProjects      []string
	RelocationResourceUUIDs []string
	RelocationTransferID    string
	RelocationPlanJSON      []byte
	RelocationSourceAddress string
	RelocationSetDomains    []string
}

// AdvancedIssuer is the installation's Advanced capability issuer as seen by
// lifecycle dispatch: the public trust bundle every managed host imports, and
// the record of which local Owner each host bound it to.
type AdvancedIssuer interface {
	TrustBundle() []byte
	RecordTrustBinding(context.Context, advancedissuer.TrustBinding) error
	// TrustBindingFor returns the Owner and stack a deployment's host bound
	// the trust to; Issue mints one capability for one Advanced operation.
	TrustBindingFor(ctx context.Context, tenantID, deploymentID string) (advancedissuer.TrustBinding, error)
	Issue(context.Context, advancedissuer.Request) (advancedissuer.Capability, error)
}

// localExecutionBinding is the Site/node/channel triple one kit's owner runs.
//
// Techstack sent the basement triple for every kit, so a cloud-kit rollout
// named a binding its own plan does not contain and StackKits rejected it. The
// values mirror each kit's authoring.standaloneOwner in the StackKits CUE
// authority -- basement-kit/stackfile.cue:212-214 and
// cloud-kit/stackfile.cue:200-204 -- and the site and node are cross-checked
// against the resolved plan at dispatch, so drift surfaces instead of applying
// silently. An unknown kit fails closed rather than inheriting basement.
type localExecutionBinding struct {
	SiteRef             string
	NodeRef             string
	ExecutionChannelRef string
}

// localExecutionBindings lists each kit's nodes. The first entry is the
// authority node the managed rollout runs on. A one-node kit has no other
// choice, so its binding never depends on the agent's hostname. A multi-node kit
// binds the exact node its addressed agent owns and refuses anything else.
//
// modern-homelab mirrors modern-homelab/stackfile.cue (standaloneOwner for
// home-main, the cloud-edge node) and the StackKits modern-two-host fixture
// (`local-<node>` channels). Advanced Mode is Techstack-only, so Techstack
// dispatches each node's phases to the agent that owns the node.
var localExecutionBindings = map[string][]localExecutionBinding{
	"basement-kit": {{SiteRef: "home", NodeRef: "main", ExecutionChannelRef: "local-home-main"}},
	"cloud-kit":    {{SiteRef: "cloud", NodeRef: "cloud-main", ExecutionChannelRef: "host-channel-cloud-main"}},
	"modern-homelab": {
		{SiteRef: "home", NodeRef: "home-main", ExecutionChannelRef: "local-home-main"},
		{SiteRef: "cloud", NodeRef: "cloud-edge", ExecutionChannelRef: "local-cloud-edge"},
	},
}

func localExecutionNodes(stackKit string) ([]localExecutionBinding, error) {
	nodes, ok := localExecutionBindings[strings.ToLower(strings.TrimSpace(stackKit))]
	if !ok {
		return nil, fmt.Errorf(
			"no local execution binding is known for StackKit %q; apply refuses an unnamed Site, node, and channel",
			stackKit,
		)
	}
	return nodes, nil
}

// localExecutionBindingFor returns the kit's primary (authority) node binding.
func localExecutionBindingFor(stackKit string) (localExecutionBinding, error) {
	nodes, err := localExecutionNodes(stackKit)
	if err != nil {
		return localExecutionBinding{}, err
	}
	return nodes[0], nil
}

// localExecutionBindingForNode returns the binding of the node an agent owns.
// A multi-node kit fails closed for an empty or unknown node; a one-node kit
// returns its only binding.
func localExecutionBindingForNode(stackKit, nodeRef string) (localExecutionBinding, error) {
	nodes, err := localExecutionNodes(stackKit)
	if err != nil {
		return localExecutionBinding{}, err
	}
	if len(nodes) == 1 {
		return nodes[0], nil
	}
	nodeRef = strings.TrimSpace(nodeRef)
	for _, node := range nodes {
		if node.NodeRef == nodeRef {
			return node, nil
		}
	}
	return localExecutionBinding{}, fmt.Errorf(
		"StackKit %q is multi-node and the addressed agent owns no node of it (node %q); dispatch refuses an unbound node",
		stackKit, nodeRef,
	)
}

// StackKitNodeRefForAgent resolves the node of a kit that the agent known by
// hostname owns, by the rule dispatch uses (localExecutionBindingForNode): a
// one-node kit has one node whatever its host is called, a multi-node kit
// needs the host name to spell the node. A node it cannot resolve is refused.
func StackKitNodeRefForAgent(stackKit, hostname string) (string, error) {
	binding, err := localExecutionBindingForNode(stackKit, hostname)
	if err != nil {
		return "", err
	}
	return binding.NodeRef, nil
}

// RequireStackKitNode reports whether nodeRef is a node the kit can dispatch
// to. It is the admission form of localExecutionBindingForNode.
func RequireStackKitNode(stackKit, nodeRef string) error {
	_, err := localExecutionBindingForNode(stackKit, nodeRef)
	return err
}

// IsStackKitAuthorityNode reports whether nodeRef is the node whole-stack
// operations (drift) address: any node of a one-node kit, the authority node of
// a multi-node kit.
func IsStackKitAuthorityNode(stackKit, nodeRef string) bool {
	nodes, err := localExecutionNodes(stackKit)
	return err == nil && (len(nodes) == 1 || nodes[0].NodeRef == strings.TrimSpace(nodeRef))
}

// PrimaryStackKitNodeRef is the node the managed rollout agent owns.
func PrimaryStackKitNodeRef(stackKit string) string {
	binding, err := localExecutionBindingFor(stackKit)
	if err != nil {
		return ""
	}
	return binding.NodeRef
}

// StackKitCommandSender is implemented by grpcserver.Server. Keeping the job
// handler on this narrow seam prevents the operator route from reaching the
// generic agent command queue.
type StackKitCommandSender interface {
	SendStackKitCommand(context.Context, string, *agentpb.StackKitCommand) (*agentpb.StackKitResult, error)
}

type tenantStackKitCommandSender interface {
	SendStackKitCommandForTenant(context.Context, string, string, *agentpb.StackKitCommand) (*agentpb.StackKitResult, error)
}

type StackKitLifecycleConfig struct {
	ConnectorAuthorize      func(context.Context, StackKitLifecycleRequest) error
	ApplicationAuthorize    func(context.Context, string, string, string) error
	ServiceRuntimeAuthorize func(context.Context, StackKitLifecycleRequest, string) error
	Services                interface {
		controlplane.ServiceRuntimeStore
		controlplane.ServiceEventWriter
		controlplane.ServiceEventReader
	}
	ManagedStackKitInventory ManagedStackKitInventoryBuilder
	ManagedAddressAuthority  ManagedAddressAuthority
	Sender                   StackKitCommandSender
	AdvancedIssuer           AdvancedIssuer
	// OriginCertificates re-issues the managed kombify.me origin certificate
	// when a change set changes the route set.
	OriginCertificates *originca.Service
	// WalletHostDelivery seals a kombify Wallet item to a node for
	// ai_provider. Nil fails that operation closed.
	WalletHostDelivery WalletHostDelivery
	// RelocationTestDomainSuffixes are the domains a workload relocation may
	// switch restored resources to; empty keeps the reserved test and lab
	// suffixes. Production domains wait for real-host acceptance.
	RelocationTestDomainSuffixes []string
	releaseResolver              func() (*stackkitrelease.Release, error)
}

func NormalizeStackKitLifecycleRequest(req StackKitLifecycleRequest) (StackKitLifecycleRequest, error) {
	req = normalizeStackKitLifecycleFields(req)
	if req.WorkingDirectory == "" {
		req.WorkingDirectory = defaultStackKitNodeWorkspace
	}
	if req.StackID == "" || req.TenantID == "" || req.OwnerID == "" || req.AgentID == "" {
		return req, fmt.Errorf("stack, tenant, Owner, and agent are required")
	}
	if err := validateStackKitLifecycleOperation(&req); err != nil {
		return req, err
	}
	if err := validateDurableStackKitServiceAction(req); err != nil {
		return req, err
	}
	return req, nil
}

func normalizeStackKitLifecycleFields(req StackKitLifecycleRequest) StackKitLifecycleRequest {
	req.StackID = strings.TrimSpace(req.StackID)
	req.StackKitInstanceID = strings.TrimSpace(req.StackKitInstanceID)
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.OwnerID = strings.TrimSpace(req.OwnerID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.NodeRef = strings.TrimSpace(req.NodeRef)
	req.Operation = strings.ToLower(strings.TrimSpace(req.Operation))
	req.TargetRelease = strings.TrimSpace(req.TargetRelease)
	req.WorkingDirectory = strings.TrimSpace(req.WorkingDirectory)
	req.SpecPath = strings.TrimSpace(req.SpecPath)
	req.StackName = strings.TrimSpace(req.StackName)
	req.Domain = strings.TrimSpace(req.Domain)
	req.ExpectedSpecHash = strings.TrimSpace(req.ExpectedSpecHash)
	req.ServiceKey = strings.TrimSpace(req.ServiceKey)
	req.LogCursor = strings.TrimSpace(req.LogCursor)
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.DurableJobID = strings.TrimSpace(req.DurableJobID)
	req.ServiceActionDigest = strings.TrimSpace(req.ServiceActionDigest)
	req.WorkloadRef = strings.TrimSpace(req.WorkloadRef)
	req.AddressPrefix = strings.TrimSpace(req.AddressPrefix)
	req.BoundSpecPath = strings.TrimSpace(req.BoundSpecPath)
	req.SnapshotAnchorID = strings.TrimSpace(req.SnapshotAnchorID)
	req.RestoreResultID = strings.TrimSpace(req.RestoreResultID)
	req.ActivationOperationID = strings.TrimSpace(req.ActivationOperationID)
	req.RestorePlanHash = strings.TrimSpace(req.RestorePlanHash)
	req.RestoreIdempotencyKeySHA256 = strings.TrimSpace(req.RestoreIdempotencyKeySHA256)
	req.RollbackTargetRef = strings.TrimSpace(req.RollbackTargetRef)
	req.HandoffStep = strings.TrimSpace(req.HandoffStep)
	req.HandoffRef = strings.TrimSpace(req.HandoffRef)
	req.HandoffRef2 = strings.TrimSpace(req.HandoffRef2)
	req.AIProviderBaseURL = strings.TrimSpace(req.AIProviderBaseURL)
	req.WalletItemID = strings.TrimSpace(req.WalletItemID)
	return req
}

func validateStackKitLifecycleOperation(req *StackKitLifecycleRequest) error {
	if req.Operation != StackKitLifecycleBackupRestoreActivate && req.Operation != StackKitLifecycleBackupRestoreRecover &&
		(req.RestoreResultID != "" || req.ActivationOperationID != "" || req.RestorePlanHash != "") {
		return fmt.Errorf("restore activation identity requires activation or recovery")
	}
	if IsApplicationConnectorLifecycleOperation(req.Operation) {
		return validateApplicationConnectorLifecycleRequest(req)
	}
	if req.Connector != nil {
		return fmt.Errorf("connector binding requires a connector operation")
	}
	if IsApplicationLifecycleOperation(req.Operation) {
		return validateApplicationLifecycleRequest(req)
	}
	if req.Operation != StackKitLifecycleAIProvider && req.Operation != stackKitStepAIProviderSetup &&
		(req.AIProviderBaseURL != "" || req.AIProviderDisable || ((req.WalletItemID != "" || req.WalletItemRotate) && !isRelocationTransferStep(req.Operation))) {
		return fmt.Errorf("AI provider inputs require ai_provider")
	}
	if isRelocationStep(req.Operation) {
		return validateRelocationStepRequest(req)
	}
	if len(req.RelocationProjects) != 0 || len(req.RelocationResourceUUIDs) != 0 || req.RelocationTransferID != "" || len(req.RelocationPlanJSON) != 0 || req.RelocationSourceAddress != "" || len(req.RelocationSetDomains) != 0 {
		return fmt.Errorf("relocation inputs require a relocation step")
	}
	switch req.Operation {
	case StackKitLifecycleGenerate, StackKitLifecyclePlan, StackKitLifecycleVerify, StackKitLifecycleDriftDetect:
	case StackKitLifecycleAddressBind:
		if req.AddressPrefix == "" || req.BoundSpecPath == "" {
			return fmt.Errorf("address bind requires prefix and bound StackSpec path")
		}
	case StackKitLifecycleDriftReconcile, StackKitLifecycleAdvancedChangeSet:
		if !req.OwnerApproved {
			return fmt.Errorf("%s requires explicit Owner approval", req.Operation)
		}
		if req.Operation == StackKitLifecycleAdvancedChangeSet && len(req.CandidateSpecJSON) == 0 {
			return fmt.Errorf("advanced_change_set requires the candidate StackSpec")
		}
		if len(req.CandidateSpecJSON) > stackkitcommand.MaxInitCandidateBytes || (len(req.CandidateSpecJSON) > 0 && !json.Valid(req.CandidateSpecJSON)) {
			return fmt.Errorf("%s candidate must be one JSON StackSpec within %d bytes", req.Operation, stackkitcommand.MaxInitCandidateBytes)
		}
	case StackKitLifecycleRollback:
		if !req.OwnerApproved {
			return fmt.Errorf("rollback requires explicit Owner approval")
		}
		if !stackKitSnapshotAnchorPattern.MatchString(req.RollbackTargetRef) {
			return fmt.Errorf("rollback requires a sha256 rollback_target_ref (snapshot or change-set id)")
		}
	case StackKitLifecyclePublicTLSRepair:
		hosts, err := originca.NormalizeHosts(req.OriginHosts)
		if err != nil || len(hosts) == 0 {
			return fmt.Errorf("public_tls_repair requires the degraded rollout's certificate hostnames")
		}
		req.OriginHosts = hosts
	case StackKitLifecycleRestoreDrill:
		if !req.OwnerApproved {
			return fmt.Errorf("restore_drill requires explicit Owner approval")
		}
		if req.SnapshotAnchorID != "" && !stackKitSnapshotAnchorPattern.MatchString(req.SnapshotAnchorID) {
			return fmt.Errorf("restore_drill snapshot_anchor_id must be a sha256 anchor")
		}
	case StackKitLifecycleInit, StackKitLifecycleApply:
		if req.Operation == StackKitLifecycleInit && (req.StackKit == "" || req.StackName == "") {
			return fmt.Errorf("init requires StackKit and stack name")
		}
		if req.Operation == StackKitLifecycleInit {
			return stackkitcommand.ValidateInitCandidate(req.CandidateSpecJSON, req.StackKit, req.StackName)
		}
	case StackKitLifecycleAdvancedTrustImport:
		if !req.OwnerApproved {
			return fmt.Errorf("advanced_trust_import requires explicit Owner approval")
		}
	case StackKitLifecycleFederationHandoff:
		if !req.OwnerApproved {
			return fmt.Errorf("federation_handoff requires explicit Owner approval")
		}
		if _, ok := stackkitcommand.LookupHandoffStep(req.HandoffStep); !ok {
			return fmt.Errorf("federation_handoff step %q is not recognized", req.HandoffStep)
		}
	case StackKitLifecycleUpgrade:
		if !req.DryRun && !req.OwnerApproved {
			return fmt.Errorf("upgrade requires explicit Owner approval")
		}
	case StackKitLifecycleServiceStart, StackKitLifecycleServiceStop, StackKitLifecycleServiceRestart:
		if !req.OwnerApproved {
			return fmt.Errorf("%s requires explicit Owner approval", req.Operation)
		}
		if req.ServiceKey == "" {
			return fmt.Errorf("%s requires service key", req.Operation)
		}
	case StackKitLifecycleServiceLogs:
		if req.ServiceKey == "" {
			return fmt.Errorf("service_logs requires service key")
		}
		if req.LogTail == 0 {
			req.LogTail = 100
		}
		if req.LogTail < 1 || req.LogTail > 200 {
			return fmt.Errorf("service_logs tail must be between 1 and 200")
		}
	case StackKitLifecycleBackupStatus, StackKitLifecycleNetworkStatus:
	case StackKitLifecycleNetworkRebind:
		if !req.OwnerApproved {
			return fmt.Errorf("network_rebind requires explicit Owner approval")
		}
	case StackKitLifecycleAIProvider, stackKitStepAIProviderSetup:
		return validateAIProviderRequest(req)
	case StackKitLifecycleSSHTrustRecipient, StackKitLifecycleSSHTrustApply:
		return validateSSHTrustRequest(req)
	case StackKitLifecycleBackupTargetRecipient, StackKitLifecycleBackupTargetImport, StackKitLifecycleBackupTargetRebind,
		stackKitStepHostDeliveryRecipient, stackKitStepWalletItemImport, stackKitStepHostSSHApply:
		if !req.OwnerApproved {
			return fmt.Errorf("%s requires explicit Owner approval", req.Operation)
		}
	case StackKitLifecycleBackupRun, StackKitLifecycleBackupConfigure:
		if !req.OwnerApproved {
			return fmt.Errorf("%s requires explicit Owner approval", req.Operation)
		}
	case StackKitLifecycleBackupRestore:
		if !req.OwnerApproved {
			return fmt.Errorf("backup_restore requires explicit Owner approval")
		}
		if !stackKitSnapshotAnchorPattern.MatchString(req.SnapshotAnchorID) {
			return fmt.Errorf("backup_restore requires a sha256 snapshot_anchor_id")
		}
	case StackKitLifecycleBackupRestoreActivate, StackKitLifecycleBackupRestoreRecover:
		if !req.OwnerApproved || !stackKitSnapshotAnchorPattern.MatchString(req.RestoreResultID) || req.DryRun || req.Offline || req.SnapshotAnchorID != "" {
			return fmt.Errorf("restore activation requires approval and an exact staged result")
		}
		if req.Operation == StackKitLifecycleBackupRestoreActivate && (req.ActivationOperationID != "" || req.RestorePlanHash != "") {
			return fmt.Errorf("activation identity and plan are derived from the durable job and current node")
		}
		if req.Operation == StackKitLifecycleBackupRestoreRecover && (!stackkitcommand.ValidRestoreActivationID(req.ActivationOperationID) || !stackKitSnapshotAnchorPattern.MatchString(req.RestorePlanHash)) {
			return fmt.Errorf("recovery requires original activation_operation_id and restore_plan_hash")
		}
	case StackKitLifecycleRemove:
		if !req.OwnerApproved {
			return fmt.Errorf("remove requires explicit Owner approval")
		}
		if req.WorkloadRef == "" || req.WorkloadRef != strings.ToLower(req.WorkloadRef) {
			return fmt.Errorf("remove requires one canonical workload ref")
		}
	default:
		return fmt.Errorf("unsupported StackKit lifecycle operation %q", req.Operation)
	}
	return nil
}

func validateDurableStackKitServiceAction(req StackKitLifecycleRequest) error {
	if req.WizardExtend != nil {
		return validateWizardExtend(req)
	}
	if req.RILOperation != nil {
		return ValidateRILStackKitBinding(req)
	}
	if req.Operation == StackKitLifecycleBackupRestoreActivate && req.RestoreIdempotencyKeySHA256 != "" {
		if req.DurableJobID == "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(req.RestoreIdempotencyKeySHA256) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(req.ServiceActionDigest) || req.ServiceID != "" {
			return fmt.Errorf("restore activation requires its scoped idempotent identity and immutable request digest")
		}
		return nil
	}
	if req.DurableJobID != "" || req.ServiceActionDigest != "" || req.ServiceID != "" {
		if req.DurableJobID == "" || req.ServiceActionDigest == "" || req.ServiceID == "" || len(req.ServiceActionDigest) != sha256.Size*2 {
			return fmt.Errorf("durable service action requires job ID, service ID, and SHA-256 request digest")
		}
	}
	return nil
}

func StackKitLifecyclePayload(req StackKitLifecycleRequest) map[string]interface{} {
	return map[string]interface{}{
		"ril_operation_binding":          req.RILOperation,
		"wizard_extend_binding":          req.WizardExtend,
		"connector_binding":              req.Connector,
		"application_binding":            req.Application,
		"stackkit_instance_id":           req.StackKitInstanceID,
		"tenant_id":                      req.TenantID,
		"owner_id":                       req.OwnerID,
		"agent_id":                       req.AgentID,
		"node_id":                        req.NodeID,
		"node_ref":                       req.NodeRef,
		"managed_runtime_lease_id":       req.ManagedRuntimeLeaseID,
		"managed_runtime_server_id":      req.ManagedRuntimeServerID,
		"operation":                      req.Operation,
		"target_release":                 req.TargetRelease,
		"dry_run":                        req.DryRun,
		"offline":                        req.Offline,
		"owner_approved":                 req.OwnerApproved,
		"working_directory":              req.WorkingDirectory,
		"spec_path":                      req.SpecPath,
		"stackkit":                       req.StackKit,
		"stackkit_id":                    req.StackKit,
		"stack_name":                     req.StackName,
		"domain":                         req.Domain,
		"expected_spec_hash":             req.ExpectedSpecHash,
		"candidate_spec_json":            string(req.CandidateSpecJSON),
		"service_key":                    req.ServiceKey,
		"log_tail":                       req.LogTail,
		"log_cursor":                     req.LogCursor,
		"service_id":                     req.ServiceID,
		"durable_job_id":                 req.DurableJobID,
		"service_action_digest":          req.ServiceActionDigest,
		"workload_ref":                   req.WorkloadRef,
		"snapshot_anchor_id":             req.SnapshotAnchorID,
		"restore_result_id":              req.RestoreResultID,
		"activation_operation_id":        req.ActivationOperationID,
		"restore_plan_hash":              req.RestorePlanHash,
		"restore_idempotency_key_sha256": req.RestoreIdempotencyKeySHA256,
		"rollback_target_ref":            req.RollbackTargetRef,
		"ai_provider_base_url":           req.AIProviderBaseURL,
		"wallet_item_id":                 req.WalletItemID,
		"wallet_item_rotate":             req.WalletItemRotate,
		"ai_provider_disable":            req.AIProviderDisable,
		"origin_hosts":                   append([]string(nil), req.OriginHosts...),
		// Only the SSH CA trust operations take these from a caller; the
		// SSH delivery carries public material only (stackkit_ssh_trust.go).
		"recipient_challenge": sshTrustPayload(req, req.RecipientChallenge),
		"sealed_delivery":     sshTrustPayload(req, string(req.SealedDelivery)),
	}
}

func sshTrustPayload(req StackKitLifecycleRequest, value string) string {
	if !IsSSHTrustOperation(req.Operation) {
		return ""
	}
	return value
}

// StackKitServiceActionJobID derives a tenant/owner-scoped durable identity
// without storing the caller's raw Idempotency-Key.
func StackKitServiceActionJobID(tenantID, ownerID, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(tenantID), strings.TrimSpace(ownerID), strings.TrimSpace(idempotencyKey),
	}, "\x00")))
	return fmt.Sprintf("job-stackkit-service-%x", digest[:16])
}

func PrepareRestoreActivationIdempotency(req StackKitLifecycleRequest, key string) (StackKitLifecycleRequest, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return req, fmt.Errorf("a bounded Idempotency-Key is required for restore activation")
	}
	req = normalizeStackKitLifecycleFields(req)
	keyDigest := sha256.Sum256([]byte(key))
	req.RestoreIdempotencyKeySHA256 = fmt.Sprintf("%x", keyDigest)
	req.DurableJobID = RestoreActivationJobIDForKeyHash(req.TenantID, req.OwnerID, req.RestoreIdempotencyKeySHA256)
	intent := struct {
		Tenant, Owner, Stack, Agent, Operation, Kit, Target, Snapshot, Result, Activation, Plan, Rollback string
		Approved, DryRun, Offline                                                                         bool
		Candidate                                                                                         json.RawMessage
	}{req.TenantID, req.OwnerID, req.StackID, req.AgentID, req.Operation, NormalizeStackKitRef(req.StackKit), req.TargetRelease, req.SnapshotAnchorID, req.RestoreResultID, req.ActivationOperationID, req.RestorePlanHash, req.RollbackTargetRef, req.OwnerApproved, req.DryRun, req.Offline, json.RawMessage(req.CandidateSpecJSON)}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return req, err
	}
	digest := sha256.Sum256(encoded)
	req.ServiceActionDigest = fmt.Sprintf("%x", digest)
	return req, nil
}

func RestoreActivationJobIDForKeyHash(tenant, owner, keyHash string) string {
	identity := sha256.Sum256([]byte(strings.Join([]string{"stackkit.restore.activate/v1", strings.TrimSpace(tenant), strings.TrimSpace(owner), keyHash}, "\x00")))
	return fmt.Sprintf("job-stackkit-restore-%x", identity)
}

func RestoreActivationRequestReceipt(req StackKitLifecycleRequest) map[string]interface{} {
	if req.Operation != StackKitLifecycleBackupRestoreActivate || req.RestoreIdempotencyKeySHA256 == "" {
		return nil
	}
	return map[string]interface{}{"schema_version": "techstack.restore-activation-request/v1", "tenant_id": req.TenantID, "owner_id": req.OwnerID, "techstack_id": req.StackID, "agent_id": req.AgentID, "stackkit_instance_id": req.StackKitInstanceID, "node_id": req.NodeID, "node_ref": req.NodeRef, "spec_path": req.SpecPath, "restore_result_id": req.RestoreResultID, "owner_approved": req.OwnerApproved, "key_sha256": req.RestoreIdempotencyKeySHA256, "request_digest": req.ServiceActionDigest}
}

func MatchesRestoreActivationRequestReceipt(result map[string]interface{}, req StackKitLifecycleRequest) bool {
	receipt, ok := result[controlplane.RestoreActivationRequestResultKey].(map[string]interface{})
	if !ok {
		return false
	}
	want := RestoreActivationRequestReceipt(req)
	if want == nil {
		return false
	}
	for key, value := range want {
		if key == "stackkit_instance_id" || key == "node_id" || key == "node_ref" || key == "spec_path" {
			continue
		}
		if stringFromInterface(receipt[key]) != stringFromInterface(value) {
			return false
		}
	}
	return true
}

func StackKitServiceActionReceipt(req StackKitLifecycleRequest) map[string]interface{} {
	if strings.TrimSpace(req.ServiceActionDigest) == "" || strings.TrimSpace(req.ServiceID) == "" {
		return nil
	}
	return map[string]interface{}{
		"schema_version":       "techstack.service-action-receipt/v1",
		"techstack_id":         req.StackID,
		"stackkit_instance_id": req.StackKitInstanceID,
		"request_digest":       req.ServiceActionDigest,
		"service_id":           req.ServiceID,
		"service_key":          req.ServiceKey,
		"action":               strings.TrimPrefix(req.Operation, "service_"),
		"log_tail":             req.LogTail,
		"log_cursor":           req.LogCursor,
	}
}

func MatchesStackKitServiceActionReceipt(result map[string]any, req StackKitLifecycleRequest) bool {
	receipt, ok := result["service_action_receipt"].(map[string]any)
	if !ok {
		return false
	}
	want := StackKitServiceActionReceipt(req)
	if actual := strings.TrimSpace(stringFromInterface(receipt["techstack_id"])); actual != "" && actual != req.StackID {
		return false
	}
	if actual := strings.TrimSpace(stringFromInterface(receipt["stackkit_instance_id"])); actual != "" && actual != req.StackKitInstanceID {
		return false
	}
	return stringFromInterface(receipt["schema_version"]) == stringFromInterface(want["schema_version"]) &&
		stringFromInterface(receipt["request_digest"]) == req.ServiceActionDigest &&
		stringFromInterface(receipt["service_id"]) == req.ServiceID &&
		stringFromInterface(receipt["service_key"]) == req.ServiceKey &&
		stringFromInterface(receipt["action"]) == strings.TrimPrefix(req.Operation, "service_")
}

func RegisterStackKitLifecycleHandler(q *Queue, cfg StackKitLifecycleConfig) {
	q.RegisterHandler(JobTypeStackKitLifecycle, StackKitLifecycleHandler(cfg))
}

func StackKitLifecycleHandler(cfg StackKitLifecycleConfig) JobHandler {
	return func(ctx context.Context, job *Job, q *Queue) (returnErr error) {
		before := job.Snapshot()
		beforeReq, beforeErr := stackKitLifecycleRequestFromJob(job)
		mutationMayHaveBeenAdmitted := before.Result[restoreActivationCheckpointKey] != nil
		defer func() {
			if returnErr == nil || mutationMayHaveBeenAdmitted || beforeErr != nil || before.StartedAt == nil || before.ID == "" || beforeReq.Operation != StackKitLifecycleBackupRestoreActivate || !MatchesRestoreActivationRequestReceipt(before.Result, beforeReq) {
				return
			}
			// This handler never attempted admission or a mutating command.
			// Keep a positive, request-bound denial rather than asking a client
			// to infer safety from a missing field or an HTTP error.
			job.mutateResult(func(result map[string]interface{}) {
				if result[restoreActivationCheckpointKey] != nil {
					return
				}
				result["schema_version"] = "techstack.stackkit-lifecycle-result/v1"
				result["techstack_id"] = beforeReq.StackID
				result["agent_id"] = beforeReq.AgentID
				result["operation"] = beforeReq.Operation
				result["success"] = false
				result[controlplane.RestoreActivationRequestResultKey] = before.Result[controlplane.RestoreActivationRequestResultKey]
				result["backup"] = map[string]interface{}{"mode": "not-admitted", "job_id": before.ID, "attempt_started_at": before.StartedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), "restore_result_id": beforeReq.RestoreResultID, "key_sha256": beforeReq.RestoreIdempotencyKeySHA256, "request_digest": beforeReq.ServiceActionDigest}
			})
		}()
		if cfg.Sender == nil {
			return NewConfigError(fmt.Errorf("typed StackKits dispatcher is not configured"))
		}
		req, err := stackKitLifecycleRequestFromJob(job)
		if err != nil {
			return NewPermanentError(err)
		}
		if req.Operation == StackKitLifecycleBackupRestoreActivate {
			// Job identity survives redelivery; command IDs may change while a
			// newly paired Guard converges. Never activate under the retry ID.
			req.ActivationOperationID = "restore-" + job.ID
			if !stackkitcommand.ValidRestoreActivationID(req.ActivationOperationID) {
				return NewPermanentError(fmt.Errorf("durable activation job identity is invalid"))
			}
		}
		resolveRelease := cfg.releaseResolver
		if resolveRelease == nil {
			resolveRelease = func() (*stackkitrelease.Release, error) {
				return TargetStackKitReleaseForStack(ctx, req.TenantID, req.StackID)
			}
		}
		release, err := resolveRelease()
		if err != nil {
			return NewConfigError(err)
		}
		if release == nil {
			return NewConfigError(fmt.Errorf("pinned published StackKits release is not configured"))
		}
		if req.WizardExtend != nil && (req.WizardExtend.ReleaseVersion != release.Receipt().Version || req.WizardExtend.ArchiveSHA256 != release.Receipt().ArchiveSHA256) {
			return NewConfigError(fmt.Errorf("wizard extension belongs to a different pinned StackKits release"))
		}
		if IsApplicationConnectorLifecycleOperation(req.Operation) {
			return runApplicationConnectorJob(ctx, cfg, job, q, req, *release)
		}
		if IsApplicationLifecycleOperation(req.Operation) {
			return runApplicationLifecycleJob(ctx, cfg, job, q, req, *release)
		}
		serviceIntent, err := captureDeclaredServiceIntent(ctx, cfg, req)
		if err != nil {
			return NewPermanentError(err)
		}

		job.setStep("stackkit_dispatch")
		q.UpdateProgress(job.ID, 10, "Dispatching typed StackKits "+req.Operation+" operation")
		if IsAdvancedStackKitLifecycleOperation(req.Operation) {
			return runAdvancedStackKitLifecycleJob(ctx, cfg, job, q, req, *release)
		}
		if req.Operation == StackKitLifecycleAIProvider {
			return runAIProviderJob(ctx, cfg, job, q, req, *release)
		}
		if IsSSHTrustOperation(req.Operation) {
			return runSSHTrustJob(ctx, cfg, job, q, req, *release)
		}
		if req.Operation == StackKitLifecycleAdvancedTrustImport {
			if cfg.AdvancedIssuer == nil {
				return NewConfigError(advancedissuer.ErrUnavailable)
			}
			req.AdvancedTrustBundle = cfg.AdvancedIssuer.TrustBundle()
		}
		req, inventoryRefresh, err := prepareManagedGenerate(ctx, cfg, job, req, *release)
		if err != nil {
			return NewPermanentError(err)
		}
		if inventoryRefresh != nil {
			job.mutateResult(func(result map[string]interface{}) { result[managedInventoryRefreshResultField] = inventoryRefresh })
		}
		expectedPlanHash := req.RestorePlanHash
		var checkpoint map[string]interface{}
		var originalRecoveryRelease *agentpb.StackKitReleasePin
		resumeActivation := false
		if req.Operation == StackKitLifecycleBackupRestoreActivate || req.Operation == StackKitLifecycleBackupRestoreRecover {
			if retained, found := job.Snapshot().Result[restoreActivationCheckpointKey]; found {
				checkpoint, _ = retained.(map[string]interface{})
				if req.Operation == StackKitLifecycleBackupRestoreActivate {
					expectedPlanHash = stringFromInterface(checkpoint["plan_hash"])
				}
				if !stackKitSnapshotAnchorPattern.MatchString(expectedPlanHash) || !matchesRestoreActivationCheckpointIdentity(checkpoint, req, *release, expectedPlanHash) {
					return NewPermanentError(fmt.Errorf("restore activation checkpoint differs from original node, owner, result or pinned release authority"))
				}
				if !matchesRestoreActivationCheckpoint(checkpoint, req, *release, expectedPlanHash) {
					originalRecoveryRelease = &agentpb.StackKitReleasePin{
						Version: stringFromInterface(checkpoint["release_version"]), PlatformOs: stringFromInterface(checkpoint["platform_os"]),
						PlatformArch: stringFromInterface(checkpoint["platform_arch"]), ArchiveSha256: stringFromInterface(checkpoint["release_archive_sha256"]),
						ReleaseIndexSha256: stringFromInterface(checkpoint["release_index_sha256"]),
					}
					if err := stackkitcommand.ValidateReleasePin(originalRecoveryRelease); err != nil {
						return NewPermanentError(fmt.Errorf("original restore release authority is invalid: %w", err))
					}
				}
				resumeActivation = req.Operation == StackKitLifecycleBackupRestoreActivate
			} else if req.Operation == StackKitLifecycleBackupRestoreRecover {
				return NewPermanentError(fmt.Errorf("recovery requires the original durable activation admission"))
			}
		}
		if req.Operation == StackKitLifecycleApply || (IsStackKitBackupOperation(req.Operation) && req.Operation != StackKitLifecycleBackupRestoreRecover && !resumeActivation) {
			// Apply and every native backup operation act on one exact
			// node-local generation; plan it first and bind the command to it.
			job.setStep("stackkit_plan_admission")
			q.UpdateProgress(job.ID, 10, "Planning the exact StackKits generation before "+req.Operation)
			expectedPlanHash, err = planStackKitLifecycleApply(ctx, cfg.Sender, job.ID, req, *release)
			if err != nil {
				return NewPermanentError(err)
			}
			job.setStep("stackkit_dispatch")
			q.UpdateProgress(job.ID, 30, "Dispatching "+req.Operation+" against the admitted StackKits plan")
		}
		if req.Operation == StackKitLifecycleBackupRestoreActivate || req.Operation == StackKitLifecycleBackupRestoreRecover {
			mutationMayHaveBeenAdmitted = true
			if req.Operation == StackKitLifecycleBackupRestoreActivate && !resumeActivation {
				checkpoint = restoreActivationCheckpoint(req, *release, expectedPlanHash)
				job.mutateResult(func(result map[string]interface{}) { result[restoreActivationCheckpointKey] = checkpoint })
			}
			if q == nil || q.executionSnapshotSyncer() == nil {
				return NewConfigError(fmt.Errorf("restore activation requires durable job checkpoint acknowledgement"))
			}
			if err := q.executionSnapshotSyncer()(ctx, job.Snapshot()); err != nil {
				return NewPermanentError(fmt.Errorf("persist original restore activation authority before dispatch: %w", err))
			}
		}
		dispatchReq, dispatchID := req, job.ID
		if resumeActivation {
			dispatchReq.Operation = StackKitLifecycleBackupRestoreRecover
			dispatchReq.RestorePlanHash = expectedPlanHash
			dispatchID += "-recover"
		}
		command, result, err := sendStackKitLifecycleCommand(ctx, cfg.Sender, dispatchID, dispatchReq, *release, expectedPlanHash, originalRecoveryRelease)
		var reconciliation map[string]interface{}
		if req.Operation == StackKitLifecycleBackupRestoreActivate && !resumeActivation && (err != nil || result == nil || !result.Success) {
			// The effect may have committed before its ACK, or the CLI may have
			// persisted an automatic rollback despite returning nonzero. Resolve
			// only this original node journal, never infer outcome from stderr.
			recoverReq := req
			recoverReq.Operation = StackKitLifecycleBackupRestoreRecover
			recoverReq.RestorePlanHash = expectedPlanHash
			recoverCommand, recovered, recoverErr := sendStackKitLifecycleCommand(ctx, cfg.Sender, job.ID+"-recover", recoverReq, *release, expectedPlanHash)
			if recoverErr == nil {
				_, recoverErr = stackkitcommand.ParseRestoreActivationEvidence(recoverCommand, recovered)
			}
			if recoverErr != nil {
				job.replaceResult(unresolvedRestoreActivation(req, expectedPlanHash, checkpoint))
				return NewPermanentError(fmt.Errorf("restore activation outcome requires original-journal recovery: %w", recoverErr))
			}
			reconciliation, err = normalizeStackKitLifecycleResult(recoverReq, recovered)
			if err != nil {
				return NewPermanentError(err)
			}
			command, result = recoverCommand, recovered
			err = nil
		}
		if (req.Operation == StackKitLifecycleBackupRestoreRecover || resumeActivation) && (err != nil || result == nil || !result.Success) {
			job.replaceResult(unresolvedRestoreActivation(req, expectedPlanHash, checkpoint))
			if err == nil {
				err = fmt.Errorf("original node journal returned no verified terminal outcome")
			}
			return NewPermanentError(err)
		}
		if err != nil {
			return err
		}
		if resumeActivation {
			reconciliation, err = normalizeStackKitLifecycleResult(dispatchReq, result)
			if err != nil {
				return NewPermanentError(err)
			}
		}

		job.setStep("stackkit_result")
		normalized, err := normalizeStackKitLifecycleResult(req, result)
		if err != nil {
			return NewPermanentError(err)
		}
		if reconciliation != nil {
			normalized["reconciliation"] = reconciliation
		}
		if checkpoint != nil {
			normalized[restoreActivationCheckpointKey] = checkpoint
		}
		if inventoryRefresh != nil {
			normalized[managedInventoryRefreshResultField] = inventoryRefresh
		}
		if !result.Success {
			job.replaceResult(normalized)
			return NewPermanentError(fmt.Errorf("StackKits %s failed with exit code %d", req.Operation, result.ExitCode))
		}
		if req.Operation == StackKitLifecycleAdvancedTrustImport {
			binding, bindErr := admitAdvancedTrustImport(ctx, cfg.AdvancedIssuer, req, req.StackKitInstanceID, command, result)
			if bindErr != nil {
				job.replaceResult(normalized)
				return NewPermanentError(bindErr)
			}
			normalized["advanced_trust"] = binding
		}
		if evidence, evidenceErr := stackKitBackupEvidence(command, result); evidenceErr != nil {
			if stackkitcommand.IsRestoreActivationOperation(command.Operation) {
				normalized["success"] = false
				normalized["backup"] = unresolvedRestoreActivation(req, expectedPlanHash, checkpoint)["backup"]
			}
			job.replaceResult(normalized)
			return NewPermanentError(evidenceErr)
		} else if evidence != nil {
			normalized["backup"] = evidence
			if req.Operation == StackKitLifecycleBackupRestoreActivate && evidence["mode"] == "recovered" {
				normalized["success"] = false
				job.replaceResult(normalized)
				return NewPermanentError(fmt.Errorf("restore activation failed; verified prior application state was recovered"))
			}
		}
		var serviceEvidence *stackkitcommand.ServiceActionEvidence
		if req.Operation == StackKitLifecycleServiceStop || req.Operation == StackKitLifecycleServiceStart {
			evidence, evidenceErr := stackkitcommand.ParseServiceActionEvidence(command, result)
			if evidenceErr != nil {
				job.replaceResult(normalized)
				return NewPermanentError(evidenceErr)
			}
			serviceEvidence = &evidence
			normalized["service_action_evidence"] = evidence
		}
		if isStackKitServiceMutation(req.Operation) && req.Operation != StackKitLifecycleServiceStop {
			job.replaceResult(normalized)
			verifyRequest := req
			verifyRequest.Operation = StackKitLifecycleVerify
			verifyRequest.OwnerApproved = false
			verifyCommand, verifyErr := stackKitLifecycleCommand(job.ID+"-verify", verifyRequest, *release)
			if verifyErr != nil {
				return NewPermanentError(fmt.Errorf("build post-service verify: %w", verifyErr))
			}
			job.setStep("stackkit_service_verify")
			q.UpdateProgress(job.ID, 70, "Verifying StackKits service state")
			verifyResult, verifyErr := sendStackKitCommandBoundedForTenant(ctx, cfg.Sender, req.TenantID, req.AgentID, verifyCommand)
			if verifyErr != nil {
				return NewPermanentError(fmt.Errorf("post-service StackKits effect retained; verify dispatch requires readback: %w", verifyErr))
			}
			verifyNormalized, verifyErr := normalizeStackKitLifecycleResult(verifyRequest, verifyResult)
			if verifyErr != nil {
				return NewPermanentError(fmt.Errorf("normalize post-service StackKits verify: %w", verifyErr))
			}
			normalized["verification"] = verifyNormalized
			if !verifyResult.Success {
				job.replaceResult(normalized)
				return NewPermanentError(fmt.Errorf("post-service StackKits verify failed with exit code %d", verifyResult.ExitCode))
			}
		}
		if serviceEvidence != nil {
			// Retain the exact successful host effect even if the aggregate CAS
			// refuses a newer intent. Guard remains the observation authority.
			job.replaceResult(normalized)
			if err := projectDeclaredServiceIntent(ctx, cfg, job.ID, req, serviceIntent, *serviceEvidence); err != nil {
				return NewPermanentError(err)
			}
		}
		if req.Operation == StackKitLifecycleApply {
			// A successful typed Apply becomes the current node-local document
			// just as a managed rollout does. Retain it before later operations
			// replace the stack's transient runtime summary.
			normalized[StackKitRolloutBindingResultField] = stackKitRolloutBindingFor(req).Map()
		}
		job.replaceResult(normalized)
		q.UpdateProgress(job.ID, 100, "StackKits "+req.Operation+" completed")
		return nil
	}
}

const RestoreActivationCheckpointResultKey = controlplane.RestoreActivationAdmissionResultKey

const restoreActivationCheckpointKey = RestoreActivationCheckpointResultKey

func restoreActivationCheckpoint(req StackKitLifecycleRequest, release stackkitrelease.Release, planHash string) map[string]interface{} {
	pin := grpcserver.StackKitReleasePinFor(release)
	return map[string]interface{}{
		"tenant_id": req.TenantID, "owner_id": req.OwnerID, "stack_id": req.StackID,
		"stackkit_instance_id": req.StackKitInstanceID, "agent_id": req.AgentID,
		"node_id": req.NodeID, "node_ref": req.NodeRef, "working_directory": req.WorkingDirectory, "spec_path": req.SpecPath,
		"restore_result_id": req.RestoreResultID, "activation_operation_id": req.ActivationOperationID, "plan_hash": planHash,
		"release_version": pin.Version, "release_archive_sha256": pin.ArchiveSha256,
		"release_index_sha256": pin.ReleaseIndexSha256, "platform_os": pin.PlatformOs, "platform_arch": pin.PlatformArch,
	}
}

func matchesRestoreActivationCheckpoint(retained map[string]interface{}, req StackKitLifecycleRequest, release stackkitrelease.Release, planHash string) bool {
	if retained == nil {
		return false
	}
	for key, expected := range restoreActivationCheckpoint(req, release, planHash) {
		if stringFromInterface(retained[key]) != stringFromInterface(expected) {
			return false
		}
	}
	return true
}

func matchesRestoreActivationCheckpointIdentity(retained map[string]interface{}, req StackKitLifecycleRequest, release stackkitrelease.Release, planHash string) bool {
	if retained == nil {
		return false
	}
	for key, expected := range restoreActivationCheckpoint(req, release, planHash) {
		switch key {
		case "release_version", "release_archive_sha256", "release_index_sha256", "platform_os", "platform_arch":
			continue
		}
		if stringFromInterface(retained[key]) != stringFromInterface(expected) {
			return false
		}
	}
	return true
}

func unresolvedRestoreActivation(req StackKitLifecycleRequest, planHash string, checkpoint map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{
		"schema_version": "techstack.stackkit-lifecycle-result/v1", "techstack_id": req.StackID,
		"stackkit_instance_id": req.StackKitInstanceID, "agent_id": req.AgentID,
		"operation": req.Operation, "success": false,
		"backup": map[string]interface{}{"mode": "recovery-required", "restore_result_id": req.RestoreResultID, "activation_operation_id": req.ActivationOperationID, "plan_hash": planHash},
	}
	if checkpoint != nil {
		result[restoreActivationCheckpointKey] = checkpoint
	}
	if receipt := RestoreActivationRequestReceipt(req); receipt != nil {
		result[controlplane.RestoreActivationRequestResultKey] = receipt
	}
	return result
}

// sendStackKitLifecycleCommand dispatches one typed lifecycle command. A Guard
// that refuses it because its runtime convergence is still pending (a server
// paired moments ago) has not run it, so the command is sent again under a new
// command ID after a short wait, like the deploy sequence does, instead of
// failing the job. Any other result, success or failure, is returned as is.
func sendStackKitLifecycleCommand(ctx context.Context, sender StackKitCommandSender, commandID string, req StackKitLifecycleRequest, release stackkitrelease.Release, expectedPlanHash string, original ...*agentpb.StackKitReleasePin) (*agentpb.StackKitCommand, *agentpb.StackKitResult, error) {
	for attempt := 1; ; attempt++ {
		id := commandID
		if attempt > 1 {
			id = fmt.Sprintf("%s-%d", commandID, attempt)
		}
		command, err := stackKitLifecycleCommand(id, req, release)
		if err != nil {
			return nil, nil, NewPermanentError(err)
		}
		command.ExpectedPlanHash = expectedPlanHash
		if len(original) > 0 && original[0] != nil {
			command.OriginalRecoveryRelease = proto.Clone(original[0]).(*agentpb.StackKitReleasePin)
		}
		result, err := sendStackKitCommandBoundedForTenant(ctx, sender, req.TenantID, req.AgentID, command)
		if err != nil {
			return nil, nil, NewResourceError(fmt.Errorf("typed StackKits dispatch failed: %w", err))
		}
		if !stackKitRuntimeConvergencePending(result) || attempt == typedStackKitRuntimePendingMaxAttempts {
			return command, result, nil
		}
		if err := waitForTypedStackKitRuntimeRetry(ctx); err != nil {
			return command, result, nil
		}
	}
}

func planStackKitLifecycleApply(ctx context.Context, sender StackKitCommandSender, jobID string, req StackKitLifecycleRequest, release stackkitrelease.Release) (string, error) {
	planRequest := req
	planRequest.Operation = StackKitLifecyclePlan
	planRequest.OwnerApproved = false
	_, result, err := sendStackKitLifecycleCommand(ctx, sender, jobID+"-plan", planRequest, release, "")
	if err != nil {
		return "", fmt.Errorf("typed StackKits plan admission dispatch failed: %w", err)
	}
	if result == nil || !result.Success {
		return "", fmt.Errorf("typed StackKits plan admission failed")
	}
	planHash, err := typedStackKitPlanHash(result)
	if err != nil {
		return "", fmt.Errorf("admit typed StackKits plan: %w", err)
	}
	return planHash, nil
}

func isStackKitServiceMutation(operation string) bool {
	switch operation {
	case StackKitLifecycleServiceStart, StackKitLifecycleServiceStop, StackKitLifecycleServiceRestart:
		return true
	default:
		return false
	}
}

func stackKitLifecycleRequestFromJob(job *Job) (StackKitLifecycleRequest, error) {
	if job == nil {
		return StackKitLifecycleRequest{}, fmt.Errorf("StackKits lifecycle job is required")
	}
	req := StackKitLifecycleRequest{
		StackID:                     job.TargetID,
		StackKitInstanceID:          stringFromInterface(job.Payload["stackkit_instance_id"]),
		TenantID:                    stringFromInterface(job.Payload["tenant_id"]),
		OwnerID:                     stringFromInterface(job.Payload["owner_id"]),
		AgentID:                     stringFromInterface(job.Payload["agent_id"]),
		NodeID:                      stringFromInterface(job.Payload["node_id"]),
		NodeRef:                     stringFromInterface(job.Payload["node_ref"]),
		ManagedRuntimeLeaseID:       stringFromInterface(job.Payload["managed_runtime_lease_id"]),
		ManagedRuntimeServerID:      stringFromInterface(job.Payload["managed_runtime_server_id"]),
		Operation:                   stringFromInterface(job.Payload["operation"]),
		TargetRelease:               stringFromInterface(job.Payload["target_release"]),
		DryRun:                      boolFromInterface(job.Payload["dry_run"]),
		Offline:                     boolFromInterface(job.Payload["offline"]),
		OwnerApproved:               boolFromInterface(job.Payload["owner_approved"]),
		WorkingDirectory:            stringFromInterface(job.Payload["working_directory"]),
		SpecPath:                    stringFromInterface(job.Payload["spec_path"]),
		StackName:                   stringFromInterface(job.Payload["stack_name"]),
		Domain:                      stringFromInterface(job.Payload["domain"]),
		ExpectedSpecHash:            stringFromInterface(job.Payload["expected_spec_hash"]),
		CandidateSpecJSON:           []byte(stringFromInterface(job.Payload["candidate_spec_json"])),
		ServiceKey:                  stringFromInterface(job.Payload["service_key"]),
		LogTail:                     int32FromInterface(job.Payload["log_tail"]),
		LogCursor:                   stringFromInterface(job.Payload["log_cursor"]),
		ServiceID:                   stringFromInterface(job.Payload["service_id"]),
		DurableJobID:                stringFromInterface(job.Payload["durable_job_id"]),
		ServiceActionDigest:         stringFromInterface(job.Payload["service_action_digest"]),
		WorkloadRef:                 stringFromInterface(job.Payload["workload_ref"]),
		SnapshotAnchorID:            stringFromInterface(job.Payload["snapshot_anchor_id"]),
		RestoreResultID:             stringFromInterface(job.Payload["restore_result_id"]),
		ActivationOperationID:       stringFromInterface(job.Payload["activation_operation_id"]),
		RestorePlanHash:             stringFromInterface(job.Payload["restore_plan_hash"]),
		RestoreIdempotencyKeySHA256: stringFromInterface(job.Payload["restore_idempotency_key_sha256"]),
		RollbackTargetRef:           stringFromInterface(job.Payload["rollback_target_ref"]),
		AIProviderBaseURL:           stringFromInterface(job.Payload["ai_provider_base_url"]),
		WalletItemID:                stringFromInterface(job.Payload["wallet_item_id"]),
		WalletItemRotate:            boolFromInterface(job.Payload["wallet_item_rotate"]),
		AIProviderDisable:           boolFromInterface(job.Payload["ai_provider_disable"]),
		OriginHosts:                 stringListFromInterface(job.Payload["origin_hosts"]),
		RecipientChallenge:          stringFromInterface(job.Payload["recipient_challenge"]),
		SealedDelivery:              []byte(stringFromInterface(job.Payload["sealed_delivery"])),
		StackKit: firstNonEmpty(
			stringFromInterface(job.Payload["stackkit"]),
			stringFromInterface(job.Payload["stackkit_catalog_ref"]),
			stringFromInterface(job.Payload["catalog_ref"]),
		),
	}
	if raw := job.Payload["connector_binding"]; raw != nil {
		data, _ := json.Marshal(raw)
		if err := json.Unmarshal(data, &req.Connector); err != nil {
			return req, err
		}
	}
	if raw := job.Payload["application_binding"]; raw != nil {
		data, _ := json.Marshal(raw)
		if err := json.Unmarshal(data, &req.Application); err != nil {
			return req, err
		}
	}
	if raw := job.Payload["wizard_extend_binding"]; raw != nil {
		data, err := json.Marshal(raw)
		if err != nil {
			return req, err
		}
		if err := json.Unmarshal(data, &req.WizardExtend); err != nil {
			return req, err
		}
	}
	if raw := job.Payload["ril_operation_binding"]; raw != nil {
		data, err := json.Marshal(raw)
		if err != nil {
			return req, err
		}
		if err := json.Unmarshal(data, &req.RILOperation); err != nil {
			return req, err
		}
	}
	normalized, err := NormalizeStackKitLifecycleRequest(req)
	if err != nil {
		return req, err
	}
	if normalized.RILOperation != nil && normalized.DurableJobID != job.ID {
		return req, fmt.Errorf("RIL lifecycle intent belongs to a different durable job")
	}
	if normalized.WizardExtend != nil && normalized.DurableJobID != job.ID {
		return req, fmt.Errorf("wizard extension belongs to a different durable job")
	}
	return normalized, nil
}

func stackKitLifecycleCommand(commandID string, req StackKitLifecycleRequest, release stackkitrelease.Release) (*agentpb.StackKitCommand, error) {
	operation, err := stackKitLifecycleAgentOperation(req.Operation)
	if err != nil {
		return nil, err
	}
	var binding localExecutionBinding
	if req.WizardExtend != nil && req.Operation == StackKitLifecyclePlan {
		// The canonical read inspects the enrolled node's own workspace and
		// needs no local execution tuple, including on an arbitrary joined host.
	} else if req.WizardExtend != nil && req.WizardExtend.ExecutionBinding != nil {
		admitted := req.WizardExtend.ExecutionBinding
		if err := admitted.Validate(); err != nil || admitted.NodeRef != req.NodeRef {
			return nil, fmt.Errorf("wizard lifecycle execution binding is invalid")
		}
		binding = localExecutionBinding{SiteRef: admitted.SiteRef, NodeRef: admitted.NodeRef, ExecutionChannelRef: admitted.ExecutionChannelRef}
	} else {
		var err error
		binding, err = localExecutionBindingForNode(req.StackKit, req.NodeRef)
		if err != nil {
			return nil, err
		}
	}
	specPath := req.SpecPath
	if specPath == "" {
		specPath = defaultStackKitSpecPath
	}
	command := &agentpb.StackKitCommand{
		CommandId:                  commandID,
		Operation:                  operation,
		WorkingDirectory:           req.WorkingDirectory,
		SpecPath:                   specPath,
		OutputDirectory:            "deploy",
		TimeoutSeconds:             stackKitLifecycleTimeout(operation),
		Release:                    grpcserver.StackKitReleasePinFor(release),
		Offline:                    req.Offline,
		DryRun:                     req.DryRun,
		TargetRelease:              req.TargetRelease,
		OwnerApproved:              req.OwnerApproved,
		LocalSiteRef:               binding.SiteRef,
		LocalNodeRef:               binding.NodeRef,
		LocalExecutionChannelRef:   binding.ExecutionChannelRef,
		Stackkit:                   req.StackKit,
		StackName:                  req.StackName,
		Domain:                     req.Domain,
		ExpectedSpecHash:           req.ExpectedSpecHash,
		ServiceKey:                 req.ServiceKey,
		LogTail:                    req.LogTail,
		LogCursor:                  req.LogCursor,
		StackkitInstanceId:         req.StackKitInstanceID,
		WorkloadRef:                req.WorkloadRef,
		AddressPrefix:              req.AddressPrefix,
		BoundSpecPath:              req.BoundSpecPath,
		SourceGenerationInspection: req.sourceGenerationInspection && (operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN || operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_RECIPIENT),
	}
	if operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE {
		command.SnapshotAnchorId = req.SnapshotAnchorID
	}
	if stackkitcommand.IsRestoreActivationOperation(operation) {
		command.RestoreResultId = req.RestoreResultID
		command.ActivationOperationId = req.ActivationOperationID
	}
	if stackkitcommand.IsWalletItemDeliveryOperation(operation) {
		// Public values and ciphertext only: the Wallet sealed the item to
		// the node's recipient key.
		switch operation {
		case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT:
			command.RecipientChallenge = req.RecipientChallenge
			command.DeliveryAuthorityKey, command.DeliveryAuthorityKeyId = req.DeliveryAuthorityKey, req.DeliveryAuthorityKeyID
		case agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT, agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_SSH_APPLY:
			command.SealedDelivery = append([]byte(nil), req.SealedDelivery...)
			command.DeliveryTenantRef = req.DeliveryTenantRef
		case agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP:
			command.AiProviderDisable = req.AIProviderDisable
			if !req.AIProviderDisable {
				command.AiProviderBaseUrl, command.WalletItemRef = req.AIProviderBaseURL, req.WalletItemID
				command.DeliveryTenantRef = stackkitcommand.DeliveryTenantRef(req.TenantID)
			}
		}
		return command, nil
	}
	if stackkitcommand.IsWorkloadRelocationOperation(operation) {
		// The plan is public; the transfer secret stays in the node's Wallet
		// item custody, so only the item and its tenant are named.
		command.RelocationProjects = append([]string(nil), req.RelocationProjects...)
		command.RelocationResourceUuids = append([]string(nil), req.RelocationResourceUUIDs...)
		command.RelocationTransferId = req.RelocationTransferID
		command.RelocationPlanJson = append([]byte(nil), req.RelocationPlanJSON...)
		command.RelocationSourceAddress = req.RelocationSourceAddress
		command.RelocationSetDomains = append([]string(nil), req.RelocationSetDomains...)
		if operation == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND || operation == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE {
			command.WalletItemRef = req.WalletItemID
			command.DeliveryTenantRef = stackkitcommand.DeliveryTenantRef(req.TenantID)
		}
		return command, nil
	}
	if stackkitcommand.IsBackupTargetDeliveryOperation(operation) {
		// The node issues its own binding: no Inventory, only the sealed
		// ciphertext (import, rebind) or the public challenge (recipient).
		command.SealedDelivery = append([]byte(nil), req.SealedDelivery...)
		command.DeliveryTenantRef = req.DeliveryTenantRef
		command.RecipientChallenge = req.RecipientChallenge
		return command, nil
	}
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE:
		command.CandidateSpecJson = append([]byte(nil), req.CandidateSpecJSON...)
		command.InventoryJson = append([]byte(nil), req.InventoryJSON...)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_REQUEST,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_INSTALL:
		// The origin certificate operations take only the capability and their
		// own public inputs (hostnames, certificate); no Inventory.
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ROLLBACK:
		command.RollbackTargetRef = req.RollbackTargetRef
		command.InventoryJson = append([]byte(nil), req.InventoryJSON...)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL:
		command.SnapshotAnchorId = req.SnapshotAnchorID
		command.InventoryJson = append([]byte(nil), req.InventoryJSON...)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_INIT:
		command.CandidateSpecJson = append([]byte(nil), req.CandidateSpecJSON...)
		command.OwnerEmail = strings.TrimSpace(req.OwnerEmail)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_FEDERATION_HANDOFF:
		// A hand-off moves one public document and takes no Inventory.
		command.HandoffStep = req.HandoffStep
		command.HandoffRef = req.HandoffRef
		command.HandoffRef2 = req.HandoffRef2
		command.HandoffDocument = append([]byte(nil), req.HandoffDocument...)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT:
		// Trust import reads only the bundle; it needs no Inventory.
		if len(req.AdvancedTrustBundle) == 0 {
			return nil, advancedissuer.ErrUnavailable
		}
		command.AdvancedTrustBundle = append([]byte(nil), req.AdvancedTrustBundle...)
	default:
		// Init admits desired intent without Inventory. Deliver observed facts
		// with the following operation, keeping the two bounded documents out
		// of the same transport frame.
		command.InventoryJson = append([]byte(nil), req.InventoryJSON...)
	}
	return command, nil
}

func stackKitLifecycleAgentOperation(operation string) (agentpb.StackKitOperation, error) {
	if operation == StackKitLifecycleApplicationConnector {
		return agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR, nil
	}
	if operation == StackKitLifecycleApplicationConnectorVerify {
		return agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR_VERIFY, nil
	}
	if op, ok := applicationAgentOperation(operation); ok {
		return op, nil
	}
	switch operation {
	case StackKitLifecycleInit:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_INIT, nil
	case StackKitLifecycleGenerate:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE, nil
	case StackKitLifecyclePlan:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN, nil
	case stackKitStepPrepareSourceGeneration:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_PREPARE_SOURCE_GENERATION, nil
	case StackKitLifecycleApply:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY, nil
	case StackKitLifecycleVerify:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY, nil
	case StackKitLifecycleUpgrade:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE, nil
	case StackKitLifecycleDriftDetect:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_DETECT, nil
	case stackKitStepChangeSetCreate:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE, nil
	case stackKitStepChangeSetApply:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY, nil
	case stackKitStepDriftReconcile:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE, nil
	case stackKitStepRollback:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ROLLBACK, nil
	case stackKitStepRestoreDrill:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL, nil
	case stackKitStepOriginCertificateRequest:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_REQUEST, nil
	case stackKitStepOriginCertificateInstall:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_INSTALL, nil
	case StackKitLifecycleServiceStart:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START, nil
	case StackKitLifecycleServiceStop:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP, nil
	case StackKitLifecycleServiceRestart:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART, nil
	case StackKitLifecycleServiceLogs:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS, nil
	case StackKitLifecycleRemove:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_REMOVE, nil
	case StackKitLifecycleAddressBind:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADDRESS_BIND, nil
	case StackKitLifecycleAdvancedTrustImport:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT, nil
	case StackKitLifecycleFederationHandoff:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_FEDERATION_HANDOFF, nil
	case StackKitLifecycleBackupRun:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN, nil
	case StackKitLifecycleBackupStatus:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_STATUS, nil
	case StackKitLifecycleNetworkRebind:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_NETWORK_REBIND, nil
	case StackKitLifecycleNetworkStatus:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_NETWORK_STATUS, nil
	case StackKitLifecycleBackupConfigure:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE, nil
	case StackKitLifecycleBackupRestore:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE, nil
	case StackKitLifecycleBackupRestoreActivate:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_ACTIVATE, nil
	case StackKitLifecycleBackupRestoreRecover:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_RECOVER, nil
	case StackKitLifecycleBackupTargetRecipient:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_RECIPIENT, nil
	case StackKitLifecycleBackupTargetImport:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_IMPORT, nil
	case StackKitLifecycleBackupTargetRebind:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_REBIND, nil
	case stackKitStepHostDeliveryRecipient:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT, nil
	case stackKitStepWalletItemImport:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT, nil
	case stackKitStepAIProviderSetup:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP, nil
	case stackKitStepHostSSHApply:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_SSH_APPLY, nil
	case stackKitStepRelocatePlan:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN, nil
	case stackKitStepRelocateCheck:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK, nil
	case stackKitStepRelocateDrain:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN, nil
	case stackKitStepRelocateUndrain:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN, nil
	case stackKitStepRelocateSend:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND, nil
	case stackKitStepRelocateReceive:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE, nil
	case stackKitStepRelocateStatus:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_STATUS, nil
	case stackKitStepRelocateAbort:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_ABORT, nil
	default:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_UNSPECIFIED, fmt.Errorf("unsupported StackKits lifecycle operation %q", operation)
	}
}

func stackKitLifecycleTimeout(operation agentpb.StackKitOperation) int32 {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_PREPARE_SOURCE_GENERATION,
		agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_NETWORK_REBIND,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_REMOVE:
		return int32(stackKitWriteCommandTimeout / time.Second)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_ACTIVATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_RECOVER,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ROLLBACK,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL:
		// The managed restore drill's per-step budget.
		return int32(stackKitBackupCommandTimeout / time.Second)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE:
		return int32(stackKitAdvancedMutationCommandTimeout / time.Second)
	default:
		return int32(stackKitReadCommandTimeout / time.Second)
	}
}

func sendStackKitCommandBoundedForTenant(ctx context.Context, sender StackKitCommandSender, tenantID, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	return sendStackKitCommandBoundedWithGraceForTenant(ctx, sender, tenantID, agentID, command, stackKitCommandResultGrace)
}

func sendStackKitCommandBoundedWithGraceForTenant(ctx context.Context, sender StackKitCommandSender, tenantID, agentID string, command *agentpb.StackKitCommand, resultGrace time.Duration) (*agentpb.StackKitResult, error) {
	if sender == nil {
		return nil, fmt.Errorf("typed StackKits dispatcher is not configured")
	}
	if resultGrace <= 0 {
		resultGrace = stackKitCommandResultGrace
	}
	timeout := stackKitReadCommandTimeout + resultGrace
	if command != nil && command.TimeoutSeconds > 0 {
		timeout = time.Duration(command.TimeoutSeconds)*time.Second + resultGrace
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if scoped, ok := sender.(tenantStackKitCommandSender); ok {
		return scoped.SendStackKitCommandForTenant(commandCtx, tenantID, agentID, command)
	}
	return sender.SendStackKitCommand(commandCtx, agentID, command)
}

func normalizeStackKitLifecycleResult(req StackKitLifecycleRequest, result *agentpb.StackKitResult) (map[string]interface{}, error) {
	if result == nil {
		return nil, fmt.Errorf("typed StackKits result is missing")
	}
	var commandResult map[string]interface{}
	if err := json.Unmarshal(result.CommandResultJson, &commandResult); err != nil {
		return nil, fmt.Errorf("decode StackKits command result: %w", err)
	}
	events := make([]interface{}, 0, len(result.EventsJsonl))
	for _, raw := range result.EventsJsonl {
		var event map[string]interface{}
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, fmt.Errorf("decode StackKits rollout event: %w", err)
		}
		events = append(events, event)
	}
	status := stackKitLifecycleCommandDataStatus(commandResult)
	normalized := map[string]interface{}{
		"schema_version":       "techstack.stackkit-lifecycle-result/v1",
		"techstack_id":         req.StackID,
		"stackkit_instance_id": req.StackKitInstanceID,
		"operation":            req.Operation,
		"agent_id":             req.AgentID,
		"success":              result.Success,
		"exit_code":            result.ExitCode,
		"status":               status,
		"command_result":       commandResult,
		"events":               events,
		"stderr":               result.Stderr,
		"release": map[string]interface{}{
			"version":              result.Release.GetVersion(),
			"platform_os":          result.Release.GetPlatformOs(),
			"platform_arch":        result.Release.GetPlatformArch(),
			"archive_sha256":       result.Release.GetArchiveSha256(),
			"release_index_sha256": result.Release.GetReleaseIndexSha256(),
		},
	}
	if receipt := StackKitServiceActionReceipt(req); receipt != nil {
		normalized["service_action_receipt"] = receipt
	}
	if receipt := RILStackKitOperationReceipt(req); receipt != nil {
		normalized[RILOperationReceiptKey] = receipt
	}
	if receipt := RestoreActivationRequestReceipt(req); receipt != nil {
		normalized[controlplane.RestoreActivationRequestResultKey] = receipt
	}
	if req.Operation == StackKitLifecycleDriftDetect && result.Success {
		report, err := stackkitcommand.ParseDriftReport(&agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_DETECT}, result)
		if err != nil {
			return nil, fmt.Errorf("admit StackKits drift report: %w", err)
		}
		normalized["drift_report"] = driftReportMap(report)
	}
	if req.Operation == StackKitLifecycleServiceLogs {
		data, _ := commandResult["data"].(map[string]interface{})
		if output := strings.TrimSpace(stringFromInterface(data["output"])); output != "" {
			var page map[string]interface{}
			if err := json.Unmarshal([]byte(output), &page); err != nil {
				return nil, fmt.Errorf("decode StackKits service log page: %w", err)
			}
			normalized["service_logs"] = page
		}
	}
	if req.Operation == StackKitLifecycleRemove && result.Success {
		data, _ := commandResult["data"].(map[string]interface{})
		raw := []byte(strings.TrimSpace(stringFromInterface(data["output"])))
		evidence, err := workloadremoval.ParseEvidence(raw)
		if err != nil {
			return nil, fmt.Errorf("validate StackKits workload-removal evidence: %w", err)
		}
		if evidence.Authority.WorkloadRef != req.WorkloadRef {
			return nil, fmt.Errorf("StackKits workload-removal evidence does not match dispatched workload authority")
		}
		var persisted map[string]interface{}
		if err := json.Unmarshal(raw, &persisted); err != nil {
			return nil, fmt.Errorf("persist StackKits workload-removal evidence: %w", err)
		}
		normalized["removal_evidence"] = persisted
	}
	return normalized, nil
}

// IsStackKitBackupOperation reports the native backup operations.
func IsStackKitBackupOperation(operation string) bool {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case StackKitLifecycleBackupRun, StackKitLifecycleBackupStatus,
		StackKitLifecycleBackupConfigure, StackKitLifecycleBackupRestore,
		StackKitLifecycleBackupRestoreActivate, StackKitLifecycleBackupRestoreRecover:
		return true
	default:
		return false
	}
}

var stackKitSnapshotAnchorPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// stackKitBackupEvidence admits the snapshot or staged-restore identity of a
// successful backup operation, so the caller can name the snapshot a later
// staged restore reads. Other operations carry no backup evidence.
func stackKitBackupEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (map[string]interface{}, error) {
	if command == nil || result == nil || !result.Success {
		return nil, nil
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN:
		evidence, err := stackkitcommand.ParseBackupRunEvidence(command, result)
		if err != nil {
			return nil, fmt.Errorf("admit StackKits backup run evidence: %w", err)
		}
		return map[string]interface{}{
			"snapshot_anchor_id": evidence.SnapshotAnchorID, "operation_id": evidence.OperationID,
			"owner_ref": evidence.OwnerRef, "plan_hash": evidence.PlanHash,
		}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE:
		evidence, err := stackkitcommand.ParseBackupRestoreEvidence(command, result)
		if err != nil {
			return nil, fmt.Errorf("admit StackKits staged restore evidence: %w", err)
		}
		return map[string]interface{}{
			"mode": "staged", "restore_result_id": evidence.RestoreResultID,
			"snapshot_anchor_id": evidence.SnapshotAnchorID, "operation_id": evidence.OperationID,
			"owner_ref": evidence.OwnerRef, "plan_hash": evidence.PlanHash,
			"verified_at": evidence.VerifiedAt.UTC().Format(time.RFC3339Nano),
		}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE:
		if err := stackkitcommand.ParseBackupConfigurationEvidence(command, result); err != nil {
			return nil, fmt.Errorf("admit StackKits backup configuration evidence: %w", err)
		}
		return map[string]interface{}{"status": "configured"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_ACTIVATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_RECOVER:
		evidence, err := stackkitcommand.ParseRestoreActivationEvidence(command, result)
		if err != nil {
			return nil, err
		}
		data := map[string]interface{}{
			"mode": evidence.Status, "restore_result_id": evidence.RestoreResultID,
			"activation_operation_id": evidence.OperationID, "safety_snapshot_id": evidence.SafetySnapshotID,
			"owner_ref": evidence.OwnerRef, "plan_hash": evidence.PlanHash,
			"managed_volume_set_hash": evidence.ManagedVolumeSetHash,
			"verified_at":             evidence.VerifiedAt.UTC().Format(time.RFC3339Nano),
		}
		if original := command.OriginalRecoveryRelease; original != nil {
			data["original_recovery_release"] = map[string]interface{}{
				"version": original.Version, "platform_os": original.PlatformOs, "platform_arch": original.PlatformArch,
				"archive_sha256": original.ArchiveSha256, "release_index_sha256": original.ReleaseIndexSha256,
			}
		}
		return data, nil
	default:
		return nil, nil
	}
}

func IsStackKitServiceLifecycleOperation(operation string) bool {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case StackKitLifecycleServiceStart, StackKitLifecycleServiceStop,
		StackKitLifecycleServiceRestart, StackKitLifecycleServiceLogs:
		return true
	default:
		return false
	}
}

func stackKitLifecycleCommandDataStatus(commandResult map[string]interface{}) string {
	data, ok := commandResult["data"].(map[string]interface{})
	if !ok {
		return ""
	}
	status, _ := data["status"].(string)
	return strings.ToLower(strings.TrimSpace(status))
}

// admitAdvancedTrustImport accepts a host's trust-import evidence only when it
// pins the exact bundle dispatched, then records the local Owner the host
// bound it to. Capabilities for the deployment are scoped to that Owner.
func admitAdvancedTrustImport(
	ctx context.Context,
	issuer AdvancedIssuer,
	req StackKitLifecycleRequest,
	stackKitStackID string,
	command *agentpb.StackKitCommand,
	result *agentpb.StackKitResult,
) (map[string]interface{}, error) {
	evidence, err := stackkitcommand.ParseAdvancedTrustImportEvidence(command, result)
	if err != nil {
		return nil, err
	}
	if issuer == nil {
		return nil, advancedissuer.ErrUnavailable
	}
	if err := issuer.RecordTrustBinding(ctx, advancedissuer.TrustBinding{
		TenantID: req.TenantID, DeploymentID: req.StackID, StackID: stackKitStackID,
		OwnerRef: evidence.OwnerRef, BundleSHA256: evidence.BundleSHA256,
	}); err != nil {
		return nil, fmt.Errorf("record advanced trust binding: %w", err)
	}
	return map[string]interface{}{
		"status": "imported", "owner_ref": evidence.OwnerRef,
		"bundle_sha256": evidence.BundleSHA256, "stackkit_stack_id": stackKitStackID,
	}, nil
}
