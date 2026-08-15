package jobs

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/pkg/core"
)

// validateFreshManagedProviderSpec enforces provider identity before the job
// persists artifacts or calls a lease manager. UI mode labels are not provider
// identities; every managed node and metadata identity must agree on one exact
// canonical provider_id.
func validateFreshManagedProviderSpec(raw map[string]interface{}, spec *core.KombinationSpec) error {
	legacyValues := []string{
		rawStringExact(raw, metadataKeyLeaseProvider),
		rawStringExact(raw, metadataKeySimulateProviderID),
	}
	providerValues := []string{rawStringExact(raw, metadataKeyProviderID)}
	runtimeProviders := []string{rawStringExact(raw, providerField)}

	for _, sectionName := range []string{"metadata", "options"} {
		section := mapFromInterface(raw[sectionName])
		legacyValues = append(legacyValues,
			rawStringExact(section, metadataKeyLeaseProvider),
			rawStringExact(section, metadataKeySimulateProviderID),
		)
		providerValues = append(providerValues, rawStringExact(section, metadataKeyProviderID))
	}
	for _, rawNode := range interfaceSlice(raw["nodes"]) {
		node := mapFromInterface(rawNode)
		providerValues = append(providerValues, rawStringExact(node, metadataKeyProviderID))
		runtimeProviders = append(runtimeProviders, rawStringExact(node, providerField))
	}
	if spec != nil {
		legacyValues = append(legacyValues,
			spec.Metadata[metadataKeyLeaseProvider],
			spec.Metadata[metadataKeySimulateProviderID],
		)
		providerValues = append(providerValues, spec.Metadata[metadataKeyProviderID])
		for _, node := range spec.Nodes {
			runtimeProviders = append(runtimeProviders, node.Provider)
		}
	}
	if err := providercatalog.ValidateNoLegacyProviderFields(legacyValues...); err != nil {
		return fmt.Errorf("jobs: fresh managed provider identity: %w", err)
	}

	managed := specUsesManagedRuntimeMode(spec) || rawUsesManagedRuntimeMode(raw)
	for _, sectionName := range []string{"metadata", "options"} {
		managed = managed || rawUsesManagedRuntimeMode(mapFromInterface(raw[sectionName]))
	}
	if spec != nil {
		managed = managed || spec.Metadata[metadataKeyProviderID] != ""
	}
	if err := validateUnmanagedRuntimeProviderSyntax(runtimeProviders); err != nil {
		return fmt.Errorf("jobs: fresh managed provider identity: %w", err)
	}
	if !managed {
		return nil
	}
	if err := validateManagedRuntimeProviders(runtimeProviders, ""); err != nil {
		return fmt.Errorf("jobs: fresh managed provider identity: %w", err)
	}
	candidates := make([]string, 0, len(providerValues))
	for _, value := range providerValues {
		switch value {
		case "", providerLocal, "homelab":
			continue
		case providerCloud:
			managed = true
			continue
		default:
			if _, err := providercatalog.CanonicalProviderID(value); err != nil {
				return fmt.Errorf("jobs: fresh managed provider identity: %w", err)
			}
			candidates = append(candidates, value)
		}
	}
	providerID, err := providercatalog.ResolveCanonicalProviderID(candidates...)
	if err != nil {
		return fmt.Errorf("jobs: fresh managed provider identity: %w", err)
	}
	if err := validateManagedRuntimeProviders(runtimeProviders, providerID); err != nil {
		return fmt.Errorf("jobs: fresh managed provider identity: %w", err)
	}
	if spec == nil {
		return nil
	}
	if spec.Metadata == nil {
		spec.Metadata = map[string]string{}
	}
	spec.Metadata[metadataKeyProviderID] = providerID
	delete(spec.Metadata, metadataKeyLeaseProvider)
	delete(spec.Metadata, metadataKeySimulateProviderID)
	return nil
}

func validateUnmanagedRuntimeProviderSyntax(values []string) error {
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		switch normalized {
		case "centron-managed", "ionos-managed":
			_, err := providercatalog.CanonicalProviderID(value)
			return err
		case providercatalog.ProviderCentron, providercatalog.ProviderIONOS:
			if value != normalized {
				_, err := providercatalog.CanonicalProviderID(value)
				return err
			}
		}
	}
	return nil
}

func rawUsesManagedRuntimeMode(values map[string]interface{}) bool {
	return rawStringExact(values, metadataKeyProviderID) != "" ||
		looksLikeManagedProviderValue(rawStringExact(values, providerField)) ||
		rawStringExact(values, metadataKeyServerProvisionMode) == serverProvisionModeKombifyCloud ||
		rawStringExact(values, metadataKeyServerMode) == serverModeMonthlyRuntime ||
		rawStringExact(values, metadataKeyServerMode) == serverModeManagedCloud ||
		rawStringExact(values, metadataKeyRuntimeLane) == serverModeMonthlyRuntime
}

func looksLikeManagedProviderValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case providerCloud, providercatalog.ProviderCentron, providercatalog.ProviderIONOS, "centron-managed", "ionos-managed":
		return true
	default:
		return false
	}
}

func validateManagedRuntimeProviders(values []string, providerID string) error {
	for _, value := range values {
		switch value {
		case "", providerLocal, providerCloud, "homelab":
			continue
		default:
			canonical, err := providercatalog.CanonicalProviderID(value)
			if err != nil {
				return err
			}
			if providerID != "" && canonical != providerID {
				return fmt.Errorf("%w: provider_id %q and runtime provider %q", providercatalog.ErrConflictingProviderIDs, providerID, canonical)
			}
		}
	}
	return nil
}

func specUsesManagedRuntimeMode(spec *core.KombinationSpec) bool {
	if spec == nil {
		return false
	}
	return spec.Metadata[metadataKeyServerMode] == serverModeMonthlyRuntime ||
		spec.Metadata[metadataKeyServerMode] == serverModeManagedCloud ||
		spec.Metadata[metadataKeyRuntimeLane] == serverModeMonthlyRuntime ||
		spec.Metadata[metadataKeyServerProvisionMode] == serverProvisionModeKombifyCloud
}

func rawStringExact(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
}
