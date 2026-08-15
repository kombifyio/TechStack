// Package jobs provides UI-to-Spec conversion utilities.
package jobs

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/pkg/core"
)

const (
	identityHeadPocketBase   = "pocketbase"
	identityHeadPocketID     = "pocket_id"
	serviceNameImmich        = "immich"
	serviceNameImmichML      = "immich-ml"
	serviceNameImmichServer  = "immich-server"
	serviceNameOTelCollector = "otel-collector"
	serviceNamePocketID      = "pocket-id"
	serviceNameTraefik       = "traefik"
	serviceNameUptimeKuma    = "uptime-kuma"
	serviceNameVaultwarden   = "vaultwarden"
	serviceNameNextcloud     = "nextcloud"
	serviceNameImmichDB      = "immich-postgres"
	serviceNameImmichCache   = "immich-redis"
	serviceTypeAuth          = "auth"
	serviceTypeCache         = "cache"
	serviceTypeDatabase      = "database"
	serviceTypeMedia         = "media"
	serviceTypeMonitoring    = "monitoring"
	serviceTypeReverseProxy  = "reverse-proxy"
	serviceTypeStorage       = "storage"
	defaultUIStackName       = runtimeActionServiceName
	uiNetworkDomainPublic    = "public"
)

func convertUIConfigToSpec(data interface{}) (*core.KombinationSpec, error) {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("spec must be a JSON object")
	}
	if err := validateFreshManagedProviderSpec(dataMap, nil); err != nil {
		return nil, err
	}
	if _, ok := dataMap["stackkit"]; ok {
		return convertStackKitConfigToSpec(dataMap)
	}

	name := defaultUIStackName
	if rawName, ok := dataMap["name"].(string); ok {
		name = sanitizeDNSName(rawName)
	}
	provider := authoritativeUIProviderID(dataMap)
	if provider == "" {
		provider = providerLocal
	}
	if p, ok := dataMap[providerField].(string); provider == providerLocal && ok && strings.TrimSpace(p) != "" {
		provider = strings.TrimSpace(p)
	}
	provider = mapWizardProviderToUnifier(provider)
	kit := uiSpecKit(dataMap)
	metadata, err := uiSpecMetadata(dataMap, provider, kit)
	if err != nil {
		return nil, err
	}
	spec := &core.KombinationSpec{
		Name:    name,
		Kit:     kit,
		Network: uiSpecNetwork(dataMap),
		Nodes: []core.NodeSpec{{
			Name:     stackRoleMain,
			Type:     stackRoleMain,
			Provider: provider,
		}},
		Services: uiSpecServices(dataMap),
		Metadata: metadata,
	}
	if len(spec.Services) == 0 && isDefaultSingleEnvironmentKit(spec.Kit) {
		spec.Services = mergeServices(defaultBaseKitVerifiedServices(), spec.Services)
	}
	return spec, nil
}

func uiSpecServices(dataMap map[string]interface{}) []core.ServiceSpec {
	var services []core.ServiceSpec
	if goals, ok := dataMap["goals"].(map[string]interface{}); ok {
		services = mapGoalsToServices(goals)
	}
	if servicesRaw, ok := dataMap["services"].([]interface{}); ok && len(servicesRaw) > 0 {
		services = mapServicesArrayToSpec(servicesRaw)
	}
	if options, ok := dataMap["options"].(map[string]interface{}); ok {
		if optServices := mapOptionsToServices(options); len(optServices) > 0 {
			services = mergeServices(services, optServices)
		}
	}
	return services
}

func uiSpecNetwork(dataMap map[string]interface{}) core.NetworkSpec {
	var network core.NetworkSpec
	if networkMap, ok := dataMap["network"].(map[string]interface{}); ok {
		if accessMode, ok := networkMap["accessMode"].(string); ok {
			network = mapAccessModeToNetwork(accessMode)
		}
	}
	if options, ok := dataMap["options"].(map[string]interface{}); ok {
		if vpn, ok := options["vpn"].(string); ok && vpn != "" {
			network.VPN = vpn
		}
		if publicAccess, ok := options["public_access"].(bool); ok && publicAccess {
			network.Domain = uiNetworkDomainPublic
		}
	}
	return network
}

func uiSpecKit(dataMap map[string]interface{}) string {
	kit := ""
	if _, fromWizard := dataMap["wizardType"]; fromWizard || dataMap[providerField] != nil || dataMap["goals"] != nil {
		kit = DefaultBaseKitRef
	}
	if isManagedRuntimeUIConfig(dataMap) {
		kit = DefaultCloudKitRef
	}
	if explicitKit, ok := dataMap["kit"].(string); ok {
		kit = normalizeUIStackKitRef(explicitKit, kit)
	}
	return normalizeUIStackKitRef(kit, DefaultBaseKitRef)
}

func normalizeUIStackKitRef(value, defaultKit string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "base-kit":
		return defaultKit
	case "basement", "basementkit":
		return DefaultBaseKitRef
	case "cloud", "cloudkit", "kombify-cloud-kit":
		return DefaultCloudKitRef
	default:
		return strings.TrimSpace(value)
	}
}

func isManagedRuntimeUIConfig(dataMap map[string]interface{}) bool {
	if rawUsesManagedRuntimeMode(dataMap) {
		return true
	}
	for _, sectionName := range []string{"metadata", "options"} {
		if rawUsesManagedRuntimeMode(mapFromInterface(dataMap[sectionName])) {
			return true
		}
	}
	for _, rawNode := range interfaceSlice(dataMap["nodes"]) {
		if node := mapFromInterface(rawNode); rawStringExact(node, metadataKeyProviderID) != "" {
			return true
		}
	}
	return false
}

func authoritativeUIProviderID(dataMap map[string]interface{}) string {
	candidates := []string{rawStringExact(dataMap, metadataKeyProviderID)}
	for _, sectionName := range []string{"metadata", "options"} {
		candidates = append(candidates, rawStringExact(mapFromInterface(dataMap[sectionName]), metadataKeyProviderID))
	}
	for _, rawNode := range interfaceSlice(dataMap["nodes"]) {
		candidates = append(candidates, rawStringExact(mapFromInterface(rawNode), metadataKeyProviderID))
	}
	providerID, err := providercatalog.ResolveCanonicalProviderID(candidates...)
	if err != nil {
		return ""
	}
	return providerID
}

func uiSpecMetadata(dataMap map[string]interface{}, provider string, kit string) (map[string]string, error) {
	metadata := defaultUIMetadata(kit, provider)
	if options, ok := dataMap["options"].(map[string]interface{}); ok {
		applyUIMetadataOverrides(metadata, options)
	}
	if isManagedRuntimeUIConfig(dataMap) {
		return normalizeMonthlyRuntimeMetadata(metadata, providerIDFromProvider(provider))
	}
	return metadata, nil
}

func defaultUIMetadata(kit string, provider string) map[string]string {
	return map[string]string{
		metadataKeyServerMode:         serverModeFromProvider(provider),
		metadataKeyRuntimeLane:        runtimeLaneFromProvider(provider),
		metadataKeyProviderID:         providerIDFromProvider(provider),
		metadataKeySimulateLifecycle:  simulateLifecycleFromProvider(provider),
		metadataKeyDesiredState:       desiredStateRunning,
		metadataKeyBillingMode:        billingModeSubscription,
		metadataKeyBillingCadence:     billingCadenceFromProvider(provider),
		metadataKeyStackKitCatalogRef: firstNonEmpty(kit, DefaultBaseKitRef),
		metadataKeyVerificationStatus: verificationStatusPending,
	}
}

func applyUIMetadataOverrides(metadata map[string]string, options map[string]interface{}) {
	for _, key := range []string{
		metadataKeyServerMode,
		metadataKeyRuntimeLane,
		metadataKeyProviderID,
		metadataKeyProviderRegion,
		metadataKeyIONOSDatacenter,
		metadataKeySimulateLifecycle,
		metadataKeyDesiredState,
		metadataKeyBillingMode,
		metadataKeyBillingCadence,
		metadataKeyStackKitCatalogRef,
		metadataKeyVerificationStatus,
	} {
		if value := strings.TrimSpace(optionString(options, key)); value != "" {
			if key == metadataKeyStackKitCatalogRef {
				value = normalizeUIStackKitRef(value, metadata[metadataKeyStackKitCatalogRef])
			}
			metadata[key] = value
		}
	}
}

func convertStackKitConfigToSpec(dataMap map[string]interface{}) (*core.KombinationSpec, error) {
	rawKit := firstNonEmpty(stringFromMap(dataMap, "kit"), stringFromMap(dataMap, "stackkit"), DefaultBaseKitRef)
	spec := &core.KombinationSpec{
		Name:     sanitizeDNSName(stringFromMap(dataMap, "name")),
		Kit:      normalizeUIStackKitRef(rawKit, DefaultBaseKitRef),
		Metadata: stringMapFromAny(dataMap["metadata"]),
	}
	if spec.Metadata == nil {
		spec.Metadata = map[string]string{}
	}
	if spec.Metadata[metadataKeyStackKitCatalogRef] == "" {
		spec.Metadata[metadataKeyStackKitCatalogRef] = spec.Kit
	} else {
		spec.Metadata[metadataKeyStackKitCatalogRef] = normalizeUIStackKitRef(spec.Metadata[metadataKeyStackKitCatalogRef], spec.Kit)
	}
	applyStackKitServiceEnablementMetadata(spec.Metadata, dataMap["services"])

	provider := defaultProviderForStackKitSpec(dataMap, spec.Metadata)
	managed := isManagedRuntimeUIConfig(dataMap)
	if managed {
		spec.Kit = DefaultCloudKitRef
		spec.Metadata[metadataKeyStackKitCatalogRef] = DefaultCloudKitRef
	}
	spec.Nodes = stackKitNodesToKombinationNodes(dataMap, provider)
	spec.Services = stackKitServicesToKombinationServices(dataMap["services"])
	if len(spec.Services) == 0 && isDefaultSingleEnvironmentKit(spec.Kit) {
		spec.Services = defaultBaseKitVerifiedServices()
	}

	spec.Network = stackKitNetworkToKombinationNetwork(dataMap)
	if managed {
		metadata, err := normalizeMonthlyRuntimeMetadata(spec.Metadata, providerIDFromProvider(provider))
		if err != nil {
			return nil, err
		}
		spec.Metadata = metadata
	}
	return spec, nil
}

func stackKitNodesToKombinationNodes(dataMap map[string]interface{}, provider string) []core.NodeSpec {
	rawNodes, _ := dataMap["nodes"].([]interface{})
	nodes := make([]core.NodeSpec, 0, len(rawNodes))
	topLevelSSH, _ := dataMap["ssh"].(map[string]interface{})
	for _, raw := range rawNodes {
		nodeMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		node := core.NodeSpec{
			Name:     firstNonEmpty(stringFromMap(nodeMap, "name"), stackRoleMain),
			Type:     stackKitRoleToKombinationType(stringFromMap(nodeMap, "role")),
			Provider: firstNonEmpty(stringFromMap(nodeMap, providerField), provider),
		}
		ssh := stackKitSSHToKombinationSSH(topLevelSSH, nodeMap)
		if ssh != nil {
			node.SSH = ssh
		}
		nodes = append(nodes, node)
	}
	if len(nodes) == 0 {
		nodes = append(nodes, core.NodeSpec{
			Name:     stackRoleMain,
			Type:     stackRoleMain,
			Provider: provider,
			SSH:      stackKitSSHToKombinationSSH(topLevelSSH, nil),
		})
	}
	return nodes
}

func stackKitSSHToKombinationSSH(topLevelSSH, nodeMap map[string]interface{}) *core.SSHConfig {
	host := ""
	if nodeMap != nil {
		host = firstNonEmpty(stringFromMap(nodeMap, "host"), stringFromMap(nodeMap, "ip"))
		if nested, ok := nodeMap["ssh"].(map[string]interface{}); ok {
			host = firstNonEmpty(stringFromMap(nested, "host"), host)
			topLevelSSH = mergeStringInterfaceMaps(topLevelSSH, nested)
		}
	}
	if host == "" {
		return nil
	}
	ssh := &core.SSHConfig{
		Host: host,
		User: firstNonEmpty(stringFromMap(topLevelSSH, "user"), "root"),
		Port: intFromMap(topLevelSSH, "port"),
	}
	if ssh.Port == 0 {
		ssh.Port = 22
	}
	if keyPath := firstNonEmpty(stringFromMap(topLevelSSH, "key_path"), stringFromMap(topLevelSSH, "keyPath")); keyPath != "" {
		ssh.KeyPath = keyPath
	}
	return ssh
}

func stackKitServicesToKombinationServices(raw interface{}) []core.ServiceSpec {
	switch services := raw.(type) {
	case []interface{}:
		return mapServicesArrayToSpec(services)
	case map[string]interface{}:
		out := make([]core.ServiceSpec, 0, len(services))
		for rawName, value := range services {
			name := canonicalServiceName(rawName)
			if name == "" || !stackKitServiceEnabled(value) {
				continue
			}
			out = append(out, core.ServiceSpec{
				Name: name,
				Type: stackKitServiceType(name, value),
				Node: stackRoleMain,
			})
		}
		return mergeServices(out)
	default:
		return nil
	}
}

func stackKitServiceEnabled(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case map[string]interface{}:
		if enabled, ok := v["enabled"].(bool); ok {
			return enabled
		}
		return true
	default:
		return true
	}
}

func stackKitServiceType(name string, value interface{}) string {
	if m, ok := value.(map[string]interface{}); ok {
		if typ := firstNonEmpty(stringFromMap(m, "type"), stringFromMap(m, "kind")); typ != "" {
			return typ
		}
	}
	return mapServiceNameToType(name)
}

func applyStackKitServiceEnablementMetadata(metadata map[string]string, raw interface{}) {
	if metadata == nil {
		return
	}
	services, ok := raw.(map[string]interface{})
	if !ok {
		return
	}
	for rawName, value := range services {
		enableVar := stackKitServiceEnableVar(canonicalServiceName(rawName))
		if enableVar == "" {
			continue
		}
		metadata[enableVar] = strconv.FormatBool(stackKitServiceEnabled(value))
	}
}

func stackKitServiceEnableVar(name string) string {
	switch canonicalServiceName(name) {
	case "base", "dashboard":
		return "enable_dashboard"
	case "homepage":
		return "enable_homepage"
	case "traefik":
		return "enable_traefik"
	case "tinyauth", "auth":
		return "enable_tinyauth"
	case serviceNamePocketID, identityHeadPocketID, "id":
		return "enable_pocketid"
	case serviceNameUptimeKuma, "uptime":
		return "enable_uptime_kuma"
	case "whoami":
		return "enable_whoami"
	case serviceNameVaultwarden, "vault":
		return "enable_vaultwarden"
	case serviceNameImmich, serviceNameImmichServer, serviceNameImmichML, serviceNameImmichDB, serviceNameImmichCache:
		return "enable_immich"
	case "files", "cloudreve", "nextcloud":
		return "enable_files"
	case "jellyfin":
		return "enable_jellyfin"
	case "dokploy":
		return "enable_dokploy"
	case "komodo":
		return "enable_komodo"
	case "dockge":
		return "enable_dockge"
	case "kombify-point", "point":
		return "enable_kombify_point"
	case "coolify":
		return "enable_coolify"
	default:
		return ""
	}
}

//nolint:goconst // StackKit network values are external wire values.
func stackKitNetworkToKombinationNetwork(dataMap map[string]interface{}) core.NetworkSpec {
	network := core.NetworkSpec{Domain: stringFromMap(dataMap, "domain")}
	metadata := stringMapFromAny(dataMap["metadata"])
	if vpn, ok := dataMap["vpn"].(map[string]interface{}); ok {
		if enabled, present := vpn["enabled"].(bool); present && !enabled {
			network.VPN = "none"
		} else if typ := stringFromMap(vpn, "type"); typ != "" {
			network.VPN = typ
		}
	}
	if networkMap, ok := dataMap["network"].(map[string]interface{}); ok {
		if vpn := stringFromMap(networkMap, "vpn"); vpn != "" {
			network.VPN = vpn
		}
		if domain := stringFromMap(networkMap, "domain"); domain != "" {
			network.Domain = domain
		}
		if network.Domain == "" {
			if mode := stringFromMap(networkMap, "mode"); networkModeAsLegacyDomain(mode) {
				network.Domain = mode
			}
		}
	}
	if network.VPN == "" && stackKitAddonsInclude(dataMap["addons"], "vpn-overlay") {
		network.VPN = "headscale"
	}
	if network.Domain == "" &&
		strings.EqualFold(strings.TrimSpace(metadata[metadataKeyAddressMode]), addressModeKombifyMe) &&
		strings.EqualFold(strings.TrimSpace(stringFromMap(dataMap, "context")), "cloud") {
		network.Domain = "kombify.me"
	}
	return network
}

func networkModeAsLegacyDomain(mode string) bool {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "", "local", "public", "hybrid":
		return false
	default:
		return strings.Contains(normalized, ".")
	}
}

func stackKitRoleToKombinationType(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "standalone", "control-plane":
		return stackRoleMain
	default:
		return role
	}
}

func defaultProviderForStackKitSpec(dataMap map[string]interface{}, metadata map[string]string) string {
	if provider := authoritativeUIProviderID(dataMap); provider != "" {
		return provider
	}
	if provider := metadata[metadataKeyProviderID]; provider != "" {
		return provider
	}
	for _, raw := range interfaceSlice(dataMap["nodes"]) {
		if provider := stringFromMap(mapFromInterface(raw), providerField); provider != "" && normalizeProvider(provider) != providerLocal {
			return provider
		}
	}
	return ""
}

func stringMapFromAny(value interface{}) map[string]string {
	switch v := value.(type) {
	case map[string]string:
		out := make(map[string]string, len(v))
		for key, item := range v {
			out[key] = item
		}
		return out
	case map[string]interface{}:
		out := make(map[string]string, len(v))
		for key, item := range v {
			// Runtime metadata can contain nested telemetry arrays (for example
			// the provider's `host` observation). Those are not scalar metadata
			// and must never become an SSH hostname through fmt.Sprint.
			if s := metricStringFromInterface(item); s != "" {
				out[key] = s
			}
		}
		return out
	default:
		return nil
	}
}

func mergeStringInterfaceMaps(base, overlay map[string]interface{}) map[string]interface{} {
	if base == nil {
		return overlay
	}
	if overlay == nil {
		return base
	}
	out := make(map[string]interface{}, len(base)+len(overlay))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range overlay {
		out[key] = value
	}
	return out
}

func stackKitAddonsInclude(value interface{}, want string) bool {
	items, ok := value.([]interface{})
	if !ok {
		return false
	}
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(item)), want) {
			return true
		}
	}
	return false
}

func stringFromMap(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func intFromMap(m map[string]interface{}, key string) int {
	if m == nil {
		return 0
	}
	switch value := m[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		n, _ := value.Int64()
		return int(n)
	default:
		return 0
	}
}

// mapServicesArrayToSpec converts a string array of service names to ServiceSpec.
func mapServicesArrayToSpec(services []interface{}) []core.ServiceSpec {
	var specs []core.ServiceSpec
	seen := map[string]bool{}
	for _, s := range services {
		name, ok := s.(string)
		if !ok || name == "" {
			continue
		}
		name = canonicalServiceName(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		specs = append(specs, core.ServiceSpec{
			Name: name,
			Type: mapServiceNameToType(name),
		})
	}
	return specs
}

// mapOptionsToServices converts options map to ServiceSpecs.
// IMPORTANT: Types must match CUE schema #ServiceType!
func mapOptionsToServices(options map[string]interface{}) []core.ServiceSpec {
	var services []core.ServiceSpec

	addService := func(name string) {
		for _, svc := range services {
			if svc.Name == name {
				return
			}
		}
		services = append(services, core.ServiceSpec{Name: name, Type: mapServiceNameToType(name)})
	}

	if enabled, ok := options["enable_traefik"].(bool); ok && enabled {
		services = append(services, core.ServiceSpec{Name: "traefik", Type: "reverse-proxy"})
	}

	identityHead := normalizeIdentityHead(optionString(options, "identity_head"))
	enablePocketBaseBackend := optionBool(options, "enable_pocketbase_backend") || optionBool(options, "enable_pocketbase")
	enablePocketID := optionBool(options, "enable_pocket_id") || identityHead == identityHeadPocketID
	requiresPasskeys := optionBool(options, "requires_passkeys")

	if enablePocketBaseBackend || identityHead == identityHeadPocketBase {
		addService(identityHeadPocketBase)
	}
	if enablePocketID || (requiresPasskeys && (enablePocketBaseBackend || identityHead == identityHeadPocketBase)) {
		addService(serviceNamePocketID)
	}
	if enabled, ok := options["enable_headscale"].(bool); ok && enabled {
		addService("headscale")
	}
	if enabled, ok := options["enable_monitoring"].(bool); ok && enabled {
		addService(serviceNameOTelCollector)
	}

	return services
}

func optionBool(options map[string]interface{}, key string) bool {
	enabled, ok := options[key].(bool)
	return ok && enabled
}

func optionString(options map[string]interface{}, key string) string {
	value, ok := options[key].(string)
	if !ok {
		return ""
	}
	return value
}

func normalizeIdentityHead(identityHead string) string {
	normalized := strings.ToLower(strings.TrimSpace(identityHead))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case "pocketid", identityHeadPocketID:
		return identityHeadPocketID
	case identityHeadPocketBase, "pocket_base":
		return identityHeadPocketBase
	default:
		return normalized
	}
}

// mapServiceNameToType maps common service names to their types.
//
//nolint:goconst // Service aliases are external StackKit/import compatibility names.
func mapServiceNameToType(name string) string {
	// IMPORTANT: Types must match CUE schema #ServiceType.
	// Keep this in sync with `pkg/unifier/schema/*.cue`.
	name = canonicalServiceName(name)
	typeMap := map[string]string{
		// Reverse proxies
		serviceNameTraefik: serviceTypeReverseProxy,
		"nginx":            serviceTypeReverseProxy,
		"caddy":            serviceTypeReverseProxy,
		// Databases
		"postgres": serviceTypeDatabase,
		"mysql":    serviceTypeDatabase,
		"mariadb":  serviceTypeDatabase,
		"mongodb":  serviceTypeDatabase,
		"redis":    serviceTypeDatabase,
		// PaaS platforms
		"caprover":  "paas",
		"coolify":   "paas",
		"dokku":     "paas",
		"portainer": "paas",
		// Monitoring
		serviceNameOTelCollector: serviceTypeMonitoring,
		"otel-gateway":           serviceTypeMonitoring,
		"victoriametrics":        serviceTypeMonitoring,
		"grafana":                serviceTypeMonitoring,
		"loki":                   serviceTypeMonitoring,
		"monitoring":             serviceTypeMonitoring,
		serviceNameUptimeKuma:    serviceTypeMonitoring,
		"uptime":                 serviceTypeMonitoring,
		// Legacy compatibility names accepted for imported specs.
		"prometheus": serviceTypeMonitoring,
		// Storage
		serviceNameNextcloud:    serviceTypeStorage,
		"minio":                 serviceTypeStorage,
		"seafile":               serviceTypeStorage,
		serviceNameVaultwarden:  serviceTypeAuth,
		serviceNameImmich:       serviceTypeMedia,
		serviceNameImmichServer: serviceTypeMedia,
		serviceNameImmichML:     serviceTypeMedia,
		serviceNameImmichDB:     serviceTypeDatabase,
		serviceNameImmichCache:  serviceTypeCache,
		// Auth
		"keycloak":           serviceTypeAuth,
		"authentik":          serviceTypeAuth,
		"authelia":           serviceTypeAuth,
		serviceNamePocketID:  serviceTypeAuth,
		identityHeadPocketID: serviceTypeAuth,
		"pocketid":           serviceTypeAuth,
		"tinyauth":           serviceTypeAuth,
		// Backup
		"restic": "backup",
		"borg":   "backup",
		// Backend services
		identityHeadPocketBase: "backend",
		// VPN overlay
		"headscale": "vpn",
		"tailscale": "vpn",
		"jellyfin":  "custom",
		"plex":      "custom",
	}
	if t, ok := typeMap[name]; ok {
		return t
	}
	return "service"
}

func canonicalServiceName(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "", "-":
		return ""
	case "monitoring", "otlp", "opentelemetry-collector":
		return serviceNameOTelCollector
	case "pocketid":
		return serviceNamePocketID
	case "pocket-base":
		return identityHeadPocketBase
	default:
		return normalized
	}
}

// nonDNSChars matches characters that are not valid in DNS names.
var nonDNSChars = regexp.MustCompile(`[^a-z0-9-]+`)

// multiDash matches multiple consecutive dashes.
var multiDash = regexp.MustCompile(`-+`)

// sanitizeDNSName sanitizes a name to DNS-safe format (RFC 1123 subdomain).
func sanitizeDNSName(name string) string {
	trimmed := strings.TrimSpace(strings.ToLower(name))
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	trimmed = strings.ReplaceAll(trimmed, " ", "-")
	trimmed = nonDNSChars.ReplaceAllString(trimmed, "-")
	trimmed = multiDash.ReplaceAllString(trimmed, "-")
	trimmed = strings.Trim(trimmed, "-")
	if trimmed == "" {
		return defaultUIStackName
	}
	// Must start with a letter for our schema (RFC 1123 subdomain)
	if trimmed[0] < 'a' || trimmed[0] > 'z' {
		trimmed = "ks-" + trimmed
	}
	// Max 63 chars for DNS label
	if len(trimmed) > 63 {
		trimmed = trimmed[:63]
		trimmed = strings.Trim(trimmed, "-")
	}
	if trimmed == "" {
		return defaultUIStackName
	}
	return trimmed
}

// mapWizardProviderToUnifier maps wizard provider labels to Unifier-compatible providers.
//
//nolint:goconst // Wizard provider labels are external UI wire values.
func mapWizardProviderToUnifier(provider string) string {
	switch provider {
	case "homelab":
		// Legacy UI/provider label: treat as local/on-prem.
		return "local"
	case "cloud":
		return "cloud"
	case "local":
		return "local"
	default:
		// If the UI already provides a concrete provider, pass through.
		return provider
	}
}

// mapGoalsToServices converts wizard goals to service definitions.
// IMPORTANT: Types must match CUE schema #ServiceType!
//
//nolint:goconst // Goal keys are external UI wire values.
func mapGoalsToServices(goals map[string]interface{}) []core.ServiceSpec {
	var services []core.ServiceSpec

	// Website/Email goal
	if websiteEmail, ok := goals["websiteEmail"].(bool); ok && websiteEmail {
		services = append(services,
			core.ServiceSpec{Name: "traefik", Type: "reverse-proxy"},
		)
	}

	// Storage goal
	if storage, ok := goals["storage"].(bool); ok && storage {
		services = append(services, defaultBaseKitVerifiedServices()...)
	}

	// Everything goal - add all standard services
	if everything, ok := goals["everything"].(bool); ok && everything {
		services = mergeServices(defaultBaseKitVerifiedServices(), []core.ServiceSpec{{Name: "otel-collector", Type: serviceTypeMonitoring}})
	}

	return services
}

func defaultBaseKitVerifiedServices() []core.ServiceSpec {
	return []core.ServiceSpec{
		{Name: serviceNameTraefik, Type: serviceTypeReverseProxy},
		{Name: serviceNamePocketID, Type: serviceTypeAuth},
		{Name: serviceNameVaultwarden, Type: serviceTypeAuth},
		{Name: serviceNameImmichServer, Type: serviceTypeMedia},
		{Name: serviceNameImmichML, Type: serviceTypeMedia},
		{Name: serviceNameImmichDB, Type: serviceTypeDatabase},
		{Name: serviceNameImmichCache, Type: serviceTypeCache},
		{Name: serviceNameOTelCollector, Type: serviceTypeMonitoring},
	}
}

func isDefaultSingleEnvironmentKit(kit string) bool {
	switch strings.TrimSpace(kit) {
	case DefaultBaseKitRef, DefaultCloudKitRef:
		return true
	default:
		return false
	}
}

func mergeServices(groups ...[]core.ServiceSpec) []core.ServiceSpec {
	var out []core.ServiceSpec
	seen := map[string]bool{}
	for _, group := range groups {
		for _, svc := range group {
			if svc.Name == "" || seen[svc.Name] {
				continue
			}
			seen[svc.Name] = true
			if svc.Type == "" {
				svc.Type = mapServiceNameToType(svc.Name)
			}
			out = append(out, svc)
		}
	}
	return out
}

func serverModeFromProvider(provider string) string {
	if providercatalog.IsCanonicalProviderID(provider) {
		return serverModeMonthlyRuntime
	}
	return serverModeUserOwned
}

func runtimeLaneFromProvider(provider string) string {
	if providercatalog.IsCanonicalProviderID(provider) {
		return serverModeMonthlyRuntime
	}
	return ""
}

func simulateLifecycleFromProvider(provider string) string {
	if providercatalog.IsCanonicalProviderID(provider) {
		return simulateLifecyclePVM
	}
	return ""
}

func billingCadenceFromProvider(provider string) string {
	if providercatalog.IsCanonicalProviderID(provider) {
		return billingCadenceMonthly
	}
	return ""
}

func providerIDFromProvider(provider string) string {
	canonical, err := providercatalog.CanonicalProviderID(provider)
	if err != nil {
		return ""
	}
	return canonical
}

// mapAccessModeToNetwork converts wizard access mode to network config.
//
//nolint:goconst // Access modes are external UI wire values.
func mapAccessModeToNetwork(accessMode string) core.NetworkSpec {
	switch accessMode {
	case "local":
		return core.NetworkSpec{
			VPN: "none",
		}
	case "anywhere":
		return core.NetworkSpec{
			VPN:    "tailscale",
			Domain: "ts.net",
		}
	case "public":
		return core.NetworkSpec{
			VPN:    "none",
			Domain: "", // Will be configured later
		}
	default:
		return core.NetworkSpec{
			VPN:    "tailscale",
			Domain: "local",
		}
	}
}

// generateRegistrationToken creates a cryptographically secure token for worker enrollment.
// Returns a token in format: ks_<64-hex-chars> (32 bytes of randomness).
func generateRegistrationToken(_ string) string {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		// This should never happen with crypto/rand, but handle gracefully
		// by returning an empty token which will fail validation
		return ""
	}
	return fmt.Sprintf("ks_%x", bytes)
}
