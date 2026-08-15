package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"gopkg.in/yaml.v3"
)

type provisionProviderIdentity struct {
	providerIDs []string
	runtimeIDs  []string
	legacy      []string
	managed     bool
}

// canonicalizeProvisionProviderIdentity is the last in-process fence before a
// provision job or stack-status mutation. Historical provider labels remain
// readable, but they cannot become authority for a fresh execution.
func canonicalizeProvisionProviderIdentity(spec, stackConfig map[string]interface{}) (map[string]interface{}, error) {
	selection := provisionProviderIdentity{}
	for _, config := range []map[string]interface{}{stackConfig, spec} {
		if err := selection.observeConfig(config); err != nil {
			return nil, err
		}
	}
	if err := providercatalog.ValidateNoLegacyProviderFields(selection.legacy...); err != nil {
		return nil, err
	}

	providerID := ""
	var err error
	switch {
	case hasProvisionProviderID(selection.providerIDs):
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
	canonical := cloneProvisionSpec(spec)
	canonical[providercatalog.ProviderIDField] = providerID
	return canonical, nil
}

func (s *provisionProviderIdentity) observeConfig(config map[string]interface{}) error {
	if config == nil {
		return nil
	}
	if err := s.observeFields(config, true); err != nil {
		return err
	}
	for _, key := range []string{"metadata", "options"} {
		if nested, ok := providerIdentityMap(config[key]); ok {
			if err := s.observeFields(nested, true); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	if userConfig, ok := providerIdentityMap(config["user_config"]); ok {
		if err := s.observeConfig(userConfig); err != nil {
			return fmt.Errorf("user_config: %w", err)
		}
	}
	if stackSpec, ok := providerIdentityMap(config["stack_spec"]); ok {
		if err := s.observeConfig(stackSpec); err != nil {
			return fmt.Errorf("stack_spec: %w", err)
		}
	}
	if raw, ok := config["user_config_raw"].(string); ok {
		if parsed, parsedOK := parseProvisionProviderRaw(raw); parsedOK {
			if err := s.observeConfig(parsed); err != nil {
				return fmt.Errorf("user_config_raw: %w", err)
			}
		}
	}
	for index, node := range providerIdentitySlice(config["nodes"]) {
		nodeMap, ok := providerIdentityMap(node)
		if !ok {
			continue
		}
		if err := s.observeFields(nodeMap, false); err != nil {
			return fmt.Errorf("nodes[%d]: %w", index, err)
		}
		for _, key := range []string{"metadata", "options"} {
			if nested, ok := providerIdentityMap(nodeMap[key]); ok {
				if err := s.observeFields(nested, false); err != nil {
					return fmt.Errorf("nodes[%d].%s: %w", index, key, err)
				}
			}
		}
	}
	return nil
}

func (s *provisionProviderIdentity) observeFields(fields map[string]interface{}, observeProviderMode bool) error {
	providerID, err := provisionProviderStringField(fields, providercatalog.ProviderIDField)
	if err != nil {
		return err
	}
	leaseProvider, err := provisionProviderStringField(fields, providercatalog.LegacyLeaseProviderField)
	if err != nil {
		return err
	}
	simulateProviderID, err := provisionProviderStringField(fields, providercatalog.LegacySimulateProviderIDField)
	if err != nil {
		return err
	}
	s.providerIDs = append(s.providerIDs, providerID)
	runtimeProvider, err := provisionProviderStringField(fields, "provider")
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
	if stringFromAny(fields[runtimeFieldProvisionMode]) == runtimeProvisionKombify ||
		stringFromAny(fields["server_mode"]) == runtimeLaneMonthly ||
		stringFromAny(fields["server_mode"]) == "managed-cloud" ||
		stringFromAny(fields[runtimeFieldLane]) == runtimeLaneMonthly {
		s.managed = true
	}
	return nil
}

func (s *provisionProviderIdentity) validateRuntimeProviders(providerID string) error {
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

func (s *provisionProviderIdentity) observeProviderMode(value string) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cloud":
		s.managed = true
	case providercatalog.ProviderCentron, providercatalog.ProviderIONOS, "centron-managed", "ionos-managed":
		s.managed = true
	case runtimeProvisionKombify, "managed-cloud", runtimeLaneMonthly:
		s.managed = true
	}
}

func provisionProviderStringField(fields map[string]interface{}, key string) (string, error) {
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

func providerIdentityMap(value interface{}) (map[string]interface{}, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		return typed, true
	case map[string]string:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			result[key] = item
		}
		return result, true
	default:
		return nil, false
	}
}

func providerIdentitySlice(value interface{}) []interface{} {
	switch typed := value.(type) {
	case []interface{}:
		return typed
	case []map[string]interface{}:
		result := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	default:
		return nil
	}
}

func parseProvisionProviderRaw(raw string) (map[string]interface{}, bool) {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil && parsed != nil {
		return parsed, true
	}
	parsed = nil
	if err := yaml.Unmarshal([]byte(raw), &parsed); err == nil && parsed != nil {
		return parsed, true
	}
	return nil, false
}

func hasProvisionProviderID(values []string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}

func cloneProvisionSpec(spec map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(spec)+1)
	for key, value := range spec {
		result[key] = value
	}
	return result
}
