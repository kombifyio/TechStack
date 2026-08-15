package unifier

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestNew(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if engine == nil {
		t.Fatal("New() returned nil engine")
	}
	if engine.ctx == nil {
		t.Fatal("engine.ctx is nil")
	}
}

func TestValidate_ValidSpec(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "basement-kit",
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Type:     "main",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host: "192.168.1.100",
					Port: 22,
					User: "ubuntu",
				},
			},
		},
		Services: []core.ServiceSpec{
			{
				Name: "traefik",
				Type: "reverse-proxy",
				Node: "main-server",
			},
		},
	}

	result, err := engine.Validate(spec)
	if err != nil {
		t.Fatalf("Validate() returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Validate() returned nil result")
	}
	if !result.Valid {
		t.Fatalf("expected spec to be valid, got errors: %v", result.Errors)
	}
}

func TestValidate_NilSpec(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Robustheit: Validate(nil) should NOT error.
	result, err := engine.Validate(nil)
	if err != nil {
		t.Fatalf("Validate(nil) should not error, got: %v", err)
	}
	if result == nil {
		t.Fatal("Validate(nil) should return a result")
	}
	// Nil spec is not expected to satisfy required schema fields (e.g., nodes).
	if result.Valid {
		t.Fatalf("expected Validate(nil) to be invalid due to missing required fields")
	}
	if len(result.Errors) == 0 {
		t.Fatalf("expected validation errors for nil spec")
	}
}

func TestUnify_BasicSpec(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "basement-kit",
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Type:     "main",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host: "192.168.1.100",
					Port: 22,
					User: "ubuntu",
				},
			},
		},
		Services: []core.ServiceSpec{
			{
				Name: "traefik",
				Type: "reverse-proxy",
				Node: "main-server",
			},
		},
	}

	// Pass empty workers slice for test - workers would come from DB in production
	unified, err := engine.Unify(spec, []core.Worker{})
	if err != nil {
		t.Fatalf("Unify() returned error: %v", err)
	}

	if unified == nil {
		t.Fatal("Unify() returned nil")
	}

	// Check that defaults were applied
	if len(unified.ResolvedNodes) != 1 {
		t.Errorf("expected 1 resolved node, got %d", len(unified.ResolvedNodes))
	}

	// Check SSH defaults were applied
	if unified.ResolvedNodes[0].SSH.User == "" {
		t.Log("SSH user was not auto-filled (expected for local provider: ubuntu)")
	}

	// Check service was assigned to main node
	if len(unified.ResolvedServices) != 1 {
		t.Errorf("expected 1 resolved service, got %d", len(unified.ResolvedServices))
	}

	if unified.ResolvedServices[0].Node != "main-server" {
		t.Errorf("expected service node to be 'main-server', got '%s'", unified.ResolvedServices[0].Node)
	}

	// Check network defaults
	if unified.ResolvedNetwork.VPNType != "none" {
		t.Errorf("expected VPN type 'none', got '%s'", unified.ResolvedNetwork.VPNType)
	}

	if unified.ResolvedNetwork.Domain != "" {
		t.Errorf("expected empty domain for local default, got '%s'", unified.ResolvedNetwork.Domain)
	}
}

func TestApplyNodeDefaults(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	testCases := []struct {
		name         string
		provider     string
		expectedUser string
	}{
		{"hetzner", "hetzner", "root"},
		{"aws", "aws", "ec2-user"},
		{"local", "local", "ubuntu"},
		{"gcp", "gcp", "admin"},
		{"azure", "azure", "azureuser"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			node := core.ResolvedNode{
				NodeSpec: core.NodeSpec{
					Name:     "test-node",
					Type:     "main",
					Provider: tc.provider,
					SSH:      &core.SSHConfig{Host: "1.2.3.4"},
				},
			}

			resolved := engine.applyNodeDefaults(node)

			if resolved.SSH.User != tc.expectedUser {
				t.Errorf("expected user '%s' for provider '%s', got '%s'",
					tc.expectedUser, tc.provider, resolved.SSH.User)
			}

			if resolved.SSH.Port != 22 {
				t.Errorf("expected port 22, got %d", resolved.SSH.Port)
			}
		})
	}
}

func TestApplyServiceDefaults(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	nodes := []core.NodeSpec{
		{Name: "worker-1", Type: "worker", Provider: "local"},
		{Name: "main-server", Type: "main", Provider: "local"},
	}

	// Service without explicit node should default to main
	svc := core.ResolvedService{
		ServiceSpec: core.ServiceSpec{
			Name: "test-service",
			Type: "docker",
		},
	}

	resolved := engine.applyServiceDefaults(svc, nodes)

	if resolved.Node != "main-server" {
		t.Errorf("expected service node 'main-server', got '%s'", resolved.Node)
	}
}

func TestApplyServiceDefaults_ImageResolution(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	nodes := []core.NodeSpec{
		{Name: "main", Type: "main", Provider: "local"},
	}

	tests := []struct {
		name        string
		serviceType string
		expectImage string
		expectPort  int
	}{
		{"reverse-proxy", "reverse-proxy", "traefik:v3.0", 80},
		{"database", "database", "postgres:16-alpine", 5432},
		{"cache", "cache", "redis:7-alpine", 6379},
		{"monitor", "monitor", "grafana/grafana:latest", 3000},
		{"storage", "storage", "minio/minio:latest", 9000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := core.ResolvedService{
				ServiceSpec: core.ServiceSpec{
					Name: tt.name,
					Type: tt.serviceType,
				},
			}

			resolved := engine.applyServiceDefaults(svc, nodes)

			if resolved.Image != tt.expectImage {
				t.Errorf("expected image %q, got %q", tt.expectImage, resolved.Image)
			}
			if resolved.Port != tt.expectPort {
				t.Errorf("expected port %d, got %d", tt.expectPort, resolved.Port)
			}
		})
	}
}

func TestApplyServiceDefaults_PreservesExplicitValues(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	nodes := []core.NodeSpec{
		{Name: "main", Type: "main", Provider: "local"},
	}

	// Service with explicit image and port should NOT be overridden
	svc := core.ResolvedService{
		ServiceSpec: core.ServiceSpec{
			Name: "custom-db",
			Type: "database",
			Node: "custom-node",
		},
		Image: "mysql:8",
		Port:  3306,
	}

	resolved := engine.applyServiceDefaults(svc, nodes)

	if resolved.Image != "mysql:8" {
		t.Errorf("expected explicit image 'mysql:8' to be preserved, got %q", resolved.Image)
	}
	if resolved.Port != 3306 {
		t.Errorf("expected explicit port 3306 to be preserved, got %d", resolved.Port)
	}
	if resolved.Node != "custom-node" {
		t.Errorf("expected explicit node 'custom-node' to be preserved, got %q", resolved.Node)
	}
}

func TestApplyNetworkDefaults(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Empty network spec should get defaults
	network := core.NetworkSpec{}
	resolved := engine.applyNetworkDefaults(network)

	if resolved.VPNType != "none" {
		t.Errorf("expected VPN type 'none', got '%s'", resolved.VPNType)
	}

	if resolved.Domain != "" {
		t.Errorf("expected empty domain for local default, got '%s'", resolved.Domain)
	}

	// Explicit values should be preserved
	network = core.NetworkSpec{
		VPN:    "headscale",
		Domain: "mylab.internal",
	}
	resolved = engine.applyNetworkDefaults(network)

	if resolved.VPNType != "headscale" {
		t.Errorf("expected VPN type 'headscale', got '%s'", resolved.VPNType)
	}

	if resolved.Domain != "mylab.internal" {
		t.Errorf("expected domain 'mylab.internal', got '%s'", resolved.Domain)
	}
}

func TestApplyNetworkDefaults_AllVPNTypes(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	testCases := []struct {
		vpnType        string
		expectedSubnet string
		expectedGW     string
	}{
		{"headscale", "100.64.0.0/10", "100.64.0.1"},
		{"tailscale", "100.64.0.0/10", "100.64.0.1"}, // Same as headscale (both use CGNAT)
		{"wireguard", "10.200.0.0/16", "10.200.0.1"},
		{"zerotier", "10.147.0.0/16", "10.147.0.1"},
		{"none", "10.0.0.0/24", "10.0.0.1"},
		{"", "10.0.0.0/24", "10.0.0.1"}, // Empty string = none
	}

	for _, tc := range testCases {
		t.Run("vpn_"+tc.vpnType, func(t *testing.T) {
			network := core.NetworkSpec{VPN: tc.vpnType}
			resolved := engine.applyNetworkDefaults(network)

			if resolved.Subnet != tc.expectedSubnet {
				t.Errorf("VPN %q: expected subnet %q, got %q",
					tc.vpnType, tc.expectedSubnet, resolved.Subnet)
			}
			if resolved.Gateway != tc.expectedGW {
				t.Errorf("VPN %q: expected gateway %q, got %q",
					tc.vpnType, tc.expectedGW, resolved.Gateway)
			}
		})
	}
}

func TestApplyNetworkDefaults_UnknownVPNType(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Unknown VPN type should fall back to safe defaults
	network := core.NetworkSpec{VPN: "unknown-vpn"}
	resolved := engine.applyNetworkDefaults(network)

	if resolved.VPNType != "unknown-vpn" {
		t.Errorf("expected VPN type to be preserved as 'unknown-vpn', got '%s'", resolved.VPNType)
	}

	// Should use safe defaults (same as "none")
	if resolved.Subnet != "10.0.0.0/24" {
		t.Errorf("expected default subnet '10.0.0.0/24', got '%s'", resolved.Subnet)
	}
	if resolved.Gateway != "10.0.0.1" {
		t.Errorf("expected default gateway '10.0.0.1', got '%s'", resolved.Gateway)
	}
}

func TestApplyNetworkDefaults_Nameservers(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	network := core.NetworkSpec{}
	resolved := engine.applyNetworkDefaults(network)

	// Should have default nameservers
	if len(resolved.Nameservers) != 3 {
		t.Errorf("expected 3 default nameservers, got %d", len(resolved.Nameservers))
	}

	// First should be Cloudflare
	if len(resolved.Nameservers) > 0 && resolved.Nameservers[0] != "1.1.1.1" {
		t.Errorf("expected first nameserver '1.1.1.1', got '%s'", resolved.Nameservers[0])
	}
}
