// Package validator provides shared validation utilities for kombifyTechstack.
// This package centralizes validation logic shared by Unifier preflight.
package validator

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DNS name validation regex - RFC 1123 compliant
var dnsNameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$|^[a-z]$`)

// stackKitNameRegex validates stackkit names (lowercase, alphanumeric with hyphens).
// Must start with a letter, may contain digits/hyphens, must not end with a hyphen.
var stackKitNameRegex = regexp.MustCompile(`^[a-z](?:[a-z0-9-]*[a-z0-9])?$`)

const (
	providerHetzner      = "hetzner"
	providerLocal        = "local"
	providerDocker       = "docker"
	providerProxmox      = "proxmox"
	providerDigitalOcean = "digitalocean"
)

// knownCloudProviders is the set of supported cloud providers.
// Keep in sync with: pkg/unifier/schema/kombination.cue
var knownCloudProviders = map[string]bool{
	providerHetzner:        true,
	"aws":                  true,
	"gcp":                  true,
	"azure":                true,
	"linode":               true,
	"vultr":                true,
	providerDigitalOcean:   true, // DigitalOcean (canonical name)
	"centron":              true,
	"digitalocean-managed": true,
	"ionos":                true,
}

// knownLocalProviders is the set of local/on-premise providers.
var knownLocalProviders = map[string]bool{
	providerLocal:   true,
	"homelab":       true,
	providerDocker:  true,
	providerProxmox: true,
	"esxi":          true,
}

// knownNodeTypes is the set of supported node roles.
// Keep in sync with: pkg/unifier/schema/kombination.cue
var knownNodeTypes = map[string]bool{
	"main":   true,
	"worker": true,
	"edge":   true,
}

// SupportedProviders returns all supported provider names in stable order.
func SupportedProviders() []string {
	providers := make([]string, 0, len(knownCloudProviders)+len(knownLocalProviders))
	for p := range knownCloudProviders {
		providers = append(providers, p)
	}
	for p := range knownLocalProviders {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	return providers
}

// SupportedProvidersString returns the supported providers as a comma-separated list.
func SupportedProvidersString() string {
	return strings.Join(SupportedProviders(), ", ")
}

// SupportedNodeTypes returns all supported node types in stable order.
func SupportedNodeTypes() []string {
	types := make([]string, 0, len(knownNodeTypes))
	for t := range knownNodeTypes {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// SupportedNodeTypesString returns the supported node types as a comma-separated list.
func SupportedNodeTypesString() string {
	return strings.Join(SupportedNodeTypes(), ", ")
}

// ValidateDNSName checks if a name is a valid DNS name.
func ValidateDNSName(name string) error {
	if name == "" {
		return fmt.Errorf("DNS name cannot be empty")
	}
	if len(name) > 63 {
		return fmt.Errorf("DNS name %q exceeds 63 character limit", name)
	}
	if !dnsNameRegex.MatchString(name) {
		return fmt.Errorf("invalid DNS name %q: must start with a letter, contain only lowercase letters, numbers, and hyphens, and end with a letter or number", name)
	}
	return nil
}

// ValidateStackKitName validates a stackkit name for security and correctness.
// Prevents path traversal and ensures valid naming conventions.
func ValidateStackKitName(name string) error {
	if name == "" {
		return fmt.Errorf("stackkit name cannot be empty")
	}

	// Reject anything that isn't a single path segment
	if filepath.Base(name) != name {
		return fmt.Errorf("invalid stackkit name %q: contains path separators", name)
	}

	// Check for path traversal attempts
	if strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("invalid stackkit name %q: contains path separators", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid stackkit name %q: contains path traversal", name)
	}

	// Check format
	if !stackKitNameRegex.MatchString(name) {
		return fmt.Errorf("invalid stackkit name %q: must start with a letter, contain only lowercase letters, numbers, and hyphens", name)
	}

	return nil
}

// IsCloudProvider checks if a provider is a known cloud provider.
func IsCloudProvider(provider string) bool {
	return knownCloudProviders[strings.ToLower(provider)]
}

// IsValidProvider checks if a provider is any known provider (cloud or local).
func IsValidProvider(provider string) bool {
	p := strings.ToLower(provider)
	return knownCloudProviders[p] || knownLocalProviders[p]
}

// IsValidNodeType checks if a node type is supported.
func IsValidNodeType(nodeType string) bool {
	return knownNodeTypes[strings.ToLower(nodeType)]
}

// ValidateSSHKeyPath validates an SSH key path for security.
func ValidateSSHKeyPath(keyPath string) error {
	if keyPath == "" {
		return nil // Empty is valid (no key specified)
	}

	// Check for path traversal
	if strings.Contains(keyPath, "..") {
		return fmt.Errorf("SSH key path %q contains path traversal", keyPath)
	}

	// Check for suspicious paths
	suspiciousPaths := []string{
		"/etc/passwd",
		"/etc/shadow",
		"/etc/sudoers",
		"/root/",
		"C:\\Windows\\System32",
	}
	lowerPath := strings.ToLower(keyPath)
	for _, suspicious := range suspiciousPaths {
		if strings.Contains(lowerPath, strings.ToLower(suspicious)) {
			return fmt.Errorf("SSH key path %q is not allowed", keyPath)
		}
	}

	return nil
}

// ServiceType constants for type-safe service type handling
type ServiceType string

const (
	ServiceTypeWeb        ServiceType = "web"
	ServiceTypeAPI        ServiceType = "api"
	ServiceTypeDatabase   ServiceType = "database"
	ServiceTypeCache      ServiceType = "cache"
	ServiceTypeQueue      ServiceType = "queue"
	ServiceTypeStorage    ServiceType = "storage"
	ServiceTypeMonitor    ServiceType = "monitor"
	ServiceTypeMonitoring ServiceType = "monitoring"
	ServiceTypeIngress    ServiceType = "ingress"
	ServiceTypeProxy      ServiceType = "reverse-proxy"
)

// defaultServiceImages maps service types to their default Docker images.
// This centralizes image resolution for the Unifier Engine.
var defaultServiceImages = map[ServiceType]string{
	ServiceTypeProxy:      "traefik:v3.0",
	ServiceTypeDatabase:   "postgres:16-alpine",
	ServiceTypeCache:      "redis:7-alpine",
	ServiceTypeQueue:      "rabbitmq:3-management-alpine",
	ServiceTypeMonitor:    "grafana/grafana:latest",
	ServiceTypeMonitoring: "grafana/grafana:latest",
	ServiceTypeStorage:    "minio/minio:latest",
	ServiceTypeIngress:    "nginx:stable-alpine",
	ServiceTypeWeb:        "nginx:stable-alpine",
	ServiceTypeAPI:        "traefik:v3.0",
}

// defaultServicePorts maps service types to their default ports.
var defaultServicePorts = map[ServiceType]int{
	ServiceTypeProxy:      80,
	ServiceTypeDatabase:   5432,
	ServiceTypeCache:      6379,
	ServiceTypeQueue:      5672,
	ServiceTypeMonitor:    3000,
	ServiceTypeMonitoring: 3000,
	ServiceTypeStorage:    9000,
	ServiceTypeIngress:    80,
	ServiceTypeWeb:        80,
	ServiceTypeAPI:        8080,
}

// GetDefaultImage returns the default Docker image for a service type.
// Returns empty string if no default is defined.
func GetDefaultImage(svcType string) string {
	return defaultServiceImages[ServiceType(strings.ToLower(svcType))]
}

// GetDefaultPort returns the default port for a service type.
// Returns 0 if no default is defined.
func GetDefaultPort(svcType string) int {
	return defaultServicePorts[ServiceType(strings.ToLower(svcType))]
}
