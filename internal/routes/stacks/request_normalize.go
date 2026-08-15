//nolint:goconst
package stacks

import (
	"encoding/json"
	"io"
	"strings"
)

const defaultCreateStackName = "homelab"

func decodeCreateStackRequest(body io.Reader) (createStackRequest, error) {
	var req createStackRequest
	err := json.NewDecoder(body).Decode(&req)
	return req, err
}

func normalizeCreateStackRequest(req createStackRequest) (normalizedCreateStackRequest, string) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultCreateStackName
	}
	if isReservedCreateStackName(name) {
		return normalizedCreateStackRequest{}, "TechStack is the platform name. Choose a Homelab name that is not techstack, techstack-*, kombify-techstack, or kombifytechstack."
	}

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = stackModeEasy
	}
	if mode != stackModeEasy && mode != stackModeTechie {
		return normalizedCreateStackRequest{}, "Invalid mode (expected 'easy' or 'techie')"
	}
	providerID, providerErr := resolveCreateProviderIdentity(req)
	if providerErr != nil {
		return normalizedCreateStackRequest{}, "Invalid provider selection: " + providerErr.Error()
	}

	userConfig := req.UserConfig
	if req.StackSpec != nil {
		userConfig = req.StackSpec
	}
	if userConfig == nil && strings.TrimSpace(req.UserConfigRaw) == "" {
		if req.Provider == "" && req.ProviderID == "" && len(req.Services) == 0 && req.Options == nil {
			return normalizedCreateStackRequest{}, "Missing stack_spec, user_config, user_config_raw, or direct config fields (provider/services/options)"
		}
		userConfig = map[string]interface{}{
			"name":        name,
			"provider":    req.Provider,
			"provider_id": req.ProviderID,
			"services":    req.Services,
			"options":     req.Options,
		}
	}
	applyCanonicalProviderID(userConfig, providerID)
	normalizeCreateStackKitRefs(userConfig, req)
	normalized := normalizedCreateStackRequest{
		Name:             name,
		Mode:             mode,
		UserConfig:       userConfig,
		UserConfigRaw:    req.UserConfigRaw,
		UserConfigFormat: req.UserConfigFormat,
		Options:          req.Options,
		ProviderID:       providerID,
	}
	if containsPlaintextRecoveryMaterialInRequest(normalized) {
		return normalizedCreateStackRequest{}, "Plaintext recovery passphrases are not accepted. Pass an argon2id hash or secret reference instead."
	}
	if msg := validateCreateOwnerBootstrap(normalized); msg != "" {
		return normalizedCreateStackRequest{}, msg
	}

	return normalized, ""
}

func isReservedCreateStackName(name string) bool {
	normalized := normalizeReservedCreateStackName(name)
	return normalized == "techstack" ||
		strings.HasPrefix(normalized, "techstack-") ||
		normalized == "kombify-techstack" ||
		strings.HasPrefix(normalized, "kombify-techstack-") ||
		normalized == "kombifytechstack" ||
		strings.HasPrefix(normalized, "kombifytechstack-")
}

func normalizeReservedCreateStackName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func normalizeCreateStackKitRefs(userConfig map[string]interface{}, req createStackRequest) {
	defaultKit := "basement-kit"
	if createRequestUsesManagedRuntime(req, userConfig) {
		defaultKit = "cloud-kit"
	}
	normalizeKitField := func(m map[string]interface{}, key string) {
		value, _ := m[key].(string)
		if normalized := normalizeCreateStackKitRef(value, defaultKit); normalized != "" {
			m[key] = normalized
		}
	}
	if userConfig != nil {
		normalizeKitField(userConfig, "kit")
		normalizeKitField(userConfig, "stackkit")
		if metadata, ok := userConfig["metadata"].(map[string]interface{}); ok {
			normalizeKitField(metadata, "stackkit_foundation")
			normalizeKitField(metadata, "stackkit_catalog_ref")
		}
		if options, ok := userConfig["options"].(map[string]interface{}); ok {
			normalizeKitField(options, "stackkit_foundation")
			normalizeKitField(options, "stackkit_catalog_ref")
		}
	}
	if req.Options != nil {
		normalizeKitField(req.Options, "stackkit_foundation")
		normalizeKitField(req.Options, "stackkit_catalog_ref")
	}
}

func createRequestUsesManagedRuntime(req createStackRequest, userConfig map[string]interface{}) bool {
	if req.ProviderID != "" || providerIndicatesManagedRuntime(req.Provider) {
		return true
	}
	if optionString(req.Options, "server_provisioning_mode") == runtimeModeKombifyCloud ||
		optionString(req.Options, "server_mode") == runtimeModeMonthlyRuntime ||
		optionString(req.Options, "provider_id") != "" {
		return true
	}
	if userConfig == nil {
		return false
	}
	if providerIndicatesManagedRuntime(optionString(userConfig, "provider")) {
		return true
	}
	if optionString(userConfig, "provider_id") != "" {
		return true
	}
	if options, ok := userConfig["options"].(map[string]interface{}); ok {
		if optionString(options, "server_provisioning_mode") == runtimeModeKombifyCloud ||
			optionString(options, "server_mode") == runtimeModeMonthlyRuntime ||
			optionString(options, "provider_id") != "" {
			return true
		}
	}
	if metadata, ok := userConfig["metadata"].(map[string]interface{}); ok {
		return optionString(metadata, "server_provisioning_mode") == runtimeModeKombifyCloud ||
			optionString(metadata, "server_mode") == runtimeModeMonthlyRuntime ||
			optionString(metadata, "provider_id") != ""
	}
	return false
}

func normalizeCreateStackKitRef(value, defaultKit string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "base-kit":
		return defaultKit
	case "basement", "basementkit", "basement-kit":
		return "basement-kit"
	case "cloud", "cloudkit", "kombify-cloud-kit", "cloud-kit":
		return "cloud-kit"
	default:
		return value
	}
}

func providerIndicatesManagedRuntime(provider string) bool {
	switch provider {
	case "cloud", runtimeModeKombifyCloud, runtimeModeManagedCloud, runtimeModeMonthlyRuntime:
		return true
	default:
		return false
	}
}

func optionString(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func createStackJobSpec(req normalizedCreateStackRequest) map[string]interface{} {
	if req.UserConfig != nil {
		spec := cloneMapForMutation(req.UserConfig)
		mergeCreateOptions(spec, req.Options)
		applyCanonicalProviderID(spec, req.ProviderID)
		if bootstrap, ok := ownerBootstrapFromRequest(req); ok {
			applyOwnerBootstrapToTransientSpec(spec, bootstrap)
		}
		return spec
	}
	if parsed, ok := parseRawUserConfigMap(req.UserConfigRaw); ok {
		mergeCreateOptions(parsed, req.Options)
		applyCanonicalProviderID(parsed, req.ProviderID)
		if bootstrap, ok := ownerBootstrapFromRequest(req); ok {
			applyOwnerBootstrapToTransientSpec(parsed, bootstrap)
		}
		return parsed
	}
	return map[string]interface{}{"raw": req.UserConfigRaw, "format": req.UserConfigFormat}
}

func mergeCreateOptions(spec map[string]interface{}, options map[string]interface{}) {
	if spec == nil || len(options) == 0 {
		return
	}
	existing, _ := spec["options"].(map[string]interface{})
	if existing == nil {
		existing = map[string]interface{}{}
		spec["options"] = existing
	}
	for key, value := range options {
		if _, present := existing[key]; !present {
			existing[key] = value
		}
	}
}
