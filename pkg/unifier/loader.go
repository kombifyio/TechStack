// Package unifier provides the YAML loader for kombination.yaml files.
package unifier

import (
	"fmt"
	"maps"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/pkg/core"
)

const vpnTypeNone = "none"

// Loader handles parsing and loading of kombination.yaml files.
type Loader struct{}

// NewLoader creates a new Loader instance.
func NewLoader() *Loader {
	return &Loader{}
}

// LoadFile reads and parses a kombination.yaml file from disk.
func (l *Loader) LoadFile(path string) (*core.KombinationSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	return l.LoadBytes(data)
}

// LoadInputFile reads and parses a kombination.yaml file from disk into an InputSpec.
func (l *Loader) LoadInputFile(path string) (*core.InputSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	return l.LoadInputBytes(data)
}

// LoadBytes parses YAML bytes into a KombinationSpec.
func (l *Loader) LoadBytes(data []byte) (*core.KombinationSpec, error) {
	input, err := l.LoadInputBytes(data)
	if err != nil {
		return nil, err
	}

	spec := NormalizeInputSpec(input)

	// Basic structural validation before CUE validation
	if err := l.preValidate(spec); err != nil {
		return nil, err
	}

	return spec, nil
}

// LoadInputBytes parses YAML/JSON bytes into an InputSpec.
//
// We intentionally parse via YAML, because YAML is a superset of JSON.
func (l *Loader) LoadInputBytes(data []byte) (*core.InputSpec, error) {
	var input core.InputSpec

	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	if err := l.preValidateInput(&input); err != nil {
		return nil, err
	}

	return &input, nil
}

// LoadInputEnvelopeBytes parses either a raw StackSpec/kombination input or an
// envelope shaped as {spec, decision_context}. The envelope form is used by
// preview clients that can supply Environment Inventory and Operator capability
// evidence without writing those derived facts into the user's intent file.
func (l *Loader) LoadInputEnvelopeBytes(data []byte) (*core.InputSpec, *core.DecisionContext, bool, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil, false, fmt.Errorf("failed to parse YAML: %w", err)
	}
	document := &root
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		document = root.Content[0]
	}
	if document.Kind != yaml.MappingNode {
		input, err := l.LoadInputBytes(data)
		return input, nil, false, err
	}

	var specNode *yaml.Node
	var decisionNode *yaml.Node
	for i := 0; i+1 < len(document.Content); i += 2 {
		switch document.Content[i].Value {
		case "spec":
			specNode = document.Content[i+1]
		case "decision_context", "decisionContext":
			decisionNode = document.Content[i+1]
		}
	}
	if specNode == nil {
		input, err := l.LoadInputBytes(data)
		return input, nil, false, err
	}

	var input core.InputSpec
	if specNode.Kind == yaml.ScalarNode {
		parsed, err := l.LoadInputBytes([]byte(specNode.Value))
		if err != nil {
			return nil, nil, true, fmt.Errorf("failed to parse envelope spec: %w", err)
		}
		input = *parsed
	} else if err := specNode.Decode(&input); err != nil {
		return nil, nil, true, fmt.Errorf("failed to parse envelope spec: %w", err)
	}
	if err := l.preValidateInput(&input); err != nil {
		return nil, nil, true, err
	}
	var decisionContext *core.DecisionContext
	if decisionNode != nil {
		var decoded core.DecisionContext
		if err := decisionNode.Decode(&decoded); err != nil {
			return nil, nil, true, fmt.Errorf("failed to parse decision_context: %w", err)
		}
		decisionContext = &decoded
	}
	return &input, decisionContext, true, nil
}

// NormalizeInputSpec converts raw/partial user input to the internal KombinationSpec.
// Missing fields are left as their zero-values; defaults are applied later in the pipeline/unifier.
//
//nolint:goconst // Loader accepts external StackKit/import wire values.
func NormalizeInputSpec(input *core.InputSpec) *core.KombinationSpec {
	if input == nil {
		return nil
	}

	metadata := maps.Clone(input.Metadata)
	if metadata == nil {
		metadata = make(map[string]string)
	}
	applyInputMetadata(metadata, input)

	spec := &core.KombinationSpec{
		Metadata: metadata,
		Nodes:    normalizeInputNodes(input),
		Services: normalizeInputServices(input.Services),
	}
	if input.Name != nil {
		spec.Name = *input.Name
	}
	if input.Version != nil {
		spec.Version = *input.Version
	}
	if input.Kit != nil {
		spec.Kit = *input.Kit
	} else if input.StackKit != nil {
		spec.Kit = *input.StackKit
	}

	spec.Network = normalizeInputNetwork(input)

	return spec
}

func applyInputMetadata(metadata map[string]string, input *core.InputSpec) {
	if input == nil {
		return
	}
	setIfPresent := func(key string, value *string) {
		if value != nil && *value != "" && metadata[key] == "" {
			metadata[key] = *value
		}
	}
	setIfPresent("stackkit_mode", input.Mode)
	setIfPresent("runtime", input.Runtime)
	setIfPresent("context", input.Context)
	setIfPresent("paas", input.PAAS)
	setIfPresent("subdomain_prefix", input.SubdomainPrefix)
	setIfPresent("mail_address", input.AdminEmail)
	setIfPresent("mail_address", input.Email)
	if metadata["mail_address"] != "" && metadata["mail_address_present"] == "" {
		metadata["mail_address_present"] = metadataValueTrue
	}
	if input.Compute != nil {
		setIfPresent("compute_tier", input.Compute.Tier)
	}
}

func normalizeInputNetwork(input *core.InputSpec) core.NetworkSpec {
	network := core.NetworkSpec{}
	if input.Network != nil {
		network.VPN = valueOrZero(input.Network.VPN)
		if input.Network.Domain != nil {
			network.Domain = *input.Network.Domain
		} else if input.Domain != nil {
			network.Domain = *input.Domain
		} else if input.Network.Mode != nil && networkModeDomainFallback(*input.Network.Mode) {
			network.Domain = *input.Network.Mode
		}
	} else if input.Domain != nil {
		network.Domain = *input.Domain
	}
	if input.VPN != nil {
		if input.VPN.Type != nil {
			network.VPN = *input.VPN.Type
		}
		if input.VPN.Enabled != nil && !*input.VPN.Enabled {
			network.VPN = vpnTypeNone
		}
	}
	return network
}

func networkModeDomainFallback(mode string) bool {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "", "local", "public", "hybrid":
		return false
	default:
		return strings.Contains(normalized, ".")
	}
}

func normalizeInputNodes(input *core.InputSpec) []core.NodeSpec {
	if len(input.Nodes) == 0 {
		return nil
	}
	nodes := make([]core.NodeSpec, 0, len(input.Nodes))
	for _, inputNode := range input.Nodes {
		nodes = append(nodes, normalizeInputNode(input, inputNode))
	}
	return nodes
}

func normalizeInputNode(input *core.InputSpec, inputNode core.InputNodeSpec) core.NodeSpec {
	node := core.NodeSpec{
		Name:     valueOrZero(inputNode.Name),
		Provider: defaultInputProvider(input),
		SSH:      normalizeInputNodeSSH(input, inputNode),
		Tags:     maps.Clone(inputNode.Tags),
	}
	if inputNode.Type != nil {
		node.Type = *inputNode.Type
	} else if inputNode.Role != nil {
		node.Type = stackSpecRoleToKombinationType(*inputNode.Role)
	}
	if inputNode.Provider != nil {
		node.Provider = *inputNode.Provider
	}
	return node
}

func normalizeInputNodeSSH(input *core.InputSpec, node core.InputNodeSpec) *core.SSHConfig {
	if node.SSH != nil {
		return &core.SSHConfig{
			Host:    valueOrZero(node.SSH.Host),
			Port:    valueOrZero(node.SSH.Port),
			User:    valueOrZero(node.SSH.User),
			KeyPath: valueOrZero(node.SSH.KeyPath),
		}
	}
	if input.SSH == nil && node.IP == nil && node.Host == nil {
		return nil
	}
	ssh := &core.SSHConfig{Host: inputNodeHost(node)}
	if input.SSH != nil {
		if input.SSH.Host != nil && ssh.Host == "" {
			ssh.Host = *input.SSH.Host
		}
		ssh.Port = valueOrZero(input.SSH.Port)
		ssh.User = valueOrZero(input.SSH.User)
		ssh.KeyPath = valueOrZero(input.SSH.KeyPath)
	}
	if ssh.Host == "" {
		return nil
	}
	return ssh
}

func inputNodeHost(node core.InputNodeSpec) string {
	if node.Host != nil {
		return *node.Host
	}
	if node.IP != nil {
		return *node.IP
	}
	return ""
}

func normalizeInputServices(inputServices core.InputServiceSpecs) []core.ServiceSpec {
	if len(inputServices) == 0 {
		return nil
	}
	services := make([]core.ServiceSpec, 0, len(inputServices))
	for _, inputService := range inputServices {
		name := canonicalInputServiceName(valueOrZero(inputService.Name))
		if name == "" {
			continue
		}
		services = append(services, core.ServiceSpec{
			Name:  name,
			Type:  inputServiceType(name, valueOrZero(inputService.Type)),
			Node:  valueOrZero(inputService.Node),
			Needs: append([]string(nil), inputService.Needs...),
		})
	}
	return services
}

// canonicalInputServiceName keeps StackKits service-map aliases DNS-safe before
// the pipeline's CUE validation runs.
//
//nolint:goconst // StackKit service names are external wire values.
func canonicalInputServiceName(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "", "-":
		return ""
	case "monitoring", "otlp", "opentelemetry-collector":
		return "otel-collector"
	case "pocketid":
		return "pocket-id"
	case "pocket-base":
		return "pocketbase"
	default:
		return normalized
	}
}

//nolint:goconst // StackKit service names and service types are external wire values.
func inputServiceType(name, explicitType string) string {
	if strings.TrimSpace(explicitType) != "" {
		return explicitType
	}
	switch canonicalInputServiceName(name) {
	case "traefik", "nginx", "caddy":
		return "reverse-proxy"
	case "postgres", "mysql", "mariadb", "mongodb", "immich-postgres":
		return "database"
	case "redis", "immich-redis":
		return "cache"
	case "dokploy", "coolify", "caprover", "dokku", "portainer":
		return "paas"
	case "otel-collector", "otel-gateway", "victoriametrics", "grafana", "loki", "prometheus", "uptime-kuma":
		return "monitoring"
	case "nextcloud", "minio", "seafile", "files":
		return "storage"
	case "vaultwarden", "pocket-id", "keycloak", "authentik", "authelia", "tinyauth":
		return "auth"
	case "headscale", "tailscale":
		return "vpn"
	case "immich", "immich-server", "immich-ml", "jellyfin", "plex":
		return "media"
	default:
		return "service"
	}
}

func valueOrZero[T any](value *T) (zero T) {
	if value != nil {
		return *value
	}
	return zero
}

//nolint:goconst // StackKit role names are external wire values.
func stackSpecRoleToKombinationType(role string) string {
	switch role {
	case "", "standalone", "control-plane":
		return "main"
	default:
		return role
	}
}

//nolint:goconst // Provider names are external spec wire values.
func defaultInputProvider(input *core.InputSpec) string {
	if input == nil {
		return "local"
	}
	if input.Metadata != nil {
		if provider := input.Metadata["provider_id"]; provider != "" {
			return provider
		}
	}
	return "local"
}

// preValidate performs basic structural checks before CUE validation.
// This catches obvious errors early with clear messages.
func (l *Loader) preValidate(spec *core.KombinationSpec) error {
	// IMPORTANT: This validates RAW user input.
	// The Easy-Path allows partial/empty specs; defaults are applied later in the pipeline/unifier.
	// Therefore: no required-field checks here.

	// Check for duplicate node names (only if nodes are provided)
	nodeNames := make(map[string]bool)
	for _, node := range spec.Nodes {
		if node.Name == "" {
			continue
		}
		if nodeNames[node.Name] {
			return fmt.Errorf("duplicate node name: %s", node.Name)
		}
		nodeNames[node.Name] = true
	}

	// Check for duplicate service names (only if names are provided)
	serviceNames := make(map[string]bool)
	for _, svc := range spec.Services {
		if svc.Name == "" {
			continue
		}
		if serviceNames[svc.Name] {
			return fmt.Errorf("duplicate service name: %s", svc.Name)
		}
		serviceNames[svc.Name] = true
	}

	// Validate service node references (only if service.node is set)
	for i, svc := range spec.Services {
		if svc.Node != "" && !nodeNames[svc.Node] {
			return fmt.Errorf("spec.services[%d].node references unknown node: %s", i, svc.Node)
		}
	}

	return nil
}

func (l *Loader) preValidateInput(input *core.InputSpec) error {
	if input == nil {
		return fmt.Errorf("input cannot be nil")
	}
	managed := false
	providerID := ""
	if input.Metadata != nil {
		for _, key := range []string{"lease_provider", "simulate_provider_id"} {
			if input.Metadata[key] != "" {
				return fmt.Errorf("spec.metadata.%s is no longer accepted; use canonical provider_id", key)
			}
		}
		providerID = input.Metadata[providercatalog.ProviderIDField]
		managed = managed || providerID != "" ||
			input.Metadata["server_provisioning_mode"] == "kombify-cloud" ||
			input.Metadata["server_mode"] == "monthly-runtime" ||
			input.Metadata["server_mode"] == "managed-cloud" ||
			input.Metadata["runtime_lane"] == "monthly-runtime"
	}
	if managed {
		canonical, err := providercatalog.CanonicalProviderID(providerID)
		if err != nil {
			return fmt.Errorf("spec.metadata.provider_id: %w", err)
		}
		providerID = canonical
	}
	for i, node := range input.Nodes {
		if node.Provider == nil {
			continue
		}
		provider := *node.Provider
		normalized := strings.ToLower(strings.TrimSpace(provider))
		if normalized == "centron-managed" || normalized == "ionos-managed" ||
			((normalized == providercatalog.ProviderCentron || normalized == providercatalog.ProviderIONOS) && provider != normalized) {
			_, err := providercatalog.CanonicalProviderID(provider)
			return fmt.Errorf("spec.nodes[%d].provider: %w", i, err)
		}
		if managed && provider != "" {
			canonical, err := providercatalog.CanonicalProviderID(provider)
			if err != nil {
				return fmt.Errorf("spec.nodes[%d].provider: %w", i, err)
			}
			if canonical != providerID {
				return fmt.Errorf("spec.nodes[%d].provider: %w", i, providercatalog.ErrConflictingProviderIDs)
			}
		}
	}

	// Check for duplicate node names (only if provided)
	nodeNames := make(map[string]bool)
	for _, node := range input.Nodes {
		if node.Name == nil || *node.Name == "" {
			continue
		}
		if nodeNames[*node.Name] {
			return fmt.Errorf("duplicate node name: %s", *node.Name)
		}
		nodeNames[*node.Name] = true
	}

	// Check for duplicate service names (only if provided)
	serviceNames := make(map[string]bool)
	for _, svc := range input.Services {
		if svc.Name == nil || *svc.Name == "" {
			continue
		}
		if serviceNames[*svc.Name] {
			return fmt.Errorf("duplicate service name: %s", *svc.Name)
		}
		serviceNames[*svc.Name] = true
	}

	// Validate service node references (only if node is set)
	for i, svc := range input.Services {
		if svc.Node == nil || *svc.Node == "" {
			continue
		}
		if !nodeNames[*svc.Node] {
			return fmt.Errorf("spec.services[%d].node references unknown node: %s", i, *svc.Node)
		}
	}

	return nil
}

// ToYAML serializes a KombinationSpec back to YAML bytes.
func (l *Loader) ToYAML(spec *core.KombinationSpec) ([]byte, error) {
	data, err := yaml.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize to YAML: %w", err)
	}
	return data, nil
}

// SaveFile writes a KombinationSpec to a YAML file.
func (l *Loader) SaveFile(spec *core.KombinationSpec, path string) error {
	data, err := l.ToYAML(spec)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write file %s: %w", path, err)
	}

	return nil
}

// LoadIntentFile reads and parses a kombination.yaml file as an IntentSpec.
// It returns the spec and a SHA256 hash of the file contents for traceability.
// The IntentSpec is the immutable user intent - it should never be modified.
func (l *Loader) LoadIntentFile(path string) (*core.IntentSpec, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read intent file %s: %w", path, err)
	}

	return l.LoadIntentBytes(data)
}

// LoadIntentBytes parses YAML bytes into an IntentSpec with hash.
func (l *Loader) LoadIntentBytes(data []byte) (*core.IntentSpec, string, error) {
	var intent core.IntentSpec

	if err := yaml.Unmarshal(data, &intent); err != nil {
		return nil, "", fmt.Errorf("failed to parse intent YAML: %w", err)
	}

	// Compute hash for traceability
	hash := ComputeDataHash(data)

	// Set defaults for API version and kind
	if intent.APIVersion == "" {
		intent.APIVersion = unifierAPIVersion
	}
	if intent.Kind == "" {
		intent.Kind = "IntentSpec"
	}

	// Validate required fields
	if intent.Name == "" {
		return nil, "", fmt.Errorf("intent: name is required")
	}

	return &intent, hash, nil
}

// ConvertIntentToKombination converts an IntentSpec to a KombinationSpec.
// This is needed for backward compatibility with the existing pipeline.
func (l *Loader) ConvertIntentToKombination(intent *core.IntentSpec) *core.KombinationSpec {
	if intent == nil {
		return nil
	}

	spec := &core.KombinationSpec{
		Name:    intent.Name,
		Version: intent.Version,
		Kit:     intent.Kit,
	}

	// Convert overrides if present
	if intent.Overrides != nil {
		if intent.Overrides.Kit != "" {
			spec.Kit = intent.Overrides.Kit
		}
	}

	// Convert network settings
	if intent.Network != nil {
		spec.Network.VPN = intent.Network.VPN
	}

	return spec
}
