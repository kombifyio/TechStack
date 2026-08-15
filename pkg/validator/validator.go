// Package validator provides shared validation utilities for kombifyTechstack.
// This package centralizes validation logic that was previously duplicated
// across pkg/unifier and pkg/tofu.
package validator

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DNS name validation regex - RFC 1123 compliant
var DNSNameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$|^[a-z]$`)

// StackKitNameRegex validates stackkit names (lowercase, alphanumeric with hyphens).
// Must start with a letter, may contain digits/hyphens, must not end with a hyphen.
var StackKitNameRegex = regexp.MustCompile(`^[a-z](?:[a-z0-9-]*[a-z0-9])?$`)

const (
	providerHetzner      = "hetzner"
	providerLocal        = "local"
	providerDocker       = "docker"
	providerProxmox      = "proxmox"
	providerDigitalOcean = "digitalocean"
)

// KnownCloudProviders is the set of supported cloud providers
// Keep in sync with: pkg/unifier/schema/kombination.cue, pkg/stackkits/base/stackkit.cue
var KnownCloudProviders = map[string]bool{
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

// KnownLocalProviders is the set of local/on-premise providers
var KnownLocalProviders = map[string]bool{
	providerLocal:   true,
	"homelab":       true,
	providerDocker:  true,
	providerProxmox: true,
	"esxi":          true,
}

// KnownNodeTypes is the set of supported node roles.
// Keep in sync with: pkg/unifier/schema/kombination.cue
var KnownNodeTypes = map[string]bool{
	"main":   true,
	"worker": true,
	"edge":   true,
}

// SupportedProviders returns all supported provider names in stable order.
func SupportedProviders() []string {
	providers := make([]string, 0, len(KnownCloudProviders)+len(KnownLocalProviders))
	for p := range KnownCloudProviders {
		providers = append(providers, p)
	}
	for p := range KnownLocalProviders {
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
	types := make([]string, 0, len(KnownNodeTypes))
	for t := range KnownNodeTypes {
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
	if !DNSNameRegex.MatchString(name) {
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
	if !StackKitNameRegex.MatchString(name) {
		return fmt.Errorf("invalid stackkit name %q: must start with a letter, contain only lowercase letters, numbers, and hyphens", name)
	}

	return nil
}

// IsCloudProvider checks if a provider is a known cloud provider.
func IsCloudProvider(provider string) bool {
	return KnownCloudProviders[strings.ToLower(provider)]
}

// IsLocalProvider checks if a provider is a known local provider.
func IsLocalProvider(provider string) bool {
	return KnownLocalProviders[strings.ToLower(provider)]
}

// IsValidProvider checks if a provider is any known provider (cloud or local).
func IsValidProvider(provider string) bool {
	p := strings.ToLower(provider)
	return KnownCloudProviders[p] || KnownLocalProviders[p]
}

// IsValidNodeType checks if a node type is supported.
func IsValidNodeType(nodeType string) bool {
	return KnownNodeTypes[strings.ToLower(nodeType)]
}

// ValidatePath checks if a path is safe (no traversal, absolute within boundaries).
func ValidatePath(path string, allowedBase string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}

	// Check for path traversal
	if strings.Contains(path, "..") {
		return fmt.Errorf("path %q contains traversal sequences", path)
	}

	// If an allowed base is specified, ensure the path stays within it
	if allowedBase != "" {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("cannot resolve path %q: %w", path, err)
		}
		absBase, err := filepath.Abs(allowedBase)
		if err != nil {
			return fmt.Errorf("cannot resolve base path %q: %w", allowedBase, err)
		}

		// Ensure the resolved path is within the allowed base
		rel, err := filepath.Rel(absBase, absPath)
		if err != nil {
			return fmt.Errorf("path %q is not within allowed base %q", path, allowedBase)
		}
		if strings.HasPrefix(rel, "..") {
			return fmt.Errorf("path %q escapes allowed base %q", path, allowedBase)
		}
	}

	return nil
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
	ServiceTypeService    ServiceType = "service"
	ServiceTypeBackend    ServiceType = "backend"
	ServiceTypeDatabase   ServiceType = "database"
	ServiceTypeCache      ServiceType = "cache"
	ServiceTypeQueue      ServiceType = "queue"
	ServiceTypePaaS       ServiceType = "paas"
	ServiceTypeStorage    ServiceType = "storage"
	ServiceTypeMonitor    ServiceType = "monitor"
	ServiceTypeMonitoring ServiceType = "monitoring"
	ServiceTypeIngress    ServiceType = "ingress"
	ServiceTypeProxy      ServiceType = "reverse-proxy"
	ServiceTypeDocker     ServiceType = "docker"
	ServiceTypeBackup     ServiceType = "backup"
	ServiceTypeAuth       ServiceType = "auth"
	ServiceTypeVPN        ServiceType = "vpn"
	ServiceTypeMedia      ServiceType = "media"
	ServiceTypeAutomation ServiceType = "automation"
	ServiceTypeDev        ServiceType = "development"
	ServiceTypeCustom     ServiceType = "custom"
)

// ValidServiceTypes is the set of valid service types
var ValidServiceTypes = map[ServiceType]bool{
	ServiceTypeWeb:        true,
	ServiceTypeAPI:        true,
	ServiceTypeService:    true,
	ServiceTypeBackend:    true,
	ServiceTypeDatabase:   true,
	ServiceTypeCache:      true,
	ServiceTypeQueue:      true,
	ServiceTypePaaS:       true,
	ServiceTypeStorage:    true,
	ServiceTypeMonitor:    true,
	ServiceTypeMonitoring: true,
	ServiceTypeIngress:    true,
	ServiceTypeProxy:      true,
	ServiceTypeDocker:     true,
	ServiceTypeBackup:     true,
	ServiceTypeAuth:       true,
	ServiceTypeVPN:        true,
	ServiceTypeMedia:      true,
	ServiceTypeAutomation: true,
	ServiceTypeDev:        true,
	ServiceTypeCustom:     true,
}

// DefaultServiceImages maps service types to their default Docker images.
// This centralizes image resolution for the Unifier Engine.
var DefaultServiceImages = map[ServiceType]string{
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

// DefaultServicePorts maps service types to their default ports.
var DefaultServicePorts = map[ServiceType]int{
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
	return DefaultServiceImages[ServiceType(strings.ToLower(svcType))]
}

// GetDefaultPort returns the default port for a service type.
// Returns 0 if no default is defined.
func GetDefaultPort(svcType string) int {
	return DefaultServicePorts[ServiceType(strings.ToLower(svcType))]
}

// IsValidServiceType checks if a service type is valid.
func IsValidServiceType(svcType string) bool {
	return ValidServiceTypes[ServiceType(strings.ToLower(svcType))]
}
