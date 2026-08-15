package routes

import (
	"context"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

// nativeTestLeaseService is shared by route tests that need the native
// provider-control inventory contract. It is not Demo provisioning logic.
type nativeTestLeaseService struct {
	*vmleases.Service
}

func (s *nativeTestLeaseService) GetInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmleases.LeaseInventoryRecord, error) {
	lease, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	state := vmleases.LeaseAuthorityStateNativeActive
	if !monthlyruntime.LeaseActive(*lease) || lease.DesiredState != vmlease.DesiredStateRunning {
		state = vmleases.LeaseAuthorityStateNativeInactive
	}
	return &vmleases.LeaseInventoryRecord{
		Lease:              *lease,
		ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
		AuthorityState:     state,
	}, nil
}

func (s *nativeTestLeaseService) ListInventoryByTenant(ctx context.Context, tenantID string) ([]vmleases.LeaseInventoryRecord, error) {
	leases, err := s.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	records := make([]vmleases.LeaseInventoryRecord, 0, len(leases))
	for index := range leases {
		record, recordErr := s.GetInventory(ctx, tenantID, leases[index].ID)
		if recordErr != nil {
			return nil, recordErr
		}
		records = append(records, *record)
	}
	return records, nil
}
