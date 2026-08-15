package serverregistry

import (
	"context"
	"time"
)

const (
	// AuthorityControlPlane owns identity bindings, desired-state projection,
	// lifecycle, and decommissioning decisions.
	AuthorityControlPlane = "control-plane"
	// AuthorityGuard owns authenticated connection, health, heartbeat,
	// service, and inventory observations only.
	AuthorityGuard = "guard"
)

// Channel is one secret-free connection projection for a runtime server.
type Channel struct {
	Type        string         `json:"type"`
	Role        string         `json:"role"`
	State       string         `json:"state"`
	EndpointRef string         `json:"endpoint_ref,omitempty"`
	ObservedAt  *time.Time     `json:"observed_at,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Aggregate is the durable tenant-owned runtime-server identity and the head
// of its orthogonal lifecycle, desired, connection, and health dimensions.
type Aggregate struct {
	ID             string
	TenantID       string
	InstanceID     string
	StackID        string
	OwnerSubjectID string
	WorkerID       string
	NodeID         string
	LeaseID        string
	ProviderRef    string
	// RuntimeTarget is the evidence-backed Local/Cloud classification. A
	// provider-native workload never appears here; it is a service placement
	// with target_kind=managed_workload instead of a fake server.
	RuntimeTarget   RuntimeTarget
	Name            string
	LifecycleState  string
	DesiredState    string
	ConnectionState string
	HealthState     string
	// ReasonCode mirrors the pre-aggregate database projection while rows are
	// migrated to authority-scoped reason fields. Commands must not write it.
	ReasonCode           string
	LifecycleReasonCode  string
	DesiredReasonCode    string
	ConnectionReasonCode string
	HealthReasonCode     string
	LifecycleChangedAt   time.Time
	DesiredChangedAt     time.Time
	ConnectionChangedAt  time.Time
	HealthChangedAt      time.Time
	LastHeartbeatAt      *time.Time
	InventoryRevision    int64
	Revision             int64
	Generation           int64
	SourceAuthority      string
	SourceID             string
	SourceEpoch          string
	SourceSequence       int64
	SourceObservedAt     *time.Time
	Channels             []Channel
	Metadata             map[string]any
	DecommissionedAt     *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Transition is one immutable accepted state-dimension change.
type Transition struct {
	ID         int64
	TenantID   string
	ServerID   string
	Dimension  string
	FromState  string
	ToState    string
	ReasonCode string
	Source     string
	ObservedAt time.Time
	Evidence   map[string]any
	CreatedAt  time.Time
}

// InventorySnapshot is the immutable, secret-free inventory payload accepted
// at one monotonically increasing per-server revision.
type InventorySnapshot struct {
	ID         int64
	TenantID   string
	ServerID   string
	Revision   int64
	Source     string
	ObservedAt time.Time
	Inventory  map[string]any
	CreatedAt  time.Time
}

// InventoryObservation carries an inventory snapshot inside an aggregate
// command. The repository assigns its next per-server revision.
type InventoryObservation struct {
	Source    string
	Inventory map[string]any
}

// OutboxItem is the secret-free integration event committed with an accepted
// aggregate revision. Payload excludes inventory contents, metadata, evidence,
// channel endpoints, credentials, and access material.
type OutboxItem struct {
	ID                int64
	TenantID          string
	ServerID          string
	AggregateRevision int64
	Generation        int64
	Authority         string
	Source            string
	EventType         string
	Payload           map[string]any
	OccurredAt        time.Time
	CreatedAt         time.Time
	PublishedAt       *time.Time
	Attempts          int
}

// Command is a partial, authority-labeled compare-and-swap mutation against
// one runtime-server aggregate. Guard commands require an issued generation,
// source epoch, and strictly monotone source sequence.
type Command struct {
	TenantID              string
	ServerID              string
	ExpectedRevision      int64
	Generation            int64
	Authority             string
	Source                string
	SourceID              string
	SourceEpoch           string
	SourceSequence        int64
	ObservedAt            time.Time
	Runtime               Aggregate
	ClearLifecycleReason  bool
	ClearDesiredReason    bool
	ClearConnectionReason bool
	ClearHealthReason     bool
	Evidence              map[string]any
	Inventory             *InventoryObservation
}

// Result reports the exact atomic repository write. Applied is false for a
// stale replay, superseded source epoch, or observation fenced by teardown.
type Result struct {
	Server      *Aggregate
	Transitions []Transition
	Inventory   *InventorySnapshot
	Outbox      *OutboxItem
	Applied     bool
}

// Repository is the authoritative transactional command boundary. One
// accepted command commits its aggregate head, transitions, optional inventory
// snapshot, and outbox event atomically.
type Repository interface {
	ApplyServerEvent(context.Context, Command) (*Result, error)
}
