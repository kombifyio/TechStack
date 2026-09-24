package providercontrol

import (
	"fmt"
	"strings"
)

// ProviderSafetyCapabilities states which safety primitives an adapter build
// actually implements. It is descriptive, never a substitute for the catalog,
// create kill switch, or an operator reconciliation decision.
type ProviderSafetyCapabilities struct {
	ProviderID                  string
	AdapterID                   string
	AtMostOnceProvision         bool
	ProvisionCandidateDiscovery bool
	ExactHandleDecommission     bool
	DefinitiveAbsenceProof      bool
	HostFirewallBaseline        bool
}

func (c ProviderSafetyCapabilities) Validate(providerID, adapterID string) error {
	if strings.TrimSpace(c.ProviderID) != strings.TrimSpace(providerID) ||
		strings.TrimSpace(c.AdapterID) != strings.TrimSpace(adapterID) {
		return fmt.Errorf("providercontrol: adapter safety capability identity mismatch")
	}
	if !c.AtMostOnceProvision || !c.ExactHandleDecommission || !c.DefinitiveAbsenceProof ||
		!c.HostFirewallBaseline {
		return fmt.Errorf("providercontrol: adapter %s safety capabilities are incomplete", c.AdapterID)
	}
	return nil
}

// ManagedProviderSafetyReporter is mandatory at production adapter
// registration. Candidate discovery may truthfully be false while the adapter
// remains available for exact-handle cleanup; the create kill switch decides
// whether new cost-bearing intent is admitted.
type ManagedProviderSafetyReporter interface {
	ProviderSafetyCapabilities() ProviderSafetyCapabilities
}
