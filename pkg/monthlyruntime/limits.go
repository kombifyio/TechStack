package monthlyruntime

import (
	"context"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

const (
	// ManagedRuntimeMaxServersErrorCode marks an authoritative native capacity
	// reservation denial.
	ManagedRuntimeMaxServersErrorCode = "managed_runtime_max_servers_reached"
	ReasonMaxServersReached           = "max_servers_reached"
)

// LeaseLister is the minimal managed-runtime inventory seam used for routing
// and visibility. It is not a capacity-policy boundary.
type LeaseLister interface {
	ListByTenant(ctx context.Context, tenantID string) ([]vmlease.Lease, error)
}

// LeaseActive reports whether a lease is visible as active inventory. It does
// not imply that the lease holds or releases commercial capacity.
func LeaseActive(lease vmlease.Lease) bool {
	if lease.CancelledAt != nil {
		return false
	}
	return lease.DesiredState != vmlease.DesiredStateArchived
}

// LeaseVisibleToOwner mirrors the dashboard's owner-visibility predicate: the
// lease belongs to the tenant and is either owned by the subject or org-scoped.
func LeaseVisibleToOwner(lease vmlease.Lease, tenantID, ownerID string) bool {
	if strings.TrimSpace(lease.Subject.OrgID) != strings.TrimSpace(tenantID) {
		return false
	}
	if strings.TrimSpace(lease.Subject.ID) == strings.TrimSpace(ownerID) {
		return true
	}
	return lease.Subject.Kind == vmlease.SubjectOrg
}

// ManagedRuntimeCapacityReservationDenialDetails describes the authoritative
// native-admission ceiling. Reserved capacity includes pending, ambiguous, and
// quarantined provider custody, so it must not be presented as running servers.
func ManagedRuntimeCapacityReservationDenialDetails(providerID string, limit, reserved int) map[string]any {
	return managedRuntimeFailureDetails(
		"managed_runtime_entitlement",
		"Managed runtime availability",
		ManagedRuntimeMaxServersErrorCode,
		ReasonMaxServersReached,
		providerID,
		false,
		map[string]any{
			"title": "Managed server capacity reserved",
			"body":  "This account already holds all managed server slots included in its plan. This version conservatively retains every reservation; a terminal operation or local decommission state alone does not free a slot.",
			"next_steps": []string{
				"Use the job IDs and original Idempotency-Keys of existing managed-server requests to inspect their custody state.",
				"Contact kombify support for operator reconciliation before creating a replacement server.",
				"Upgrade your plan for more managed servers.",
			},
		},
		map[string]any{
			"limit":            limit,
			"reserved_servers": reserved,
		},
	)
}
