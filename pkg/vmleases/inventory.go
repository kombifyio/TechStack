package vmleases

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

// LeaseAuthorityState is the closed, read-only inventory projection of a
// lease's execution authority and current lifecycle state.
type LeaseAuthorityState string

const (
	LeaseAuthorityStateUnbound           LeaseAuthorityState = "unbound"
	LeaseAuthorityStateLegacyQuarantined LeaseAuthorityState = "legacy_quarantined"
	LeaseAuthorityStateNativeInactive    LeaseAuthorityState = "native_inactive"
	LeaseAuthorityStateNativeActive      LeaseAuthorityState = "native_active"
)

var ErrLeaseInventoryUnavailable = errors.New("vmleases: authority-aware lease inventory unavailable")

// LeaseInventoryRecord keeps inventory identity and execution authority in one
// tenant-scoped read model. Callers must use NativeActive before exposing any
// action that could cause a provider side effect.
type LeaseInventoryRecord struct {
	Lease              vmlease.Lease           `json:"lease"`
	ExecutionAuthority LeaseExecutionAuthority `json:"execution_authority"`
	AuthorityState     LeaseAuthorityState     `json:"authority_state"`
}

// NativeActive is the central fail-closed predicate for native provider
// actions. Neither an authority binding nor a running lease is sufficient on
// its own.
func (record LeaseInventoryRecord) NativeActive() bool {
	return normalizeLeaseExecutionAuthority(record.ExecutionAuthority) == LeaseExecutionAuthorityTechStackProviderControl &&
		record.AuthorityState == LeaseAuthorityStateNativeActive
}

type leaseInventoryRow struct {
	lease          vmlease.Lease
	authority      LeaseExecutionAuthority
	authorityBound bool
}

type leaseInventoryStore interface {
	getInventory(ctx context.Context, tenantID string, leaseID vmlease.LeaseID) (*leaseInventoryRow, error)
	listInventoryByTenant(ctx context.Context, tenantID string) ([]leaseInventoryRow, error)
}

func classifyLeaseInventory(row leaseInventoryRow, now time.Time) (LeaseInventoryRecord, error) {
	record := LeaseInventoryRecord{Lease: cloneLease(row.lease)}
	if !row.authorityBound {
		record.AuthorityState = LeaseAuthorityStateUnbound
		return record, nil
	}

	authority := normalizeLeaseExecutionAuthority(row.authority)
	if err := validateLeaseExecutionAuthority(authority); err != nil {
		return LeaseInventoryRecord{}, err
	}
	record.ExecutionAuthority = authority
	switch authority {
	case LeaseExecutionAuthorityLegacySimulate:
		record.AuthorityState = LeaseAuthorityStateLegacyQuarantined
	case LeaseExecutionAuthorityTechStackProviderControl:
		record.AuthorityState = LeaseAuthorityStateNativeInactive
		if leaseLifecycleActive(record.Lease, now) {
			record.AuthorityState = LeaseAuthorityStateNativeActive
		}
	}
	return record, nil
}

func leaseLifecycleActive(lease vmlease.Lease, now time.Time) bool {
	now = now.UTC()
	return lease.DesiredState == vmlease.DesiredStateRunning && lease.ActiveAt(now)
}

func (s *Service) GetInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*LeaseInventoryRecord, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	reader, ok := s.store.(leaseInventoryStore)
	if !ok {
		return nil, ErrLeaseInventoryUnavailable
	}
	row, err := reader.getInventory(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	record, err := classifyLeaseInventory(*row, s.now().UTC())
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *Service) ListInventoryByTenant(ctx context.Context, tenantID string) ([]LeaseInventoryRecord, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	reader, ok := s.store.(leaseInventoryStore)
	if !ok {
		return nil, ErrLeaseInventoryUnavailable
	}
	rows, err := reader.listInventoryByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	records := make([]LeaseInventoryRecord, 0, len(rows))
	for _, row := range rows {
		record, classifyErr := classifyLeaseInventory(row, now)
		if classifyErr != nil {
			return nil, classifyErr
		}
		records = append(records, record)
	}
	return records, nil
}
