// Package discovery provides network device discovery functionality for kombifyTechstack.
// This file implements router/gateway detection templates that can be loaded from
// external YAML files or embedded defaults.

package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// RouterTemplates holds all patterns for detecting routers and gateways.
// It can be loaded from a YAML configuration file or use embedded defaults.
type RouterTemplates struct {
	Version     string `yaml:"version"`
	Description string `yaml:"description"`

	// HostnamePatterns contains patterns matched against device hostnames
	HostnamePatterns []HostnamePattern `yaml:"hostname_patterns"`

	// MACVendors maps MAC OUI prefixes to vendor information
	MACVendors []MACVendorEntry `yaml:"mac_vendors"`

	// GatewayPortSignatures defines port combinations indicating gateway functionality
	GatewayPortSignatures []PortSignature `yaml:"gateway_port_signatures"`

	// IPPatterns defines IP address patterns typical for gateways
	IPPatterns []IPPattern `yaml:"ip_patterns"`
}

// HostnamePattern represents a pattern for matching device hostnames.
type HostnamePattern struct {
	Pattern    string  `yaml:"pattern"`    // Substring to match (case-insensitive)
	Vendor     string  `yaml:"vendor"`     // Detected vendor name
	Type       string  `yaml:"type"`       // Device type (router, access-point, firewall, etc.)
	Confidence float64 `yaml:"confidence"` // Confidence score (0.0 - 1.0)
}

// MACVendorEntry maps a MAC OUI prefix to vendor information.
type MACVendorEntry struct {
	OUI    string `yaml:"oui"`    // First 3 bytes, format: "XX:XX:XX"
	Vendor string `yaml:"vendor"` // Vendor name
	Type   string `yaml:"type"`   // Device type
}

// PortSignature defines a combination of ports that indicate gateway functionality.
type PortSignature struct {
	Name        string  `yaml:"name"`        // Signature name for logging
	Description string  `yaml:"description"` // Human-readable description
	Ports       []int   `yaml:"ports"`       // Required ports (all must be present)
	Confidence  float64 `yaml:"confidence"`  // Confidence score (0.0 - 1.0)
}

// IPPattern defines an IP address suffix pattern typical for gateways.
type IPPattern struct {
	Suffix      string  `yaml:"suffix"`      // IP suffix to match (e.g., ".1", ".254")
	Description string  `yaml:"description"` // Human-readable description
	Confidence  float64 `yaml:"confidence"`  // Confidence score (0.0 - 1.0)
}

// MatchResult holds the result of a template match operation.
type MatchResult struct {
	IsGateway  bool     // Whether the device appears to be a gateway/router
	Vendor     string   // Detected vendor (if any)
	DeviceType string   // Detected device type
	Confidence float64  // Overall confidence score (0.0 - 1.0)
	Reasons    []string // Human-readable reasons for the match
}

// TemplateLoader defines the interface for loading router templates.
type TemplateLoader interface {
	// Load returns the router templates or an error
	Load() (*RouterTemplates, error)
}

// FileTemplateLoader loads templates from a YAML file.
type FileTemplateLoader struct {
	FilePath string
}

// NewFileTemplateLoader creates a new file-based template loader.
func NewFileTemplateLoader(filePath string) *FileTemplateLoader {
	return &FileTemplateLoader{FilePath: filePath}
}

// Load reads and parses the YAML template file.
func (l *FileTemplateLoader) Load() (*RouterTemplates, error) {
	data, err := os.ReadFile(l.FilePath)
	if err != nil {
		return nil, fmt.Errorf("reading template file %s: %w", l.FilePath, err)
	}

	var templates RouterTemplates
	if err := yaml.Unmarshal(data, &templates); err != nil {
		return nil, fmt.Errorf("parsing template file %s: %w", l.FilePath, err)
	}

	if err := templates.Validate(); err != nil {
		return nil, fmt.Errorf("validating templates from %s: %w", l.FilePath, err)
	}

	return &templates, nil
}

// EmbeddedTemplateLoader provides built-in default templates.
type EmbeddedTemplateLoader struct{}

// NewEmbeddedTemplateLoader creates a new embedded template loader.
func NewEmbeddedTemplateLoader() *EmbeddedTemplateLoader {
	return &EmbeddedTemplateLoader{}
}

// Load returns the embedded default templates.
func (l *EmbeddedTemplateLoader) Load() (*RouterTemplates, error) {
	return getDefaultTemplates(), nil
}

// getDefaultTemplates returns the hardcoded fallback templates.
// These are used when no external template file is available.
func getDefaultTemplates() *RouterTemplates {
	return &RouterTemplates{
		Version:     "1.0",
		Description: "Embedded default router detection patterns",
		HostnamePatterns: []HostnamePattern{
			{Pattern: "fritz", Vendor: "AVM", Type: "router", Confidence: 0.9},
			{Pattern: "fritzbox", Vendor: "AVM", Type: "router", Confidence: 0.95},
			{Pattern: "speedport", Vendor: "Telekom", Type: "router", Confidence: 0.9},
			{Pattern: "unifi", Vendor: "Ubiquiti", Type: "router", Confidence: 0.85},
			{Pattern: "ubnt", Vendor: "Ubiquiti", Type: "access-point", Confidence: 0.8},
			{Pattern: "edgerouter", Vendor: "Ubiquiti", Type: "router", Confidence: 0.95},
			{Pattern: "mikrotik", Vendor: "MikroTik", Type: "router", Confidence: 0.95},
			{Pattern: "openwrt", Vendor: "OpenWrt", Type: "router", Confidence: 0.9},
			{Pattern: "pfsense", Vendor: "Netgate", Type: "firewall", Confidence: 0.95},
			{Pattern: "opnsense", Vendor: "OPNsense", Type: "firewall", Confidence: 0.95},
			{Pattern: "netgear", Vendor: "Netgear", Type: "router", Confidence: 0.85},
			{Pattern: "linksys", Vendor: "Linksys", Type: "router", Confidence: 0.85},
			{Pattern: "tplink", Vendor: "TP-Link", Type: "router", Confidence: 0.85},
			{Pattern: "tp-link", Vendor: "TP-Link", Type: "router", Confidence: 0.85},
			{Pattern: "dlink", Vendor: "D-Link", Type: "router", Confidence: 0.85},
			{Pattern: "d-link", Vendor: "D-Link", Type: "router", Confidence: 0.85},
			{Pattern: "asus", Vendor: "ASUS", Type: "router", Confidence: 0.7},
			{Pattern: "vodafone", Vendor: "Vodafone", Type: "router", Confidence: 0.85},
			{Pattern: "telekom", Vendor: "Telekom", Type: "router", Confidence: 0.8},
			{Pattern: "easybox", Vendor: "Vodafone/Arcadyan", Type: "router", Confidence: 0.9},
			{Pattern: "router", Vendor: "Unknown", Type: "router", Confidence: 0.8},
			{Pattern: "gateway", Vendor: "Unknown", Type: "gateway", Confidence: 0.85},
			{Pattern: "gw", Vendor: "Unknown", Type: "gateway", Confidence: 0.6},
		},
		MACVendors: []MACVendorEntry{
			{OUI: "00:1F:3F", Vendor: "AVM", Type: "router"},
			{OUI: "B0:4E:26", Vendor: "Ubiquiti", Type: "network-device"},
			{OUI: "00:0C:42", Vendor: "MikroTik", Type: "router"},
			{OUI: "00:14:6C", Vendor: "Netgear", Type: "router"},
			{OUI: "14:CC:20", Vendor: "TP-Link", Type: "router"},
			{OUI: "00:05:5D", Vendor: "D-Link", Type: "router"},
			{OUI: "00:14:BF", Vendor: "Linksys", Type: "router"},
			{OUI: "00:1F:C6", Vendor: "ASUS", Type: "router"},
		},
		GatewayPortSignatures: []PortSignature{
			{Name: "standard-gateway", Ports: []int{53, 67}, Confidence: 0.9},
			{Name: "upnp-gateway", Ports: []int{53, 1900}, Confidence: 0.85},
			{Name: "full-gateway", Ports: []int{53, 67, 80, 443}, Confidence: 0.95},
			{Name: "dns-only", Ports: []int{53}, Confidence: 0.5},
			{Name: "dhcp-only", Ports: []int{67}, Confidence: 0.8},
		},
		IPPatterns: []IPPattern{
			{Suffix: ".1", Confidence: 0.7},
			{Suffix: ".254", Confidence: 0.6},
		},
	}
}

// Validate checks if the templates are valid.
func (t *RouterTemplates) Validate() error {
	for i, hp := range t.HostnamePatterns {
		if hp.Pattern == "" {
			return fmt.Errorf("hostname pattern %d: empty pattern", i)
		}
		if hp.Confidence < 0 || hp.Confidence > 1 {
			return fmt.Errorf("hostname pattern %d (%s): confidence must be between 0 and 1", i, hp.Pattern)
		}
	}

	for i, mv := range t.MACVendors {
		if mv.OUI == "" {
			return fmt.Errorf("MAC vendor %d: empty OUI", i)
		}
		// Validate OUI format (XX:XX:XX)
		if len(mv.OUI) != 8 {
			return fmt.Errorf("MAC vendor %d (%s): OUI must be in format XX:XX:XX", i, mv.OUI)
		}
	}

	for i, ps := range t.GatewayPortSignatures {
		if ps.Name == "" {
			return fmt.Errorf("port signature %d: empty name", i)
		}
		if len(ps.Ports) == 0 {
			return fmt.Errorf("port signature %d (%s): no ports specified", i, ps.Name)
		}
		if ps.Confidence < 0 || ps.Confidence > 1 {
			return fmt.Errorf("port signature %d (%s): confidence must be between 0 and 1", i, ps.Name)
		}
	}

	for i, ip := range t.IPPatterns {
		if ip.Suffix == "" {
			return fmt.Errorf("IP pattern %d: empty suffix", i)
		}
		if !strings.HasPrefix(ip.Suffix, ".") {
			return fmt.Errorf("IP pattern %d (%s): suffix must start with '.'", i, ip.Suffix)
		}
		if ip.Confidence < 0 || ip.Confidence > 1 {
			return fmt.Errorf("IP pattern %d (%s): confidence must be between 0 and 1", i, ip.Suffix)
		}
	}

	return nil
}

// TemplateMatcher provides thread-safe matching against router templates.
type TemplateMatcher struct {
	templates *RouterTemplates
	mu        sync.RWMutex
	once      sync.Once
	loader    TemplateLoader
	loadErr   error
}

// NewTemplateMatcher creates a new matcher with the given loader.
// If loader is nil, it will use a fallback chain of file + embedded templates.
func NewTemplateMatcher(loader TemplateLoader) *TemplateMatcher {
	return &TemplateMatcher{
		loader: loader,
	}
}

// DefaultTemplateMatcher returns a matcher that tries to load from the default
// config file path, falling back to embedded templates.
func DefaultTemplateMatcher() *TemplateMatcher {
	return NewTemplateMatcher(nil)
}

// ensureLoaded performs lazy loading of templates using sync.Once for thread safety.
func (m *TemplateMatcher) ensureLoaded() error {
	m.once.Do(func() {
		if m.loader != nil {
			m.templates, m.loadErr = m.loader.Load()
			return
		}

		// Try default file paths
		configPaths := []string{
			"configs/router-templates.yaml",
			"/etc/techstack/router-templates.yaml",
		}

		// Try to find the config relative to executable
		if execPath, err := os.Executable(); err == nil {
			execDir := filepath.Dir(execPath)
			configPaths = append([]string{
				filepath.Join(execDir, "configs", "router-templates.yaml"),
				filepath.Join(execDir, "..", "configs", "router-templates.yaml"),
			}, configPaths...)
		}

		for _, path := range configPaths {
			loader := NewFileTemplateLoader(path)
			templates, err := loader.Load()
			if err == nil {
				m.templates = templates
				return
			}
		}

		// Fall back to embedded templates
		m.templates = getDefaultTemplates()
	})

	return m.loadErr
}

// GetTemplates returns the loaded templates.
func (m *TemplateMatcher) GetTemplates() (*RouterTemplates, error) {
	if err := m.ensureLoaded(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.templates, nil
}

// MatchDevice analyzes a discovered device and returns match results.
func (m *TemplateMatcher) MatchDevice(device *DiscoveredDevice) (*MatchResult, error) {
	if err := m.ensureLoaded(); err != nil {
		return nil, fmt.Errorf("loading templates: %w", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	result := &MatchResult{
		Reasons: make([]string, 0),
	}

	var confidenceScores []float64

	// Check hostname patterns
	if device.Hostname != "" {
		if match := m.matchHostname(device.Hostname); match != nil {
			result.Vendor = match.Vendor
			result.DeviceType = match.Type
			confidenceScores = append(confidenceScores, match.Confidence)
			result.Reasons = append(result.Reasons,
				fmt.Sprintf("hostname matches pattern '%s' (vendor: %s)", match.Pattern, match.Vendor))
		}
	}

	// Check MAC OUI
	if device.MAC != "" {
		if match := m.matchMAC(device.MAC); match != nil {
			if result.Vendor == "" {
				result.Vendor = match.Vendor
			}
			if result.DeviceType == "" {
				result.DeviceType = match.Type
			}
			// MAC match adds moderate confidence
			confidenceScores = append(confidenceScores, 0.7)
			result.Reasons = append(result.Reasons,
				fmt.Sprintf("MAC OUI %s belongs to %s", match.OUI, match.Vendor))
		}
	}

	// Check port signatures
	ports := device.Ports
	if len(ports) == 0 && len(device.Services) > 0 {
		// Extract ports from services
		ports = make([]int, len(device.Services))
		for i, svc := range device.Services {
			ports[i] = svc.Port
		}
	}

	if match := m.matchPorts(ports); match != nil {
		confidenceScores = append(confidenceScores, match.Confidence)
		result.Reasons = append(result.Reasons,
			fmt.Sprintf("port signature '%s' detected", match.Name))
	}

	// Check IP pattern
	if match := m.matchIP(device.IP); match != nil {
		confidenceScores = append(confidenceScores, match.Confidence)
		result.Reasons = append(result.Reasons,
			fmt.Sprintf("IP ends with '%s' (common gateway pattern)", match.Suffix))
	}

	// Calculate overall confidence
	if len(confidenceScores) > 0 {
		result.Confidence = combineConfidenceScores(confidenceScores)
		// Consider it a gateway if confidence exceeds threshold
		result.IsGateway = result.Confidence >= 0.6
	}

	return result, nil
}

// matchHostname finds the best matching hostname pattern.
func (m *TemplateMatcher) matchHostname(hostname string) *HostnamePattern {
	if hostname == "" {
		return nil
	}

	lower := strings.ToLower(hostname)
	var bestMatch *HostnamePattern
	var bestConfidence float64

	for i := range m.templates.HostnamePatterns {
		pattern := &m.templates.HostnamePatterns[i]
		if strings.Contains(lower, strings.ToLower(pattern.Pattern)) {
			// Prefer longer matches and higher confidence
			score := pattern.Confidence * float64(len(pattern.Pattern))
			if bestMatch == nil || score > bestConfidence*float64(len(bestMatch.Pattern)) {
				bestMatch = pattern
				bestConfidence = pattern.Confidence
			}
		}
	}

	return bestMatch
}

// matchMAC finds a matching MAC vendor entry.
func (m *TemplateMatcher) matchMAC(mac string) *MACVendorEntry {
	if mac == "" {
		return nil
	}

	// Normalize MAC address to uppercase with colons
	normalized := normalizeMAC(mac)
	if len(normalized) < 8 {
		return nil
	}

	oui := normalized[:8] // First 8 chars = "XX:XX:XX"

	for i := range m.templates.MACVendors {
		entry := &m.templates.MACVendors[i]
		if strings.EqualFold(entry.OUI, oui) {
			return entry
		}
	}

	return nil
}

// normalizeMAC converts MAC address to uppercase XX:XX:XX:XX:XX:XX format.
func normalizeMAC(mac string) string {
	// Remove common separators and convert to uppercase
	mac = strings.ToUpper(mac)
	mac = strings.ReplaceAll(mac, "-", ":")
	mac = strings.ReplaceAll(mac, ".", ":")

	// If no separators, add them
	if len(mac) == 12 && !strings.Contains(mac, ":") {
		return fmt.Sprintf("%s:%s:%s:%s:%s:%s",
			mac[0:2], mac[2:4], mac[4:6], mac[6:8], mac[8:10], mac[10:12])
	}

	return mac
}

// matchPorts finds the best matching port signature.
func (m *TemplateMatcher) matchPorts(ports []int) *PortSignature {
	if len(ports) == 0 {
		return nil
	}

	portSet := make(map[int]bool)
	for _, p := range ports {
		portSet[p] = true
	}

	var bestMatch *PortSignature
	var bestScore float64

	for i := range m.templates.GatewayPortSignatures {
		sig := &m.templates.GatewayPortSignatures[i]

		// Check if all required ports are present
		allPresent := true
		for _, reqPort := range sig.Ports {
			if !portSet[reqPort] {
				allPresent = false
				break
			}
		}

		if allPresent {
			// Prefer signatures with more ports and higher confidence
			score := sig.Confidence * float64(len(sig.Ports))
			if bestMatch == nil || score > bestScore {
				bestMatch = sig
				bestScore = score
			}
		}
	}

	return bestMatch
}

// matchIP checks if the IP matches any gateway patterns.
func (m *TemplateMatcher) matchIP(ip string) *IPPattern {
	if ip == "" {
		return nil
	}

	for i := range m.templates.IPPatterns {
		pattern := &m.templates.IPPatterns[i]
		if strings.HasSuffix(ip, pattern.Suffix) {
			return pattern
		}
	}

	return nil
}

// combineConfidenceScores combines multiple confidence scores.
// Uses a weighted approach that increases confidence with multiple indicators
// but caps at 1.0.
func combineConfidenceScores(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}

	if len(scores) == 1 {
		return scores[0]
	}

	// Find maximum score
	maxScore := scores[0]
	for _, s := range scores[1:] {
		if s > maxScore {
			maxScore = s
		}
	}

	// Add bonus for multiple indicators (diminishing returns)
	bonus := 0.0
	for _, s := range scores {
		if s < maxScore {
			// Each additional indicator adds a fraction of its confidence
			bonus += s * 0.2
		}
	}

	result := maxScore + bonus
	if result > 1.0 {
		result = 1.0
	}

	return result
}

// MatchDeviceFunc is a convenience function that creates a default matcher
// and matches the given device. For repeated use, prefer creating a
// TemplateMatcher instance directly.
func MatchDeviceFunc(device *DiscoveredDevice) (*MatchResult, error) {
	matcher := DefaultTemplateMatcher()
	return matcher.MatchDevice(device)
}
