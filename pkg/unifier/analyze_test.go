package unifier

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestAnalyze_NilSpec(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	// Robustheit: nil spec should NOT error, should return defaults
	result, err := engine.Analyze(nil)
	if err != nil {
		t.Fatalf("Analyze(nil) should never error, got: %v", err)
	}
	if result == nil {
		t.Fatal("Analyze(nil) should never return nil result")
	}

	// Check defaults are applied
	if result.StackKit != "basement-kit" {
		t.Errorf("Expected default StackKit 'basement-kit', got '%s'", result.StackKit)
	}
	if result.RequiredWorkers.MinLocalServers < 1 {
		t.Error("Expected at least 1 local server for nil spec")
	}
}

func TestAnalyze_EmptySpec(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "empty-test",
	}

	result, err := engine.Analyze(spec)
	if err != nil {
		t.Fatalf("Analyze should never error, got: %v", err)
	}

	if result.IntentName != "empty-test" {
		t.Errorf("Expected IntentName 'empty-test', got '%s'", result.IntentName)
	}
	if result.StackKit != "basement-kit" {
		t.Errorf("Expected auto-selected 'basement-kit', got '%s'", result.StackKit)
	}
	if result.AppliedDefaults["autoSelectedKit"] != "basement-kit" {
		t.Error("Expected AppliedDefaults to record auto-selected kit")
	}
}

func TestAnalyze_ExplicitKit(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "explicit-kit-test",
		Kit:  "cloud-kit",
	}

	result, err := engine.Analyze(spec)
	if err != nil {
		t.Fatalf("Analyze should never error, got: %v", err)
	}

	if result.StackKit != "cloud-kit" {
		t.Errorf("Expected explicit kit 'cloud-kit', got '%s'", result.StackKit)
	}
	// Should NOT have autoSelectedKit in defaults when explicit
	if _, ok := result.AppliedDefaults["autoSelectedKit"]; ok {
		t.Error("Should not have autoSelectedKit when kit is explicit")
	}
}

func TestAnalyze_ServiceBasedKitSelection(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tests := []struct {
		name        string
		services    []core.ServiceSpec
		expectedKit string
	}{
		{
			name:        "no services",
			services:    nil,
			expectedKit: "basement-kit",
		},
		{
			name: "single service",
			services: []core.ServiceSpec{
				{Name: "nginx", Type: "reverse-proxy"},
			},
			expectedKit: "basement-kit",
		},
		{
			name: "multiple services with database",
			services: []core.ServiceSpec{
				{Name: "traefik", Type: "reverse-proxy"},
				{Name: "postgres", Type: "database"},
				{Name: "app", Type: "docker"},
			},
			expectedKit: "basement-kit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &core.KombinationSpec{
				Name:     tt.name,
				Services: tt.services,
			}

			result, err := engine.Analyze(spec)
			if err != nil {
				t.Fatalf("Analyze should never error, got: %v", err)
			}

			if result.StackKit != tt.expectedKit {
				t.Errorf("Expected kit '%s', got '%s'", tt.expectedKit, result.StackKit)
			}
		})
	}
}

func TestAnalyze_WorkerRequirements(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "worker-test",
		Nodes: []core.NodeSpec{
			{Name: "main", Type: "main", Provider: "hetzner"},
			{Name: "local1", Type: "worker", Provider: "local"},
		},
		Services: []core.ServiceSpec{
			{Name: "postgres", Type: "postgres"},
			{Name: "grafana", Type: "grafana"},
		},
	}

	result, err := engine.Analyze(spec)
	if err != nil {
		t.Fatalf("Analyze should never error, got: %v", err)
	}

	if result.RequiredWorkers.MinCloudServers != 1 {
		t.Errorf("Expected 1 cloud server, got %d", result.RequiredWorkers.MinCloudServers)
	}
	if result.RequiredWorkers.MinLocalServers != 1 {
		t.Errorf("Expected 1 local server, got %d", result.RequiredWorkers.MinLocalServers)
	}
	if result.RequiredWorkers.MinRAM < 1500 {
		t.Errorf("Expected elevated RAM requirement for stack services, got %d MB", result.RequiredWorkers.MinRAM)
	}
	if len(result.RequiredWorkers.SpecialRequirements) != 0 {
		t.Error("Did not expect special GPU requirements for the selected services")
	}
}

func TestAnalyze_ManagedMonthlyRuntimeRequirements(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	for _, provider := range []string{"centron", "ionos"} {
		t.Run(provider, func(t *testing.T) {
			spec := &core.KombinationSpec{
				Name: "managed-runtime-test",
				Nodes: []core.NodeSpec{
					{Name: "main", Type: "main", Provider: provider},
				},
				Metadata: map[string]string{
					"server_mode":  "monthly-runtime",
					"runtime_lane": "monthly-runtime",
				},
				Services: []core.ServiceSpec{
					{Name: "coolify", Type: "paas"},
					{Name: "pocket-id", Type: "auth"},
					{Name: "tinyauth", Type: "auth"},
					{Name: "whoami", Type: "service"},
				},
			}

			result, err := engine.Analyze(spec)
			if err != nil {
				t.Fatalf("Analyze should never error, got: %v", err)
			}
			if result.RequiredWorkers.MinCloudServers != 1 {
				t.Fatalf("MinCloudServers = %d, want 1", result.RequiredWorkers.MinCloudServers)
			}
			if result.RequiredWorkers.MinLocalServers != 0 {
				t.Fatalf("MinLocalServers = %d, want 0 for managed runtime", result.RequiredWorkers.MinLocalServers)
			}
			if result.RequiredWorkers.MinCPU > 4 {
				t.Fatalf("MinCPU = %d, want <= 4 so the managed premium runtime satisfies the selected cloud-kit services", result.RequiredWorkers.MinCPU)
			}
			if result.RequiredWorkers.MinRAM > 16*1024 {
				t.Fatalf("MinRAM = %dMB, want <= 16384MB so the managed premium runtime satisfies the selected cloud-kit services", result.RequiredWorkers.MinRAM)
			}
			addons := map[string]bool{}
			for _, addon := range result.DetectedAddons {
				addons[addon] = true
			}
			if !addons["managed-vm-lease"] {
				t.Fatalf("DetectedAddons missing managed-vm-lease: %v", result.DetectedAddons)
			}
			if !addons["authentication"] {
				t.Fatalf("DetectedAddons missing authentication: %v", result.DetectedAddons)
			}
		})
	}
}

func TestAnalyze_ServiceRequirementsPreferKnownTypeForCustomNames(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "custom-named-database",
		Services: []core.ServiceSpec{
			{Name: "primary", Type: "postgres"},
		},
	}

	result, err := engine.Analyze(spec)
	if err != nil {
		t.Fatalf("Analyze should never error, got: %v", err)
	}
	if result.RequiredWorkers.MinCPU < 4 {
		t.Fatalf("MinCPU = %d, want database type requirements to apply despite custom service name", result.RequiredWorkers.MinCPU)
	}
	if result.RequiredWorkers.MinRAM < 3072 {
		t.Fatalf("MinRAM = %dMB, want database type requirements to apply despite custom service name", result.RequiredWorkers.MinRAM)
	}
}

func TestAnalyze_CredentialDetection(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "cred-test",
		Nodes: []core.NodeSpec{
			{Name: "cloud1", Type: "main", Provider: "hetzner"},
		},
		Services: []core.ServiceSpec{
			{Name: "tunnel", Type: "cloudflare-tunnel"},
		},
	}

	result, err := engine.Analyze(spec)
	if err != nil {
		t.Fatalf("Analyze should never error, got: %v", err)
	}

	// Should require Hetzner and Cloudflare credentials
	credKeys := make(map[string]bool)
	for _, c := range result.RequiredCredentials {
		credKeys[c.Key] = true
	}

	if !credKeys["hetzner_api_token"] {
		t.Error("Expected hetzner_api_token credential requirement")
	}
	if !credKeys["cloudflare_api_token"] {
		t.Error("Expected cloudflare_api_token credential requirement")
	}
}

func TestAnalyze_PreCheckDetection(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "precheck-test",
		Services: []core.ServiceSpec{
			{Name: "nginx", Type: "reverse-proxy"},
			{Name: "jellyfin", Type: "jellyfin"},
		},
	}

	result, err := engine.Analyze(spec)
	if err != nil {
		t.Fatalf("Analyze should never error, got: %v", err)
	}

	checkTypes := make(map[string]bool)
	for _, c := range result.RequiredPreChecks {
		checkTypes[c.Type] = true
	}

	if !checkTypes["docker_version"] {
		t.Error("Expected docker_version pre-check")
	}
	if !checkTypes["hardware_transcoding"] {
		t.Error("Expected hardware_transcoding pre-check for jellyfin")
	}
}

func TestAnalyze_AddonDetection(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "addon-test",
		Services: []core.ServiceSpec{
			{Name: "traefik", Type: "traefik"},
			{Name: "otel-collector", Type: "monitoring"},
			{Name: "tunnel", Type: "cloudflare"},
		},
		Network: core.NetworkSpec{
			VPN: "wireguard",
		},
	}

	result, err := engine.Analyze(spec)
	if err != nil {
		t.Fatalf("Analyze should never error, got: %v", err)
	}

	addonSet := make(map[string]bool)
	for _, a := range result.DetectedAddons {
		addonSet[a] = true
	}

	if !addonSet["reverse-proxy"] {
		t.Error("Expected reverse-proxy addon")
	}
	if !addonSet["monitoring"] {
		t.Error("Expected monitoring addon")
	}
	if !addonSet["cloudflare"] {
		t.Error("Expected cloudflare addon")
	}
	if !addonSet["vpn-wireguard"] {
		t.Error("Expected vpn-wireguard addon")
	}
}

func TestAnalyze_Description(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "my-homelab",
		Services: []core.ServiceSpec{
			{Name: "nginx", Type: "reverse-proxy"},
			{Name: "postgres", Type: "postgres"},
		},
	}

	result, err := engine.Analyze(spec)
	if err != nil {
		t.Fatalf("Analyze should never error, got: %v", err)
	}

	if result.Description == "" {
		t.Error("Expected non-empty description")
	}
	// Should contain stack name
	if !containsSubstring(result.Description, "my-homelab") {
		t.Errorf("Description should contain stack name, got: %s", result.Description)
	}
}

func TestAnalyze_AlwaysReturnsValidResult(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	// Test various edge cases that should NEVER cause errors
	testCases := []struct {
		name string
		spec *core.KombinationSpec
	}{
		{"nil", nil},
		{"empty", &core.KombinationSpec{}},
		{"name only", &core.KombinationSpec{Name: "test"}},
		{"unknown provider", &core.KombinationSpec{
			Name:  "test",
			Nodes: []core.NodeSpec{{Name: "n1", Provider: "unknown-provider-xyz"}},
		}},
		{"unknown service type", &core.KombinationSpec{
			Name:     "test",
			Services: []core.ServiceSpec{{Name: "s1", Type: "unknown-service-xyz"}},
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.Analyze(tc.spec)

			// NEVER error
			if err != nil {
				t.Fatalf("Analyze should NEVER return error, got: %v", err)
			}

			// NEVER nil
			if result == nil {
				t.Fatal("Analyze should NEVER return nil result")
			}

			// Always have valid StackKit
			if result.StackKit == "" {
				t.Error("StackKit should never be empty")
			}

			// Always have valid worker requirements
			if result.RequiredWorkers.MinRAM < 1024 {
				t.Error("MinRAM should be at least 1024 MB")
			}
			if result.RequiredWorkers.MinCPU < 1 {
				t.Error("MinCPU should be at least 1")
			}
		})
	}
}

// containsSubstring checks if s contains substr
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstringHelper(s, substr))
}

func containsSubstringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
