package providercontrol

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

const (
	closedControlTransitionalContract = "transitional_contract"
	closedControlNoCertifiedAdapter   = "no_certified_adapter"

	productionMutationBlockMessage = "providercontrol: provider mutations are not activated: closed controls: " +
		closedControlTransitionalContract + ", " +
		closedControlNoCertifiedAdapter
)

// ErrMutationActivationBlocked identifies a fail-closed provider-mutation
// admission decision.
var ErrMutationActivationBlocked = errors.New("providercontrol: provider mutations are not activated")

// MutationActivationGate is the admission seam that every provider-mutating
// manager must cross immediately before it can authorize work.
//
// Implementations return nil only when every production activation control is
// independently satisfied. Callers must treat every error as a closed gate.
type MutationActivationGate interface {
	// Require returns nil only while mutation activation is permitted.
	Require(context.Context) error
}

// MutationActivationBlockedError reports the immutable production controls
// which currently prevent provider mutations.
type MutationActivationBlockedError struct{}

// Error returns a stable, secret-free operator message.
func (MutationActivationBlockedError) Error() string {
	return productionMutationBlockMessage
}

// Unwrap makes blocked activation decisions detectable with errors.Is.
func (MutationActivationBlockedError) Unwrap() error {
	return ErrMutationActivationBlocked
}

// ClosedControlIDs returns a fresh copy of the closed production control IDs.
func (MutationActivationBlockedError) ClosedControlIDs() []string {
	return []string{
		closedControlTransitionalContract,
		closedControlNoCertifiedAdapter,
	}
}

// ProductionBlockedGate is the immutable production gate while the tagged
// contract cutover and native adapter certification remain incomplete. Runtime
// construction is already separately
// fail-closed unless the dedicated NOBYPASSRLS role passes its physical-cluster,
// exact-grant, FORCE-RLS and SECURITY-DEFINER posture proof. The gate has no
// configuration or environment-variable bypass.
type ProductionBlockedGate struct{}

// Require always rejects provider mutations and reports every closed control.
func (ProductionBlockedGate) Require(context.Context) error {
	return MutationActivationBlockedError{}
}

var _ MutationActivationGate = ProductionBlockedGate{}

// CertifiedMutationGate opens provider mutation only when the runtime registry
// contains at least one adapter which passed the registry's capability
// admission. Provider-specific profile and credential custody checks still run
// transactionally for every operation and every side-effecting claim.
type CertifiedMutationGate struct {
	Registry *Registry
}

// Require rejects cancellation and empty/unconfigured registries. It has no
// environment-variable bypass: production opens only by composing an admitted
// adapter into the same registry used by the coordinator.
func (g CertifiedMutationGate) Require(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if g.Registry == nil || g.Registry.Len() == 0 {
		return fmt.Errorf("%w: no admitted provider adapter is registered", ErrMutationActivationBlocked)
	}
	return nil
}

var _ MutationActivationGate = CertifiedMutationGate{}

func nilMutationActivationGate(gate MutationActivationGate) bool {
	if gate == nil {
		return true
	}
	value := reflect.ValueOf(gate)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
