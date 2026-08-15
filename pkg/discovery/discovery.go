// Package discovery provides network device discovery functionality for kombifyTechstack.
//
// The package is designed to be modular and reusable across different applications:
//   - kombifyTechstack Core: Uses the real implementation for network discovery
//   - KombiSim: Uses a mock implementation for simulated environments
//   - Tests: Uses configurable mocks for unit testing
//
// # Usage
//
// Create a real discovery service:
//
//	svc := discovery.New(
//	    discovery.WithLogger(logger),
//	    discovery.WithTimeout(30 * time.Second),
//	)
//
// Create a mock for testing:
//
//	mock := discovery.NewMock(
//	    discovery.MockWithDevices(fakeDevices),
//	)
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
// This interface enables dependency injection and allows for mock implementations
// in testing and simulation environments like KombiSim.
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

// Option configures a discovery service.
type Option func(*serviceOptions)

type serviceOptions struct {
	config       *DiscoveryConfig
	logger       *slog.Logger
	probeTimeout time.Duration
	cacheTTL     time.Duration
}

// WithConfig sets the discovery configuration.
func WithConfig(config *DiscoveryConfig) Option {
	return func(o *serviceOptions) {
		o.config = config
	}
}

// WithLogger sets a custom logger.
func WithLogger(logger *slog.Logger) Option {
	return func(o *serviceOptions) {
		o.logger = logger
	}
}

// WithProbeTimeout sets the SSH probe timeout.
func WithProbeTimeout(timeout time.Duration) Option {
	return func(o *serviceOptions) {
		o.probeTimeout = timeout
	}
}

// WithCacheTTL sets the cache time-to-live.
func WithCacheTTL(ttl time.Duration) Option {
	return func(o *serviceOptions) {
		o.cacheTTL = ttl
	}
}

// New creates a new discovery service with the given options.
// This is the preferred constructor for production use.
func New(opts ...Option) Discovery {
	options := &serviceOptions{
		config:       DefaultConfig(),
		logger:       slog.Default(),
		probeTimeout: 30 * time.Second,
		cacheTTL:     5 * time.Minute,
	}

	for _, opt := range opts {
		opt(options)
	}

	svc := &Service{
		config:      options.config,
		scanner:     NewScanner(options.config),
		prober:      NewProber(options.probeTimeout),
		cache:       make(map[string]*DiscoveredDevice),
		scans:       make(map[string]*ScanResult),
		scanCancels: make(map[string]context.CancelFunc),
		logger:      options.logger,
		cacheTTL:    options.cacheTTL,
		maxScans:    100,
		scanTTL:     1 * time.Hour,
	}

	return svc
}

// Ensure Service implements Discovery interface.
var _ Discovery = (*Service)(nil)
