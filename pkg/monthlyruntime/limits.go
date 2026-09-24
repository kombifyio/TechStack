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

// LeaseVisibleToOwner is the dashboard's owner-visibility predicate. A lease is
// visible when it belongs to the tenant AND either the asking subject holds it
// or the organization itself does.
//
// The org rung requires the organization to actually be the lease subject.
// Subject kind alone grants nothing: a lease that merely carries kind "org"
// while naming some other subject stays invisible, so a stray or mislabeled
// subject cannot turn one owner's machine into tenant-wide inventory. Tenant
// membership is never sufficient on its own - a tenant can hold several
// unrelated owners.
func LeaseVisibleToOwner(lease vmlease.Lease, tenantID, ownerID string) bool {
	tenantID = strings.TrimSpace(tenantID)
	ownerID = strings.TrimSpace(ownerID)
	if tenantID == "" || ownerID == "" {
		return false
	}
	if strings.TrimSpace(lease.Subject.OrgID) != tenantID {
		return false
	}
	subjectID := strings.TrimSpace(lease.Subject.ID)
	if subjectID == ownerID {
		return true
	}
	return lease.Subject.Kind == vmlease.SubjectOrg && subjectID == tenantID
}

// ManagedRuntimeCapacityReservationDenialDetails describes the authoritative
// native-admission ceiling. Reserved capacity includes pending, ambiguous, and
// quarantined provider custody, so it must not be presented as running servers.
func ManagedRuntimeCapacityReservationDenialDetails(providerID string, limit, reserved int) map[string]any {
	return structuredFailureDetails(failureEnvelope{
		phase: "managed_runtime_entitlement", phaseLabel: "Managed runtime availability",
		errorCode: ManagedRuntimeMaxServersErrorCode, reasonCode: ReasonMaxServersReached,
		capability: ManagedRuntimeCapability, providerID: managedRuntimeProviderID(providerID),
		guidance: map[string]any{
			"title": "Managed server capacity reserved",
			"body":  "This account already holds all managed server slots included in its plan. This version conservatively retains every reservation; a terminal operation or local decommission state alone does not free a slot.",
			"next_steps": []string{
				"Use the job IDs and original Idempotency-Keys of existing managed-server requests to inspect their custody state.",
				"Contact kombify support for operator reconciliation before creating a replacement server.",
				"Upgrade your plan for more managed servers.",
			},
		},
	}, map[string]any{
		"limit":            limit,
		"reserved_servers": reserved,
	})
}
