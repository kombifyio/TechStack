// Package stacks provides validation functions for kombifyTechstack stack configurations.
// This file contains YAML/JSON validation, node validation, and service validation.
package stacks

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

const stackSupportedProvidersReadable = "local, hetzner, docker, aws, gcp, azure, digitalocean, digitalocean-managed, centron, ionos"

// YAMLParseError is a custom error type for YAML parsing errors.
type YAMLParseError struct {
	Message string
}

func (e *YAMLParseError) Error() string {
	return e.Message
}

// ParseYAMLOrJSON parses YAML or JSON data into a map.
// Supports both JSON and YAML formats. JSON is tried first since YAML is a superset.
func ParseYAMLOrJSON(data []byte) (map[string]interface{}, error) {
	var result map[string]interface{}

	// Try JSON first (faster and more strict)
	if err := json.Unmarshal(data, &result); err == nil {
		return result, nil
	}

	// Try YAML parsing
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, &YAMLParseError{Message: fmt.Sprintf("failed to parse as JSON or YAML: %v", err)}
	}

	return result, nil
}

// ValidateKombinationSpec validates a kombination spec and returns validation results.
// IMPORTANT: This validates RAW user input, NOT the final unified spec.
// - No fields are required - the Unifier fills in defaults
// - Empty spec = valid (uses Basement Kit with defaults)
// - Only validates format/syntax of provided fields
//
//nolint:goconst // Accepted kit names are user-facing wire values.
func ValidateKombinationSpec(spec map[string]interface{}) map[string]interface{} {
	errors := []map[string]string{}
	warnings := []map[string]string{}

	// Optional: validate name format IF provided
	if name, hasName := spec["name"].(string); hasName && name != "" {
		if !IsValidDNSName(name) {
			errors = append(errors, map[string]string{
				"path":    "name",
				"code":    "INVALID_DNS_NAME",
				"message": "Name muss DNS-kompatibel sein (nur Kleinbuchstaben, Zahlen und Bindestriche)",
			})
		}
	}

	// Optional: validate StackKit IF provided
	kit, hasKit := spec["kit"].(string)
	kitPath := "kit"
	if kit == "" {
		kit, hasKit = spec["stackkit"].(string)
		kitPath = "stackkit"
	}
	if hasKit && kit != "" {
		validKits := map[string]bool{
			"basement-kit": true, "cloud-kit": true,
		}
		if !validKits[kit] {
			warnings = append(warnings, map[string]string{
				"path":    kitPath,
				"code":    "UNKNOWN_KIT",
				"message": "Unbekanntes StackKit '" + kit + "' - wird vom Unifier geprüft",
			})
		}
	}

	// Optional: validate nodes IF provided
	if nodes, hasNodes := spec["nodes"].([]interface{}); hasNodes {
		for i, node := range nodes {
			if nodeMap, ok := node.(map[string]interface{}); ok {
				nodeErrors := ValidateNodeFormat(nodeMap, i)
				errors = append(errors, nodeErrors...)
			}
		}
	}

	// Optional: validate services IF provided
	if services, ok := spec["services"].([]interface{}); ok {
		for i, service := range services {
			if serviceMap, ok := service.(map[string]interface{}); ok {
				serviceErrors := ValidateServiceFormat(serviceMap, i)
				errors = append(errors, serviceErrors...)
			}
		}
	}

	return map[string]interface{}{
		"valid":    len(errors) == 0,
		"errors":   errors,
		"warnings": warnings,
	}
}

// ValidateNodeFormat validates only the format of provided node fields (no required checks).
// This is lenient validation for user input before the Unifier fills in defaults.
//
//nolint:goconst // Provider names are external spec wire values.
func ValidateNodeFormat(node map[string]interface{}, index int) []map[string]string {
	errors := []map[string]string{}
	basePath := fmt.Sprintf("nodes[%d]", index)

	// Validate name format IF provided
	if name, ok := node["name"].(string); ok && name != "" {
		if !IsValidDNSName(name) {
			errors = append(errors, map[string]string{
				"path":    basePath + ".name",
				"code":    "INVALID_DNS_NAME",
				"message": "Node-Name muss DNS-kompatibel sein",
			})
		}
	}

	// Validate type IF provided
	if nodeType, ok := node["type"].(string); ok && nodeType != "" {
		validTypes := map[string]bool{"main": true, "worker": true, "edge": true}
		if !validTypes[nodeType] {
			errors = append(errors, map[string]string{
				"path":    basePath + ".type",
				"code":    "INVALID_VALUE",
				"message": "Ungültiger Node-Typ. Erlaubt: main, worker, edge",
			})
		}
	}

	// Validate provider IF provided
	if provider, ok := node["provider"].(string); ok && provider != "" {
		validProviders := map[string]bool{"local": true, "hetzner": true, "docker": true, "aws": true, "gcp": true, "azure": true, "digitalocean": true, "digitalocean-managed": true, "centron": true, "ionos": true}
		if !validProviders[provider] {
			errors = append(errors, map[string]string{
				"path":    basePath + ".provider",
				"code":    "INVALID_VALUE",
				"message": "Ungültiger Provider. Erlaubt: " + stackSupportedProvidersReadable,
			})
		}
	}

	return errors
}

// ValidateServiceFormat validates only the format of provided service fields (no required checks).
// This is lenient validation for user input before the Unifier fills in defaults.
func ValidateServiceFormat(service map[string]interface{}, index int) []map[string]string {
	errors := []map[string]string{}
	basePath := fmt.Sprintf("services[%d]", index)

	// Validate name format IF provided
	if name, ok := service["name"].(string); ok && name != "" {
		if !IsValidDNSName(name) {
			errors = append(errors, map[string]string{
				"path":    basePath + ".name",
				"code":    "INVALID_DNS_NAME",
				"message": "Service-Name muss DNS-kompatibel sein",
			})
		}
	}

	return errors
}

// IsValidDNSName checks if a string is a valid DNS name.
// Rules:
// - Length: 1-63 characters
// - Must start with lowercase letter
// - Can contain lowercase letters, numbers, and hyphens
// - Must not end with hyphen
func IsValidDNSName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	// Must start with lowercase letter
	if name[0] < 'a' || name[0] > 'z' {
		return false
	}
	// Can contain lowercase letters, numbers, and hyphens
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	// Must not end with hyphen
	if name[len(name)-1] == '-' {
		return false
	}
	return true
}
