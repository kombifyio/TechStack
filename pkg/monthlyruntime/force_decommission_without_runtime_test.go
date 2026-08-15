package monthlyruntime

import (
	"testing"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
)

// dependencyOnlyLeaseAuthority satisfies the lease-authority presence check
// without implementing behaviour: validateActionDependencies only tests whether
// a lease authority exists, never calls it.
type dependencyOnlyLeaseAuthority struct {
	LeaseAuthority
}

// Production runs without the retired Simulate runtime client on purpose
// (routes_wiring logs legacy_runtime_client=false). Requiring it for a forced
// decommission made the one recovery path operators actually need return 503,
// which is how every managed server in the demo tenant became impossible to
// tear down.
//
// forceDecommission never contacts the runtime — that is the whole point of
// force — so its dependency is Reconcile, not Runtime.
func TestForceDecommissionDoesNotRequireTheRuntimeClient(t *testing.T) {
	service := &Service{Leases: dependencyOnlyLeaseAuthority{}}

	err := service.validateActionDependencies(ActionRequest{
		Action: serverruntime.RuntimeActionDecommission,
		Force:  true,
	})
	if err != nil {
		t.Fatalf("forced decommission was refused without a runtime client: %v", err)
	}
}

func TestPlainDecommissionUsesDurableProviderControlWithoutRuntimeClient(t *testing.T) {
	service := &Service{
		Leases:    dependencyOnlyLeaseAuthority{},
		Reconcile: &fakeReconciler{durable: true},
	}

	err := service.validateActionDependencies(ActionRequest{
		Action: serverruntime.RuntimeActionDecommission,
	})
	if err != nil {
		t.Fatalf("plain native decommission was refused without a runtime client: %v", err)
	}
}

// Everything that genuinely executes through the runtime client must still be
// refused when it is absent, or a missing dependency turns into a silent no-op.
func TestActionsThatUseTheRuntimeClientStillRequireIt(t *testing.T) {
	service := &Service{Leases: dependencyOnlyLeaseAuthority{}}

	for name, req := range map[string]ActionRequest{
		"unforced decommission": {Action: serverruntime.RuntimeActionDecommission},
		"start":                 {Action: serverruntime.RuntimeActionStart},
		"stop":                  {Action: serverruntime.RuntimeActionStop},
		"forced start":          {Action: serverruntime.RuntimeActionStart, Force: true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := service.validateActionDependencies(req); err != ErrRuntimeClient {
				t.Fatalf("err = %v, want ErrRuntimeClient", err)
			}
		})
	}
}

// SSH info was already exempt and must stay exempt.
func TestSSHInfoRemainsExemptFromTheRuntimeClient(t *testing.T) {
	service := &Service{Leases: dependencyOnlyLeaseAuthority{}}
	if err := service.validateActionDependencies(
		ActionRequest{Action: serverruntime.RuntimeActionSSHInfo},
	); err != nil {
		t.Fatalf("ssh-info was refused without a runtime client: %v", err)
	}
}

// A service with no lease authority at all is still refused first.
func TestMissingLeaseAuthorityIsRefusedBeforeTheRuntimeClient(t *testing.T) {
	service := &Service{}
	if err := service.validateActionDependencies(
		ActionRequest{Action: serverruntime.RuntimeActionDecommission, Force: true},
	); err == nil {
		t.Fatal("a service without a lease authority accepted a forced decommission")
	}
}
