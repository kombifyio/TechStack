// Package tunnel - Network Topology Detection for Sprint 15.2
package tunnel

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
)

// NetworkTopology represents detected network environment.
type NetworkTopology struct {
	// Environment type
	Environment EnvironmentType `json:"environment"`

	// Constraints detected
	HasNAT             bool `json:"has_nat"`
	HasDocker          bool `json:"has_docker"`
	HasWSL             bool `json:"has_wsl"`
	HasFirewall        bool `json:"has_firewall"`
	PublicIPReachable  bool `json:"public_ip_reachable"`
	LocalNetReachable  bool `json:"local_net_reachable"`
	PortForwardEnabled bool `json:"port_forward_enabled"`

	// Network details
	LocalIP       string   `json:"local_ip"`
	PublicIP      string   `json:"public_ip"`
	DefaultGW     string   `json:"default_gateway"`
	DNSServers    []string `json:"dns_servers"`
	OpenPorts     []int    `json:"open_ports"`
	PrivateSubnet string   `json:"private_subnet"` // e.g., "10.0.0.0/8", "192.168.0.0/16"

	// Recommendations
	RecommendedMode NetworkMode `json:"recommended_mode"`
	Reason          string      `json:"reason"`
}

// EnvironmentType defines the detected environment.
type EnvironmentType string

const (
	EnvBareMetalHome EnvironmentType = "baremetal_home" // Direct home network
	EnvBareMetalDC   EnvironmentType = "baremetal_dc"   // Datacenter/VPS
	EnvDockerHost    EnvironmentType = "docker_host"    // Docker on host
	EnvDockerDesktop EnvironmentType = "docker_desktop" // Docker Desktop (WSL2 backend)
	EnvWSL2          EnvironmentType = "wsl2"           // WSL2 without Docker
	EnvVM            EnvironmentType = "vm"             // Virtual Machine
	EnvCloud         EnvironmentType = "cloud"          // Cloud provider (AWS/Azure/GCP)
	EnvUnknown       EnvironmentType = "unknown"
)

// TopologyDetector detects network topology.
type TopologyDetector struct {
	log     *logger.Logger
	timeout time.Duration
}

// NewTopologyDetector creates a new detector.
func NewTopologyDetector(log *logger.Logger) *TopologyDetector {
	return &TopologyDetector{
		log:     log,
		timeout: 5 * time.Second,
	}
}

// Detect performs full topology detection.
func (d *TopologyDetector) Detect(ctx context.Context) (*NetworkTopology, error) {
	d.log.Info("Starting network topology detection")

	topo := &NetworkTopology{
		Environment: EnvUnknown,
	}

	// Detect environment type
	topo.Environment = d.detectEnvironment()
	d.log.Info("Environment detected", "type", topo.Environment)

	// Detect network constraints
	topo.HasDocker = d.detectDocker()
	topo.HasWSL = d.detectWSL()
	topo.HasNAT = d.detectNAT()

	// Get network details
	topo.LocalIP = d.getLocalIP()
	topo.PublicIP = d.getPublicIP(ctx)
	topo.DefaultGW = d.getDefaultGateway()
	topo.DNSServers = d.getDNSServers()
	topo.PrivateSubnet = d.getPrivateSubnet(topo.LocalIP)

	// Test reachability
	topo.PublicIPReachable = d.testPublicReachability(ctx)
	topo.LocalNetReachable = d.testLocalNetworkReachability(ctx, topo.DefaultGW)

	// Recommend network mode
	topo.RecommendedMode, topo.Reason = d.recommendMode(topo)

	d.log.Info("Topology detection complete",
		"env", topo.Environment,
		"mode", topo.RecommendedMode,
		"local_ip", topo.LocalIP,
		"public_ip", topo.PublicIP)

	return topo, nil
}

// detectEnvironment determines the environment type.
func (d *TopologyDetector) detectEnvironment() EnvironmentType {
	// Check WSL2 first
	if d.detectWSL() {
		if d.detectDocker() {
			return EnvDockerDesktop
		}
		return EnvWSL2
	}

	// Check Docker
	if d.detectDocker() {
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			return EnvDockerDesktop
		}
		return EnvDockerHost
	}

	// Check cloud providers
	if d.detectCloudProvider() != "" {
		return EnvCloud
	}

	// Check if VM
	if d.detectVirtualization() {
		return EnvVM
	}

	// Default to bare metal
	// TODO: Distinguish between home/datacenter based on IP ranges
	return EnvBareMetalHome
}

// detectDocker checks if Docker is available.
func (d *TopologyDetector) detectDocker() bool {
	cmd := exec.Command("docker", "version")
	err := cmd.Run()
	return err == nil
}

// detectWSL checks if running in WSL2.
func (d *TopologyDetector) detectWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}

	// Check /proc/version for "microsoft"
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}

	return strings.Contains(strings.ToLower(string(data)), "microsoft")
}

// detectNAT checks if behind NAT.
func (d *TopologyDetector) detectNAT() bool {
	localIP := d.getLocalIP()
	if localIP == "" {
		return false
	}

	ip := net.ParseIP(localIP)
	if ip == nil {
		return false
	}

	// Check if private IP (RFC1918)
	return ip.IsPrivate()
}

// detectCloudProvider checks for cloud metadata services.
func (d *TopologyDetector) detectCloudProvider() string {
	// AWS
	if d.checkMetadataService("169.254.169.254", "/latest/meta-data/") {
		return "aws"
	}

	// Azure
	if d.checkMetadataService("169.254.169.254", "/metadata/instance?api-version=2021-02-01") {
		return "azure"
	}

	// GCP
	if d.checkMetadataService("metadata.google.internal", "/computeMetadata/v1/") {
		return "gcp"
	}

	return ""
}

// checkMetadataService checks if a cloud metadata service is available.
func (d *TopologyDetector) checkMetadataService(host, path string) bool {
	conn, err := net.DialTimeout("tcp", host+":80", 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// detectVirtualization checks if running in a VM.
func (d *TopologyDetector) detectVirtualization() bool {
	if runtime.GOOS == "linux" {
		// Check systemd-detect-virt
		cmd := exec.Command("systemd-detect-virt")
		output, err := cmd.Output()
		if err == nil && string(output) != "none\n" {
			return true
		}

		// Check /proc/cpuinfo for hypervisor
		data, err := os.ReadFile("/proc/cpuinfo")
		if err == nil {
			lower := strings.ToLower(string(data))
			return strings.Contains(lower, "hypervisor") ||
				strings.Contains(lower, "vmware") ||
				strings.Contains(lower, "virtualbox")
		}
	}

	return false
}

// getLocalIP gets the primary local IP address.
func (d *TopologyDetector) getLocalIP() string {
	// Try to connect to a public IP to determine local IP
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

// getPublicIP attempts to get the public IP address.
func (d *TopologyDetector) getPublicIP(ctx context.Context) string {
	// Try multiple services
	services := []string{
		"https://api.ipify.org",
		"https://icanhazip.com",
		"https://ifconfig.me/ip",
	}

	for _, service := range services {
		cmd := exec.CommandContext(ctx, "curl", "-s", "-m", "3", service)
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			ip := strings.TrimSpace(string(output))
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}

	return ""
}

// getDefaultGateway gets the default gateway IP.
func (d *TopologyDetector) getDefaultGateway() string {
	if runtime.GOOS == "windows" {
		return d.getDefaultGatewayWindows()
	}
	return d.getDefaultGatewayUnix()
}

// getDefaultGatewayWindows gets gateway on Windows.
func (d *TopologyDetector) getDefaultGatewayWindows() string {
	cmd := exec.Command("route", "print", "0.0.0.0")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	// Parse route output
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "0.0.0.0" && fields[1] == "0.0.0.0" {
			if ip := net.ParseIP(fields[2]); ip != nil {
				return fields[2]
			}
		}
	}

	return ""
}

// getDefaultGatewayUnix gets gateway on Linux/macOS.
func (d *TopologyDetector) getDefaultGatewayUnix() string {
	cmd := exec.Command("ip", "route", "show", "default")
	output, err := cmd.Output()
	if err != nil {
		// Try netstat as fallback
		cmd = exec.Command("netstat", "-rn")
		output, err = cmd.Output()
		if err != nil {
			return ""
		}
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "default") {
			fields := strings.Fields(line)
			for i, field := range fields {
				if field == "via" && i+1 < len(fields) {
					return fields[i+1]
				}
				if ip := net.ParseIP(field); ip != nil && !ip.IsLoopback() {
					return field
				}
			}
		}
	}

	return ""
}

// getDNSServers gets configured DNS servers.
func (d *TopologyDetector) getDNSServers() []string {
	// Try reading /etc/resolv.conf on Unix
	if runtime.GOOS != "windows" {
		data, err := os.ReadFile("/etc/resolv.conf")
		if err == nil {
			var servers []string
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "nameserver") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						servers = append(servers, fields[1])
					}
				}
			}
			return servers
		}
	}

	// Default fallback
	return []string{"8.8.8.8", "1.1.1.1"}
}

// getPrivateSubnet determines the private subnet.
func (d *TopologyDetector) getPrivateSubnet(localIP string) string {
	ip := net.ParseIP(localIP)
	if ip == nil {
		return ""
	}

	// Check RFC1918 ranges
	if ip.To4()[0] == 10 {
		return "10.0.0.0/8"
	}
	if ip.To4()[0] == 172 && ip.To4()[1] >= 16 && ip.To4()[1] <= 31 {
		return "172.16.0.0/12"
	}
	if ip.To4()[0] == 192 && ip.To4()[1] == 168 {
		return "192.168.0.0/16"
	}

	return ""
}

// testPublicReachability tests if publicly reachable.
func (d *TopologyDetector) testPublicReachability(ctx context.Context) bool {
	// Try to bind to a random port
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return false
	}
	defer listener.Close()

	// Just checking if we CAN bind, not if actually reachable from outside
	// Full test would require external service
	return true
}

// testLocalNetworkReachability tests local network reachability.
func (d *TopologyDetector) testLocalNetworkReachability(ctx context.Context, gateway string) bool {
	if gateway == "" {
		return false
	}

	// Ping gateway
	conn, err := net.DialTimeout("tcp", gateway+":80", 2*time.Second)
	if err == nil {
		conn.Close()
		return true
	}

	// Try ICMP ping
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", "1000", gateway)
	} else {
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", gateway)
	}

	err = cmd.Run()
	return err == nil
}

// recommendMode recommends the best network mode.
func (d *TopologyDetector) recommendMode(topo *NetworkTopology) (NetworkMode, string) {
	// Cloud/VPS with public IP -> Use local
	if topo.Environment == EnvCloud && topo.PublicIP != "" {
		return NetworkModeLocal, "Public IP available in cloud environment"
	}

	// WSL2/Docker Desktop -> Must use tunnel
	if topo.Environment == EnvWSL2 || topo.Environment == EnvDockerDesktop {
		return NetworkModeTunnel, "WSL2/Docker Desktop requires tunnel for external access"
	}

	// Behind NAT without port forwarding -> Tunnel
	if topo.HasNAT && !topo.PortForwardEnabled {
		return NetworkModeTunnel, "Behind NAT without port forwarding"
	}

	// Local network with reachability -> Local
	if topo.LocalNetReachable {
		return NetworkModeLocal, "Local network accessible"
	}

	// Default to tunnel (safest)
	return NetworkModeTunnel, "Tunnel recommended for maximum compatibility"
}

// String returns a human-readable description.
func (t *NetworkTopology) String() string {
	return fmt.Sprintf("Environment: %s, NAT: %v, Docker: %v, WSL: %v, Mode: %s",
		t.Environment, t.HasNAT, t.HasDocker, t.HasWSL, t.RecommendedMode)
}
