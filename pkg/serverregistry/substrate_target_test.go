package serverregistry_test

import (
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"testing"
	"time"
)

// Stable custody invariant: local guests never imply Kombify-owned infrastructure.
func TestSubstrateGuestRequiresItsOwnLease(t *testing.T) {
	target := serverregistry.SubstrateVMRuntimeTarget("substrate-a/guest-900", "lease-a", time.Now())
	if target.AvailabilityOwner != serverregistry.AvailabilityCustomer || target.OperationsOwner != serverregistry.OperationsCustomer || serverregistry.ValidateRuntimeTarget(target, "lease-a") != nil {
		t.Fatal("customer guest classification failed")
	}
	if serverregistry.ValidateRuntimeTarget(target, "lease-b") == nil || serverregistry.ValidateRuntimeTarget(target, "") == nil {
		t.Fatal("guest accepted without exact custody")
	}
}
