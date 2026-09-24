package providercontrol

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

// Provider-control errors classify admission, persistence, claim, authority,
// cleanup-custody, and immutable-command failures without exposing secrets.
var (
	ErrAdapterNotRegistered     = errors.New("providercontrol: adapter not registered")
	ErrDuplicateAdapter         = errors.New("providercontrol: adapter already registered")
	ErrInvalidRequest           = errors.New("providercontrol: invalid request")
	ErrOperationNotFound        = errors.New("providercontrol: operation not found")
	ErrLedgerConflict           = errors.New("providercontrol: ledger conflict")
	ErrExecutionClaimHeld       = errors.New("providercontrol: execution claim is held")
	ErrExecutionClaimLost       = errors.New("providercontrol: execution claim was lost")
	ErrExecutionClaimNeeded     = errors.New("providercontrol: execution claim is required")
	ErrDispatchPermitNeeded     = errors.New("providercontrol: provision dispatch permit is required")
	ErrProvisionManualReconcile = errors.New("providercontrol: provision requires manual reconciliation")
	ErrAdapterSafety            = errors.New("providercontrol: adapter execution-safety capability is required")
	ErrCrashRecovery            = errors.New("providercontrol: crash-recovery capability is required")
	ErrCleanupCustody           = errors.New("providercontrol: provider resource cleanup custody is required")
	ErrEvidenceVerifier         = errors.New("providercontrol: provider evidence verifier is required")
	ErrExecutionAuthority       = errors.New("providercontrol: execution authority conflict")
	ErrLeaseFence               = errors.New("providercontrol: runtime lease fence conflict")
	ErrProfileUnavailable       = errors.New("providercontrol: execution profile unavailable")
	ErrClaimCredentialDenied    = errors.New("providercontrol: claim credential authorization denied")
)

// ExecutionAuthority identifies the component which must finish a provider
// operation once it has begun. It is immutable for the operation lifetime.
type ExecutionAuthority string

const (
	// ExecutionAuthorityTechstackProviderControl is the only executable
	// authority admitted by this package.
	ExecutionAuthorityTechstackProviderControl ExecutionAuthority = "techstack_provider_control"
)

// ExecutionProfile contains opaque, server-side references and immutable
// hashes selected by TechStack. It must never contain raw provider credentials.
type ExecutionProfile struct {
	ProviderID             string
	AdapterID              string
	CredentialMode         CredentialMode
	RuntimeProfileID       string
	OfferingID             string
	CatalogVersion         string
	CapabilitySnapshotHash string
	AdapterManifestHash    string
	ProvisionDispatchMode  ProvisionDispatchMode
	CustodyRef             string
	CustodyHash            string
	ConnectionRef          string
	ConnectionHash         string
	// ManagedBootstrapNetwork is the normalized catalog contract consumed by
	// the execution-profile admission/replay boundary. Provider adapters keep
	// ownership of their provider-specific execution details.
	ManagedBootstrapNetwork *ManagedBootstrapNetworkProfile
	ExecutionProfileHash    string
}

// CredentialMode identifies how an opaque, tenant-scoped credential handle is
// resolved. It never carries credential material.
type CredentialMode string

const (
	// CredentialModeManaged resolves a platform-managed tenant credential handle.
	CredentialModeManaged CredentialMode = "managed"
	// CredentialModeBYOK resolves a tenant-provided tenant credential handle.
	CredentialModeBYOK CredentialMode = "byok"
	// The token stays on an enrolled customer substrate Guard.
	CredentialModeWorkerHeld CredentialMode = "worker_held"
)

// ExecutionProfileSnapshot is the secret-free, immutable profile identity
// persisted in the operation envelope beside the sealed provider command.
type ExecutionProfileSnapshot struct {
	ProviderID             string                `json:"provider_id"`
	AdapterID              string                `json:"adapter_id"`
	CredentialMode         CredentialMode        `json:"credential_mode"`
	RuntimeProfileID       string                `json:"runtime_profile_id"`
	OfferingID             string                `json:"offering_id"`
	CatalogVersion         string                `json:"catalog_version"`
	CapabilitySnapshotHash string                `json:"capability_snapshot_hash"`
	AdapterManifestHash    string                `json:"adapter_manifest_hash"`
	ProvisionDispatchMode  ProvisionDispatchMode `json:"provision_dispatch_mode"`
	// ManagedBootstrapNetwork is persisted with the operation so retries and
	// crash recovery validate the same catalog-selected bootstrap contract.
	ManagedBootstrapNetwork *ManagedBootstrapNetworkProfile `json:"managed_bootstrap_network,omitempty"`
	ExecutionProfileHash    string                          `json:"execution_profile_hash"`
}

// ProfileRequest is the minimum authority context needed to select an
// execution profile. Provider-specific settings stay behind the resolver.
type ProfileRequest struct {
	TenantID             string
	LeaseID              string
	LeaseRevision        uint64
	RuntimeServerID      string
	ResourceGenerationID string
	Operation            providerexecutor.Operation
}

// ExecutionProfileResolver resolves TechStack-owned adapter and credential
// custody references. Implementations may use tenant configuration or a secret
// broker, but must return references and hashes rather than credentials.
type ExecutionProfileResolver interface {
	ResolveExecutionProfile(context.Context, ProfileRequest) (ExecutionProfile, error)
}

// DesiredSpecRevision is the immutable TechStack custody record for the
// provider-neutral desired input used by plan, provision, and reconcile.
// Payload must be valid JSON and must not contain raw provider credentials.
type DesiredSpecRevision struct {
	TenantID  string
	LeaseID   string
	Revision  uint64
	Ref       string
	Digest    string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// StartRequest authorizes one provider operation. The resource-graph hash is
// derived from Targets while LedgerRevision is supplied by TechStack's
// authoritative ledger. RequestedAt must remain stable across retries.
type StartRequest struct {
	TenantID             string
	LeaseID              string
	LeaseRevision        uint64
	RuntimeServerID      string
	ResourceGenerationID string
	Operation            providerexecutor.Operation
	IdempotencyKey       string
	LedgerRevision       uint64
	DesiredSpec          *DesiredSpecRevision
	Targets              []providerexecutor.ResourceTarget
	RequestedAt          time.Time
}

// OperationRecord is the durable command and current ledger head.
type OperationRecord struct {
	ExecutionAuthority ExecutionAuthority       `json:"execution_authority"`
	ExecutionProfile   ExecutionProfileSnapshot `json:"execution_profile"`
	ProvisionDispatch  ProvisionDispatchMode    `json:"provision_dispatch_mode"`
	AutomationState    OperationAutomationState `json:"automation_state"`
	// AutomationReasonCode is a stable, secret-free explanation derived from
	// durable custody. It is intentionally separate from the neutral receipt's
	// terminal Reason, which cannot exist on a pending accepted head.
	AutomationReasonCode string `json:"automation_reason_code,omitempty"`
	// RuntimeServerGeneration is TechStack's immutable private pin to the
	// numeric RuntimeServer generation which admitted this operation. The
	// shared providerexecutor wire continues to carry the independent UUID
	// ResourceGenerationID owned by Lease Authority.
	RuntimeServerGeneration int64                    `json:"runtime_server_generation"`
	Command                 providerexecutor.Command `json:"command"`
	Head                    providerexecutor.Receipt `json:"head"`
}

// OperationAutomationState is TechStack's derived scheduling projection. It
// is deliberately not a providerexecutor receipt phase.
type OperationAutomationState string

const (
	OperationAutomationRunnable                OperationAutomationState = "runnable"
	OperationAutomationManualReconcileRequired OperationAutomationState = "manual_reconcile_required"
	OperationAutomationComplete                OperationAutomationState = "complete"
)

// OperationRef identifies one tenant-scoped operation without exposing its
// command, credential references, or receipt payload.
type OperationRef struct {
	TenantID    string `json:"tenant_id"`
	OperationID string `json:"operation_id"`
}

// ExecutionClaim is a short-lived, durable capability to invoke an adapter
// for precisely one current receipt head. It is not provider authority: the
// adapter must derive its provider-native idempotency key from Command's
// immutable OperationID and IdempotencyKey before performing a side effect.
type ExecutionClaim struct {
	TenantID             string
	OperationID          string
	ResourceGenerationID string
	HeadSequence         uint64
	HeadReceiptDigest    string
	Access               ExecutionClaimAccess
	Token                string
	Owner                string
	ExpiresAt            time.Time
}

// ExecutionClaimAccess states whether one exact head claim may cross a
// provider-mutation seam. Read-only claims never grant mutation authority.
type ExecutionClaimAccess string

const (
	ExecutionClaimReadOnly      ExecutionClaimAccess = "read_only"
	ExecutionClaimSideEffecting ExecutionClaimAccess = "side_effecting"
)

// ProvisionDispatchMode is the immutable operation-level safety rule for the
// single provision accepted-to-resources-bound transition. It is persisted by
// TechStack, not inferred from an adapter after an operation has started.
type ProvisionDispatchMode string

const (
	// ProvisionDispatchBlocked quarantines pre-migration or otherwise unclassified provision work.
	ProvisionDispatchBlocked ProvisionDispatchMode = "blocked"
	// ProvisionDispatchNativeIdempotency permits crash recovery through provider-native idempotency.
	ProvisionDispatchNativeIdempotency ProvisionDispatchMode = "native_idempotency"
	// ProvisionDispatchProviderCorrelation permits crash recovery through authoritative unique correlation.
	ProvisionDispatchProviderCorrelation ProvisionDispatchMode = "provider_correlation"
	// ProvisionDispatchAtMostOnceManualReconcile permits exactly one TechStack
	// dispatch and requires manual reconciliation after an ambiguous boundary.
	ProvisionDispatchAtMostOnceManualReconcile ProvisionDispatchMode = "at_most_once_dispatch_manual_reconcile"
)

// ExecutePermit is an in-memory capability created only when TechStack
// atomically inserts a provision dispatch guard and its first head claim. Its
// fields are deliberately private and it must never be serialized or stored.
type ExecutePermit struct {
	tenantID          string
	operationID       string
	headReceiptDigest string
	claimTokenDigest  string
	preparationDigest string
}

// MarshalJSON rejects persistence of the in-memory execute capability.
func (ExecutePermit) MarshalJSON() ([]byte, error) {
	return nil, errors.New("providercontrol: execute permit must not be serialized")
}

// ProvisionDispatchGrant couples the first head claim to its non-replayable
// execute permit. Renewing or taking over a claim never creates another grant.
type ProvisionDispatchGrant struct {
	Claim  ExecutionClaim
	Permit ExecutePermit
}

// PreparedProvisionBinding is the secret-free identity of one canonical
// provider create request. Every digest is sealed before the dispatch guard is
// acquired. CredentialVersionHash and ProviderScopeHash bind opaque custody
// and connection versions; they never contain raw credentials or endpoints.
type PreparedProvisionBinding struct {
	RequestDigest         string
	CredentialVersionHash string
	ProviderScopeHash     string
	CorrelationHash       string
	AdapterManifestHash   string
}

// PreparedProvisionRequest is an adapter-owned, in-memory canonical request.
// PrepareProvision may resolve local handles and build request JSON but must
// perform no provider mutation. The concrete value may retain sensitive
// material needed to send the request; callers must never serialize, persist,
// log, or otherwise expose it. Binding must remain stable for the value's
// lifetime.
type PreparedProvisionRequest interface {
	ProvisionBinding() PreparedProvisionBinding
}

// CrashRecoveryMode identifies how an adapter prevents duplicate resources
// when TechStack crashes after provider acceptance and before local commit.
type CrashRecoveryMode string

const (
	// CrashRecoveryNativeIdempotency uses provider-native idempotency on every call.
	CrashRecoveryNativeIdempotency CrashRecoveryMode = "native_idempotency"
	// CrashRecoveryProviderCorrelation recovers through provider-persisted correlation.
	CrashRecoveryProviderCorrelation CrashRecoveryMode = "provider_correlation"
)

// CrashRecoveryCapability is an adapter's registration-time safety promise.
// Correlation recovery must be provider-persisted, unique, and queryable.
type CrashRecoveryCapability struct {
	AdapterManifestHash          string
	Mode                         CrashRecoveryMode
	PerHeadInvocationKey         bool
	ProviderPersistedCorrelation bool
	UniqueCorrelation            bool
	RecoveryByCorrelation        bool
}

// AdapterInvocation wraps the shared transport-neutral request with the
// TechStack-owned provider invocation identity. The key changes for every
// receipt head while remaining stable across crash recovery for that head.
type AdapterInvocation struct {
	Request       providerexecutor.ExecutionRequest
	Key           string
	CorrelationID string
}

// CrashRecoverableReadOnlyExecutor is the only crash-recoverable adapter view
// available to a read-only claim. It deliberately exposes no mutation method.
type CrashRecoverableReadOnlyExecutor interface {
	ExecuteCrashRecoverableReadOnly(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
}

// CrashRecoverableMutationExecutor is the only crash-recoverable adapter view
// available to a side-effecting claim. The coordinator never hands this view
// to the read-only execution helper.
type CrashRecoverableMutationExecutor interface {
	ExecuteCrashRecoverableMutation(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
}

// CrashRecoverableExecutor is the complete registration-time adapter shape.
// The Registry captures its immutable capability once, then stores separate
// read-only and mutation interface views. Both execution methods must consume
// AdapterInvocation.Key according to the admitted recovery mode.
type CrashRecoverableExecutor interface {
	CrashRecoveryCapability() CrashRecoveryCapability
	CrashRecoverableReadOnlyExecutor
	CrashRecoverableMutationExecutor
}

// AtMostOnceProvisionCapability is an adapter's registration-time promise
// for the non-idempotent provision path. Read paths and exact-handle cleanup
// remain separate from the one-shot create dispatch.
type AtMostOnceProvisionCapability struct {
	AdapterManifestHash string
	// CompatibleContinuationManifestHashes lists historical adapter manifests
	// whose already-dispatched operations may continue through certified
	// read-only phases. These hashes never authorize a fresh prepare or create
	// dispatch; the current AdapterManifestHash remains the only start pin.
	CompatibleContinuationManifestHashes []string
	PerHeadInvocationKey                 bool
	SideEffectFreePreparation            bool
	PreparedRequestDigestBinding         bool
	ReadOnlyProvisionPolling             bool
	ReadOnlyGeneralObservation           bool
	ExactHandleDecommission              bool
	ReadOnlyAbsencePolling               bool
	// ExactHandleReconcile admits reconcile for this adapter. It is only
	// valid when the adapter also implements ExactHandleReconcileExecutor:
	// a reconcile converges the already-present exact resource graph toward
	// a desired state and never creates, replaces, or deletes a resource, so
	// the non-idempotent create path stays the only at-most-once dispatch.
	ExactHandleReconcile bool
	// ReadOnlyReconcilePolling declares that convergence after the single
	// side-effecting reconcile step is observed through read-only calls.
	ReadOnlyReconcilePolling bool
}

// ExactHandleReconcileExecutor is the optional reconcile boundary of an
// at-most-once provision adapter. ReconcileExactHandles runs under the
// side-effecting accepted-head claim and must be idempotent against the
// provider: it reads current state first and only requests the transition
// the desired spec still needs. PollReconcileConvergence runs under a
// read-only claim. Both return exactly the command's target graph; a result
// that changes resource identity is rejected by the coordinator.
type ExactHandleReconcileExecutor interface {
	ReconcileExactHandles(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
	PollReconcileConvergence(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
}

// AtMostOnceProvisionExecutor is the operation-aware adapter boundary for a
// provider without documented create idempotency or authoritative unique
// correlation. PrepareProvision runs before dispatch custody is consumed and
// must be side-effect-free. DispatchPreparedProvision is the only create
// method and requires both that exact prepared request and the opaque permit
// returned with the first digest-bound guarded claim.
type AtMostOnceProvisionExecutor interface {
	AtMostOnceProvisionCapability() AtMostOnceProvisionCapability
	PrepareProvision(context.Context, AdapterInvocation) (PreparedProvisionRequest, error)
	DispatchPreparedProvision(context.Context, PreparedProvisionRequest, ExecutePermit) providerexecutor.ExecutionResult
	PollProvisionPresence(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
	ObserveReadOnly(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
	DecommissionExactHandles(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
	PollDecommissionAbsence(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
}

// Ledger is the durable, tenant-scoped custody boundary. BeginOperation must
// atomically persist the command, optional desired-spec revision, and initial
// receipt. AppendReceipt must compare-and-swap against previous before
// persisting next and all resource/evidence custody rows.
type Ledger interface {
	BeginOperation(
		context.Context,
		ExecutionAuthority,
		ExecutionProfileSnapshot,
		ProvisionDispatchMode,
		providerexecutor.Command,
		providerexecutor.Receipt,
		*DesiredSpecRevision,
	) (record OperationRecord, created bool, err error)
	LoadOperation(context.Context, string, string) (OperationRecord, error)
	AppendReceipt(context.Context, providerexecutor.Command, providerexecutor.Receipt, providerexecutor.Receipt) error
	AcquireExecutionClaim(context.Context, providerexecutor.Command, providerexecutor.Receipt, ExecutionClaimAccess, string, string, time.Duration) (ExecutionClaim, error)
	AcquireProvisionDispatchClaim(context.Context, providerexecutor.Command, providerexecutor.Receipt, PreparedProvisionBinding, string, string, time.Duration) (ProvisionDispatchGrant, error)
	RenewExecutionClaim(context.Context, ExecutionClaim, time.Duration) (ExecutionClaim, error)
	ReleaseExecutionClaim(context.Context, ExecutionClaim) error
	AppendClaimedReceipt(context.Context, providerexecutor.Command, providerexecutor.Receipt, providerexecutor.Receipt, ExecutionClaim) error
}

// ResourceFreeTeardownFinalizer is the optional durable ledger capability
// which closes a canceled provision only after proving that no provider
// resource exists. It never invokes an adapter.
type ResourceFreeTeardownFinalizer interface {
	FinalizeResourceFreeTeardown(
		context.Context,
		providerexecutor.Command,
		providerexecutor.Receipt,
	) (record OperationRecord, finalized bool, err error)
}

// RunnableOperationLister returns a bounded, tenant-scoped set of nonterminal
// native provider-control operations.
type RunnableOperationLister interface {
	ListRunnableOperations(context.Context, string, int) ([]OperationRef, error)
}
