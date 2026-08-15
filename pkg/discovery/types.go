// Package discovery provides network device discovery functionality for kombifyTechstack.
// It supports passive (ARP/Ping), active (port scan), and deep (SSH probe) discovery.
package discovery

import (
	"net"
	"strings"
	"time"
)

// ScanProfile defines the depth of network scanning.
type ScanProfile string

const (
	// ProfilePassive performs ARP and ping scans only - fast and non-intrusive.
	ProfilePassive ScanProfile = "passive"
	// ProfileActive adds port scanning for common services.
	ProfileActive ScanProfile = "active"
	// ProfileDeep adds SSH-based system probing (requires credentials).
	ProfileDeep ScanProfile = "deep"
)

// ProbeStatus indicates the current state of deep discovery for a device.
type ProbeStatus string

const (
	ProbeStatusPending ProbeStatus = "pending"
	ProbeStatusProbing ProbeStatus = "probing"
	ProbeStatusDone    ProbeStatus = "done"
	ProbeStatusFailed  ProbeStatus = "failed"
	ProbeStatusSkipped ProbeStatus = "skipped"
)

// ScanStatus indicates the current state of a discovery scan.
type ScanStatus string

const (
	ScanStatusPending   ScanStatus = "pending"
	ScanStatusRunning   ScanStatus = "running"
	ScanStatusCompleted ScanStatus = "completed"
	ScanStatusFailed    ScanStatus = "failed"
	ScanStatusCancelled ScanStatus = "canceled"
)

// DeviceRole represents suggested roles for discovered devices.
type DeviceRole string

const (
	RoleMain    DeviceRole = "main"    // Primary kombifyTechstack controller
	RoleWorker  DeviceRole = "worker"  // Worker node
	RoleStorage DeviceRole = "storage" // Storage-focused node
	RoleUtility DeviceRole = "utility" // Utility services (DNS, monitoring)
	RoleGateway DeviceRole = "gateway" // Router/Gateway device (excluded from worker rollout)
	RoleUnknown DeviceRole = "unknown" // Cannot determine role
)

// DiscoveredService represents a detected network service.
type DiscoveredService struct {
	Name    string `json:"name"`              // e.g., "ssh", "docker", "proxmox"
	Port    int    `json:"port"`              // Port number
	Version string `json:"version,omitempty"` // Detected version if available
	Banner  string `json:"banner,omitempty"`  // Raw banner if captured
}

// SystemInfo contains hardware and OS information from deep discovery.
type SystemInfo struct {
	OS           string `json:"os,omitempty"`            // e.g., "linux", "windows"
	Distribution string `json:"distribution,omitempty"`  // e.g., "ubuntu", "debian"
	Version      string `json:"version,omitempty"`       // OS version
	Kernel       string `json:"kernel,omitempty"`        // Kernel version
	Arch         string `json:"arch,omitempty"`          // CPU architecture
	CPUCores     int    `json:"cpu_cores,omitempty"`     // Number of CPU cores
	MemoryMB     int    `json:"memory_mb,omitempty"`     // Total RAM in MB
	DiskGB       int    `json:"disk_gb,omitempty"`       // Available disk in GB
	DockerStatus string `json:"docker_status,omitempty"` // "installed", "running", "not_installed"
}

// DiscoveredDevice represents a device found during network discovery.
type DiscoveredDevice struct {
	// Unique identifier for this device (generated from MAC or IP)
	DeviceID string `json:"device_id"`

	// Network identity
	IP       string `json:"ip"`
	MAC      string `json:"mac,omitempty"`
	Hostname string `json:"hostname,omitempty"`

	// Discovered services (from active scan)
	Services []DiscoveredService `json:"services,omitempty"`
	Ports    []int               `json:"ports,omitempty"`

	// System information (from deep probe)
	System *SystemInfo `json:"system,omitempty"`

	// Role analysis
	RolesSuggested []DeviceRole `json:"roles_suggested,omitempty"`
	Confidence     float64      `json:"confidence"` // 0.0 - 1.0

	// Discovery metadata
	DiscoveryProfile ScanProfile `json:"discovery_profile"`
	ProbeStatus      ProbeStatus `json:"probe_status"`
	ProbeError       string      `json:"probe_error,omitempty"`
	FirstSeen        time.Time   `json:"first_seen"`
	LastSeen         time.Time   `json:"last_seen"`

	// Additional metadata
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ScanRequest defines parameters for initiating a network scan.
type ScanRequest struct {
	// Profile determines scan depth (passive, active, deep)
	Profile ScanProfile `json:"profile"`

	// Subnet to scan (CIDR notation, e.g., "192.168.1.0/24")
	// If empty, auto-detect local subnets
	Subnet string `json:"subnet,omitempty"`

	// TargetIPs limits scan to specific IPs (overrides Subnet)
	TargetIPs []string `json:"target_ips,omitempty"`

	// ExcludeIPs to skip during scan
	ExcludeIPs []string `json:"exclude_ips,omitempty"`

	// Timeout per host in seconds (default: 2)
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	// MaxConcurrent limits parallel scans (default: 50)
	MaxConcurrent int `json:"max_concurrent,omitempty"`

	// Experimental: detect tailscale interfaces and include their subnets
	DetectTailscale bool `json:"detect_tailscale,omitempty"`

	// Experimental: perform mDNS discovery (multicast)
	Mdns bool `json:"mdns,omitempty"`
}

// ScanResult contains the results of a discovery scan.
type ScanResult struct {
	// Unique scan identifier
	ScanID string `json:"scan_id"`

	// Current status
	Status ScanStatus `json:"status"`

	// Scan parameters used
	Profile ScanProfile `json:"profile"`
	Subnet  string      `json:"subnet,omitempty"`

	// Timing
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// Progress (for running scans)
	Progress     int `json:"progress"`      // 0-100
	TotalHosts   int `json:"total_hosts"`   // Total hosts to scan
	ScannedHosts int `json:"scanned_hosts"` // Hosts scanned so far

	// Results
	Devices []DiscoveredDevice `json:"devices"`

	// Errors encountered
	Errors []string `json:"errors,omitempty"`
}

// ProbeRequest defines parameters for deep discovery of a specific device.
type ProbeRequest struct {
	// Target IP to probe
	IP string `json:"ip"`

	// SSH credentials (for deep probe)
	SSHUser       string `json:"ssh_user,omitempty"`
	SSHPassword   string `json:"ssh_password,omitempty"`
	SSHPrivateKey string `json:"ssh_private_key,omitempty"`
	SSHPort       int    `json:"ssh_port,omitempty"` // Default: 22

	// Timeout in seconds (default: 30)
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// ProbeResult contains the results of a deep discovery probe.
type ProbeResult struct {
	DeviceID string        `json:"device_id"`
	IP       string        `json:"ip"`
	Status   ProbeStatus   `json:"status"`
	System   *SystemInfo   `json:"system,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

// NetworkInterface represents a local network interface.
type NetworkInterface struct {
	Name    string   `json:"name"`
	MAC     string   `json:"mac"`
	IPs     []string `json:"ips"`
	Subnets []string `json:"subnets"` // CIDR notation
}

// DiscoveryConfig holds configuration for the discovery service.
type DiscoveryConfig struct {
	// Default scan profile
	DefaultProfile ScanProfile `json:"default_profile"`

	// Default timeout per host in seconds
	DefaultTimeout int `json:"default_timeout"`

	// Maximum concurrent scans
	MaxConcurrent int `json:"max_concurrent"`

	// Common ports to check during active scan
	ActivePorts []int `json:"active_ports"`

	// Cache TTL for discovered devices (0 = no cache)
	CacheTTLSeconds int `json:"cache_ttl_seconds"`

	// Auto-exclude local IPs from scan results
	ExcludeLocalIPs bool `json:"exclude_local_ips"`

	// SampleLimit limits the number of hosts scanned for very large subnets
	// to avoid overwhelming the host in a single run (0 = unlimited)
	SampleLimit int `json:"sample_limit"`
}

// DefaultConfig returns sensible defaults for discovery.
func DefaultConfig() *DiscoveryConfig {
	return &DiscoveryConfig{
		DefaultProfile:  ProfileActive,
		DefaultTimeout:  2,
		MaxConcurrent:   50,
		ActivePorts:     DefaultActivePorts(),
		CacheTTLSeconds: 300, // 5 minutes
		ExcludeLocalIPs: true,
		SampleLimit:     4096,
	}
}

// DefaultActivePorts returns the standard ports to check during active discovery.
func DefaultActivePorts() []int {
	return []int{
		22,   // SSH
		53,   // DNS (gateway indicator)
		67,   // DHCP Server (gateway indicator)
		80,   // HTTP
		443,  // HTTPS
		1900, // UPnP/SSDP (gateway indicator)
		2375, // Docker (unencrypted)
		2376, // Docker (TLS)
		5260, // kombifyTechstack Core
		5263, // kombifyTechstack gRPC
		6443, // Kubernetes API
		8006, // Proxmox VE
		8080, // HTTP Alt
		9090, // Prometheus
		9100, // Node Exporter
	}
}

// Helper functions

// GenerateDeviceID creates a unique device ID from MAC or IP.
func GenerateDeviceID(mac, ip string) string {
	if mac != "" {
		// Normalize MAC address
		hw, err := net.ParseMAC(mac)
		if err == nil {
			return "mac-" + hw.String()
		}
	}
	return "ip-" + ip
}

// gatewayHostnamePatterns contains common hostname patterns for routers/gateways.
var gatewayHostnamePatterns = []string{
	"router", "gateway", "gw", "fritz", "fritzbox", "speedport",
	"unifi", "ubnt", "mikrotik", "openwrt", "pfsense", "opnsense",
	"netgear", "linksys", "asus", "tplink", "tp-link", "dlink", "d-link",
	"vodafone", "telekom", "easybox", "homespot", "edgerouter",
}

// isGatewayIP checks if an IP address looks like a typical gateway address.
func isGatewayIP(ip string) bool {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return false
	}
	lastOctet := parts[3]
	// Common gateway addresses: .1, .254
	return lastOctet == "1" || lastOctet == "254"
}

// hasGatewayHostname checks if hostname matches router/gateway patterns.
func hasGatewayHostname(hostname string) bool {
	if hostname == "" {
		return false
	}
	lower := strings.ToLower(hostname)
	for _, pattern := range gatewayHostnamePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// hasGatewayServices checks if device has services typical for routers/gateways.
func hasGatewayServices(device *DiscoveredDevice) bool {
	hasDNS := false
	hasDHCP := false
	hasUPnP := false

	for _, svc := range device.Services {
		switch svc.Port {
		case 53:
			hasDNS = true
		case 67, 68:
			hasDHCP = true
		case 1900:
			hasUPnP = true
		}
	}

	// Check port list as well (services might not have names)
	for _, port := range device.Ports {
		switch port {
		case 53:
			hasDNS = true
		case 67, 68:
			hasDHCP = true
		case 1900:
			hasUPnP = true
		}
	}

	// Gateway if has DNS + DHCP, or DNS + UPnP
	return (hasDNS && hasDHCP) || (hasDNS && hasUPnP) || hasDHCP
}

// SuggestRoles analyzes services and system info to suggest device roles.
func SuggestRoles(device *DiscoveredDevice) []DeviceRole {
	roles := make([]DeviceRole, 0)

	// Check for gateway/router first (exclude from worker candidates)
	gatewayScore := 0
	if isGatewayIP(device.IP) {
		gatewayScore++
	}
	if hasGatewayHostname(device.Hostname) {
		gatewayScore += 2 // Hostname is a strong indicator
	}
	if hasGatewayServices(device) {
		gatewayScore += 2 // Services are a strong indicator
	}

	// If strong gateway indicators, mark as gateway and return early
	if gatewayScore >= 2 {
		return []DeviceRole{RoleGateway}
	}

	hasDocker := false
	hasProxmox := false
	hasKubernetes := false
	hasSSH := false

	for _, svc := range device.Services {
		switch svc.Name {
		case "docker":
			hasDocker = true
		case "proxmox":
			hasProxmox = true
		case "kubernetes":
			hasKubernetes = true
		case "ssh":
			hasSSH = true
		}
	}

	// Check system info if available
	if device.System != nil {
		if device.System.DockerStatus == "running" {
			hasDocker = true
		}
		// High memory/CPU = good for main or worker
		if device.System.MemoryMB >= 8192 && device.System.CPUCores >= 4 {
			roles = append(roles, RoleMain)
		}
		if device.System.DiskGB >= 500 {
			roles = append(roles, RoleStorage)
		}
	}

	if hasDocker || hasProxmox || hasKubernetes {
		roles = append(roles, RoleWorker)
	}

	// SSH available = potentially usable as worker
	if hasSSH && len(roles) == 0 {
		roles = append(roles, RoleWorker)
	}

	if len(roles) == 0 {
		roles = append(roles, RoleUnknown)
	}

	return roles
}

// CalculateConfidence determines how confident we are about device role suggestions.
func CalculateConfidence(device *DiscoveredDevice) float64 {
	confidence := 0.0

	// Hostname resolved
	if device.Hostname != "" {
		confidence += 0.1
	}

	// MAC address known
	if device.MAC != "" {
		confidence += 0.1
	}

	// Services detected
	if len(device.Services) > 0 {
		confidence += 0.2
		if len(device.Services) > 3 {
			confidence += 0.1
		}
	}

	// Deep probe completed
	if device.System != nil {
		confidence += 0.3
		// Docker status known
		if device.System.DockerStatus != "" {
			confidence += 0.1
		}
		// Full system info
		if device.System.CPUCores > 0 && device.System.MemoryMB > 0 {
			confidence += 0.1
		}
	}

	if confidence > 1.0 {
		confidence = 1.0
	}

	return confidence
}
