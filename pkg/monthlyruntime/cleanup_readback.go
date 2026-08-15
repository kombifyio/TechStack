package monthlyruntime

import (
	"context"
	"errors"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

// ErrCleanupReadbackUnavailable means the native provider-control ledger is
// not available to prove cleanup state. Callers must not infer provider
// absence, server terminality, or capacity release from a lease alone.
var ErrCleanupReadbackUnavailable = errors.New("monthlyruntime: cleanup readback is unavailable")

// CleanupReadbackSource is the narrow, read-only provider-control seam used
// after monthly-runtime authorization has completed. It deliberately exposes
// neither provider resource identifiers nor provider credentials.
type CleanupReadbackSource interface {
	ReadManagedRuntimeCleanup(context.Context, string, vmlease.LeaseID) (*CleanupReadbackFacts, error)
}

// CleanupReadbackFacts are provider-control facts for the lease's exact live
// resource generation. EvidenceRef is an opaque provider-evidence reference,
// never the evidence document or a provider-native resource reference.
type CleanupReadbackFacts struct {
	ServerBound               bool
	ServerTerminal            bool
	ProviderOperationFound    bool
	ProviderOperationTerminal bool
	AbsenceEvidenceRef        string
	CapacityReleased          bool
}

// CleanupReadback is the owner-visible, redacted cleanup projection. It
// contains only terminality predicates and an opaque absence-evidence
// reference so E2E and the UI can prove lifecycle completion without exposing
// a provider handle, endpoint, command, or credential.
type CleanupReadback struct {
	LeaseID string `json:"lease_id"`
	Lease   struct {
		DesiredTerminal  bool `json:"desired_terminal"`
		ObservedTerminal bool `json:"observed_terminal"`
	} `json:"lease"`
	Server struct {
		Bound    bool `json:"bound"`
		Terminal bool `json:"terminal"`
	} `json:"server"`
	ProviderOperation struct {
		Found              bool   `json:"found"`
		Terminal           bool   `json:"terminal"`
		AbsenceEvidenceRef string `json:"absence_evidence_ref,omitempty"`
		CapacityReleased   bool   `json:"capacity_released"`
	} `json:"provider_operation"`
}

// CleanupStatus returns the exact owner/tenant-scoped cleanup projection for a
// provider-control-owned managed runtime. It performs database reads only; it
// never calls a provider or a runtime agent.
func (s *Service) CleanupStatus(ctx context.Context, tenantID, userID string, leaseID vmlease.LeaseID) (*CleanupReadback, error) {
	if s == nil || s.Leases == nil {
		return nil, vmleases.ErrEnrollmentRequired
	}
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	if tenantID == "" {
		return nil, vmleases.ErrTenantRequired
	}
	inventory, ok := s.Leases.(LeaseInventoryReader)
	if !ok {
		return nil, vmleases.ErrLeaseInventoryUnavailable
	}
	record, err := inventory.GetInventory(ctx, tenantID, leaseID)
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
	// A canceled native lease is intentionally native-inactive, but its
	// provider-control custody remains the only valid cleanup authority. Only
	// a different or absent authority is rejected here.
	if record.ExecutionAuthority != vmleases.LeaseExecutionAuthorityTechStackProviderControl {
		return nil, ErrExecutionAuthorityInactive
	}
	if s.CleanupReadback == nil {
		return nil, ErrCleanupReadbackUnavailable
	}
	facts, err := s.CleanupReadback.ReadManagedRuntimeCleanup(ctx, tenantID, leaseID)
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
	response.Lease.ObservedTerminal = cleanupLeaseObservedTerminal(lease)
	response.Server.Bound = facts.ServerBound
	response.Server.Terminal = facts.ServerTerminal
	response.ProviderOperation.Found = facts.ProviderOperationFound
	response.ProviderOperation.Terminal = facts.ProviderOperationTerminal
	response.ProviderOperation.AbsenceEvidenceRef = facts.AbsenceEvidenceRef
	response.ProviderOperation.CapacityReleased = facts.CapacityReleased
	return response, nil
}

func cleanupLeaseObservedTerminal(lease vmlease.Lease) bool {
	observed := strings.ToLower(strings.TrimSpace(lease.Metadata["runtime_observed_state"]))
	return observed == runtimeObservedStateNotFound ||
		strings.EqualFold(strings.TrimSpace(lease.Metadata["custody_resolution_status"]), "resolved")
}
