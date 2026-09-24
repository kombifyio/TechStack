package providercontrol

import (
	"encoding/json"
	"testing"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestNormalizeManagedBootstrapNetworkReturnsProviderNeutralRequirements(t *testing.T) {
	requirements, err := NormalizeManagedBootstrapNetwork(json.RawMessage(`{
		"host_firewall":"ssh_only",
		"public_ipv4":true,
		"ssh_ingress_port":22,
		"dns_resolution":true,
		"control_plane_egress":"https"
	}`))
	if err != nil {
		t.Fatalf("NormalizeManagedBootstrapNetwork: %v", err)
	}
	if requirements.ControlPlaneEgress != "https" || !requirements.DNSResolution ||
		!requirements.PublicIPv4 || requirements.SSHIngressPort != 22 ||
		requirements.HostFirewall != "ssh_only" {
		t.Fatalf("normalized requirements = %+v", requirements)
	}
}

func TestNormalizeManagedBootstrapNetworkRejectsNonBootstrapSafeRequirements(t *testing.T) {
	for _, payload := range []string{
		`{"control_plane_egress":"http","dns_resolution":true,"public_ipv4":true,"ssh_ingress_port":22,"host_firewall":"ssh_only"}`,
		`{"control_plane_egress":"https","dns_resolution":true,"public_ipv4":false,"ssh_ingress_port":22,"host_firewall":"ssh_only"}`,
		`{"control_plane_egress":"https","dns_resolution":true,"public_ipv4":true,"ssh_ingress_port":22,"host_firewall":"allow_all"}`,
		`{"control_plane_egress":"https","dns_resolution":true,"public_ipv4":true,"ssh_ingress_port":22,"host_firewall":"ssh_only","secret":"must-not-be-accepted"}`,
	} {
		if _, err := NormalizeManagedBootstrapNetwork(json.RawMessage(payload)); err == nil {
			t.Fatalf("NormalizeManagedBootstrapNetwork(%s) accepted unsafe requirements", payload)
		}
	}
}

func TestManagedBootstrapNetworkProfileForKeepsAdapterExceptionsExplicit(t *testing.T) {
	requirements := ManagedBootstrapNetworkRequirements{
		ControlPlaneEgress: "https", DNSResolution: true, PublicIPv4: true,
		SSHIngressPort: 22, HostFirewall: "ssh_only",
	}
	tests := []struct {
		provider, adapter string
		want              ManagedBootstrapAdapterException
	}{
		{"ionos", "ionos-cloudapi-v6", ManagedBootstrapAdapterExceptionIONOSPublicLAN},
		{"centron", "centron-ccloud-v1", ManagedBootstrapAdapterExceptionCentronNetworkReadback},
	}
	for _, test := range tests {
		profile, err := ManagedBootstrapNetworkProfileFor(test.provider, test.adapter, requirements)
		if err != nil {
			t.Fatalf("ManagedBootstrapNetworkProfileFor(%s/%s): %v", test.provider, test.adapter, err)
		}
		if profile.Requirements != requirements || profile.AdapterException != test.want {
			t.Fatalf("profile for %s/%s = %+v", test.provider, test.adapter, profile)
		}
	}
	if _, err := ManagedBootstrapNetworkProfileFor("ionos", "unknown-adapter", requirements); err == nil {
		t.Fatal("unknown adapter was admitted to managed bootstrap network contract")
	}
}

func TestCanonicalCatalogCapabilitiesValidatesManagedBootstrapNetwork(t *testing.T) {
	canonical, err := canonicalCatalogCapabilities(json.RawMessage(`{
		"network": {
			"ipv4": true,
			"managed_bootstrap": {
				"control_plane_egress": "https",
				"dns_resolution": true,
				"public_ipv4": true,
				"ssh_ingress_port": 22,
				"host_firewall": "ssh_only"
			}
		}
	}`))
	if err != nil || len(canonical) == 0 {
		t.Fatalf("canonicalCatalogCapabilities: payload was rejected: %v", err)
	}
	if err := validateManagedBootstrapNetworkAdapter("ionos", "ionos-cloudapi-v6", canonical); err != nil {
		t.Fatalf("validateManagedBootstrapNetworkAdapter: %v", err)
	}

	invalid, err := canonicalCatalogCapabilities(json.RawMessage(`{
		"network": {"managed_bootstrap": {
			"control_plane_egress": "https",
			"dns_resolution": true,
			"public_ipv4": true,
			"ssh_ingress_port": 22,
			"host_firewall": "ssh_only"
		}}
	}`))
	if err != nil {
		t.Fatalf("canonicalCatalogCapabilities invalid-adapter fixture: %v", err)
	}
	if err := validateManagedBootstrapNetworkAdapter("ionos", "centron-ccloud-v1", invalid); err == nil {
		t.Fatal("provider/adapter mismatch was admitted")
	}
}

func TestExecutionProfileBindsManagedBootstrapNetworkToReplaySnapshot(t *testing.T) {
	requirements := ManagedBootstrapNetworkRequirements{
		ControlPlaneEgress: "https", DNSResolution: true, PublicIPv4: true,
		SSHIngressPort: 22, HostFirewall: "ssh_only",
	}
	profile := testProfile("ionos-cloudapi-v6")
	profile.ManagedBootstrapNetwork = &ManagedBootstrapNetworkProfile{
		Requirements:     requirements,
		AdapterException: ManagedBootstrapAdapterExceptionIONOSPublicLAN,
	}
	normalized, snapshot, err := normalizeExecutionProfile(profile)
	if err != nil {
		t.Fatalf("normalizeExecutionProfile: %v", err)
	}
	if normalized.ManagedBootstrapNetwork == nil || snapshot.ManagedBootstrapNetwork == nil ||
		normalized.ManagedBootstrapNetwork.Requirements != requirements ||
		snapshot.ManagedBootstrapNetwork.AdapterException != ManagedBootstrapAdapterExceptionIONOSPublicLAN {
		t.Fatalf("managed bootstrap profile was not bound to execution snapshot: normalized=%+v snapshot=%+v", normalized.ManagedBootstrapNetwork, snapshot.ManagedBootstrapNetwork)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal execution profile snapshot: %v", err)
	}
	var replay ExecutionProfileSnapshot
	if err := json.Unmarshal(encoded, &replay); err != nil {
		t.Fatalf("unmarshal execution profile snapshot: %v", err)
	}
	command := providerexecutor.Command{
		ProviderID:             profile.ProviderID,
		AdapterID:              profile.AdapterID,
		CapabilitySnapshotHash: profile.CapabilitySnapshotHash,
		ExecutionProfileHash:   profile.ExecutionProfileHash,
	}
	if err := validateExecutionProfileSnapshot(replay, command); err != nil {
		t.Fatalf("validate replay execution profile snapshot: %v", err)
	}

	replay.ManagedBootstrapNetwork.AdapterException = ManagedBootstrapAdapterExceptionCentronNetworkReadback
	if err := validateExecutionProfileSnapshot(replay, command); err == nil {
		t.Fatal("replay accepted an adapter exception from another provider")
	}
}
