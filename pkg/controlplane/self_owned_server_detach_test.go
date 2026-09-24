package controlplane

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func TestSelfOwnedServerDetachAllowsOnlyCustomerOperatedBYO(t *testing.T) {
	observedAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	local := serverregistry.RuntimeTarget{EnvironmentClass: serverregistry.EnvironmentLocal, Offering: serverregistry.OfferingSelfOwnedDevice, AvailabilityOwner: serverregistry.AvailabilityCustomer, OperationsOwner: serverregistry.OperationsCustomer, EvidenceRef: "owner-pairing:agent-1", ObservedAt: &observedAt}
	external := serverregistry.RuntimeTarget{EnvironmentClass: serverregistry.EnvironmentCloud, Offering: serverregistry.OfferingExternalVPS, ProviderID: "hostinger", ProviderTargetRef: "srv-1", AvailabilityOwner: serverregistry.AvailabilityProvider, OperationsOwner: serverregistry.OperationsCustomer, EvidenceRef: "server-provider-binding:hostinger", ObservedAt: &observedAt}
	tests := []struct {
		name   string
		server ServerRuntime
		want   bool
	}{
		{name: "local self-owned", server: ServerRuntime{RuntimeTarget: local}, want: true},
		{name: "external customer-operated VPS", server: ServerRuntime{RuntimeTarget: external}, want: true},
		{name: "managed VPS", server: ServerRuntime{LeaseID: "lease-1", RuntimeTarget: serverregistry.RuntimeTarget{Offering: serverregistry.OfferingManagedVPS, OperationsOwner: serverregistry.OperationsKombify}}},
		{name: "unknown custody", server: ServerRuntime{}},
		{name: "lease on otherwise external VPS", server: ServerRuntime{LeaseID: "lease-1", RuntimeTarget: external}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := selfOwnedServerDetachAllowed(test.server); got != test.want {
				t.Fatalf("selfOwnedServerDetachAllowed() = %t, want %t", got, test.want)
			}
		})
	}
}
