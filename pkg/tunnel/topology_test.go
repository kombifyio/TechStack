package tunnel

import (
	"context"
	"net"
	"testing"

	"github.com/kombifyio/techstack/pkg/logger"
)

func TestNewTopologyDetector(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewTopologyDetector(log)

	if det == nil {
		t.Fatal("NewTopologyDetector returned nil")
	}
}

func TestDetect(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewTopologyDetector(log)

	ctx := context.Background()
	topo, err := det.Detect(ctx)

	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if topo == nil {
		t.Fatal("Detect returned nil topology")
	}

	t.Logf("Detected topology: %s", topo.String())
	t.Logf("Environment: %s", topo.Environment)
	t.Logf("Local IP: %s", topo.LocalIP)
	t.Logf("Public IP: %s", topo.PublicIP)
	t.Logf("Gateway: %s", topo.DefaultGW)
	t.Logf("Recommended mode: %s (%s)", topo.RecommendedMode, topo.Reason)

	// Basic validations
	if topo.Environment == "" {
		t.Error("Environment not detected")
	}

	if topo.RecommendedMode == "" {
		t.Error("No mode recommended")
	}
}

func TestDetectDocker(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewTopologyDetector(log)

	hasDocker := det.detectDocker()
	t.Logf("Docker detected: %v", hasDocker)
}

func TestDetectWSL(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewTopologyDetector(log)

	hasWSL := det.detectWSL()
	t.Logf("WSL detected: %v", hasWSL)
}

func TestDetectNAT(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewTopologyDetector(log)

	hasNAT := det.detectNAT()
	t.Logf("NAT detected: %v", hasNAT)

	// Most development machines are behind NAT
	if !hasNAT {
		t.Logf("Warning: No NAT detected (unusual for dev environment)")
	}
}

func TestGetLocalIP(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewTopologyDetector(log)

	ip := det.getLocalIP()
	t.Logf("Local IP: %s", ip)

	if ip == "" {
		t.Error("Failed to get local IP")
	}

	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		t.Errorf("Invalid IP format: %s", ip)
	}
}

func TestGetDefaultGateway(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewTopologyDetector(log)

	gw := det.getDefaultGateway()
	t.Logf("Default gateway: %s", gw)

	if gw == "" {
		t.Log("Warning: No default gateway detected")
		return
	}

	parsedIP := net.ParseIP(gw)
	if parsedIP == nil {
		t.Errorf("Invalid gateway IP format: %s", gw)
	}
}

func TestGetDNSServers(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewTopologyDetector(log)

	servers := det.getDNSServers()
	t.Logf("DNS servers: %v", servers)

	if len(servers) == 0 {
		t.Error("No DNS servers detected")
	}

	for _, server := range servers {
		if net.ParseIP(server) == nil {
			t.Errorf("Invalid DNS server IP: %s", server)
		}
	}
}

func TestGetPrivateSubnet(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewTopologyDetector(log)

	tests := []struct {
		ip     string
		subnet string
	}{
		{"10.0.0.1", "10.0.0.0/8"},
		{"172.16.0.1", "172.16.0.0/12"},
		{"192.168.1.1", "192.168.0.0/16"},
		{"8.8.8.8", ""},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			subnet := det.getPrivateSubnet(tt.ip)
			if subnet != tt.subnet {
				t.Errorf("Expected subnet %s, got %s", tt.subnet, subnet)
			}
		})
	}
}

func TestEnvironmentTypeString(t *testing.T) {
	types := []EnvironmentType{
		EnvBareMetalHome,
		EnvBareMetalDC,
		EnvDockerHost,
		EnvDockerDesktop,
		EnvWSL2,
		EnvVM,
		EnvCloud,
		EnvUnknown,
	}

	for _, envType := range types {
		t.Logf("Environment type: %s", envType)
		if string(envType) == "" {
			t.Errorf("Empty string for environment type")
		}
	}
}

func TestRecommendMode(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewTopologyDetector(log)

	tests := []struct {
		name     string
		topo     *NetworkTopology
		expected NetworkMode
	}{
		{
			name: "WSL2 should use tunnel",
			topo: &NetworkTopology{
				Environment: EnvWSL2,
				HasWSL:      true,
			},
			expected: NetworkModeTunnel,
		},
		{
			name: "Cloud with public IP should use local",
			topo: &NetworkTopology{
				Environment: EnvCloud,
				PublicIP:    "1.2.3.4",
			},
			expected: NetworkModeLocal,
		},
		{
			name: "NAT without forwarding should use tunnel",
			topo: &NetworkTopology{
				Environment:        EnvBareMetalHome,
				HasNAT:             true,
				PortForwardEnabled: false,
			},
			expected: NetworkModeTunnel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, reason := det.recommendMode(tt.topo)
			t.Logf("Mode: %s, Reason: %s", mode, reason)

			if mode != tt.expected {
				t.Errorf("Expected mode %s, got %s", tt.expected, mode)
			}
		})
	}
}

func TestTopologyString(t *testing.T) {
	topo := &NetworkTopology{
		Environment:     EnvBareMetalHome,
		HasNAT:          true,
		HasDocker:       false,
		HasWSL:          false,
		RecommendedMode: NetworkModeTunnel,
	}

	str := topo.String()
	t.Logf("Topology string: %s", str)

	if str == "" {
		t.Error("Empty topology string")
	}
}
