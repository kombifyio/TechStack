// Package unifier provides the Pre-Validation step for the Unifier Pipeline.
package unifier

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"cuelang.org/go/cue"
	cueerrors "cuelang.org/go/cue/errors"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/validator"
)

// PreValidator handles schema validation before the main Unifier processing.
// This is Step 1 of the Unifier Pipeline.
//
// Pipeline: Pre-Validation → StackKit Resolution → Add-On Detection → Unifier Engine
type PreValidator struct {
	ctx    *cue.Context
	schema cue.Value
}

// NewPreValidator creates a pre-validator with the engine's CUE context and schema.
func NewPreValidator(ctx *cue.Context, schema cue.Value) *PreValidator {
	return &PreValidator{
		ctx:    ctx,
		schema: schema,
	}
}

// ValidateSchema performs schema-level validation without StackKit logic.
// This checks:
//   - Required fields are present
//   - Types are correct
//   - Basic constraints are met
//   - DNS-compatible names
//   - No duplicate names
//   - Valid references
//   - SSH config for local providers
//   - Service dependency graph
//
// Returns a ValidationResult with any schema errors.
func (v *PreValidator) ValidateSchema(spec *core.KombinationSpec) (*core.ValidationResult, error) {
	if spec == nil {
		return &core.ValidationResult{
			Valid: false,
			Errors: []core.ValidationError{{
				Path:    "",
				Code:    "nil_spec",
				Message: "spec cannot be nil",
			}},
		}, nil
	}

	var errors []core.ValidationError

	// 1. Validate spec name (only if provided)
	errors = append(errors, v.validateSpecName(spec)...)

	// 2. Validate nodes (only if provided)
	nodeNames := make(map[string]int) // name -> first occurrence index
	for i, node := range spec.Nodes {
		errors = append(errors, v.validateNode(i, node, nodeNames)...)
	}

	// 3. Validate services (only if provided)
	serviceNames := make(map[string]int)
	for i, svc := range spec.Services {
		errors = append(errors, v.validateService(i, svc, nodeNames, serviceNames)...)
	}

	// 4. Validate service dependencies (cycle detection)
	if depErrors := v.validateServiceDependencies(spec.Services); len(depErrors) > 0 {
		errors = append(errors, depErrors...)
	}

	// 5. Validate network config
	errors = append(errors, v.validateNetwork(spec)...)

	// 6. Perform CUE schema validation if available
	// For raw/partial user input, we allow non-concrete validation so missing required
	// fields do not fail here. Full concretization happens later after defaults.
	if v.schema.Exists() {
		cueErrors := v.validateWithCUE(spec)
		errors = append(errors, cueErrors...)
	}

	return &core.ValidationResult{
		Valid:  len(errors) == 0,
		Errors: errors,
	}, nil
}

// validateSpecName validates the spec name.
func (v *PreValidator) validateSpecName(spec *core.KombinationSpec) []core.ValidationError {
	var errors []core.ValidationError

	if spec.Name == "" {
		return nil
	}

	if !isValidDNSName(spec.Name) {
		errors = append(errors, core.ValidationError{
			Path:    "name",
			Code:    "invalid_format",
			Message: fmt.Sprintf("name '%s' must be a valid DNS name (lowercase, alphanumeric, hyphens, max 63 chars)", spec.Name),
		})
	}

	return errors
}

// validateNode validates a single node.
func (v *PreValidator) validateNode(idx int, node core.NodeSpec, nodeNames map[string]int) []core.ValidationError {
	var errors []core.ValidationError
	path := fmt.Sprintf("nodes[%d]", idx)

	// Name validation (only if provided)
	if node.Name != "" {
		// DNS name validation
		if !isValidDNSName(node.Name) {
			errors = append(errors, core.ValidationError{
				Path:    path + ".name",
				Code:    "invalid_format",
				Message: fmt.Sprintf("node name '%s' must be a valid DNS name", node.Name),
			})
		}

		// Duplicate check (only when name present)
		if firstIdx, exists := nodeNames[node.Name]; exists {
			errors = append(errors, core.ValidationError{
				Path:    path + ".name",
				Code:    "duplicate",
				Message: fmt.Sprintf("duplicate node name '%s' (first defined at nodes[%d])", node.Name, firstIdx),
			})
		} else {
			nodeNames[node.Name] = idx
		}
	}

	// Provider validation (only if provided)
	if node.Provider != "" && !validator.IsValidProvider(node.Provider) {
		errors = append(errors, core.ValidationError{
			Path:    path + ".provider",
			Code:    "invalid_value",
			Message: fmt.Sprintf("invalid provider '%s', must be one of: %s", node.Provider, validator.SupportedProvidersString()),
		})
	}

	// Node type validation
	if node.Type != "" && !validator.IsValidNodeType(node.Type) {
		errors = append(errors, core.ValidationError{
			Path:    path + ".type",
			Code:    "invalid_value",
			Message: fmt.Sprintf("invalid node type '%s', must be one of: %s", node.Type, validator.SupportedNodeTypesString()),
		})
	}

	// SSH config validation (optional at creation time)
	// kombifyTechstack can run agent-based; workers connect later, so SSH may not be available yet.
	if node.SSH != nil {
		errors = append(errors, v.validateSSHConfig(path+".ssh", node.SSH)...)
	}

	return errors
}

// validateSSHConfig validates SSH configuration.
func (v *PreValidator) validateSSHConfig(path string, ssh *core.SSHConfig) []core.ValidationError {
	var errors []core.ValidationError

	if ssh.Host == "" {
		errors = append(errors, core.ValidationError{
			Path:    path + ".host",
			Code:    "required",
			Message: "SSH host is required",
		})
	} else if !isValidHostOrIP(ssh.Host) {
		errors = append(errors, core.ValidationError{
			Path:    path + ".host",
			Code:    "invalid_format",
			Message: fmt.Sprintf("invalid SSH host '%s' (must be IP or hostname)", ssh.Host),
		})
	}

	if ssh.Port != 0 && (ssh.Port < 1 || ssh.Port > 65535) {
		errors = append(errors, core.ValidationError{
			Path:    path + ".port",
			Code:    "out_of_range",
			Message: fmt.Sprintf("SSH port %d out of valid range (1-65535)", ssh.Port),
		})
	}

	// Security: Validate SSH key path to prevent path traversal attacks
	if ssh.KeyPath != "" {
		if err := validator.ValidateSSHKeyPath(ssh.KeyPath); err != nil {
			errors = append(errors, core.ValidationError{
				Path:    path + ".key_path",
				Code:    "security",
				Message: fmt.Sprintf("invalid SSH key path: %v", err),
			})
		}
	}

	return errors
}

// validateService validates a single service.
func (v *PreValidator) validateService(idx int, svc core.ServiceSpec, nodeNames, serviceNames map[string]int) []core.ValidationError {
	var errors []core.ValidationError
	path := fmt.Sprintf("services[%d]", idx)

	// Name validation (only if provided)
	if svc.Name != "" {
		// DNS name validation
		if !isValidDNSName(svc.Name) {
			errors = append(errors, core.ValidationError{
				Path:    path + ".name",
				Code:    "invalid_format",
				Message: fmt.Sprintf("service name '%s' must be a valid DNS name", svc.Name),
			})
		}

		// Duplicate check
		if firstIdx, exists := serviceNames[svc.Name]; exists {
			errors = append(errors, core.ValidationError{
				Path:    path + ".name",
				Code:    "duplicate",
				Message: fmt.Sprintf("duplicate service name '%s' (first defined at services[%d])", svc.Name, firstIdx),
			})
		} else {
			serviceNames[svc.Name] = idx
		}
	}

	// Node reference validation
	if svc.Node != "" {
		if _, exists := nodeNames[svc.Node]; !exists {
			errors = append(errors, core.ValidationError{
				Path:    path + ".node",
				Code:    "invalid_reference",
				Message: fmt.Sprintf("service references unknown node '%s'", svc.Node),
			})
		}
	}

	return errors
}

// validateServiceDependencies checks for circular dependencies in services.
func (v *PreValidator) validateServiceDependencies(services []core.ServiceSpec) []core.ValidationError {
	var errors []core.ValidationError

	// Build dependency graph
	serviceMap := make(map[string]int)
	for i, svc := range services {
		if svc.Name == "" {
			continue
		}
		serviceMap[svc.Name] = i
	}

	// Check that all dependencies exist
	for i, svc := range services {
		if svc.Name == "" {
			// Can't validate deps without a stable identifier
			continue
		}
		for _, dep := range svc.Needs {
			if _, exists := serviceMap[dep]; !exists {
				errors = append(errors, core.ValidationError{
					Path:    fmt.Sprintf("services[%d].needs", i),
					Code:    "invalid_reference",
					Message: fmt.Sprintf("service '%s' depends on unknown service '%s'", svc.Name, dep),
				})
			}
		}
	}

	// Detect cycles using DFS
	visited := make(map[string]int) // 0=unvisited, 1=in-progress, 2=done
	var detectCycle func(name string, path []string) bool

	detectCycle = func(name string, path []string) bool {
		if visited[name] == 1 {
			// Found cycle
			cycleStart := 0
			for i, p := range path {
				if p == name {
					cycleStart = i
					break
				}
			}
			cyclePath := append(path[cycleStart:], name)
			errors = append(errors, core.ValidationError{
				Path:    "services",
				Code:    "circular_dependency",
				Message: fmt.Sprintf("circular dependency detected: %s", strings.Join(cyclePath, " → ")),
			})
			return true
		}
		if visited[name] == 2 {
			return false
		}

		visited[name] = 1
		idx, exists := serviceMap[name]
		if exists {
			for _, dep := range services[idx].Needs {
				if detectCycle(dep, append(path, name)) {
					return true
				}
			}
		}
		visited[name] = 2
		return false
	}

	for _, svc := range services {
		if svc.Name == "" {
			continue
		}
		if visited[svc.Name] == 0 {
			detectCycle(svc.Name, nil)
		}
	}

	return errors
}

// validateNetwork validates network configuration.
func (v *PreValidator) validateNetwork(spec *core.KombinationSpec) []core.ValidationError {
	var errors []core.ValidationError

	if spec.Network.VPN != "" && !isValidVPN(spec.Network.VPN) {
		errors = append(errors, core.ValidationError{
			Path:    "network.vpn",
			Code:    "invalid_value",
			Message: fmt.Sprintf("invalid VPN type '%s', must be one of: headscale, tailscale, wireguard, none", spec.Network.VPN),
		})
	}

	return errors
}

// validateWithCUE performs CUE-based schema validation.
func (v *PreValidator) validateWithCUE(spec *core.KombinationSpec) []core.ValidationError {
	// NOTE: cue.Context.Encode(spec) doesn't respect json `omitempty` consistently.
	// This can accidentally include empty optional structs (e.g. network: {vpn:""})
	// and fail validation. We marshal to JSON first to preserve the intended shape.
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return []core.ValidationError{{
			Path:    "",
			Code:    "encode_error",
			Message: fmt.Sprintf("failed to marshal spec: %v", err),
		}}
	}

	// Treat empty string / null / empty slices / empty nested structs as "not provided".
	// This is important for Easy-Path where the raw user input may be partial/empty,
	// but Go structs default to "" / nil / zero structs.
	var specMap map[string]any
	if err := json.Unmarshal(specJSON, &specMap); err == nil {
		var prune func(v any) (any, bool)
		prune = func(v any) (any, bool) {
			switch t := v.(type) {
			case nil:
				return nil, false
			case string:
				if t == "" {
					return nil, false
				}
				return t, true
			case []any:
				out := make([]any, 0, len(t))
				for _, item := range t {
					if pv, ok := prune(item); ok {
						out = append(out, pv)
					}
				}
				if len(out) == 0 {
					return nil, false
				}
				return out, true
			case map[string]any:
				out := make(map[string]any, len(t))
				for k, vv := range t {
					if pv, ok := prune(vv); ok {
						out[k] = pv
					}
				}
				if len(out) == 0 {
					return nil, false
				}
				return out, true
			default:
				// numbers/bools/etc: keep as-is
				return v, true
			}
		}

		if prunedAny, ok := prune(specMap); ok {
			if prunedMap, ok := prunedAny.(map[string]any); ok {
				if prunedJSON, err := json.Marshal(prunedMap); err == nil {
					specJSON = prunedJSON
				}
			}
		} else {
			// If everything prunes away, treat as an empty object.
			// This keeps Easy-Path empty specs valid at the pre-validation stage.
			specJSON = []byte("{}")
		}
	}

	specValue := v.ctx.CompileBytes(specJSON)
	if specValue.Err() != nil {
		return []core.ValidationError{{
			Path:    "",
			Code:    "encode_error",
			Message: fmt.Sprintf("failed to compile spec JSON: %v", specValue.Err()),
		}}
	}

	// Get the schema definition
	schemaPath := cue.ParsePath("#KombinationSpec")
	schemaDef := v.schema.LookupPath(schemaPath)
	if !schemaDef.Exists() {
		// Schema not found, skip CUE validation
		return nil
	}

	// Unify spec with schema
	unified := schemaDef.Unify(specValue)

	// Check for validation errors
	if err := unified.Validate(cue.Concrete(false)); err != nil {
		return extractCUEValidationErrors(err)
	}

	return nil
}

// extractCUEValidationErrors converts CUE errors to ValidationError slice with path extraction.
func extractCUEValidationErrors(err error) []core.ValidationError {
	if err == nil {
		return nil
	}

	var errors []core.ValidationError
	for _, e := range cueerrors.Errors(err) {
		path := strings.Join(e.Path(), ".")
		errors = append(errors, core.ValidationError{
			Path:    path,
			Code:    "cue_validation_error",
			Message: e.Error(),
		})
	}

	if len(errors) == 0 {
		errors = append(errors, core.ValidationError{
			Path:    "",
			Code:    "cue_validation_error",
			Message: err.Error(),
		})
	}

	return errors
}

// isValidVPN checks if a VPN type is valid.
func isValidVPN(vpn string) bool {
	validVPNs := map[string]bool{
		"headscale": true,
		"tailscale": true,
		"wireguard": true,
		"none":      true,
	}
	return validVPNs[vpn]
}

// isValidDNSName checks if a name is a valid DNS subdomain name.
func isValidDNSName(name string) bool {
	return validator.ValidateDNSName(name) == nil
}

// isValidHostOrIP checks if a string is a valid hostname or IP address.
func isValidHostOrIP(host string) bool {
	if host == "" {
		return false
	}
	// Basic validation - allow IPs and hostnames
	ipPattern := regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}$`)
	hostPattern := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*[a-zA-Z0-9]$|^[a-zA-Z0-9]$`)
	return ipPattern.MatchString(host) || hostPattern.MatchString(host)
}
