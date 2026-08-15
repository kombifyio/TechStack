package validator

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/errors"
)

func TestSpecValidator_Validate(t *testing.T) {
	validator := NewSpecValidator()

	tests := []struct {
		name    string
		spec    *core.KombinationSpec
		wantErr bool
	}{
		{
			name: "valid minimal spec",
			spec: &core.KombinationSpec{
				Name: "test-stack",
				Nodes: []core.NodeSpec{
					{
						Name:     "main",
						Type:     "main",
						Provider: "local",
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "nil spec",
			spec:    nil,
			wantErr: true,
		},
		{
			name: "missing name",
			spec: &core.KombinationSpec{
				Kit: "basement-kit",
				Nodes: []core.NodeSpec{
					{Name: "main", Type: "main", Provider: "local"},
				},
			},
			wantErr: true,
		},
		{
			name: "missing kit (auto-select)",
			spec: &core.KombinationSpec{
				Name: "test-stack",
				Nodes: []core.NodeSpec{
					{Name: "main", Type: "main", Provider: "local"},
				},
			},
			wantErr: false,
		},
		{
			name: "no nodes",
			spec: &core.KombinationSpec{
				Name:  "test-stack",
				Kit:   "basement-kit",
				Nodes: []core.NodeSpec{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSpecValidator_validateName(t *testing.T) {
	validator := NewSpecValidator()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid name", "test-stack", false},
		{"valid simple", "test", false},
		{"valid with numbers", "test-123", false},
		{"empty name", "", true},
		{"too long", string(make([]byte, 100)), true},
		{"uppercase", "TestStack", true},
		{"spaces", "test stack", true},
		{"special chars", "test_stack", true},
		{"starts with hyphen", "-test", true},
		{"ends with hyphen", "test-", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateName() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSpecValidator_validateKit(t *testing.T) {
	validator := NewSpecValidator()

	tests := []struct {
		name    string
		kit     string
		wantErr bool
	}{
		{"basement-kit", "basement-kit", false},
		{"homelab-starter (legacy alias)", "homelab-starter", false},
		{"custom kit name", "my-custom-kit", false},
		{"empty (auto-select)", "", false},
		{"path traversal", "../evil", true},
		{"path separator", "evil/kit", true},
		{"uppercase invalid", "InvalidKit", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateKit(tt.kit)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateKit() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSpecValidator_validateNodes(t *testing.T) {
	validator := NewSpecValidator()

	tests := []struct {
		name    string
		nodes   []core.NodeSpec
		wantErr bool
	}{
		{
			name: "valid single node",
			nodes: []core.NodeSpec{
				{Name: "main", Type: "main", Provider: "local"},
			},
			wantErr: false,
		},
		{
			name: "valid multiple nodes",
			nodes: []core.NodeSpec{
				{Name: "main", Type: "main", Provider: "local"},
				{Name: "worker-1", Type: "worker", Provider: "docker"},
			},
			wantErr: false,
		},
		{
			name:    "empty nodes",
			nodes:   []core.NodeSpec{},
			wantErr: true,
		},
		{
			name: "duplicate names",
			nodes: []core.NodeSpec{
				{Name: "main", Type: "main", Provider: "local"},
				{Name: "main", Type: "worker", Provider: "docker"},
			},
			wantErr: true,
		},
		{
			name: "missing node name",
			nodes: []core.NodeSpec{
				{Name: "", Type: "main", Provider: "local"},
			},
			wantErr: true,
		},
		{
			name: "missing node type",
			nodes: []core.NodeSpec{
				{Name: "main", Type: "", Provider: "local"},
			},
			wantErr: true,
		},
		{
			name: "invalid node type",
			nodes: []core.NodeSpec{
				{Name: "main", Type: "invalid", Provider: "local"},
			},
			wantErr: true,
		},
		{
			name: "missing provider",
			nodes: []core.NodeSpec{
				{Name: "main", Type: "main", Provider: ""},
			},
			wantErr: true,
		},
		{
			name: "invalid provider",
			nodes: []core.NodeSpec{
				{Name: "main", Type: "main", Provider: "invalid"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateNodes(tt.nodes)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateNodes() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSpecValidator_validateSSHConfig(t *testing.T) {
	validator := NewSpecValidator()

	tests := []struct {
		name    string
		ssh     *core.SSHConfig
		wantErr bool
	}{
		{
			name: "valid ssh config",
			ssh: &core.SSHConfig{
				Host: "192.168.1.1",
				Port: 22,
				User: "admin",
			},
			wantErr: false,
		},
		{
			name: "missing host",
			ssh: &core.SSHConfig{
				Port: 22,
				User: "admin",
			},
			wantErr: true,
		},
		{
			name: "missing user",
			ssh: &core.SSHConfig{
				Host: "192.168.1.1",
				Port: 22,
			},
			wantErr: true,
		},
		{
			name: "invalid port negative",
			ssh: &core.SSHConfig{
				Host: "192.168.1.1",
				Port: -1,
				User: "admin",
			},
			wantErr: true,
		},
		{
			name: "invalid port too high",
			ssh: &core.SSHConfig{
				Host: "192.168.1.1",
				Port: 70000,
				User: "admin",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateSSHConfig(tt.ssh, "test")
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSSHConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSpecValidator_validateServices(t *testing.T) {
	validator := NewSpecValidator()

	nodes := []core.NodeSpec{
		{Name: "main", Type: "main", Provider: "local"},
		{Name: "worker", Type: "worker", Provider: "docker"},
	}

	tests := []struct {
		name     string
		services []core.ServiceSpec
		nodes    []core.NodeSpec
		wantErr  bool
	}{
		{
			name: "valid services",
			services: []core.ServiceSpec{
				{Name: "nginx", Type: "reverse-proxy", Node: "main"},
				{Name: "app", Type: "docker", Node: "worker"},
			},
			nodes:   nodes,
			wantErr: false,
		},
		{
			name: "service with dependencies",
			services: []core.ServiceSpec{
				{Name: "database", Type: "postgres", Node: "main"},
				{Name: "app", Type: "docker", Node: "worker", Needs: []string{"database"}},
			},
			nodes:   nodes,
			wantErr: false,
		},
		{
			name: "duplicate service names",
			services: []core.ServiceSpec{
				{Name: "app", Type: "docker", Node: "main"},
				{Name: "app", Type: "docker", Node: "worker"},
			},
			nodes:   nodes,
			wantErr: true,
		},
		{
			name: "missing service name",
			services: []core.ServiceSpec{
				{Name: "", Type: "docker", Node: "main"},
			},
			nodes:   nodes,
			wantErr: true,
		},
		{
			name: "missing service type",
			services: []core.ServiceSpec{
				{Name: "app", Type: "", Node: "main"},
			},
			nodes:   nodes,
			wantErr: true,
		},
		{
			name: "missing node reference",
			services: []core.ServiceSpec{
				{Name: "app", Type: "docker", Node: ""},
			},
			nodes:   nodes,
			wantErr: true,
		},
		{
			name: "invalid node reference",
			services: []core.ServiceSpec{
				{Name: "app", Type: "docker", Node: "nonexistent"},
			},
			nodes:   nodes,
			wantErr: true,
		},
		{
			name: "self dependency",
			services: []core.ServiceSpec{
				{Name: "app", Type: "docker", Node: "main", Needs: []string{"app"}},
			},
			nodes:   nodes,
			wantErr: true,
		},
		{
			name: "circular dependency",
			services: []core.ServiceSpec{
				{Name: "app1", Type: "docker", Node: "main", Needs: []string{"app2"}},
				{Name: "app2", Type: "docker", Node: "worker", Needs: []string{"app1"}},
			},
			nodes:   nodes,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateServices(tt.services, tt.nodes)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateServices() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSpecValidator_validateNetwork(t *testing.T) {
	validator := NewSpecValidator()

	tests := []struct {
		name    string
		network *core.NetworkSpec
		wantErr bool
	}{
		{
			name:    "nil network",
			network: nil,
			wantErr: false,
		},
		{
			name: "valid network with headscale",
			network: &core.NetworkSpec{
				VPN:    "headscale",
				Domain: "homelab.local",
			},
			wantErr: false,
		},
		{
			name: "valid network with wireguard",
			network: &core.NetworkSpec{
				VPN:    "wireguard",
				Domain: "example.com",
			},
			wantErr: false,
		},
		{
			name: "invalid VPN type",
			network: &core.NetworkSpec{
				VPN: "openvpn",
			},
			wantErr: true,
		},
		{
			name: "invalid domain format",
			network: &core.NetworkSpec{
				Domain: "invalid domain!",
			},
			wantErr: true,
		},
		{
			name: "domain too long",
			network: &core.NetworkSpec{
				Domain: string(make([]byte, 300)),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateNetwork(tt.network)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateNetwork() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{"empty version", "", false},
		{"valid version", "1.0.0", false},
		{"valid version with v", "v1.0.0", false},
		{"valid version with suffix", "1.0.0-beta", false},
		{"invalid version", "1.0", true},
		{"invalid version", "1", true},
		{"invalid version", "abc", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVersion(tt.version)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateVersion() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		wantErr  bool
	}{
		{
			name:     "nil metadata",
			metadata: nil,
			wantErr:  false,
		},
		{
			name:     "empty metadata",
			metadata: map[string]string{},
			wantErr:  false,
		},
		{
			name: "valid metadata",
			metadata: map[string]string{
				"environment": "production",
				"owner":       "team-a",
			},
			wantErr: false,
		},
		{
			name: "invalid key format",
			metadata: map[string]string{
				"Invalid_Key": "value",
			},
			wantErr: true,
		},
		{
			name: "value too long",
			metadata: map[string]string{
				"key": string(make([]byte, 2000)),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMetadata(tt.metadata)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateMetadata() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCircularDependencyDetection(t *testing.T) {
	validator := NewSpecValidator()

	tests := []struct {
		name     string
		services []core.ServiceSpec
		wantErr  bool
	}{
		{
			name: "no dependencies",
			services: []core.ServiceSpec{
				{Name: "app1", Type: "docker", Node: "main"},
				{Name: "app2", Type: "docker", Node: "main"},
			},
			wantErr: false,
		},
		{
			name: "linear dependencies",
			services: []core.ServiceSpec{
				{Name: "db", Type: "postgres", Node: "main"},
				{Name: "api", Type: "docker", Node: "main", Needs: []string{"db"}},
				{Name: "frontend", Type: "docker", Node: "main", Needs: []string{"api"}},
			},
			wantErr: false,
		},
		{
			name: "simple circular dependency",
			services: []core.ServiceSpec{
				{Name: "app1", Type: "docker", Node: "main", Needs: []string{"app2"}},
				{Name: "app2", Type: "docker", Node: "main", Needs: []string{"app1"}},
			},
			wantErr: true,
		},
		{
			name: "complex circular dependency",
			services: []core.ServiceSpec{
				{Name: "app1", Type: "docker", Node: "main", Needs: []string{"app2"}},
				{Name: "app2", Type: "docker", Node: "main", Needs: []string{"app3"}},
				{Name: "app3", Type: "docker", Node: "main", Needs: []string{"app1"}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.checkCircularDependencies(tt.services)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkCircularDependencies() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMultipleValidationErrors(t *testing.T) {
	validator := NewSpecValidator()

	// Spec with multiple errors
	spec := &core.KombinationSpec{
		Name: "", // Error: missing name
		Kit:  "", // Kit is optional (auto-select), no error expected
		Nodes: []core.NodeSpec{
			{Name: "node1", Type: "invalid", Provider: "local"}, // Error: invalid type
			{Name: "node2", Type: "main", Provider: ""},         // Error: missing provider
		},
	}

	err := validator.Validate(spec)
	if err == nil {
		t.Fatal("expected validation errors, got nil")
	}

	// Check if it's a MultiError
	me, ok := err.(*errors.MultiError)
	if !ok {
		t.Fatalf("expected MultiError, got %T", err)
	}

	// Should have multiple errors (name + node issues)
	if len(me.Errors) < 2 {
		t.Errorf("expected at least 2 errors, got %d", len(me.Errors))
	}
}

func TestComplexSpec(t *testing.T) {
	validator := NewSpecValidator()

	spec := &core.KombinationSpec{
		Name:    "my-homelab",
		Version: "1.0.0",
		Kit:     "cloud-kit",
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Type:     "main",
				Provider: "hetzner",
				SSH: &core.SSHConfig{
					Host: "192.168.1.100",
					Port: 22,
					User: "admin",
				},
			},
			{
				Name:     "worker-1",
				Type:     "worker",
				Provider: "docker",
			},
		},
		Services: []core.ServiceSpec{
			{Name: "traefik", Type: "reverse-proxy", Node: "main-server"},
			{Name: "postgres", Type: "database", Node: "main-server"},
			{Name: "redis", Type: "cache", Node: "main-server"},
			{Name: "api", Type: "docker", Node: "worker-1", Needs: []string{"postgres", "redis"}},
			{Name: "frontend", Type: "docker", Node: "worker-1", Needs: []string{"api"}},
		},
		Network: core.NetworkSpec{
			VPN:    "headscale",
			Domain: "homelab.local",
		},
		Metadata: map[string]string{
			"environment": "production",
			"owner":       "admin",
			"created":     "2026-01-03",
		},
	}

	err := validator.Validate(spec)
	if err != nil {
		t.Errorf("expected valid spec, got error: %v", err)
	}
}
