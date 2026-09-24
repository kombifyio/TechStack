package tunnel

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/logger"
)

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
			mode, _ := det.recommendMode(tt.topo)

			if mode != tt.expected {
				t.Errorf("Expected mode %s, got %s", tt.expected, mode)
			}
		})
	}
}
