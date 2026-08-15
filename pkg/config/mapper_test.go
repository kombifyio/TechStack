package config

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestMapperValidation(t *testing.T) {
	tests := []struct {
		name      string
		config    *UserConfig
		wantError bool
	}{
		{
			name: "valid single-node config",
			config: &UserConfig{
				Stack: StackIntent{
					Name:    "test-stack",
					Pattern: "single-node",
				},
				Server: ServerIntent{
					Provider: "hetzner",
					Region:   "nbg1",
					Size:     "small",
				},
				Admin: AdminIntent{
					Username: "admin",
				},
			},
			wantError: false,
		},
		{
			name: "missing stack name",
			config: &UserConfig{
				Stack: StackIntent{
					Pattern: "single-node",
				},
				Server: ServerIntent{
					Provider: "hetzner",
				},
				Admin: AdminIntent{
					Username: "admin",
				},
			},
			wantError: true,
		},
		{
			name: "missing pattern",
			config: &UserConfig{
				Stack: StackIntent{
					Name: "test",
				},
				Server: ServerIntent{
					Provider: "hetzner",
				},
				Admin: AdminIntent{
					Username: "admin",
				},
			},
			wantError: true,
		},
		{
			name: "missing provider",
			config: &UserConfig{
				Stack: StackIntent{
					Name:    "test",
					Pattern: "single-node",
				},
				Server: ServerIntent{},
				Admin: AdminIntent{
					Username: "admin",
				},
			},
			wantError: true,
		},
		{
			name: "missing admin username",
			config: &UserConfig{
				Stack: StackIntent{
					Name:    "test",
					Pattern: "single-node",
				},
				Server: ServerIntent{
					Provider: "hetzner",
				},
				Admin: AdminIntent{},
			},
			wantError: true,
		},
	}

	mapper := NewMapper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mapper.MapToKombinationSpec(tt.config)
			if (err != nil) != tt.wantError {
				t.Errorf("MapToKombinationSpec() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestMapToKombinationSpec(t *testing.T) {
	mapper := NewMapper()

	userConfig := &UserConfig{
		Stack: StackIntent{
			Name:    "my-stack",
			Pattern: "single-node",
		},
		Server: ServerIntent{
			Provider: "hetzner",
			Region:   "nbg1",
			Size:     "small",
		},
		Services: ServicesIntent{
			VPN:     "headscale",
			Backend: "pocketbase",
			PaaS:    "caprover",
		},
		Admin: AdminIntent{
			Username:   "kombi",
			SSHKeyPath: "~/.ssh/id_ed25519.pub",
			Email:      "admin@example.com",
		},
		Network: NetworkIntent{
			Domain:          "kombi.local",
			EnablePublicWeb: true,
		},
	}

	spec, err := mapper.MapToKombinationSpec(userConfig)
	if err != nil {
		t.Fatalf("MapToKombinationSpec() failed: %v", err)
	}

	// Verify basic fields
	if spec.Name != "my-stack" {
		t.Errorf("Name = %v, want %v", spec.Name, "my-stack")
	}

	// Verify node configuration
	if len(spec.Nodes) != 1 {
		t.Fatalf("Expected 1 node, got %d", len(spec.Nodes))
	}

	node := spec.Nodes[0]
	if node.Type != "main" {
		t.Errorf("Node type = %v, want %v", node.Type, "main")
	}

	if node.Provider != "hetzner" {
		t.Errorf("Node provider = %v, want %v", node.Provider, "hetzner")
	}

	// Provider-specific values are stored in Tags
	if node.Tags["server_type"] != "cx22" {
		t.Errorf("Node server_type tag = %v, want %v (mapped from 'small')", node.Tags["server_type"], "cx22")
	}

	if node.Tags["location"] != "nbg1" {
		t.Errorf("Node location tag = %v, want %v", node.Tags["location"], "nbg1")
	}

	if node.Tags["image"] != "ubuntu-24.04" {
		t.Errorf("Node image tag = %v, want %v", node.Tags["image"], "ubuntu-24.04")
	}

	// Verify services
	if len(spec.Services) != 3 {
		t.Fatalf("Expected 3 services, got %d", len(spec.Services))
	}

	// Check service types
	serviceTypes := make(map[string]bool)
	for _, svc := range spec.Services {
		serviceTypes[svc.Type] = true
	}

	expectedServices := []string{"headscale", "pocketbase", "caprover"}
	for _, expected := range expectedServices {
		if !serviceTypes[expected] {
			t.Errorf("Expected service %s not found", expected)
		}
	}

	// Verify network
	if spec.Network.VPN != "headscale" {
		t.Errorf("VPN = %v, want %v", spec.Network.VPN, "headscale")
	}

	if spec.Network.Domain != "kombi.local" {
		t.Errorf("Domain = %v, want %v", spec.Network.Domain, "kombi.local")
	}

	// Verify metadata
	if spec.Metadata["pattern"] != "single-node" {
		t.Errorf("Pattern metadata = %v, want %v", spec.Metadata["pattern"], "single-node")
	}

	if spec.Metadata["admin_username"] != "kombi" {
		t.Errorf("Admin username metadata = %v, want %v", spec.Metadata["admin_username"], "kombi")
	}
}

func TestServerSizeMapping(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		size         string
		expectedType string
	}{
		{
			name:         "hetzner small",
			provider:     "hetzner",
			size:         "small",
			expectedType: "cx22",
		},
		{
			name:         "hetzner medium",
			provider:     "hetzner",
			size:         "medium",
			expectedType: "cx32",
		},
		{
			name:         "hetzner large",
			provider:     "hetzner",
			size:         "large",
			expectedType: "cx42",
		},
		{
			name:         "unknown size uses as-is",
			provider:     "hetzner",
			size:         "custom-type",
			expectedType: "custom-type",
		},
	}

	mapper := NewMapper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userConfig := &UserConfig{
				Stack: StackIntent{
					Name:    "test",
					Pattern: "single-node",
				},
				Server: ServerIntent{
					Provider: tt.provider,
					Size:     tt.size,
					Region:   "test-region",
				},
				Admin: AdminIntent{
					Username: "test",
				},
			}

			spec, err := mapper.MapToKombinationSpec(userConfig)
			if err != nil {
				t.Fatalf("MapToKombinationSpec() failed: %v", err)
			}

			if len(spec.Nodes) == 0 {
				t.Fatal("No nodes created")
			}

			if spec.Nodes[0].Tags["server_type"] != tt.expectedType {
				t.Errorf("Node server_type tag = %v, want %v", spec.Nodes[0].Tags["server_type"], tt.expectedType)
			}
		})
	}
}

func TestServiceMapping(t *testing.T) {
	tests := []struct {
		name          string
		services      ServicesIntent
		expectedCount int
		expectedTypes []string
	}{
		{
			name: "all services enabled",
			services: ServicesIntent{
				VPN:     "headscale",
				Backend: "pocketbase",
				PaaS:    "caprover",
			},
			expectedCount: 3,
			expectedTypes: []string{"headscale", "pocketbase", "caprover"},
		},
		{
			name: "only vpn",
			services: ServicesIntent{
				VPN: "headscale",
			},
			expectedCount: 1,
			expectedTypes: []string{"headscale"},
		},
		{
			name:          "no services",
			services:      ServicesIntent{},
			expectedCount: 0,
			expectedTypes: []string{},
		},
		{
			name: "alternate paas (caprover)",
			services: ServicesIntent{
				PaaS: "caprover",
			},
			expectedCount: 1,
			expectedTypes: []string{"caprover"},
		},
	}

	mapper := NewMapper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userConfig := &UserConfig{
				Stack: StackIntent{
					Name:    "test",
					Pattern: "single-node",
				},
				Server: ServerIntent{
					Provider: "hetzner",
				},
				Services: tt.services,
				Admin: AdminIntent{
					Username: "test",
				},
			}

			spec, err := mapper.MapToKombinationSpec(userConfig)
			if err != nil {
				t.Fatalf("MapToKombinationSpec() failed: %v", err)
			}

			if len(spec.Services) != tt.expectedCount {
				t.Errorf("Service count = %d, want %d", len(spec.Services), tt.expectedCount)
			}

			// Check expected service types
			serviceMap := make(map[string]*core.ServiceSpec)
			for i := range spec.Services {
				svc := &spec.Services[i]
				serviceMap[svc.Type] = svc
			}

			for _, expectedType := range tt.expectedTypes {
				if _, found := serviceMap[expectedType]; !found {
					t.Errorf("Expected service type %s not found", expectedType)
				}
			}
		})
	}
}
