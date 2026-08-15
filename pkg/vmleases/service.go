// Package vmleases implements TechStack's VM lease authority.
package vmleases

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

var (
	ErrUnsupportedProvider = errors.New("vmleases: unsupported provider for managed lease")
	ErrNotFound            = errors.New("vmleases: lease not found")
	ErrTenantRequired      = errors.New("vmleases: tenant_id required")
	ErrEnrollmentRequired  = errors.New("vmleases: native provider-control admission required")
	ErrProviderRefRequired = errors.New("vmleases: provider resource reference required for inventory import")
	ErrTenantMismatch      = errors.New("vmleases: tenant mismatch")
	ErrLeaseCancelled      = errors.New("vmleases: cancelled lease is immutable")
)

type Store interface {
	Upsert(ctx context.Context, lease vmlease.Lease, idempotencyKey string) (*vmlease.Lease, error)
	Get(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmlease.Lease, error)
	Update(ctx context.Context, tenantID string, lease vmlease.Lease) (*vmlease.Lease, error)
}

type LeaseListStore interface {
	ListByTenant(ctx context.Context, tenantID string) ([]vmlease.Lease, error)
}

type ServiceConfig struct {
	Now func() time.Time
	// SnapshotSecret and SnapshotTTL remain accepted while callers migrate off
	// the deleted legacy validation surface. They do not enable execution.
	SnapshotSecret []byte
	SnapshotTTL    time.Duration
	// AllowedProviders is retained as a source-compatible catalog projection,
	// but it never authorizes or rejects this inventory-only service. Native
	// provider admission is enforced by providercontrol's transactional catalog
	// resolver and mutation gate.
	AllowedProviders map[string]bool
}

type Service struct {
	store Store
	now   func() time.Time
}

type CreateRequest struct {
	Lease          vmlease.Lease `json:"lease"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
}

type PatchRequest struct {
	TenantID                         string                `json:"tenant_id,omitempty"`
	DesiredState                     *vmlease.DesiredState `json:"desired_state,omitempty"`
	ValidUntil                       *time.Time            `json:"valid_until,omitempty"`
	Cancel                           bool                  `json:"cancel,omitempty"`
	Metadata                         map[string]string     `json:"metadata,omitempty"`
	ExpectedResourceGenerationDigest string                `json:"expected_resource_generation_digest,omitempty"`
	// ClaimDecommission is control-plane only. It atomically persists the
	// expected generation digest before any provider teardown side effect.
	ClaimDecommission bool `json:"-"`
}

func NewService(store Store, cfg ServiceConfig) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		store: store,
		now:   cfg.Now,
	}
}

func (s *Service) CreateOrUpdate(ctx context.Context, req CreateRequest) (*vmlease.Lease, error) {
	lease := req.Lease
	// The request and persisted lease must never share the caller's mutable map:
	// assigning a generation to a retried request must not mutate the already
	// stored generation before the idempotency check runs.
	lease.Metadata = maps.Clone(req.Lease.Metadata)
	lease.Resource.ProviderID = strings.ToLower(strings.TrimSpace(lease.Resource.ProviderID))
	if _, tenantErr := tenantIDFromLease(lease); tenantErr != nil {
		return nil, tenantErr
	}
	// This method is deliberately inventory-only after the native-only cutover.
	// A provider resource must already exist and be named by its durable handle;
	// provisioning is admitted atomically through the separate provider-control
	// boundary and must never create a placeholder lease here first.
	if strings.TrimSpace(lease.Resource.EngineVMID) == "" {
		return nil, ErrProviderRefRequired
	}
	// Never accept a caller-selected generation identifier. The store returns
	// the existing lease (and therefore its existing identifier) for a satisfied
	// idempotent request; a new or ghost-replacement generation persists this
	// freshly generated value even when it reuses the same lease ID.
	if generationErr := assignNewResourceGenerationID(&lease); generationErr != nil {
		return nil, generationErr
	}
	// Claims are authority-owned and never cross into a fresh generation from
	// caller metadata or a failed-generation replacement.
	setLeaseMetadata(&lease, MetadataKeyDecommissionClaimDigest, "")
	now := s.now().UTC()
	if lease.RenewedAt.IsZero() {
		lease.RenewedAt = now
	}
	if err := lease.Validate(now); err != nil {
		return nil, err
	}
	return s.store.Upsert(ctx, lease, strings.TrimSpace(req.IdempotencyKey))
}

func setLeaseMetadata(lease *vmlease.Lease, key, value string) {
	if lease.Metadata == nil {
		lease.Metadata = map[string]string{}
	}
	if value == "" {
		delete(lease.Metadata, key)
		return
	}
	lease.Metadata[key] = value
}

func (s *Service) Get(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmlease.Lease, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrTenantRequired
	}
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) ListByTenant(ctx context.Context, tenantID string) ([]vmlease.Lease, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	lister, ok := s.store.(LeaseListStore)
	if !ok {
		return nil, ErrNotFound
	}
	return lister.ListByTenant(ctx, tenantID)
}

func (s *Service) Patch(ctx context.Context, tenantID string, id vmlease.LeaseID, req PatchRequest) (*vmlease.Lease, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(req.TenantID)
	}
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	lease, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	expectedDigest := strings.TrimSpace(req.ExpectedResourceGenerationDigest)
	if expectedDigest != "" {
		if !validResourceGenerationDigest(expectedDigest) {
			return nil, ErrResourceGenerationDigest
		}
		currentDigest, digestErr := ResourceGenerationDigest(tenantID, *lease)
		if digestErr != nil {
			return nil, digestErr
		}
		if currentDigest != expectedDigest {
			return nil, ErrResourceGenerationSuperseded
		}
	}
	if req.ClaimDecommission && expectedDigest == "" {
		return nil, ErrResourceGenerationDigest
	}
	if lease.CancelledAt != nil {
		if req.Cancel {
			return lease, nil
		}
		if req.DesiredState != nil && *req.DesiredState != vmlease.DesiredStateStopped && *req.DesiredState != vmlease.DesiredStateArchived {
			return nil, ErrLeaseCancelled
		}
	}
	now := s.now().UTC()
	if req.DesiredState != nil {
		lease.DesiredState = *req.DesiredState
	}
	if req.ValidUntil != nil {
		lease.ValidUntil = req.ValidUntil.UTC()
	}
	if req.Cancel {
		lease.CancelledAt = &now
		lease.DesiredState = vmlease.DesiredStateStopped
		if req.DesiredState != nil && *req.DesiredState == vmlease.DesiredStateArchived {
			lease.DesiredState = vmlease.DesiredStateArchived
		}
	}
	for key, value := range req.Metadata {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if key == MetadataKeyResourceGenerationID {
			if strings.TrimSpace(value) != ResourceGenerationID(*lease) {
				return nil, ErrResourceGenerationImmutable
			}
			continue
		}
		if key == MetadataKeyDecommissionClaimDigest {
			if strings.TrimSpace(value) != strings.TrimSpace(lease.Metadata[MetadataKeyDecommissionClaimDigest]) {
				return nil, ErrDecommissionClaimImmutable
			}
			continue
		}
		setLeaseMetadata(lease, key, strings.TrimSpace(value))
	}
	if req.ClaimDecommission {
		setLeaseMetadata(lease, MetadataKeyDecommissionClaimDigest, expectedDigest)
	}
	lease.RenewedAt = now
	return s.store.Update(ctx, tenantID, *lease)
}
