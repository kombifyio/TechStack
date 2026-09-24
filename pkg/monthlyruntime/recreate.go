package monthlyruntime

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

var (
	ErrRecreateConfirmationRequired = errors.New("monthlyruntime: recreate requires explicit confirmation")
	ErrRecreateNotReady             = errors.New("monthlyruntime: recreate requires terminal cleanup evidence")
	ErrRecreateIntentInvalid        = errors.New("monthlyruntime: recreate intent is incomplete")
)

// RecreateRequest identifies an old managed-runtime generation. Preparing a
// recreate is read-only: the existing managed-runtime expansion authority owns
// every mutation after this service has proved terminal cleanup.
type RecreateRequest struct {
	TenantID  string
	UserID    string
	LeaseID   vmlease.LeaseID
	Confirmed bool
}

// RecreateIntent is the immutable, provider-neutral intent recovered from the
// released generation. Provider handles and credentials never cross this seam.
type RecreateIntent struct {
	StackID                   string
	RuntimeSlotKey            string
	PreviousGenerationOrdinal uint64
	NodeRole                  string
	RuntimeOfferingID         string
	ProviderID                string
	ProviderRegion            string
	StackKit                  string
	Services                  []string
}

// PrepareRecreate proves the old exact generation is terminal and reconstructs
// its immutable creation intent. It never calls a provider, reserves capacity,
// or creates a lease, server, operation, or job.
func (s *Service) PrepareRecreate(ctx context.Context, req RecreateRequest) (*RecreateIntent, error) {
	if !req.Confirmed {
		return nil, ErrRecreateConfirmationRequired
	}
	if s == nil || s.Leases == nil {
		return nil, vmleases.ErrEnrollmentRequired
	}
	tenantID := strings.TrimSpace(req.TenantID)
	userID := strings.TrimSpace(req.UserID)
	if tenantID == "" {
		return nil, vmleases.ErrTenantRequired
	}
	inventory, ok := s.Leases.(LeaseInventoryReader)
	if !ok {
		return nil, vmleases.ErrLeaseInventoryUnavailable
	}
	record, err := inventory.GetInventory(ctx, tenantID, req.LeaseID)
	if err != nil {
		return nil, err
	}
	lease := record.Lease
	if !canAccessLease(lease, userID, tenantID) {
		return nil, ErrForbidden
	}
	if !IsMonthlyRuntimeMetadata(lease.Metadata) {
		return nil, ErrInvalidLease
	}
	if record.ExecutionAuthority != vmleases.LeaseExecutionAuthorityTechStackProviderControl {
		return nil, ErrExecutionAuthorityInactive
	}
	if lease.RecreatePolicy != vmlease.RecreatePolicyManual {
		return nil, ErrRecreateIntentInvalid
	}

	cleanup, err := s.cleanupStatusForRecord(ctx, tenantID, lease)
	if err != nil {
		return nil, err
	}
	if !recreateCleanupComplete(cleanup) {
		return nil, ErrRecreateNotReady
	}

	generation, err := strconv.ParseUint(strings.TrimSpace(lease.Metadata["runtime_slot_generation"]), 10, 64)
	if err != nil || generation == 0 {
		return nil, ErrRecreateIntentInvalid
	}
	providerID, err := providercatalog.CanonicalProviderID(strings.TrimSpace(lease.Resource.ProviderID))
	if err != nil {
		return nil, ErrRecreateIntentInvalid
	}
	region := strings.TrimSpace(lease.Metadata["provider_region"])
	if region == "" {
		region = strings.TrimSpace(lease.Resource.Region)
	}
	if providerID == providercatalog.ProviderIONOS && region == "" {
		return nil, ErrRecreateIntentInvalid
	}
	intent := &RecreateIntent{
		StackID:                   strings.TrimSpace(lease.Metadata["stack_id"]),
		RuntimeSlotKey:            strings.TrimSpace(lease.Metadata["runtime_slot_key"]),
		PreviousGenerationOrdinal: generation,
		NodeRole:                  firstNonEmptyMetadata(lease.Metadata, "server_node_role", "node_role"),
		RuntimeOfferingID:         strings.TrimSpace(lease.Metadata["runtime_offering_id"]),
		ProviderID:                providerID,
		ProviderRegion:            region,
		StackKit:                  strings.TrimSpace(lease.Metadata["stackkit"]),
		Services:                  splitRecreateServices(lease.Metadata["requested_services"]),
	}
	if intent.StackID == "" || intent.RuntimeSlotKey == "" || intent.NodeRole == "" ||
		intent.RuntimeOfferingID == "" || intent.StackKit == "" {
		return nil, ErrRecreateIntentInvalid
	}
	if _, ok := OfferingByID(serverruntime.RuntimeOfferingID(intent.RuntimeOfferingID)); !ok {
		return nil, ErrRecreateIntentInvalid
	}
	return intent, nil
}

func (s *Service) cleanupStatusForRecord(ctx context.Context, tenantID string, lease vmlease.Lease) (*CleanupReadback, error) {
	if s.CleanupReadback == nil {
		return nil, ErrCleanupReadbackUnavailable
	}
	facts, err := s.CleanupReadback.ReadManagedRuntimeCleanup(ctx, tenantID, lease.ID)
	if err != nil {
		return nil, err
	}
	if facts == nil {
		return nil, ErrCleanupReadbackUnavailable
	}
	facts.AbsenceEvidenceRef = strings.TrimSpace(facts.AbsenceEvidenceRef)
	if facts.AbsenceEvidenceRef != "" && !strings.HasPrefix(facts.AbsenceEvidenceRef, "provider-evidence://") {
		return nil, ErrCleanupReadbackUnavailable
	}
	response := &CleanupReadback{LeaseID: string(lease.ID)}
	response.Lease.DesiredTerminal = lease.DesiredState == vmlease.DesiredStateArchived || lease.CancelledAt != nil
	response.Lease.ObservedTerminal = cleanupLeaseObservedTerminal(facts)
	response.Server.Bound = facts.ServerBound
	response.Server.Terminal = facts.ServerTerminal
	response.ProviderOperation.Found = facts.ProviderOperationFound
	response.ProviderOperation.Terminal = facts.ProviderOperationTerminal
	response.ProviderOperation.AbsenceEvidenceRef = facts.AbsenceEvidenceRef
	response.ProviderOperation.CapacityReleased = facts.CapacityReleased
	return response, nil
}

func recreateCleanupComplete(status *CleanupReadback) bool {
	return status != nil && status.Lease.DesiredTerminal && status.Lease.ObservedTerminal &&
		status.Server.Bound && status.Server.Terminal && status.ProviderOperation.Found &&
		status.ProviderOperation.Terminal && status.ProviderOperation.AbsenceEvidenceRef != "" &&
		status.ProviderOperation.CapacityReleased
}

func firstNonEmptyMetadata(metadata map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			return value
		}
	}
	return ""
}

func splitRecreateServices(value string) []string {
	parts := strings.Split(value, ",")
	services := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		service := strings.TrimSpace(part)
		if service == "" {
			continue
		}
		if _, ok := seen[service]; ok {
			continue
		}
		seen[service] = struct{}{}
		services = append(services, service)
	}
	return services
}
