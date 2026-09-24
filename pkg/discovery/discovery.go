// Package discovery provides network device discovery functionality for kombifyTechstack.
//
// # Usage
//
// Create a real discovery service:
//
//	svc := discovery.New()
//
// Use via interface for dependency injection:
//
//	func NewHandler(disc discovery.Discovery) *Handler { ... }
package discovery

import (
	"context"
	"log/slog"
	"time"
)

// Discovery defines the interface for network discovery operations.
// This interface enables dependency injection for discovery consumers.
type Discovery interface {
	// Scanning operations
	StartScan(ctx context.Context, req *ScanRequest) (*ScanResult, error)
	GetScanStatus(scanID string) (*ScanResult, bool)
	CancelScan(scanID string) error
	// EnrichScan triggers on-demand enrichment (e.g., ARP cache) for an existing scan
	EnrichScan(scanID string) (*ScanResult, error)

	// Device operations
	GetDevices() []*DiscoveredDevice
	GetDevice(deviceID string) (*DiscoveredDevice, bool)
	GetDeviceByIP(ip string) (*DiscoveredDevice, bool)

	// Deep discovery
	ProbeDevice(ctx context.Context, req *ProbeRequest) (*ProbeResult, error)
	TestSSH(ctx context.Context, host string, port int, username, password string) error

	// Network info
	GetLocalNetworks() ([]NetworkInterface, error)
	GetLocalSubnets() ([]string, error)

	// Cache management
	ClearCache()
	GetStats() ServiceStats
}

// ServiceStats provides statistics about the discovery service.
type ServiceStats struct {
	CachedDevices int       `json:"cached_devices"`
	ActiveScans   int       `json:"active_scans"`
	LastScanTime  time.Time `json:"last_scan_time,omitempty"`
	TotalScans    int       `json:"total_scans"`
}

// New creates the discovery service used by the runtime.
func New() *Service {
	config := DefaultConfig()
	return &Service{
		config:      config,
		scanner:     NewScanner(config),
		prober:      NewProber(30 * time.Second),
		cache:       make(map[string]*DiscoveredDevice),
		scans:       make(map[string]*ScanResult),
		scanCancels: make(map[string]context.CancelFunc),
		logger:      slog.Default(),
		cacheTTL:    5 * time.Minute,
		maxScans:    100,
		scanTTL:     1 * time.Hour,
	}
}

// Ensure Service implements Discovery interface.
var _ Discovery = (*Service)(nil)
