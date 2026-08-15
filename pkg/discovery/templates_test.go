package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileTemplateLoader(t *testing.T) {
	// Create a temporary YAML file
	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "test-templates.yaml")

	yamlContent := `
version: "1.0"
description: "Test templates"
hostname_patterns:
  - pattern: "test-router"
    vendor: "TestVendor"
    type: "router"
    confidence: 0.9
mac_vendors:
  - oui: "AA:BB:CC"
    vendor: "TestMAC"
    type: "router"
gateway_port_signatures:
  - name: "test-sig"
    ports: [53, 80]
    confidence: 0.85
ip_patterns:
  - suffix: ".1"
    confidence: 0.7
`
	if err := os.WriteFile(tempFile, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	loader := NewFileTemplateLoader(tempFile)
	templates, err := loader.Load()
	if err != nil {
		t.Fatalf("Failed to load templates: %v", err)
	}

	if templates.Version != "1.0" {
		t.Errorf("Expected version 1.0, got %s", templates.Version)
	}

	if len(templates.HostnamePatterns) != 1 {
		t.Errorf("Expected 1 hostname pattern, got %d", len(templates.HostnamePatterns))
	}

	if templates.HostnamePatterns[0].Pattern != "test-router" {
		t.Errorf("Expected pattern 'test-router', got %s", templates.HostnamePatterns[0].Pattern)
	}
}

func TestFileTemplateLoader_FileNotFound(t *testing.T) {
	loader := NewFileTemplateLoader("/nonexistent/path/templates.yaml")
	_, err := loader.Load()
	if err == nil {
		t.Error("Expected error for nonexistent file, got nil")
	}
}

func TestFileTemplateLoader_InvalidYAML(t *testing.T) {
	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "invalid.yaml")

	if err := os.WriteFile(tempFile, []byte("invalid: yaml: content: ["), 0o644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	loader := NewFileTemplateLoader(tempFile)
	_, err := loader.Load()
	if err == nil {
		t.Error("Expected error for invalid YAML, got nil")
	}
}

func TestEmbeddedTemplateLoader(t *testing.T) {
	loader := NewEmbeddedTemplateLoader()
	templates, err := loader.Load()
	if err != nil {
		t.Fatalf("Failed to load embedded templates: %v", err)
	}

	if templates.Version == "" {
		t.Error("Expected non-empty version")
	}

	if len(templates.HostnamePatterns) == 0 {
		t.Error("Expected at least one hostname pattern in embedded templates")
	}

	// Verify some expected patterns exist
	foundFritz := false
	for _, p := range templates.HostnamePatterns {
		if p.Pattern == "fritz" {
			foundFritz = true
			break
		}
	}
	if !foundFritz {
		t.Error("Expected 'fritz' pattern in embedded templates")
	}
}

func TestRouterTemplates_Validate(t *testing.T) {
	tests := []struct {
		name        string
		templates   *RouterTemplates
		expectError bool
	}{
		{
			name: "valid templates",
			templates: &RouterTemplates{
				HostnamePatterns: []HostnamePattern{
					{Pattern: "test", Confidence: 0.8},
				},
				MACVendors: []MACVendorEntry{
					{OUI: "AA:BB:CC", Vendor: "Test"},
				},
				GatewayPortSignatures: []PortSignature{
					{Name: "test", Ports: []int{53}, Confidence: 0.9},
				},
				IPPatterns: []IPPattern{
					{Suffix: ".1", Confidence: 0.7},
				},
			},
			expectError: false,
		},
		{
			name: "empty hostname pattern",
			templates: &RouterTemplates{
				HostnamePatterns: []HostnamePattern{
					{Pattern: "", Confidence: 0.8},
				},
			},
			expectError: true,
		},
		{
			name: "invalid confidence - negative",
			templates: &RouterTemplates{
				HostnamePatterns: []HostnamePattern{
					{Pattern: "test", Confidence: -0.1},
				},
			},
			expectError: true,
		},
		{
			name: "invalid confidence - over 1",
			templates: &RouterTemplates{
				HostnamePatterns: []HostnamePattern{
					{Pattern: "test", Confidence: 1.5},
				},
			},
			expectError: true,
		},
		{
			name: "empty MAC OUI",
			templates: &RouterTemplates{
				MACVendors: []MACVendorEntry{
					{OUI: "", Vendor: "Test"},
				},
			},
			expectError: true,
		},
		{
			name: "invalid MAC OUI format",
			templates: &RouterTemplates{
				MACVendors: []MACVendorEntry{
					{OUI: "AABBCC", Vendor: "Test"},
				},
			},
			expectError: true,
		},
		{
			name: "empty port signature name",
			templates: &RouterTemplates{
				GatewayPortSignatures: []PortSignature{
					{Name: "", Ports: []int{53}, Confidence: 0.9},
				},
			},
			expectError: true,
		},
		{
			name: "empty ports in signature",
			templates: &RouterTemplates{
				GatewayPortSignatures: []PortSignature{
					{Name: "test", Ports: []int{}, Confidence: 0.9},
				},
			},
			expectError: true,
		},
		{
			name: "empty IP suffix",
			templates: &RouterTemplates{
				IPPatterns: []IPPattern{
					{Suffix: "", Confidence: 0.7},
				},
			},
			expectError: true,
		},
		{
			name: "IP suffix without dot",
			templates: &RouterTemplates{
				IPPatterns: []IPPattern{
					{Suffix: "1", Confidence: 0.7},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.templates.Validate()
			if tt.expectError && err == nil {
				t.Error("Expected validation error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected validation error: %v", err)
			}
		})
	}
}

func TestTemplateMatcher_MatchHostname(t *testing.T) {
	matcher := NewTemplateMatcher(NewEmbeddedTemplateLoader())

	tests := []struct {
		hostname     string
		expectMatch  bool
		expectVendor string
	}{
		{"fritz.box", true, "AVM"},
		{"FritzBox-7590", true, "AVM"},
		{"FRITZ!Box 7530", true, "AVM"},
		{"speedport.ip", true, "Telekom"},
		{"UniFi-Dream-Machine", true, "Ubiquiti"},
		{"mikrotik-rb4011", true, "MikroTik"},
		{"openwrt-router", true, "OpenWrt"},
		{"pfsense.localdomain", true, "Netgate"},
		{"my-desktop-pc", false, ""},
		{"nas-server", false, ""},
		{"", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.hostname, func(t *testing.T) {
			device := &DiscoveredDevice{
				IP:       "192.168.1.100",
				Hostname: tt.hostname,
			}
			result, err := matcher.MatchDevice(device)
			if err != nil {
				t.Fatalf("MatchDevice failed: %v", err)
			}

			if tt.expectMatch {
				if result.Vendor != tt.expectVendor {
					t.Errorf("Expected vendor %s, got %s", tt.expectVendor, result.Vendor)
				}
				if len(result.Reasons) == 0 {
					t.Error("Expected at least one reason for match")
				}
			}
		})
	}
}

func TestTemplateMatcher_MatchMAC(t *testing.T) {
	matcher := NewTemplateMatcher(NewEmbeddedTemplateLoader())

	tests := []struct {
		mac          string
		expectVendor string
	}{
		{"00:1F:3F:12:34:56", "AVM"},
		{"00-1F-3F-12-34-56", "AVM"}, // Dash separator
		{"001f3f123456", "AVM"},      // No separator
		{"B0:4E:26:AA:BB:CC", "Ubiquiti"},
		{"00:0C:42:11:22:33", "MikroTik"},
		{"00:14:6C:AA:BB:CC", "Netgear"},
		{"14:CC:20:12:34:56", "TP-Link"},
		{"AA:BB:CC:DD:EE:FF", ""}, // Unknown OUI
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.mac, func(t *testing.T) {
			device := &DiscoveredDevice{
				IP:  "192.168.1.100",
				MAC: tt.mac,
			}
			result, err := matcher.MatchDevice(device)
			if err != nil {
				t.Fatalf("MatchDevice failed: %v", err)
			}

			if tt.expectVendor != "" {
				if result.Vendor != tt.expectVendor {
					t.Errorf("Expected vendor %s for MAC %s, got %s", tt.expectVendor, tt.mac, result.Vendor)
				}
			}
		})
	}
}

func TestTemplateMatcher_MatchPorts(t *testing.T) {
	matcher := NewTemplateMatcher(NewEmbeddedTemplateLoader())

	tests := []struct {
		name          string
		ports         []int
		expectGateway bool
		minConfidence float64
	}{
		{
			name:          "standard gateway (DNS + DHCP)",
			ports:         []int{22, 53, 67, 80},
			expectGateway: true,
			minConfidence: 0.8,
		},
		{
			name:          "UPnP gateway (DNS + UPnP)",
			ports:         []int{53, 1900, 443},
			expectGateway: true,
			minConfidence: 0.7,
		},
		{
			name:          "full gateway",
			ports:         []int{53, 67, 80, 443},
			expectGateway: true,
			minConfidence: 0.9,
		},
		{
			name:          "DNS only",
			ports:         []int{53},
			expectGateway: false, // DNS alone has low confidence
			minConfidence: 0.3,
		},
		{
			name:          "DHCP only",
			ports:         []int{67},
			expectGateway: true, // DHCP is strong indicator
			minConfidence: 0.7,
		},
		{
			name:          "no gateway ports",
			ports:         []int{22, 80, 443},
			expectGateway: false,
			minConfidence: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device := &DiscoveredDevice{
				IP:    "192.168.1.100",
				Ports: tt.ports,
			}
			result, err := matcher.MatchDevice(device)
			if err != nil {
				t.Fatalf("MatchDevice failed: %v", err)
			}

			if tt.expectGateway && !result.IsGateway {
				t.Errorf("Expected device to be identified as gateway")
			}
			if !tt.expectGateway && result.IsGateway {
				t.Errorf("Expected device NOT to be identified as gateway")
			}
			if result.Confidence < tt.minConfidence {
				t.Errorf("Expected confidence >= %f, got %f", tt.minConfidence, result.Confidence)
			}
		})
	}
}

func TestTemplateMatcher_MatchIP(t *testing.T) {
	matcher := NewTemplateMatcher(NewEmbeddedTemplateLoader())

	tests := []struct {
		ip            string
		expectPattern bool
		minConfidence float64
	}{
		{"192.168.1.1", true, 0.6},
		{"10.0.0.1", true, 0.6},
		{"192.168.1.254", true, 0.5},
		{"192.168.1.100", false, 0},
		{"192.168.1.50", false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			device := &DiscoveredDevice{
				IP: tt.ip,
			}
			result, err := matcher.MatchDevice(device)
			if err != nil {
				t.Fatalf("MatchDevice failed: %v", err)
			}

			if tt.expectPattern && result.Confidence < tt.minConfidence {
				t.Errorf("Expected confidence >= %f for IP %s, got %f", tt.minConfidence, tt.ip, result.Confidence)
			}
		})
	}
}

func TestTemplateMatcher_CombinedMatching(t *testing.T) {
	matcher := NewTemplateMatcher(NewEmbeddedTemplateLoader())

	// Device with multiple indicators should have higher confidence
	device := &DiscoveredDevice{
		IP:       "192.168.1.1",
		MAC:      "00:1F:3F:12:34:56", // AVM
		Hostname: "fritz.box",
		Ports:    []int{53, 67, 80, 443},
	}

	result, err := matcher.MatchDevice(device)
	if err != nil {
		t.Fatalf("MatchDevice failed: %v", err)
	}

	if !result.IsGateway {
		t.Error("Expected device to be identified as gateway")
	}

	if result.Vendor != "AVM" {
		t.Errorf("Expected vendor AVM, got %s", result.Vendor)
	}

	// Should have high confidence with multiple indicators
	if result.Confidence < 0.9 {
		t.Errorf("Expected high confidence (>=0.9) with multiple indicators, got %f", result.Confidence)
	}

	// Should have multiple reasons
	if len(result.Reasons) < 3 {
		t.Errorf("Expected at least 3 reasons, got %d: %v", len(result.Reasons), result.Reasons)
	}
}

func TestCombineConfidenceScores(t *testing.T) {
	tests := []struct {
		name      string
		scores    []float64
		expected  float64
		tolerance float64
	}{
		{
			name:      "single score",
			scores:    []float64{0.8},
			expected:  0.8,
			tolerance: 0.01,
		},
		{
			name:      "two scores",
			scores:    []float64{0.8, 0.6},
			expected:  0.92, // 0.8 + 0.6*0.2
			tolerance: 0.01,
		},
		{
			name:      "multiple scores capped at 1.0",
			scores:    []float64{0.95, 0.9, 0.85, 0.8},
			expected:  1.0,
			tolerance: 0.01,
		},
		{
			name:      "empty scores",
			scores:    []float64{},
			expected:  0,
			tolerance: 0.01,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := combineConfidenceScores(tt.scores)
			if result < tt.expected-tt.tolerance || result > tt.expected+tt.tolerance {
				t.Errorf("Expected %f (±%f), got %f", tt.expected, tt.tolerance, result)
			}
		})
	}
}

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"00:1F:3F:12:34:56", "00:1F:3F:12:34:56"},
		{"00-1f-3f-12-34-56", "00:1F:3F:12:34:56"},
		{"001f3f123456", "00:1F:3F:12:34:56"},
		{"AA:BB:CC:DD:EE:FF", "AA:BB:CC:DD:EE:FF"},
		{"aabbccddeeff", "AA:BB:CC:DD:EE:FF"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeMAC(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeMAC(%s) = %s, expected %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestTemplateMatcher_ThreadSafety(t *testing.T) {
	matcher := NewTemplateMatcher(NewEmbeddedTemplateLoader())

	// Run multiple goroutines accessing the matcher concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				device := &DiscoveredDevice{
					IP:       "192.168.1.1",
					Hostname: "fritz.box",
					Ports:    []int{53, 67},
				}
				_, err := matcher.MatchDevice(device)
				if err != nil {
					t.Errorf("Goroutine %d: MatchDevice failed: %v", id, err)
				}
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestMatchDeviceFunc(t *testing.T) {
	device := &DiscoveredDevice{
		IP:       "192.168.1.1",
		Hostname: "fritz.box",
	}

	result, err := MatchDeviceFunc(device)
	if err != nil {
		t.Fatalf("MatchDeviceFunc failed: %v", err)
	}

	if result.Vendor != "AVM" {
		t.Errorf("Expected vendor AVM, got %s", result.Vendor)
	}
}

func TestTemplateMatcher_ServicesAsPorts(t *testing.T) {
	matcher := NewTemplateMatcher(NewEmbeddedTemplateLoader())

	// Test that services are used when ports array is empty
	device := &DiscoveredDevice{
		IP: "192.168.1.1",
		Services: []DiscoveredService{
			{Name: "dns", Port: 53},
			{Name: "dhcp", Port: 67},
			{Name: "http", Port: 80},
		},
	}

	result, err := matcher.MatchDevice(device)
	if err != nil {
		t.Fatalf("MatchDevice failed: %v", err)
	}

	if !result.IsGateway {
		t.Error("Expected device with DNS+DHCP services to be identified as gateway")
	}
}

func TestDefaultTemplateMatcher(t *testing.T) {
	matcher := DefaultTemplateMatcher()

	// Should load without error (falling back to embedded templates)
	device := &DiscoveredDevice{
		IP:       "192.168.1.1",
		Hostname: "router",
	}

	result, err := matcher.MatchDevice(device)
	if err != nil {
		t.Fatalf("MatchDevice failed: %v", err)
	}

	if result == nil {
		t.Error("Expected non-nil result")
	}
}
