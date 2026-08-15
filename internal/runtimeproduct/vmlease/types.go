// Package vmlease owns Techstack's legacy managed-runtime product projection.
// Provider, billing, catalog, and provisioning fields deliberately live here,
// outside the neutral runtimelease module.
package vmlease

import (
	"errors"
	"time"
)

type LeaseID string
type SubjectKind string

const (
	SubjectUser SubjectKind = "user"
	SubjectOrg  SubjectKind = "org"
)

type DesiredState string

const (
	DesiredStateRunning  DesiredState = "running"
	DesiredStateStopped  DesiredState = "stopped"
	DesiredStateArchived DesiredState = "archived"
)

type BillingMode string

const (
	BillingModeTokenMetered BillingMode = "token_metered"
	BillingModeSubscription BillingMode = "subscription"
	BillingModeBYOK         BillingMode = "byok"
	BillingModeLocal        BillingMode = "local"
)

type LifecycleClass string

const (
	LifecycleClassOneTime      LifecycleClass = "one_time"
	LifecycleClassSubscription LifecycleClass = "subscription"
)

type RestartPolicy string

const (
	RestartPolicyNone             RestartPolicy = "none"
	RestartPolicyOnUnexpectedStop RestartPolicy = "on_unexpected_stop"
)

type RecreatePolicy string

const (
	RecreatePolicyNever                RecreatePolicy = "never"
	RecreatePolicyManual               RecreatePolicy = "manual"
	RecreatePolicyAutomaticIfStateless RecreatePolicy = "automatic_if_stateless"
)

var (
	ErrInvalidLease     = errors.New("vmlease: invalid lease")
	ErrLeaseExpired     = errors.New("vmlease: lease expired")
	ErrLeaseCancelled   = errors.New("vmlease: lease cancelled")
	ErrUnsupportedLease = errors.New("vmlease: unsupported lease for v1")
)

type Subject struct {
	Kind  SubjectKind `json:"kind"`
	ID    string      `json:"id"`
	OrgID string      `json:"org_id,omitempty"`
}

type ResourceRef struct {
	ProviderID   string `json:"provider_id"`
	EngineVMID   string `json:"engine_vm_id,omitempty"`
	SimulationID string `json:"simulation_id,omitempty"`
	VMID         string `json:"vm_id,omitempty"`
	Region       string `json:"region,omitempty"`
}

type ProvisioningSpec struct {
	Name     string            `json:"name"`
	Role     string            `json:"role,omitempty"`
	Image    string            `json:"image,omitempty"`
	VCPUs    int               `json:"vcpus,omitempty"`
	MemoryMB int               `json:"memory_mb,omitempty"`
	DiskGB   int               `json:"disk_gb,omitempty"`
	Region   string            `json:"region,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Lease struct {
	ID             LeaseID           `json:"id"`
	Subject        Subject           `json:"subject"`
	Resource       ResourceRef       `json:"resource"`
	DesiredState   DesiredState      `json:"desired_state"`
	BillingMode    BillingMode       `json:"billing_mode"`
	LifecycleClass LifecycleClass    `json:"lifecycle_class"`
	RestartPolicy  RestartPolicy     `json:"restart_policy"`
	RecreatePolicy RecreatePolicy    `json:"recreate_policy"`
	ValidFrom      time.Time         `json:"valid_from"`
	ValidUntil     time.Time         `json:"valid_until"`
	RenewedAt      time.Time         `json:"renewed_at,omitempty"`
	CancelledAt    *time.Time        `json:"cancelled_at,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

//nolint:gocyclo // Frozen product-state validation keeps the legacy persisted constraints explicit.
func (l Lease) Validate(now time.Time) error {
	if l.ID == "" || l.Subject.ID == "" || l.Resource.ProviderID == "" {
		return ErrInvalidLease
	}
	if l.Subject.Kind != SubjectUser && l.Subject.Kind != SubjectOrg {
		return ErrInvalidLease
	}
	if l.DesiredState != DesiredStateRunning && l.DesiredState != DesiredStateStopped && l.DesiredState != DesiredStateArchived {
		return ErrInvalidLease
	}
	if l.BillingMode != BillingModeSubscription || l.LifecycleClass != LifecycleClassSubscription {
		return ErrUnsupportedLease
	}
	if l.RestartPolicy != RestartPolicyNone && l.RestartPolicy != RestartPolicyOnUnexpectedStop {
		return ErrInvalidLease
	}
	if l.RecreatePolicy != RecreatePolicyNever && l.RecreatePolicy != RecreatePolicyManual && l.RecreatePolicy != RecreatePolicyAutomaticIfStateless {
		return ErrInvalidLease
	}
	if l.ValidFrom.IsZero() || l.ValidUntil.IsZero() || !l.ValidUntil.After(l.ValidFrom) {
		return ErrInvalidLease
	}
	if l.CancelledAt != nil && !l.CancelledAt.After(now) {
		return ErrLeaseCancelled
	}
	if !now.Before(l.ValidUntil) {
		return ErrLeaseExpired
	}
	return nil
}

func (l Lease) ActiveAt(now time.Time) bool {
	return l.Validate(now) == nil && !now.Before(l.ValidFrom)
}
