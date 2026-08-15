// Package controlplane defines Postgres-first domain store contracts for
// TechStack runtime state. These contracts are the boundary used to retire
// PocketBase collection access from routes, schedulers, and orchestrators.
package controlplane

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

var (
	ErrNotFound                   = errors.New("controlplane: not found")
	ErrConflict                   = errors.New("controlplane: conflict")
	ErrInventoryProjectionPending = errors.New("controlplane: inventory projection pending")
	ErrStackExecutionBusy         = errors.New("controlplane: stack execution busy")
)

const (
	jobStatePending   = "pending"
	jobStateRunning   = "running"
	jobStateWaiting   = "waiting"
	jobStateCompleted = "completed"
	jobStateFailed    = "failed"
	jobStateCanceled  = "canceled"
	jobStateCancelled = "cancelled"
	jobTypeProvision  = "provision"
	jobTypeDeploy     = "deploy"
	jobTypeDestroy    = "destroy"
	jobWaitResultKey  = "job_wait"
	jobServerIDKey    = "server_id"
)

type Tenant struct {
	ID            string
	ExternalOrgID string
	DisplayName   string
	Kind          string
	Status        string
	Metadata      map[string]any
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type User struct {
	ID           string
	PrimaryEmail string
	DisplayName  string
	Status       string
	Metadata     map[string]any
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Membership struct {
	ID          string
	TenantID    string
	UserID      string
	RoleKey     string
	ProviderKey string
	SubjectID   string
	Status      string
	Metadata    map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Stack struct {
	ID             string
	TenantID       string
	InstanceID     string
	OwnerSubjectID string
	// HomelabID links the kit deployment to its homelab umbrella (ADR-0036).
	// Resolved by every control-plane create boundary; readers still tolerate
	// empty values while legacy rows are being healed.
	HomelabID      string
	Name           string
	Description    string
	Mode           string
	Status         string
	Config         map[string]any
	Services       []map[string]any
	RuntimeSummary map[string]any
	DriftStatus    string
	DriftCheckedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
}

type CreateStackRequest struct {
	ID             string
	TenantID       string
	InstanceID     string
	OwnerSubjectID string
	// HomelabID must reference an existing homelab in the same tenant (the
	// composite FK from migration 044 enforces this). Public create paths
	// resolve the owner's canonical homelab before calling the store; empty is
	// retained only for migration/backfill compatibility at this low-level
	// contract.
	HomelabID   string
	Name        string
	Description string
	Mode        string
	Status      string
	Config      map[string]any
	Services    []map[string]any
}

type StackStore interface {
	CreateStack(ctx context.Context, req CreateStackRequest) (*Stack, error)
	GetStack(ctx context.Context, tenantID, stackID string) (*Stack, error)
	GetActiveStackByName(ctx context.Context, tenantID, ownerSubjectID, name string) (*Stack, error)
	ListStacksByTenant(ctx context.Context, tenantID string) ([]Stack, error)
	SoftDeleteStack(ctx context.Context, tenantID, stackID string) error
	UpdateStackRuntime(ctx context.Context, tenantID, stackID string, runtime RuntimeUpdate) (*Stack, error)
	// UpdateStackConfig replaces the stored stack spec (config_json). Writers:
	// the domain-attach handover (sets the spec `domain` before a re-roll) and
	// the wizard-run join persist (replaces config with the appended-node v2
	// spec). There is no CAS token; concurrent writers last-write-win.
	UpdateStackConfig(ctx context.Context, tenantID, stackID string, config map[string]any) (*Stack, error)
	// SetStackHomelab links an existing stack to its homelab umbrella. The
	// wizard-run path uses it to heal legacy stacks created without the link.
	SetStackHomelab(ctx context.Context, tenantID, stackID, homelabID string) (*Stack, error)
}

type RuntimeUpdate struct {
	Status         string
	RuntimeSummary map[string]any
	DriftStatus    string
	DriftCheckedAt *time.Time
}

type Job struct {
	ID           string
	TenantID     string
	InstanceID   string
	StackID      string
	Type         string
	State        string
	Priority     int
	Progress     int
	Step         string
	Message      string
	Error        string
	ErrorDetails string
	Logs         []map[string]any
	Result       map[string]any
	ScheduledFor time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type UpsertJobRequest struct {
	ID           string
	TenantID     string
	InstanceID   string
	StackID      string
	Type         string
	State        string
	Priority     int
	Progress     int
	Step         string
	Message      string
	Error        string
	ErrorDetails string
	Logs         []map[string]any
	Result       map[string]any
	ScheduledFor time.Time
}

// ClaimWaitingJobResumeRequest atomically retires one exact persisted wait
// cycle and attaches the deterministic replacement receipt. Implementations
// must compare state/type/stack/reason/schedule in the same write. A failed
// precondition returns ErrConflict.
type ClaimWaitingJobResumeRequest struct {
	TenantID     string
	JobID        string
	StackID      string
	JobType      string
	WaitReason   string
	NextResumeAt string
	LeaseID      string
	ServerID     string
	ResultPatch  map[string]any
	ClaimedAt    time.Time
}

// ReclaimStaleManagedDestroyRecoveryRequest describes the one narrowly safe
// transition that may recover a lost managed-provider destroy execution. The
// caller supplies an exact, server-generated result marker; implementations
// must compare every field atomically and never reclaim a merely slow or
// unrelated running job. StaleBefore is derived from the queue heartbeat
// cadence, not from untrusted job payload or result data.
type ReclaimStaleManagedDestroyRecoveryRequest struct {
	TenantID             string
	JobID                string
	StackID              string
	RecoveryMarkerKey    string
	RecoveryMarkerSchema string
	StaleBefore          time.Time
	ReclaimedAt          time.Time
}

// SyncJobSnapshotRequest persists one observation from a process-local queue
// without allowing that observation to resurrect a terminal job or replace a
// newer execution attempt. AttemptStartedAt is the durable execution
// generation written by StartJob; implementations must compare it in the same
// update as the state transition.
type SyncJobSnapshotRequest struct {
	Job              UpsertJobRequest
	ObservedState    string
	AttemptStartedAt *time.Time
	CompletedAt      *time.Time
}

func validJobSnapshotProjection(observedState, persistedState string) bool {
	observed := strings.ToLower(strings.TrimSpace(observedState))
	persisted := strings.ToLower(strings.TrimSpace(persistedState))
	switch observed {
	case jobStateWaiting:
		return persisted == jobStatePending
	case jobStateCanceled, jobStateCancelled:
		return persisted == jobStateCanceled || persisted == jobStateCancelled
	default:
		return observed != "" && observed == persisted
	}
}

func jobWriteBypassesExecutionClaim(state string) bool {
	return strings.EqualFold(strings.TrimSpace(state), jobStateRunning)
}

type JobStore interface {
	CreateJob(ctx context.Context, req UpsertJobRequest) (*Job, error)
	UpsertJob(ctx context.Context, req UpsertJobRequest) (*Job, error)
	SyncJobSnapshot(ctx context.Context, req SyncJobSnapshotRequest) (*Job, error)
	ClaimWaitingJobResume(ctx context.Context, req ClaimWaitingJobResumeRequest) (*Job, error)
	ReclaimStaleManagedDestroyRecovery(ctx context.Context, req ReclaimStaleManagedDestroyRecoveryRequest) (*Job, error)
	GetJob(ctx context.Context, tenantID, jobID string) (*Job, error)
	ListJobsByTenant(ctx context.Context, tenantID string, limit int) ([]Job, error)
	// ListProviderProvisionRecoveryCandidates is a tenant-scoped, bounded
	// lookup on the durable provider operation correlation. It intentionally
	// does not fall back to a broad tenant job page: operation_id is the only
	// safe recovery fence for a provider wait.
	ListProviderProvisionRecoveryCandidates(ctx context.Context, tenantID, operationID string, limit int) ([]Job, error)
	// ListManagedDestroyRecoveryCandidates is a tenant-scoped, bounded index
	// over only server-generated managed-provider destroy recovery markers. It
	// includes due pending rows and running rows so stale-running recovery never
	// depends on an arbitrary newest-jobs page.
	ListManagedDestroyRecoveryCandidates(ctx context.Context, tenantID, markerKey, markerSchema string, limit int) ([]Job, error)
	ListJobsByStack(ctx context.Context, tenantID, stackID string, limit int) ([]Job, error)
	ListPendingJobs(ctx context.Context, tenantID string, limit int) ([]Job, error)
	StartJob(ctx context.Context, tenantID, jobID string, at time.Time) (*Job, error)
	CompleteJob(ctx context.Context, tenantID, jobID string, result map[string]any, at time.Time) (*Job, error)
	FailJob(ctx context.Context, tenantID, jobID string, message, details string, at time.Time) (*Job, error)
}

type PairingToken struct {
	ID             string
	TenantID       string
	InstanceID     string
	StackID        string
	OwnerSubjectID string
	Name           string
	TokenHash      string
	Status         string
	ExpiresAt      *time.Time
	UsedAt         *time.Time
	Metadata       map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Worker struct {
	ID             string
	TenantID       string
	InstanceID     string
	StackID        string
	Hostname       string
	IP             string
	OS             string
	Arch           string
	TokenHash      string
	Status         string
	Approved       bool
	ApprovedAt     *time.Time
	LastSeenAt     *time.Time
	CPUCores       int
	RAMMB          int
	DiskGB         int
	GPU            string
	HasNVME        bool
	HasHWTranscode bool
	DockerVersion  string
	Type           string
	Provider       string
	Tags           map[string]any
	OwnerSubjectID string
	Capabilities   map[string]any
	Resources      map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type WorkerStore interface {
	UpsertWorkerHeartbeat(ctx context.Context, worker Worker) (*Worker, error)
	GetWorker(ctx context.Context, tenantID, workerID string) (*Worker, error)
	ListWorkersByTenant(ctx context.Context, tenantID string) ([]Worker, error)
	ApproveWorker(ctx context.Context, tenantID, workerID, ownerSubjectID string, approvedAt time.Time) (*Worker, error)
	UpsertPairingToken(ctx context.Context, token PairingToken) (*PairingToken, error)
	GetPairingTokenByHash(ctx context.Context, tenantID, tokenHash string) (*PairingToken, error)
	// ClaimPairingToken atomically transitions one active, unused, unexpired
	// tenant-scoped token to used and returns the claimed row. Missing or
	// ineligible tokens return ErrNotFound without revealing their state.
	ClaimPairingToken(ctx context.Context, tenantID, tokenHash string, claimedAt time.Time) (*PairingToken, error)
	RevokePairingToken(ctx context.Context, tenantID, tokenID string) error
}

type Node struct {
	ID         string
	TenantID   string
	InstanceID string
	StackID    string
	WorkerID   string
	Name       string
	Role       string
	Status     string
	Address    string
	Metadata   map[string]any
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Service struct {
	ID         string
	TenantID   string
	InstanceID string
	StackID    string
	NodeID     string
	ServiceKey string
	Name       string
	Status     string
	// Source is provenance: which pipeline last reported this row.
	Source string
	// ManagementState is the persisted ownership dimension (managed|observed).
	// It is written once by the aggregate boundary and only read here; no route
	// re-derives it from Source or Status.
	ManagementState string
	URL             string
	MigrationStatus string
	Metadata        map[string]any
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type RegistryStore interface {
	UpsertNode(ctx context.Context, node Node) (*Node, error)
	UpsertService(ctx context.Context, service Service) (*Service, error)
	GetNode(ctx context.Context, tenantID, nodeID string) (*Node, error)
	GetService(ctx context.Context, tenantID, serviceID string) (*Service, error)
	ListNodesByStack(ctx context.Context, tenantID, stackID string) ([]Node, error)
	ListServicesByStack(ctx context.Context, tenantID, stackID string) ([]Service, error)
	DeleteService(ctx context.Context, tenantID, serviceID string) error
}

// ServiceRuntimeStore is the canonical measured service projection. The
// legacy RegistryStore remains a migration adapter; operator APIs use this
// contract so desired state, observation, health, access and capabilities are
// never collapsed into one status string.
type ServiceRuntimeStore interface {
	UpsertServiceRuntime(ctx context.Context, service ServiceRuntime) (*ServiceRuntime, error)
	GetServiceRuntime(ctx context.Context, tenantID, serviceID string) (*ServiceRuntime, error)
	ListServiceRuntimes(ctx context.Context, tenantID, stackID, serverID string) ([]ServiceRuntime, error)
}

type ServiceRuntime struct {
	ID         string
	TenantID   string
	InstanceID string
	StackID    string
	ServerID   string
	// Placement is distinct from management and measured state. A server
	// placement requires ServerID; a managed workload has provider-native
	// target/receipt/policy evidence and deliberately has no ServerID.
	Placement       serviceregistry.Placement
	ServiceKey      string
	ServiceInstance string
	Name            string
	// DesiredState is only meaningful for a managed service: it is the target
	// declared by the StackKit contract. For an observed service there is no
	// declared target, the stored value is not authoritative, and drift
	// comparison is undefined.
	DesiredState  string
	ObservedState string
	HealthState   string
	// ManagementState is the persisted ownership dimension (managed|observed),
	// written through ApplyServiceEvent exactly like the other dimensions.
	ManagementState string
	ObservedAt      *time.Time
	StackKitVersion string
	Access          map[string]any
	Capabilities    []string
	// Source is provenance, a separate axis from ManagementState.
	Source    string
	Metadata  map[string]any
	CreatedAt time.Time
	UpdatedAt time.Time
}

type WalletItem struct {
	ID          string
	TenantID    string
	InstanceID  string
	StackID     string
	ItemType    string
	Provider    string
	ExternalRef string
	Metadata    map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type WalletStore interface {
	UpsertWalletItem(ctx context.Context, item WalletItem) (*WalletItem, error)
	GetWalletItem(ctx context.Context, tenantID, itemID string) (*WalletItem, error)
	ListWalletItems(ctx context.Context, tenantID, stackID string) ([]WalletItem, error)
	DeleteWalletItem(ctx context.Context, tenantID, itemID string) error
}

// OwnerSpecToken is the short-lived, one-use handoff that lets a freshly
// enrolled StackKit fetch its local owner bootstrap without depending on the
// retired PocketBase stack projection.
type OwnerSpecToken struct {
	TokenHash string
	TenantID  string
	StackID   string
	OwnerID   string
	ExpiresAt time.Time
}

// OwnerSpecTokenStore persists and atomically consumes owner bootstrap tokens.
// It is deliberately separate from WalletStore: tokens are execution custody,
// not user-visible wallet entries.
type OwnerSpecTokenStore interface {
	StoreOwnerSpecToken(ctx context.Context, token OwnerSpecToken) error
	ConsumeOwnerSpecToken(ctx context.Context, token OwnerSpecToken, consumedAt time.Time) error
}

type ActivityEvent struct {
	ID              string
	TenantID        string
	InstanceID      string
	StackID         string
	RuntimeScopeKey string
	ServerScopeKey  string
	ServiceScopeKey string
	CorrelationID   string
	ActorSubjectID  string
	Action          string
	Category        string
	Severity        string
	Message         string
	Details         map[string]any
	CreatedAt       time.Time
}

type ActivityFilter struct {
	StackID         string
	RuntimeScopeKey string
	ServerScopeKey  string
	ServiceScopeKey string
	CursorCreatedAt time.Time
	CursorID        string
	Limit           int
}

func normalizeActivityEvent(event ActivityEvent) ActivityEvent {
	event.TenantID = strings.TrimSpace(event.TenantID)
	event.StackID = strings.TrimSpace(event.StackID)
	event.RuntimeScopeKey = firstNonEmpty(
		strings.TrimSpace(event.RuntimeScopeKey),
		strings.TrimSpace(stringValue(event.Details["runtime_scope_key"])),
	)
	event.ServerScopeKey = firstNonEmpty(
		strings.TrimSpace(event.ServerScopeKey),
		strings.TrimSpace(stringValue(event.Details["server_scope_key"])),
		strings.TrimSpace(stringValue(event.Details["server_id"])),
		strings.TrimSpace(stringValue(event.Details["node_id"])),
	)
	event.ServiceScopeKey = firstNonEmpty(
		strings.TrimSpace(event.ServiceScopeKey),
		strings.TrimSpace(stringValue(event.Details["service_scope_key"])),
		strings.TrimSpace(stringValue(event.Details["service_id"])),
	)
	event.CorrelationID = firstNonEmpty(
		strings.TrimSpace(event.CorrelationID),
		strings.TrimSpace(stringValue(event.Details["correlation_id"])),
		strings.TrimSpace(stringValue(event.Details["trace_id"])),
		strings.TrimSpace(stringValue(event.Details["runtime_action_id"])),
		strings.TrimSpace(stringValue(event.Details["job_id"])),
	)
	if event.RuntimeScopeKey == "" {
		if targetID := strings.TrimSpace(stringValue(event.Details["runtime_target_id"])); targetID != "" {
			event.RuntimeScopeKey = "managed_target:" + targetID
		} else if event.ServerScopeKey != "" {
			event.RuntimeScopeKey = "server:" + event.ServerScopeKey
		} else if event.StackID != "" {
			event.RuntimeScopeKey = "stack:" + event.StackID
		}
	}
	return event
}

const defaultActivityLimit = 50

type ActivityStore interface {
	AppendActivity(ctx context.Context, event ActivityEvent) (*ActivityEvent, error)
	// ListActivity returns newest activity first for the tenant and optional exact stack.
	// Equal timestamps are ordered by descending ID; a non-positive limit defaults to 50.
	ListActivity(ctx context.Context, tenantID, stackID string, limit int) ([]ActivityEvent, error)
	ListActivityScoped(ctx context.Context, tenantID string, filter ActivityFilter) ([]ActivityEvent, error)
}

type AuthStore interface {
	UpsertTenant(ctx context.Context, tenant Tenant) (*Tenant, error)
	UpsertUser(ctx context.Context, user User) (*User, error)
	UpsertMembership(ctx context.Context, membership Membership) (*Membership, error)
	GetMembership(ctx context.Context, tenantID, userID string) (*Membership, error)
	// ListMembershipsByUser returns the user's active memberships across all
	// tenants, newest first. Used for organization resolution at login.
	ListMembershipsByUser(ctx context.Context, userID string) ([]Membership, error)
	UpsertAuthConfig(ctx context.Context, config AuthConfig) (*AuthConfig, error)
	UpsertBreakglassAdmin(ctx context.Context, admin BreakglassAdmin) (*BreakglassAdmin, error)
	GetBreakglassAdmin(ctx context.Context, tenantID string) (*BreakglassAdmin, error)
}

type AuthConfig struct {
	ID         string
	TenantID   string
	InstanceID string
	Mode       string
	Config     map[string]any
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type BreakglassAdmin struct {
	ID           string
	TenantID     string
	UserID       string
	Email        string
	PasswordHash string
	Locked       bool
	LastUsedAt   *time.Time
	Metadata     map[string]any
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type FeatureStore interface {
	SetFeaturePreference(ctx context.Context, tenantID, subjectID, featureKey string, enabled bool) error
	SaveFeatureConsent(ctx context.Context, tenantID, subjectID, featureKey string, metadata map[string]any) error
	RevokeFeatureConsent(ctx context.Context, tenantID, subjectID, featureKey string) error
}

// ServerRuntimeStore is the canonical Postgres-first runtime projection used
// by operator APIs. Workers, leases, nodes, and RIL records feed this store;
// none of those satellite records are independently authoritative for server
// connection or health state.
type ServerRuntimeStore interface {
	UpsertServerRuntime(ctx context.Context, server ServerRuntime) (*ServerRuntime, error)
	GetServerRuntime(ctx context.Context, tenantID, serverID string) (*ServerRuntime, error)
	ListServerRuntimesByTenant(ctx context.Context, tenantID, stackID string) ([]ServerRuntime, error)
	AppendServerTransition(ctx context.Context, transition ServerStateTransition) (*ServerStateTransition, error)
	ListServerTransitions(ctx context.Context, tenantID, serverID string, limit int) ([]ServerStateTransition, error)
	RecordServerInventory(ctx context.Context, snapshot ServerInventorySnapshot) (*ServerInventorySnapshot, error)
}

// ServerEventStore is the authoritative compare-and-swap command boundary for
// one runtime-server aggregate and its atomic transition, inventory, and
// outbox projections.
type ServerEventStore = serverregistry.Repository

// ServerRuntimeProjectionStore is the race-safe read-through creation seam for
// an authority-backed canonical server projection. Unlike UpsertServerRuntime,
// it never overwrites an agent-observed row that won a concurrent insert.
type ServerRuntimeProjectionStore interface {
	EnsureServerRuntimeProjection(ctx context.Context, server ServerRuntime) (runtime *ServerRuntime, created bool, err error)
}

// These aliases keep the persistence adapter honest: the serverregistry
// package owns the aggregate and projections; controlplane only persists them.
type ServerChannel = serverregistry.Channel
type ServerRuntime = serverregistry.Aggregate

// ServerRegistryOutboxItem is the secret-free integration event committed
// with one accepted aggregate revision.
type ServerRegistryOutboxItem = serverregistry.OutboxItem

type ServerStateTransition = serverregistry.Transition
type ServerInventorySnapshot = serverregistry.InventorySnapshot

type RILStore interface {
	UpsertRILServer(ctx context.Context, server RILServer) (*RILServer, error)
	ListRILServersByTenant(ctx context.Context, tenantID string) ([]RILServer, error)
	GetRILServer(ctx context.Context, tenantID, serverID string) (*RILServer, error)
	EnqueueRILCommand(ctx context.Context, command RILCommand) (*RILCommand, error)
	GetRILCommand(ctx context.Context, tenantID, commandID string) (*RILCommand, error)
	UpsertActionCard(ctx context.Context, card RILActionCard) (*RILActionCard, error)
	RecordHealEvent(ctx context.Context, event RILHealEvent) (*RILHealEvent, error)
}

type RILServer struct {
	ID         string
	TenantID   string
	InstanceID string
	StackID    string
	NodeID     string
	Name       string
	Status     string
	Health     map[string]any
	Inventory  map[string]any
	LastSeenAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type RILCommand struct {
	ID             string
	TenantID       string
	ServerID       string
	ActorSubjectID string
	CommandClass   string
	Status         string
	Request        map[string]any
	Result         map[string]any
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}

type RILHealEvent struct {
	ID           string
	TenantID     string
	ServerID     string
	ActionCardID string
	Status       string
	Cause        string
	Details      map[string]any
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type RILActionCard struct {
	ID         string
	TenantID   string
	ServerID   string
	StackID    string
	Title      string
	Status     string
	Severity   string
	Action     map[string]any
	Decision   map[string]any
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ResolvedAt *time.Time
}

type MigrationStore interface {
	StartPocketBaseMigration(ctx context.Context, run PocketBaseMigrationRun) (*PocketBaseMigrationRun, error)
	RecordPocketBaseMigrationCount(ctx context.Context, count PocketBaseMigrationCount) error
	CompletePocketBaseMigration(ctx context.Context, runID string, report map[string]any, completedAt time.Time) error
	FailPocketBaseMigration(ctx context.Context, runID string, cause error, completedAt time.Time) error
}

type PocketBaseMigrationRun struct {
	ID          string
	TenantID    string
	SourcePath  string
	Status      string
	StartedAt   time.Time
	CompletedAt *time.Time
	Report      map[string]any
	Error       string
}

type PocketBaseMigrationCount struct {
	RunID          string
	CollectionName string
	SourceCount    int
	ImportedCount  int
	SkippedCount   int
	MismatchCount  int
	Details        map[string]any
}
