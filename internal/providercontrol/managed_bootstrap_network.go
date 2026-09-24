package providercontrol

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/providercatalog"
)

// ManagedBootstrapNetworkRequirements is the provider-neutral network
// contract needed to install and enrol Guard before StackKits is applied.
//
// It describes admission requirements only. It does not create a network,
// reserve a port, or own any provider lifecycle.
type ManagedBootstrapNetworkRequirements struct {
	ControlPlaneEgress string `json:"control_plane_egress"`
	DNSResolution      bool   `json:"dns_resolution"`
	PublicIPv4         bool   `json:"public_ipv4"`
	SSHIngressPort     int    `json:"ssh_ingress_port"`
	HostFirewall       string `json:"host_firewall"`
}

const (
	managedBootstrapNetworkField = "managed_bootstrap"
	managedBootstrapHTTPS        = "https"
	managedBootstrapSSHOnly      = "ssh_only"
	managedBootstrapSSHPort      = 22
)

// ManagedBootstrapAdapterException records the one provider adapter detail
// that cannot be expressed by the normalized network requirements.
type ManagedBootstrapAdapterException string

const (
	// ManagedBootstrapAdapterExceptionIONOSPublicLAN identifies the IONOS
	// composite request's public LAN/DHCP NIC and disabled default security
	// group. The host firewall remains the normalized SSH-only baseline.
	ManagedBootstrapAdapterExceptionIONOSPublicLAN ManagedBootstrapAdapterException = "ionos_public_lan_without_default_security_group"
	// ManagedBootstrapAdapterExceptionCentronNetworkReadback identifies the
	// Centron response shape where the public IPv4 is observed under
	// network_adapters[].ip_addresses[], rather than supplied in create input.
	ManagedBootstrapAdapterExceptionCentronNetworkReadback ManagedBootstrapAdapterException = "centron_network_adapter_ipv4_readback"
)

// ManagedBootstrapNetworkProfile combines the normalized catalog contract
// with the explicit adapter exception selected by the provider/adapter pair.
type ManagedBootstrapNetworkProfile struct {
	Requirements     ManagedBootstrapNetworkRequirements `json:"requirements"`
	AdapterException ManagedBootstrapAdapterException    `json:"adapter_exception"`
}

// NormalizeManagedBootstrapNetwork validates one catalog
// network.managed_bootstrap object and returns its normalized requirements.
// The payload is public capability data only; secrets and provider credentials
// are never accepted by this contract.
func NormalizeManagedBootstrapNetwork(payload json.RawMessage) (ManagedBootstrapNetworkRequirements, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return ManagedBootstrapNetworkRequirements{}, fmt.Errorf("managed bootstrap network must be an object")
	}
	for key := range fields {
		switch key {
		case "control_plane_egress", "dns_resolution", "public_ipv4", "ssh_ingress_port", "host_firewall":
		default:
			return ManagedBootstrapNetworkRequirements{}, fmt.Errorf("managed bootstrap network contains unsupported field %q", key)
		}
	}
	var requirements ManagedBootstrapNetworkRequirements
	if err := json.Unmarshal(payload, &requirements); err != nil {
		return ManagedBootstrapNetworkRequirements{}, fmt.Errorf("managed bootstrap network is invalid")
	}
	if err := validateManagedBootstrapNetworkRequirements(requirements); err != nil {
		return ManagedBootstrapNetworkRequirements{}, err
	}
	return requirements, nil
}

// ManagedBootstrapNetworkProfileFor resolves the explicit adapter exception
// for one canonical provider catalog profile. The adapter ID is part of the
// selection so a different adapter build cannot silently inherit provider
// network assumptions.
func ManagedBootstrapNetworkProfileFor(providerID, adapterID string, requirements ManagedBootstrapNetworkRequirements) (ManagedBootstrapNetworkProfile, error) {
	providerID = strings.TrimSpace(providerID)
	adapterID = strings.TrimSpace(adapterID)
	if err := validateManagedBootstrapNetworkRequirements(requirements); err != nil {
		return ManagedBootstrapNetworkProfile{}, err
	}
	var exception ManagedBootstrapAdapterException
	switch {
	case providerID == providercatalog.ProviderIONOS && adapterID == "ionos-cloudapi-v6":
		exception = ManagedBootstrapAdapterExceptionIONOSPublicLAN
	case providerID == providercatalog.ProviderCentron && adapterID == "centron-ccloud-v1":
		exception = ManagedBootstrapAdapterExceptionCentronNetworkReadback
	default:
		return ManagedBootstrapNetworkProfile{}, fmt.Errorf("managed bootstrap network adapter exception is not registered for %s/%s", providerID, adapterID)
	}
	return ManagedBootstrapNetworkProfile{Requirements: requirements, AdapterException: exception}, nil
}

func validateManagedBootstrapNetworkRequirements(requirements ManagedBootstrapNetworkRequirements) error {
	if requirements.ControlPlaneEgress != managedBootstrapHTTPS ||
		!requirements.DNSResolution || !requirements.PublicIPv4 ||
		requirements.SSHIngressPort != managedBootstrapSSHPort ||
		requirements.HostFirewall != managedBootstrapSSHOnly {
		return fmt.Errorf("managed bootstrap network requirements are incomplete")
	}
	return nil
}

func normalizeManagedBootstrapNetworkFromSnapshot(snapshot json.RawMessage) (ManagedBootstrapNetworkRequirements, bool, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &root); err != nil || root == nil {
		return ManagedBootstrapNetworkRequirements{}, false, fmt.Errorf("capability snapshot is invalid")
	}
	networkPayload, ok := root["network"]
	if !ok {
		return ManagedBootstrapNetworkRequirements{}, false, nil
	}
	var network map[string]json.RawMessage
	if err := json.Unmarshal(networkPayload, &network); err != nil || network == nil {
		return ManagedBootstrapNetworkRequirements{}, false, fmt.Errorf("network capability is invalid")
	}
	managedBootstrap, ok := network[managedBootstrapNetworkField]
	if !ok {
		return ManagedBootstrapNetworkRequirements{}, false, nil
	}
	requirements, err := NormalizeManagedBootstrapNetwork(managedBootstrap)
	if err != nil {
		return ManagedBootstrapNetworkRequirements{}, true, err
	}
	return requirements, true, nil
}

func validateManagedBootstrapNetworkAdapter(providerID, adapterID string, snapshot json.RawMessage) error {
	requirements, declared, err := normalizeManagedBootstrapNetworkFromSnapshot(snapshot)
	if err != nil {
		return err
	}
	if !declared {
		return nil
	}
	_, err = ManagedBootstrapNetworkProfileFor(providerID, adapterID, requirements)
	return err
}

// validateManagedBootstrapNetworkProfile binds a persisted execution-profile
// projection back to the canonical provider/adapter exception. A nil profile
// is retained for pre-contract catalog operations; new catalog profiles carry
// the typed profile and therefore cannot silently switch adapter semantics on
// retry or crash recovery.
func validateManagedBootstrapNetworkProfile(providerID, adapterID string, profile *ManagedBootstrapNetworkProfile) error {
	if profile == nil {
		return nil
	}
	resolved, err := ManagedBootstrapNetworkProfileFor(providerID, adapterID, profile.Requirements)
	if err != nil {
		return err
	}
	if profile.AdapterException != resolved.AdapterException {
		return fmt.Errorf("managed bootstrap network adapter exception does not match %s/%s", providerID, adapterID)
	}
	return nil
}

func validCapabilityNetworkObject(payload json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return false
	}
	for key, raw := range fields {
		switch key {
		case "ipv4", "ipv6", "private_network", "floating_ip":
			var value bool
			if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
				return false
			}
		case managedBootstrapNetworkField:
			if _, err := NormalizeManagedBootstrapNetwork(raw); err != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}
