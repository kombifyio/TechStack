package providercontrol

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// AllowGate is the test-only activation gate for callers which need to prove
// behavior beyond mutation admission.
type AllowGate struct{}

// Require allows activation only inside tests.
func (AllowGate) Require(context.Context) error { return nil }

func newTestCoordinator(cfg CoordinatorConfig) (*Coordinator, error) {
	cfg.ActivationGate = AllowGate{}
	return NewCoordinator(cfg)
}

var _ MutationActivationGate = AllowGate{}

func TestProductionBlockedGateReportsClosedControls(t *testing.T) {
	err := (ProductionBlockedGate{}).Require(context.Background())
	if !errors.Is(err, ErrMutationActivationBlocked) {
		t.Fatalf("Require() error = %v, want ErrMutationActivationBlocked", err)
	}

	var blocked MutationActivationBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("Require() error type = %T, want MutationActivationBlockedError", err)
	}
	want := []string{
		"transitional_contract",
		"no_certified_adapter",
	}
	if got := blocked.ClosedControlIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ClosedControlIDs() = %#v, want %#v", got, want)
	}
}

func TestCertifiedMutationGateAllowsOnlyAnAdmittedRegistry(t *testing.T) {
	empty := NewRegistry()
	if err := (CertifiedMutationGate{Registry: empty}).Require(context.Background()); !errors.Is(err, ErrMutationActivationBlocked) {
		t.Fatalf("empty certified gate error = %v, want ErrMutationActivationBlocked", err)
	}

	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-cloudapi-v6", &atMostOnceCapabilityExecutor{
		capability: AtMostOnceProvisionCapability{
			AdapterManifestHash:          digest("test-adapter-manifest"),
			PerHeadInvocationKey:         true,
			SideEffectFreePreparation:    true,
			PreparedRequestDigestBinding: true,
			ReadOnlyProvisionPolling:     true,
			ReadOnlyGeneralObservation:   true,
			ExactHandleDecommission:      true,
			ReadOnlyAbsencePolling:       true,
		},
	}); err != nil {
		t.Fatalf("register certified adapter: %v", err)
	}
	if err := (CertifiedMutationGate{Registry: registry}).Require(context.Background()); err != nil {
		t.Fatalf("certified gate rejected admitted registry: %v", err)
	}
}

func TestProductionBlockedGateControlIDsCannotBeMutated(t *testing.T) {
	firstErr := (ProductionBlockedGate{}).Require(context.Background())
	var first MutationActivationBlockedError
	if !errors.As(firstErr, &first) {
		t.Fatalf("first Require() error type = %T, want MutationActivationBlockedError", firstErr)
	}
	firstIDs := first.ClosedControlIDs()
	firstIDs[0] = "opened_by_caller"

	secondErr := (ProductionBlockedGate{}).Require(context.Background())
	var second MutationActivationBlockedError
	if !errors.As(secondErr, &second) {
		t.Fatalf("second Require() error type = %T, want MutationActivationBlockedError", secondErr)
	}
	if got := second.ClosedControlIDs()[0]; got != "transitional_contract" {
		t.Fatalf("ClosedControlIDs()[0] = %q after caller mutation, want transitional_contract", got)
	}
}

func TestProductionBlockedGateHasNoBypassSurface(t *testing.T) {
	t.Setenv("TECHSTACK_PROVIDER_MUTATIONS_ENABLED", "true")
	t.Setenv("TECHSTACK_PROVIDER_ACTIVATION_BYPASS", "true")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for name, ctx := range map[string]context.Context{
		"background": context.Background(),
		"cancelled":  cancelled,
		"nil":        nil,
	} {
		t.Run(name, func(t *testing.T) {
			if err := (ProductionBlockedGate{}).Require(ctx); !errors.Is(err, ErrMutationActivationBlocked) {
				t.Fatalf("Require() error = %v, want ErrMutationActivationBlocked", err)
			}
		})
	}
}

func TestCoordinatorDefaultsToProductionBlockedGate(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: testProfile("ionos-v1")}, Ledger: ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if _, _, err := coordinator.Start(t.Context(), planRequest()); !errors.Is(err, ErrMutationActivationBlocked) {
		t.Fatalf("Start error = %v, want ErrMutationActivationBlocked", err)
	}
	if ledger.beginCalls != 0 {
		t.Fatalf("blocked Start performed %d ledger writes", ledger.beginCalls)
	}
}

type activationCountingTenantSource struct{ calls int }

func (s *activationCountingTenantSource) ListRunnableTenants(context.Context, string, int) (RunnableTenantPage, error) {
	s.calls++
	return RunnableTenantPage{}, nil
}

func TestRuntimeRunChecksActivationBeforeTenantDiscovery(t *testing.T) {
	source := &activationCountingTenantSource{}
	var reported error
	runtime := &Runtime{
		activation: ProductionBlockedGate{},
		worker: &Worker{
			tenantSource: source,
			onError: func(err error) {
				reported = err
			},
		},
	}

	runtime.Run(t.Context())
	if source.calls != 0 {
		t.Fatalf("blocked runtime performed %d tenant-discovery reads", source.calls)
	}
	if !errors.Is(reported, ErrMutationActivationBlocked) {
		t.Fatalf("reported error = %v, want ErrMutationActivationBlocked", reported)
	}
}
