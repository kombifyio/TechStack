package portinventory

import (
	"errors"
	"testing"
	"time"
)

// Stable invariant: a fresh observed foreign listener cannot be adopted by
// reserving its port. Same-number listeners on another transport stay separate.
func TestHostBaselinePreservesForeignListenerAndScopesUnknownExposure(t *testing.T) {
	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	requirement := Requirement{ID: "dns", NodeRef: "home", Transport: TransportUDP, BindAddress: "127.0.0.1", Port: 5353, Sharing: SharingExclusive, Exposure: ExposureLocal}
	inventory := Inventory{ObservedAt: &now, ExpiresAt: &expires, ListenersComplete: true, Allocations: []Allocation{{Transport: TransportUDP, BindAddress: "127.0.0.1", Port: 5353, ObservedState: EvidencePresent}}}
	if !errors.Is(EvaluateHostBaseline(inventory, []Requirement{requirement}, "deployment", now), ErrAllocationConflict) {
		t.Fatal("foreign UDP listener could be replaced")
	}
	requirement.Transport = TransportTCP
	if err := EvaluateHostBaseline(inventory, []Requirement{requirement}, "deployment", now); err != nil {
		t.Fatalf("unrelated transport or unknown exposure blocked an available binding: %v", err)
	}
	inventory.Allocations = []Allocation{
		{Transport: TransportTCP, BindAddress: "127.0.0.1", Port: 5353, ObservedState: EvidencePresent, Desired: true, StackID: "deployment", ClaimState: ClaimStateActive},
		{Transport: TransportTCP, BindAddress: "127.0.0.1", Port: 5353, ObservedState: EvidencePresent, Desired: true, StackID: "deployment", ClaimState: ClaimStatePending},
	}
	if err := EvaluateHostBaseline(inventory, []Requirement{requirement}, "deployment", now); err != nil {
		t.Fatalf("owned successor rollout was blocked by its own pending claim: %v", err)
	}
	inventory.ListenersComplete = false
	if EvaluateHostBaseline(inventory, []Requirement{requirement}, "deployment", now) == nil {
		t.Fatal("incomplete evidence was treated as free")
	}
	if err := EvaluateHostBaseline(inventory, nil, "deployment", now); err != nil {
		t.Fatal("unrelated unknown scope blocked a listener-free plan")
	}
}
