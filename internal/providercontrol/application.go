package providercontrol

import (
	"context"
	"fmt"
	"strings"
)

// OperationApplication is the narrow command/query seam for internal
// transports and workers. Start never performs a provider side effect;
// Advance performs at most one legal receipt transition.
type OperationApplication interface {
	Start(context.Context, StartRequest) (OperationRecord, bool, error)
	Get(context.Context, string, string) (OperationRecord, error)
	Advance(context.Context, string, string) (OperationRecord, bool, error)
}

// ApplicationService exposes provider-control use cases without leaking the
// ledger or adapter registry to transports and schedulers.
type ApplicationService struct {
	coordinator *Coordinator
	ledger      Ledger
}

// NewApplicationService creates the internal command/query boundary.
func NewApplicationService(coordinator *Coordinator, ledger Ledger) (*ApplicationService, error) {
	if coordinator == nil || ledger == nil {
		return nil, fmt.Errorf("%w: coordinator and ledger are required", ErrInvalidRequest)
	}
	return &ApplicationService{coordinator: coordinator, ledger: ledger}, nil
}

// Start persists a native provider-control operation without invoking an adapter.
func (s *ApplicationService) Start(ctx context.Context, req StartRequest) (OperationRecord, bool, error) {
	if s == nil || s.coordinator == nil {
		return OperationRecord{}, false, fmt.Errorf("%w: application service is not configured", ErrInvalidRequest)
	}
	return s.coordinator.Start(ctx, req)
}

// Get loads and validates one tenant-scoped native operation.
func (s *ApplicationService) Get(ctx context.Context, tenantID, operationID string) (OperationRecord, error) {
	if s == nil || s.ledger == nil || s.coordinator == nil {
		return OperationRecord{}, fmt.Errorf("%w: application service is not configured", ErrInvalidRequest)
	}
	tenantID = strings.TrimSpace(tenantID)
	operationID = strings.TrimSpace(operationID)
	if tenantID == "" || operationID == "" {
		return OperationRecord{}, fmt.Errorf("%w: tenant and operation id are required", ErrInvalidRequest)
	}
	record, err := s.ledger.LoadOperation(ctx, tenantID, operationID)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := validateOperationRecord(ctx, record, s.coordinator.verifier); err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// Advance performs at most one legal receipt transition.
func (s *ApplicationService) Advance(ctx context.Context, tenantID, operationID string) (OperationRecord, bool, error) {
	if s == nil || s.coordinator == nil {
		return OperationRecord{}, false, fmt.Errorf("%w: application service is not configured", ErrInvalidRequest)
	}
	return s.coordinator.Advance(ctx, tenantID, operationID)
}

var _ OperationApplication = (*ApplicationService)(nil)
