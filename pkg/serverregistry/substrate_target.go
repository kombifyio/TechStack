package serverregistry

import (
	"strings"
	"time"
)

// SubstrateVMRuntimeTarget projects an admitted, customer-owned guest. The
// caller must have proved creation custody; enrollment of an existing VM is
// not that proof and must retain self_owned_device classification.
func SubstrateVMRuntimeTarget(guestRef, leaseID string, observedAt time.Time) RuntimeTarget {
	guestRef, leaseID = strings.TrimSpace(guestRef), strings.TrimSpace(leaseID)
	if guestRef == "" || leaseID == "" || observedAt.IsZero() {
		return UnknownRuntimeTarget()
	}
	observedAt = observedAt.UTC()
	return RuntimeTarget{EnvironmentClass: EnvironmentLocal, Offering: OfferingSubstrateVM,
		ProviderID: "proxmox", ProviderTargetRef: guestRef, AvailabilityOwner: AvailabilityCustomer,
		OperationsOwner: OperationsCustomer, EvidenceRef: "runtime-lease:" + leaseID, ObservedAt: &observedAt}
}
