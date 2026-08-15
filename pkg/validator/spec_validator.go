// Package validator provides input validation for kombifyTechstack specifications.
package validator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/errors"
)

var (
	// nameRegex validates names (lowercase alphanumeric with hyphens)
	nameRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

	// domainRegex validates domain names
	domainRegex = regexp.MustCompile(`^([a-z0-9]+(-[a-z0-9]+)*\.)+[a-z]{2,}$`)
)

const (
	// MaxNameLength is the maximum length for names
	MaxNameLength = 63

	// MaxNodesCount is the maximum number of nodes
	MaxNodesCount = 100

	// MaxServicesCount is the maximum number of services
	MaxServicesCount = 200
)

// SpecValidator validates KombinationSpec specifications.
type SpecValidator struct{}

// NewSpecValidator creates a new spec validator.
func NewSpecValidator() *SpecValidator {
	return &SpecValidator{}
}

// Validate validates a KombinationSpec and returns any validation errors.
func (v *SpecValidator) Validate(spec *core.KombinationSpec) error {
	if spec == nil {
		return errors.NewValidationError("spec", "spec cannot be nil")
	}

	me := errors.NewMultiError()

	// Validate basic fields
	if err := v.validateName(spec.Name); err != nil {
		me.Add(err)
	}

	if err := v.validateKit(spec.Kit); err != nil {
		me.Add(err)
	}

	// Validate nodes
	if err := v.validateNodes(spec.Nodes); err != nil {
		me.Add(err)
	}

	// Validate services
	if err := v.validateServices(spec.Services, spec.Nodes); err != nil {
		me.Add(err)
	}

	// Validate network
	if err := v.validateNetwork(&spec.Network); err != nil {
		me.Add(err)
	}

	return me.ErrorOrNil()
}

// validateName validates the spec name.
func (v *SpecValidator) validateName(name string) error {
	if name == "" {
		return errors.NewValidationError("name", "name is required")
	}

	if len(name) > MaxNameLength {
		return errors.NewValidationErrorWithValue("name",
			fmt.Sprintf("name too long (max %d characters)", MaxNameLength), name)
	}

	if !nameRegex.MatchString(name) {
		return errors.NewValidationErrorWithValue("name",
			"name must contain only lowercase letters, numbers, and hyphens, and start/end with alphanumeric", name)
	}

	return nil
}

// validateKit validates the StackKit reference.
func (v *SpecValidator) validateKit(kit string) error {
	// Kit is optional: empty string means "auto-select" by Unifier.
	if kit == "" {
		return nil
	}

	// Prevent path traversal / filesystem injection. The StackKit name must be a simple
	// DNS-ish identifier (same constraints as other names) and must not contain path separators.
	if strings.ContainsAny(kit, `/\\`) {
		return errors.NewValidationErrorWithValue("kit", "kit must not contain path separators", kit)
	}

	// Reuse the existing name regex constraints (lowercase alphanumeric with hyphens).
	if !nameRegex.MatchString(kit) {
		return errors.NewValidationErrorWithValue("kit",
			"kit must contain only lowercase letters, numbers, and hyphens, and start/end with alphanumeric", kit)
	}

	return nil
}

// validateNodes validates the nodes array.
func (v *SpecValidator) validateNodes(nodes []core.NodeSpec) error {
	if len(nodes) == 0 {
		return errors.NewValidationError("nodes", "at least one node is required")
	}

	if len(nodes) > MaxNodesCount {
		return errors.NewValidationError("nodes",
			fmt.Sprintf("too many nodes (max %d)", MaxNodesCount))
	}

	me := errors.NewMultiError()
	seen := make(map[string]bool)

	for i, node := range nodes {
		path := fmt.Sprintf("nodes[%d]", i)

		// Validate node name
		if node.Name == "" {
			me.Add(errors.NewValidationError(path+".name", "name is required"))
			continue
		}

		if err := v.validateName(node.Name); err != nil {
			if ve, ok := err.(*errors.ValidationError); ok {
				ve.Field = path + ".name"
				me.Add(ve)
			} else {
				me.Add(err)
			}
		}

		// Check for duplicate names
		if seen[node.Name] {
			me.Add(errors.NewValidationErrorWithValue(path+".name",
				"duplicate node name", node.Name))
		}
		seen[node.Name] = true

		// Validate node type
		if err := v.validateNodeType(node.Type, path); err != nil {
			me.Add(err)
		}

		// Validate provider
		if err := v.validateProvider(node.Provider, path); err != nil {
			me.Add(err)
		}

		// Validate SSH config if present
		if node.SSH != nil {
			if err := v.validateSSHConfig(node.SSH, path); err != nil {
				me.Add(err)
			}
		}
	}

	return me.ErrorOrNil()
}

// validateNodeType validates the node type.
func (v *SpecValidator) validateNodeType(nodeType, path string) error {
	if nodeType == "" {
		return errors.NewValidationError(path+".type", "type is required")
	}

	if !IsValidNodeType(nodeType) {
		return errors.NewValidationErrorWithValue(path+".type",
			"invalid type (valid: "+SupportedNodeTypesString()+")", nodeType)
	}

	return nil
}

// validateProvider validates the provider.
// Keep in sync with: pkg/unifier/schema/kombination.cue, pkg/stackkits/base/stackkit.cue
func (v *SpecValidator) validateProvider(provider, path string) error {
	if provider == "" {
		return errors.NewValidationError(path+".provider", "provider is required")
	}

	if !IsValidProvider(provider) {
		return errors.NewValidationErrorWithValue(path+".provider",
			"unsupported provider (valid: "+SupportedProvidersString()+")", provider)
	}

	return nil
}

// validateSSHConfig validates SSH configuration.
func (v *SpecValidator) validateSSHConfig(ssh *core.SSHConfig, path string) error {
	me := errors.NewMultiError()

	if ssh.Host == "" {
		me.Add(errors.NewValidationError(path+".ssh.host", "host is required"))
	}

	if ssh.Port < 0 || ssh.Port > 65535 {
		me.Add(errors.NewValidationError(path+".ssh.port",
			fmt.Sprintf("port must be between 0 and 65535, got %d", ssh.Port)))
	}

	if ssh.User == "" {
		me.Add(errors.NewValidationError(path+".ssh.user", "user is required"))
	}

	return me.ErrorOrNil()
}

// validateServices validates the services array.
func (v *SpecValidator) validateServices(services []core.ServiceSpec, nodes []core.NodeSpec) error {
	if len(services) > MaxServicesCount {
		return errors.NewValidationError("services",
			fmt.Sprintf("too many services (max %d)", MaxServicesCount))
	}

	me := errors.NewMultiError()
	seen := make(map[string]bool)
	nodeNames := make(map[string]bool)

	// Build node name map for validation
	for _, node := range nodes {
		nodeNames[node.Name] = true
	}

	for i, service := range services {
		path := fmt.Sprintf("services[%d]", i)

		// Validate service name
		if service.Name == "" {
			me.Add(errors.NewValidationError(path+".name", "name is required"))
			continue
		}

		if err := v.validateName(service.Name); err != nil {
			if ve, ok := err.(*errors.ValidationError); ok {
				ve.Field = path + ".name"
				me.Add(ve)
			} else {
				me.Add(err)
			}
		}

		// Check for duplicate names
		if seen[service.Name] {
			me.Add(errors.NewValidationErrorWithValue(path+".name",
				"duplicate service name", service.Name))
		}
		seen[service.Name] = true

		// Validate service type
		if service.Type == "" {
			me.Add(errors.NewValidationError(path+".type", "type is required"))
		}

		// Validate node reference
		if service.Node == "" {
			me.Add(errors.NewValidationError(path+".node", "node is required"))
		} else if !nodeNames[service.Node] {
			me.Add(errors.NewValidationErrorWithValue(path+".node",
				"referenced node does not exist", service.Node))
		}

		// Validate dependencies
		for j, dep := range service.Needs {
			if dep == service.Name {
				me.Add(errors.NewValidationError(
					fmt.Sprintf("%s.needs[%d]", path, j),
					"service cannot depend on itself"))
			}
		}
	}

	// Check for circular dependencies
	if err := v.checkCircularDependencies(services); err != nil {
		me.Add(err)
	}

	return me.ErrorOrNil()
}

// checkCircularDependencies checks for circular dependencies in services.
func (v *SpecValidator) checkCircularDependencies(services []core.ServiceSpec) error {
	// Build dependency graph
	deps := make(map[string][]string)
	for _, service := range services {
		deps[service.Name] = service.Needs
	}

	// Check each service for circular dependencies
	for _, service := range services {
		visited := make(map[string]bool)
		if v.hasCycle(service.Name, deps, visited, make(map[string]bool)) {
			return errors.NewValidationErrorWithValue("services",
				"circular dependency detected involving service", service.Name)
		}
	}

	return nil
}

// hasCycle checks if there's a cycle in the dependency graph starting from node.
func (v *SpecValidator) hasCycle(node string, deps map[string][]string, visited, recStack map[string]bool) bool {
	visited[node] = true
	recStack[node] = true

	for _, dep := range deps[node] {
		if !visited[dep] {
			if v.hasCycle(dep, deps, visited, recStack) {
				return true
			}
		} else if recStack[dep] {
			return true
		}
	}

	recStack[node] = false
	return false
}

// validateNetwork validates network configuration.
func (v *SpecValidator) validateNetwork(network *core.NetworkSpec) error {
	if network == nil {
		return nil
	}

	me := errors.NewMultiError()

	// Validate VPN type
	if network.VPN != "" {
		validVPN := map[string]bool{
			"headscale": true,
			"wireguard": true,
			"tailscale": true,
			"none":      true,
		}

		if !validVPN[network.VPN] {
			me.Add(errors.NewValidationErrorWithValue("network.vpn",
				"unsupported VPN type (valid: headscale, wireguard, tailscale, none)", network.VPN))
		}
	}

	// Validate domain
	if network.Domain != "" {
		// Allow simple hostnames or full domains
		if !nameRegex.MatchString(network.Domain) && !domainRegex.MatchString(network.Domain) {
			me.Add(errors.NewValidationErrorWithValue("network.domain",
				"invalid domain format", network.Domain))
		}

		if len(network.Domain) > 253 {
			me.Add(errors.NewValidationError("network.domain",
				"domain too long (max 253 characters)"))
		}
	}

	return me.ErrorOrNil()
}

// ValidateVersion validates a spec version string.
func ValidateVersion(version string) error {
	if version == "" {
		return nil // Version is optional
	}

	// Simple semantic version validation (e.g., "1.0.0", "2.1.3-beta")
	versionRegex := regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[a-z0-9]+)?$`)
	if !versionRegex.MatchString(strings.ToLower(version)) {
		return errors.NewValidationErrorWithValue("version",
			"invalid version format (expected semantic version like 1.0.0 or 1.0.0-beta)", version)
	}

	return nil
}

// ValidateMetadata validates metadata key-value pairs.
func ValidateMetadata(metadata map[string]string) error {
	if len(metadata) == 0 {
		return nil
	}

	me := errors.NewMultiError()

	for key, value := range metadata {
		// Validate key format
		if !nameRegex.MatchString(key) {
			me.Add(errors.NewValidationErrorWithValue("metadata."+key,
				"invalid metadata key format", key))
		}

		// Validate value length
		if len(value) > 1024 {
			me.Add(errors.NewValidationError("metadata."+key,
				"metadata value too long (max 1024 characters)"))
		}
	}

	return me.ErrorOrNil()
}
