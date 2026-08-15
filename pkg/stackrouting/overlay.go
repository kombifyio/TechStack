package stackrouting

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/unifier"
	"gopkg.in/yaml.v3"
)

// ApplyToKombination applies desired routing after immutable intent loading.
// It removes stale kombify.me allocation metadata so StackKits cannot select a
// conflicting address mode over the explicit custom domain.
func ApplyToKombination(spec *core.KombinationSpec, state *DesiredState) error {
	if spec == nil || state == nil {
		return nil
	}
	if state.Mode != ModeCustomDomain || strings.TrimSpace(state.Domain) == "" {
		return fmt.Errorf("%w: unsupported persisted routing overlay", ErrInvalid)
	}
	spec.Network.Domain = state.Domain
	if spec.Metadata == nil {
		spec.Metadata = map[string]string{}
	}
	removeStaleStringMetadata(spec.Metadata)
	spec.Metadata["address_mode"] = ModeCustomDomain
	spec.Metadata["domain"] = state.Domain
	// StackKits flat routing treats an absent prefix as eligible for legacy
	// defaults in some precedence paths. An explicit empty value is the
	// authoritative custom-domain handoff.
	spec.Metadata["subdomainPrefix"] = ""
	spec.Metadata["routing_revision"] = strconv.FormatInt(state.Revision, 10)
	spec.Metadata["routing_server_id"] = state.ServerID
	if state.LeaseID != "" {
		spec.Metadata["routing_lease_id"] = state.LeaseID
	}
	spec.Metadata["routing_source"] = state.Provenance.Source
	spec.Metadata["dns_provider"] = state.Provenance.DNSProvider
	if state.Provenance.ZoneID != "" {
		spec.Metadata["dns_zone_id"] = state.Provenance.ZoneID
	}
	if state.Provenance.ExternalDomainID != "" {
		spec.Metadata["routing_external_domain_id"] = state.Provenance.ExternalDomainID
	}
	return nil
}

// ApplyToPersistedStackSpec rewrites only the derived StackKits handoff file;
// kombination.yaml remains byte-exact user intent.
func ApplyToPersistedStackSpec(persister *unifier.SpecPersister, state *DesiredState) (string, string, error) {
	if persister == nil || state == nil || !persister.StackSpecExists() {
		return "", "", nil
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		return "", "", fmt.Errorf("read StackKits routing handoff: %w", err)
	}
	next, err := ApplyToStackSpecBytes(data, state)
	if err != nil {
		return "", "", err
	}
	path, hash, err := persister.SaveStackSpecBytes(next)
	if err != nil {
		return "", "", fmt.Errorf("persist StackKits routing handoff: %w", err)
	}
	return path, hash, nil
}

func ApplyToStackSpecBytes(data []byte, state *DesiredState) ([]byte, error) {
	if state == nil || state.Mode != ModeCustomDomain || strings.TrimSpace(state.Domain) == "" {
		return nil, fmt.Errorf("%w: unsupported persisted routing overlay", ErrInvalid)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse StackKits routing handoff: %w", err)
	}
	if spec == nil {
		spec = map[string]any{}
	}
	applyMapRoutingOverlay(spec, state)
	// Legacy/control-plane configs can carry a nested copy whose network/domain
	// fields otherwise outrank the root handoff. Update existing copies too;
	// never create a second desired-state container when one was absent.
	for _, key := range []string{"user_config", "config"} {
		if nested, ok := spec[key].(map[string]any); ok {
			applyMapRoutingOverlay(nested, state)
			spec[key] = nested
		}
	}
	encoded, err := yaml.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("serialize StackKits routing handoff: %w", err)
	}
	return encoded, nil
}

func removeStaleStringMetadata(metadata map[string]string) {
	for key := range metadata {
		if overlayOwnedKey(key) {
			delete(metadata, key)
		}
	}
}

func removeStaleMapRouting(values map[string]any) {
	for key := range values {
		if overlayOwnedKey(key) {
			delete(values, key)
		}
	}
	if mode, ok := values["mode"].(string); ok && strings.EqualFold(strings.TrimSpace(mode), "kombify.me") {
		delete(values, "mode")
	}
}

func overlayOwnedKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch normalized {
	case "domain", "address_mode", "addressmode", "requested_address_mode", "requestedaddressmode",
		"subdomainprefix", "subdomain_prefix", "dns_provider", "dns_zone_id",
		"kombify_me", "kombifyme":
		return true
	}
	return strings.HasPrefix(normalized, "routing_") ||
		strings.HasPrefix(normalized, "kombify_me_") ||
		strings.HasPrefix(normalized, "kombifyme_")
}

func applyMapRoutingOverlay(values map[string]any, state *DesiredState) {
	removeStaleMapRouting(values)
	values["domain"] = state.Domain
	values["address_mode"] = ModeCustomDomain
	values["subdomainPrefix"] = ""
	network := mapFromAny(values["network"])
	removeStaleMapRouting(network)
	network["domain"] = state.Domain
	network["address_mode"] = ModeCustomDomain
	network["subdomainPrefix"] = ""
	values["network"] = network
	metadata := mapFromAny(values["metadata"])
	removeStaleMapRouting(metadata)
	metadata["address_mode"] = ModeCustomDomain
	metadata["domain"] = state.Domain
	metadata["subdomainPrefix"] = ""
	metadata["routing_revision"] = strconv.FormatInt(state.Revision, 10)
	metadata["routing_server_id"] = state.ServerID
	if state.LeaseID != "" {
		metadata["routing_lease_id"] = state.LeaseID
	}
	metadata["routing_source"] = state.Provenance.Source
	metadata["dns_provider"] = state.Provenance.DNSProvider
	if state.Provenance.ZoneID != "" {
		metadata["dns_zone_id"] = state.Provenance.ZoneID
	}
	if state.Provenance.ExternalDomainID != "" {
		metadata["routing_external_domain_id"] = state.Provenance.ExternalDomainID
	}
	values["metadata"] = metadata
}

func mapFromAny(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok && typed != nil {
		return typed
	}
	return map[string]any{}
}
