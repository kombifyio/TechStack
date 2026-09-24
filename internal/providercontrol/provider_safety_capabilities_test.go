package providercontrol

import "testing"

func TestProviderSafetyCapabilitiesValidateRequiredMutationSafety(t *testing.T) {
	complete := ProviderSafetyCapabilities{
		ProviderID: "ionos", AdapterID: "ionos-v1", AtMostOnceProvision: true,
		ExactHandleDecommission: true, DefinitiveAbsenceProof: true, HostFirewallBaseline: true,
	}
	if err := complete.Validate("ionos", "ionos-v1"); err != nil {
		t.Fatalf("complete capabilities: %v", err)
	}
	complete.HostFirewallBaseline = false
	if err := complete.Validate("ionos", "ionos-v1"); err == nil {
		t.Fatal("missing firewall baseline must fail conformance")
	}
}

func TestProviderSafetyCapabilitiesAllowsTruthfulMissingDiscovery(t *testing.T) {
	capabilities := ProviderSafetyCapabilities{
		ProviderID: "ionos", AdapterID: "ionos-v1", AtMostOnceProvision: true,
		ProvisionCandidateDiscovery: false, ExactHandleDecommission: true,
		DefinitiveAbsenceProof: true, HostFirewallBaseline: true,
	}
	if err := capabilities.Validate("ionos", "ionos-v1"); err != nil {
		t.Fatalf("candidate discovery is reported separately from minimum exact-handle safety: %v", err)
	}
}
