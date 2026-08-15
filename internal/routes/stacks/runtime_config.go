//nolint:goconst
package stacks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

const managedRuntimeEntitlementDenied = monthlyruntime.ManagedRuntimeEntitlementDeniedMessage

type managedRuntimeFeatureChecker interface {
	IsEnabled(ctx context.Context, featureKey string, userID string) (bool, error)
}

type managedRuntimeEntitlementDecision struct {
	Denied          bool
	Message         string
	ProviderID      string
	ReasonCode      string
	RequiredFeature []string
	MissingFeature  []string
}

func (d managedRuntimeEntitlementDecision) Details() map[string]any {
	return monthlyruntime.ManagedRuntimeEntitlementDenialDetails(
		d.ProviderID,
		d.ReasonCode,
		d.RequiredFeature,
		d.MissingFeature,
	)
}

func applyRuntimeFieldsFromConfig(stack *core.Record, config map[string]interface{}) {
	if stack == nil || config == nil {
		return
	}
	for key, value := range runtimeFieldsFromConfig(config) {
		stack.Set(key, value)
	}
}

func runtimeConfigFromRequest(req normalizedCreateStackRequest) map[string]interface{} {
	if req.UserConfig != nil {
		config := cloneMapForMutation(req.UserConfig)
		applyCanonicalProviderID(config, req.ProviderID)
		return config
	}
	if strings.TrimSpace(req.UserConfigRaw) == "" {
		return nil
	}
	if parsed, ok := parseRawUserConfigMap(req.UserConfigRaw); ok {
		applyCanonicalProviderID(parsed, req.ProviderID)
		return parsed
	}
	return nil
}

func runtimePolicyConfigFromRequest(req normalizedCreateStackRequest) map[string]interface{} {
	config := cloneMapForMutation(runtimeConfigFromRequest(req))
	if config == nil {
		config = map[string]interface{}{}
	}
	if len(req.Options) > 0 {
		options := cloneMapForMutation(req.Options)
		config["options"] = options
		for key, value := range options {
			if _, exists := config[key]; !exists {
				config[key] = value
			}
		}
	}
	applyCanonicalProviderID(config, req.ProviderID)
	return config
}

func validateDeploymentLane(req normalizedCreateStackRequest, mode config.DeploymentMode) string {
	if !mode.IsValid() {
		mode = config.ModeSelfHosted
	}
	policyConfig := runtimePolicyConfigFromRequest(req)
	fields := runtimeFieldsFromConfig(policyConfig)
	if mode.IsSaaS() || allowLocalManagedRuntimeE2E() {
		return ""
	}
	if hasManagedRuntimeFields(policyConfig, fields) {
		return "Managed monthly runtime can only be created from kombify Cloud mode"
	}
	return ""
}

func validateManagedRuntimeEntitlement(ctx context.Context, req normalizedCreateStackRequest, userID string, checker managedRuntimeFeatureChecker) string {
	decision := evaluateManagedRuntimeEntitlement(ctx, req, userID, checker)
	if decision.Denied {
		return decision.Message
	}
	return ""
}

func evaluateManagedRuntimeEntitlement(ctx context.Context, req normalizedCreateStackRequest, userID string, checker managedRuntimeFeatureChecker) managedRuntimeEntitlementDecision {
	policyConfig := runtimePolicyConfigFromRequest(req)
	fields := runtimeFieldsFromConfig(policyConfig)
	if !hasManagedRuntimeFields(policyConfig, fields) {
		return managedRuntimeEntitlementDecision{}
	}
	if allowLocalManagedRuntimeE2E() {
		return managedRuntimeEntitlementDecision{}
	}
	if managedRuntimeAdminEntitled(ctx) {
		return managedRuntimeEntitlementDecision{}
	}
	providerID := fieldString(fields, providercatalog.ProviderIDField)
	requiredFeatures := monthlyruntime.RequiredFeatureKeysForProvider(providerID)
	if checker == nil {
		return managedRuntimeEntitlementDecision{
			Denied:          true,
			Message:         managedRuntimeEntitlementDenied,
			ProviderID:      providerID,
			ReasonCode:      monthlyruntime.EntitlementReasonCheckerUnavailable,
			RequiredFeature: requiredFeatures,
		}
	}
	for _, featureKey := range requiredFeatures {
		enabled, err := checker.IsEnabled(ctx, featureKey, userID)
		if err != nil {
			return managedRuntimeEntitlementDecision{
				Denied:          true,
				Message:         managedRuntimeEntitlementDenied,
				ProviderID:      providerID,
				ReasonCode:      monthlyruntime.EntitlementReasonFeatureCheckFailed,
				RequiredFeature: requiredFeatures,
				MissingFeature:  []string{featureKey},
			}
		}
		if !enabled {
			return managedRuntimeEntitlementDecision{
				Denied:          true,
				Message:         managedRuntimeEntitlementDenied,
				ProviderID:      providerID,
				ReasonCode:      monthlyruntime.EntitlementReasonFeatureDisabled,
				RequiredFeature: requiredFeatures,
				MissingFeature:  []string{featureKey},
			}
		}
	}
	return managedRuntimeEntitlementDecision{}
}

func managedRuntimeAdminEntitled(ctx context.Context) bool {
	id := identity.FromContext(ctx)
	if id == nil || !id.IsAuthenticated() {
		return false
	}
	return id.HasRole("admin") || id.HasRole("super_admin") || id.HasRole("global_admin")
}

func hasManagedRuntimeFields(policyConfig map[string]interface{}, fields map[string]any) bool {
	if hasManagedRuntimeCreatePolicy(policyConfig) {
		return true
	}
	for _, key := range []string{
		"runtime_offering_id",
		providercatalog.ProviderIDField,
		"lease_id",
		"simulate_node_lifecycle",
	} {
		if fieldString(fields, key) != "" {
			return true
		}
	}
	switch strings.ToLower(fieldString(fields, "server_provisioning_mode")) {
	case runtimeModeKombifyCloud:
		return true
	}
	switch strings.ToLower(fieldString(fields, "server_mode")) {
	case runtimeModeMonthlyRuntime, runtimeModeManagedCloud:
		return true
	}
	if strings.EqualFold(fieldString(fields, "runtime_lane"), runtimeModeMonthlyRuntime) {
		return true
	}
	return strings.EqualFold(fieldString(fields, "billing_mode"), "subscription")
}

func allowLocalManagedRuntimeE2E() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TECHSTACK_ENV")), "development") &&
		truthyRuntimeConfigEnv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE") &&
		truthyRuntimeConfigEnv("TECHSTACK_ALLOW_LOCAL_MANAGED_RUNTIME_E2E")
}

func truthyRuntimeConfigEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func fieldString(fields map[string]any, key string) string {
	value, ok := fields[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func normalizeRuntimeStackKitRef(value, defaultKit string) string {
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

func isManagedLeaseProvider(provider string) bool {
	return providercatalog.IsCanonicalProviderID(provider)
}

//nolint:goconst,gocyclo // Runtime projection accepts several legacy and StackKit wire keys.
func runtimeFieldsFromConfig(config map[string]interface{}) map[string]any {
	fields := map[string]any{}
	setString := func(field, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			fields[field] = value
		}
	}
	setBool := func(field string, value bool, present bool) {
		if present {
			fields[field] = value
		}
	}

	serverMode := runtimeStringFromConfig(config, "server_mode")
	runtimeLane := runtimeStringFromConfig(config, "runtime_lane")
	providerID := runtimeStringFromConfig(config, providercatalog.ProviderIDField)
	stackKitRef := firstNonEmpty(
		runtimeStringFromConfig(config, "stackkit_foundation"),
		runtimeStringFromConfig(config, "stackkit_catalog_ref"),
		runtimeStringFromConfig(config, "stackkit"),
		runtimeStringFromConfig(config, "kit"),
	)
	serverProvisioningMode := runtimeStringFromConfig(config, "server_provisioning_mode")
	serverConnectionMode := runtimeStringFromConfig(config, "server_connection_mode")
	providerRegion := runtimeStringFromConfig(config, "provider_region")
	ionosDatacenter := runtimeStringFromConfig(config, "ionos_datacenter")

	if serverMode == "" && providerID != "" {
		serverMode = runtimeModeMonthlyRuntime
	}
	switch strings.ToLower(strings.TrimSpace(serverProvisioningMode)) {
	case runtimeProvisioningConnectRemote:
		if serverMode == "" {
			serverMode = runtimeModeUserOwned
		}
		if serverConnectionMode == "" {
			serverConnectionMode = "remote-ssh"
		}
	case runtimeProvisioningInstall:
		if serverMode == "" {
			serverMode = runtimeModeUserOwned
		}
		if serverConnectionMode == "" {
			serverConnectionMode = "agent-oneliner"
		}
	}
	if runtimeLane == "" && serverMode == runtimeModeMonthlyRuntime {
		runtimeLane = runtimeModeMonthlyRuntime
	}
	if serverProvisioningMode == "" && serverMode == runtimeModeMonthlyRuntime {
		serverProvisioningMode = runtimeModeKombifyCloud
	}
	if serverConnectionMode == "" && serverProvisioningMode == runtimeModeKombifyCloud {
		serverConnectionMode = "managed-subscription"
	}
	if providerID == providercatalog.ProviderIONOS {
		ionosDatacenter = monthlyruntime.NormalizeIONOSDatacenter(firstNonEmpty(ionosDatacenter, providerRegion))
		providerRegion = ionosDatacenter
	}
	defaultStackKit := "basement-kit"
	if serverProvisioningMode == runtimeModeKombifyCloud || serverMode == runtimeModeMonthlyRuntime || isManagedLeaseProvider(providerID) {
		defaultStackKit = "cloud-kit"
	}
	stackKitRef = normalizeRuntimeStackKitRef(stackKitRef, defaultStackKit)
	if stackKitRef == "" {
		stackKitRef = defaultStackKit
	}

	setString("runtime_phase", runtimeStringFromConfig(config, "runtime_phase"))
	setString("server_mode", serverMode)
	setString("runtime_lane", runtimeLane)
	setString("runtime_offering_id", runtimeStringFromConfig(config, "runtime_offering_id"))
	setString(providercatalog.ProviderIDField, providerID)
	setString("provider_region", providerRegion)
	setString("ionos_datacenter", ionosDatacenter)
	setString("lease_id", runtimeStringFromConfig(config, "lease_id"))
	setString("simulate_node_lifecycle", runtimeStringFromConfig(config, "simulate_node_lifecycle"))
	setString("desired_state", runtimeStringFromConfig(config, "desired_state"))
	setString("billing_mode", runtimeStringFromConfig(config, "billing_mode"))
	setString("billing_cadence", runtimeStringFromConfig(config, "billing_cadence"))
	setString("stackkit_catalog_ref", stackKitRef)
	setString("verification_status", runtimeStringFromConfig(config, "verification_status"))
	setString("server_provisioning_mode", serverProvisioningMode)
	setString("server_connection_mode", serverConnectionMode)
	setString("server_remote_auth_method", runtimeStringFromConfig(config, "server_remote_auth_method"))
	setString("server_remote_credential_ref", firstNonEmpty(
		runtimeStringFromConfig(config, "server_remote_credential_ref"),
		runtimeStringFromConfig(config, "server_remote_ssh_key_label"),
	))

	if value, present := runtimeBoolFromConfig(config, "server_remote_host_present"); present {
		setBool("server_remote_host_present", value, true)
	} else if strings.TrimSpace(runtimeStringFromConfig(config, "server_remote_host")) != "" {
		setBool("server_remote_host_present", true, true)
	}
	if value, present := runtimeBoolFromConfig(config, "server_remote_user_present"); present {
		setBool("server_remote_user_present", value, true)
	} else if strings.TrimSpace(runtimeStringFromConfig(config, "server_remote_user")) != "" {
		setBool("server_remote_user_present", true, true)
	}
	if value, present := runtimeBoolFromConfig(config, "server_remote_use_sudo"); present {
		setBool("server_remote_use_sudo", value, true)
	}
	if value, present := runtimeBoolFromConfig(config, "server_install_command_required"); present {
		setBool("server_install_command_required", value, true)
	} else if serverProvisioningMode == runtimeProvisioningInstall {
		setBool("server_install_command_required", true, true)
	}

	return fields
}

func runtimeStringFromConfig(config map[string]interface{}, key string) string {
	return firstNonEmpty(
		provisioningValueFromMap(config, key),
		provisioningValueFromNestedMap(config, "metadata", key),
		provisioningValueFromNestedMap(config, "options", key),
	)
}

func runtimeBoolFromConfig(config map[string]interface{}, key string) (bool, bool) {
	for _, value := range []interface{}{
		config[key],
		nestedValueFromMap(config, "metadata", key),
		nestedValueFromMap(config, "options", key),
	} {
		switch v := value.(type) {
		case bool:
			return v, true
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "1", "true", "yes", "on":
				return true, true
			case "0", "false", "no", "off":
				return false, true
			}
		}
	}
	return false, false
}

func nestedValueFromMap(spec map[string]interface{}, parentKey, key string) interface{} {
	nested, ok := mapFromAny(spec[parentKey])
	if !ok {
		return nil
	}
	return nested[key]
}

func provisioningValueFromNestedMap(spec map[string]interface{}, parentKey, key string) string {
	nested, ok := mapFromAny(spec[parentKey])
	if !ok {
		return ""
	}
	return provisioningValueFromMap(nested, key)
}

func provisioningValueFromMap(spec map[string]interface{}, key string) string {
	if spec == nil {
		return ""
	}
	return stringFromAny(spec[key])
}

func mapFromAny(value interface{}) (map[string]interface{}, bool) {
	switch v := value.(type) {
	case map[string]interface{}:
		return v, true
	case map[string]string:
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			out[key] = item
		}
		return out, true
	case types.JSONRaw:
		var out map[string]interface{}
		if err := json.Unmarshal(v, &out); err == nil {
			return out, true
		}
		return nil, false
	default:
		return nil, false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

//nolint:goconst // Provider and metadata names are external wizard/StackKit wire values.
func hasManagedRuntimeCreatePolicy(spec map[string]interface{}) bool {
	if spec == nil {
		return false
	}
	if strings.EqualFold(stringFromAny(spec["provider"]), "cloud") {
		return true
	}
	if metadata, ok := spec["metadata"].(map[string]interface{}); ok {
		if isManagedRuntimeMetadata(metadata) {
			return true
		}
	}
	if metadata, ok := spec["metadata"].(map[string]string); ok {
		asAny := make(map[string]interface{}, len(metadata))
		for key, value := range metadata {
			asAny[key] = value
		}
		if isManagedRuntimeMetadata(asAny) {
			return true
		}
	}
	for _, node := range sliceFromAny(spec["nodes"]) {
		nodeMap, ok := node.(map[string]interface{})
		if !ok {
			continue
		}
		provider := stringFromAny(nodeMap["provider"])
		if provider == "cloud" || provider == runtimeModeManagedCloud ||
			providercatalog.IsCanonicalProviderID(stringFromAny(nodeMap[providercatalog.ProviderIDField])) {
			return true
		}
	}
	return false
}

//nolint:goconst // Metadata names are external wizard/StackKit wire values.
func isManagedRuntimeMetadata(metadata map[string]interface{}) bool {
	switch strings.ToLower(strings.TrimSpace(stringFromAny(metadata["server_provisioning_mode"]))) {
	case runtimeModeKombifyCloud:
		return true
	}
	switch strings.ToLower(strings.TrimSpace(stringFromAny(metadata["server_mode"]))) {
	case runtimeModeMonthlyRuntime, runtimeModeManagedCloud:
		return true
	}
	if strings.EqualFold(stringFromAny(metadata["runtime_lane"]), runtimeModeMonthlyRuntime) {
		return true
	}
	return providercatalog.IsCanonicalProviderID(stringFromAny(metadata[providercatalog.ProviderIDField]))
}

func stringFromAny(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func sliceFromAny(value interface{}) []interface{} {
	switch v := value.(type) {
	case []interface{}:
		return v
	case []map[string]interface{}:
		out := make([]interface{}, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}
