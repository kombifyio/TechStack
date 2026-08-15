package stacks

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/providercatalog"
)

// freshProviderIdentity collects provider selection only from the documented
// stack-create locations. It deliberately does not recursively inspect
// arbitrary service configuration, where provider_id may describe a
// completely different external integration.
type freshProviderIdentity struct {
	providerIDs []string
	runtimeIDs  []string
	legacy      []string
	managed     bool
}

func resolveCreateProviderIdentity(req createStackRequest) (string, error) {
	selection := freshProviderIdentity{
		providerIDs: []string{req.ProviderID},
		runtimeIDs:  []string{req.Provider},
		legacy:      []string{req.LeaseProvider, req.SimulateProviderID},
	}
	selection.observeProviderMode(req.Provider)

	for _, candidate := range []map[string]interface{}{req.Options, req.UserConfig, req.StackSpec} {
		if err := selection.observeConfig(candidate); err != nil {
			return "", err
		}
	}
	if parsed, ok := parseRawUserConfigMap(req.UserConfigRaw); ok {
		if err := selection.observeConfig(parsed); err != nil {
			return "", err
		}
	}
	if err := providercatalog.ValidateNoLegacyProviderFields(selection.legacy...); err != nil {
		return "", err
	}

	if hasNonEmptyProviderID(selection.providerIDs) {
		providerID, err := providercatalog.ResolveCanonicalProviderID(selection.providerIDs...)
		if err != nil {
			return "", err
		}
		if err := selection.validateRuntimeProviders(providerID); err != nil {
			return "", err
		}
		return providerID, nil
	}
	if selection.managed {
		if err := selection.validateRuntimeProviders(""); err != nil {
			return "", err
		}
		return "", providercatalog.ErrProviderIDRequired
	}
	if err := selection.validateRuntimeProviders(""); err != nil {
		return "", err
	}
	return "", nil
}

func canonicalizeFreshProvisionSpec(spec map[string]interface{}) (map[string]interface{}, error) {
	selection := freshProviderIdentity{}
	if err := selection.observeConfig(spec); err != nil {
		return nil, err
	}
	if err := providercatalog.ValidateNoLegacyProviderFields(selection.legacy...); err != nil {
		return nil, err
	}
	providerID := ""
	var err error
	switch {
	case hasNonEmptyProviderID(selection.providerIDs):
		providerID, err = providercatalog.ResolveCanonicalProviderID(selection.providerIDs...)
	case selection.managed:
		if validateErr := selection.validateRuntimeProviders(""); validateErr != nil {
			err = validateErr
		} else {
			err = providercatalog.ErrProviderIDRequired
		}
	}
	if err == nil {
		err = selection.validateRuntimeProviders(providerID)
	}
	if err != nil {
		return nil, err
	}
	if providerID == "" {
		return spec, nil
	}
	canonical := cloneMapForMutation(spec)
	if canonical == nil {
		canonical = map[string]interface{}{}
	}
	applyCanonicalProviderID(canonical, providerID)
	return canonical, nil
}

func (s *freshProviderIdentity) observeConfig(config map[string]interface{}) error {
	if config == nil {
		return nil
	}
	if err := s.observeFields(config, true); err != nil {
		return err
	}
	for _, key := range []string{"metadata", "options"} {
		if nested, ok := mapFromAny(config[key]); ok {
			if err := s.observeFields(nested, true); err != nil {
				return err
			}
		}
	}
	if userConfig, ok := mapFromAny(config["user_config"]); ok {
		if err := s.observeConfig(userConfig); err != nil {
			return fmt.Errorf("user_config: %w", err)
		}
	}
	if stackSpec, ok := mapFromAny(config["stack_spec"]); ok {
		if err := s.observeConfig(stackSpec); err != nil {
			return fmt.Errorf("stack_spec: %w", err)
		}
	}
	if raw, ok := config["user_config_raw"].(string); ok {
		if parsed, parsedOK := parseRawUserConfigMap(raw); parsedOK {
			if err := s.observeConfig(parsed); err != nil {
				return fmt.Errorf("user_config_raw: %w", err)
			}
		}
	}
	for index, node := range sliceFromAny(config["nodes"]) {
		nodeMap, ok := mapFromAny(node)
		if !ok {
			continue
		}
		if err := s.observeFields(nodeMap, false); err != nil {
			return fmt.Errorf("nodes[%d]: %w", index, err)
		}
		for _, key := range []string{"metadata", "options"} {
			if nested, ok := mapFromAny(nodeMap[key]); ok {
				if err := s.observeFields(nested, false); err != nil {
					return fmt.Errorf("nodes[%d].%s: %w", index, key, err)
				}
			}
		}
	}
	return nil
}

func (s *freshProviderIdentity) observeFields(fields map[string]interface{}, observeProviderMode bool) error {
	providerID, err := freshProviderStringField(fields, providercatalog.ProviderIDField)
	if err != nil {
		return err
	}
	leaseProvider, err := freshProviderStringField(fields, providercatalog.LegacyLeaseProviderField)
	if err != nil {
		return err
	}
	simulateProviderID, err := freshProviderStringField(fields, providercatalog.LegacySimulateProviderIDField)
	if err != nil {
		return err
	}
	s.providerIDs = append(s.providerIDs, providerID)
	runtimeProvider, err := freshProviderStringField(fields, "provider")
	if err != nil {
		return err
	}
	s.runtimeIDs = append(s.runtimeIDs, runtimeProvider)
	s.legacy = append(s.legacy, leaseProvider, simulateProviderID)
	if providerID != "" {
		s.managed = true
	}
	if observeProviderMode {
		s.observeProviderMode(runtimeProvider)
	}
	s.observeRuntimeMode(fields)
	return nil
}

func (s *freshProviderIdentity) validateRuntimeProviders(providerID string) error {
	for _, value := range s.runtimeIDs {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "centron-managed" || normalized == "ionos-managed" ||
			((normalized == providercatalog.ProviderCentron || normalized == providercatalog.ProviderIONOS) && value != normalized) {
			_, err := providercatalog.CanonicalProviderID(value)
			return err
		}
		if !s.managed && providerID == "" {
			continue
		}
		switch value {
		case "", "cloud", "local", "homelab":
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

func (s *freshProviderIdentity) observeProviderMode(value string) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cloud":
		s.managed = true
	case providercatalog.ProviderCentron, providercatalog.ProviderIONOS, "centron-managed", "ionos-managed":
		s.managed = true
	case runtimeModeKombifyCloud, runtimeModeManagedCloud, runtimeModeMonthlyRuntime:
		s.managed = true
	}
}

func (s *freshProviderIdentity) observeRuntimeMode(fields map[string]interface{}) {
	if stringFromAny(fields["server_provisioning_mode"]) == runtimeModeKombifyCloud ||
		stringFromAny(fields["server_mode"]) == runtimeModeMonthlyRuntime ||
		stringFromAny(fields["server_mode"]) == runtimeModeManagedCloud ||
		stringFromAny(fields["runtime_lane"]) == runtimeModeMonthlyRuntime {
		s.managed = true
	}
}

func freshProviderStringField(fields map[string]interface{}, key string) (string, error) {
	value, present := fields[key]
	if !present || value == nil {
		return "", nil
	}
	provider, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return provider, nil
}

func hasNonEmptyProviderID(values []string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}

func applyCanonicalProviderID(config map[string]interface{}, providerID string) {
	if config != nil && providerID != "" {
		config[providercatalog.ProviderIDField] = providerID
	}
}
