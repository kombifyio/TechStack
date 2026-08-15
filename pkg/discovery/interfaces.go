// Package discovery provides interfaces for network device discovery.
// These interfaces enable dependency inversion and improve testability.
package discovery

import "context"

// NetworkScanner defines the contract for network scanning operations.
// This interface enables mock implementations for testing and supports
// the Strategy pattern for different scanning approaches.
type NetworkScanner interface {
	// GetLocalInterfaces returns all local network interfaces with their IPs and subnets.
	GetLocalInterfaces() ([]NetworkInterface, error)

	// GetLocalSubnets returns all local subnets in CIDR notation.
	GetLocalSubnets() ([]string, error)

	// GetLocalIPs returns all local IP addresses.
	GetLocalIPs() ([]string, error)

	// ScanPassive performs a passive network scan using ARP and ping.
	// The onProgress callback is invoked after each host is scanned, providing
	// real-time updates on discovered devices and scan progress percentage.
	ScanPassive(ctx context.Context, req *ScanRequest, onProgress func(*ScanResult)) (*ScanResult, error)

	// ScanActive performs an active port scan on discovered or specified hosts.
	// The onProgress callback is invoked after port scanning completes for each
	// device, providing real-time updates on discovered services and scan progress.
	ScanActive(ctx context.Context, req *ScanRequest, onProgress func(*ScanResult)) (*ScanResult, error)
}

// DeviceProber defines the contract for deep device probing via SSH.
// This interface separates the probing logic from the scanning logic,
// following the Single Responsibility Principle.
type DeviceProber interface {
	// Probe performs deep discovery on a single device via SSH.
	// It retrieves system information, detects Docker status, and analyzes capabilities.
	Probe(ctx context.Context, req *ProbeRequest) (*ProbeResult, error)

	// QuickProbe performs a lightweight SSH connectivity test without full system probing.
	QuickProbe(ctx context.Context, ip string, port int, user, password, privateKey string) error
}

// Ensure concrete types implement their interfaces at compile time.
// This provides early detection of interface compliance issues.
var (
	_ NetworkScanner = (*Scanner)(nil)
	_ DeviceProber   = (*Prober)(nil)
)
