package discovery

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MockDiscovery provides a configurable mock implementation of the Discovery interface.
// It is designed for:
//   - Unit testing components that depend on Discovery
//   - KombiSim simulation with virtual/simulated devices
//   - Demo environments without real network access
type MockDiscovery struct {
	mu sync.RWMutex

	// Configurable devices
	devices map[string]*DiscoveredDevice

	// Scan behavior
	scanDelay    time.Duration
	scanError    error
	scanCallback func(*ScanRequest) (*ScanResult, error)

	// Probe behavior
	probeDelay    time.Duration
	probeError    error
	probeCallback func(*ProbeRequest) (*ProbeResult, error)

	// SSH test behavior
	sshTestError error

	// Network interfaces
	networks []NetworkInterface

	// Statistics tracking
	stats      ServiceStats
	scanCalls  int
	probeCalls int
}

// MockOption configures a MockDiscovery instance.
type MockOption func(*MockDiscovery)

// NewMock creates a new mock discovery service.
func NewMock(opts ...MockOption) *MockDiscovery {
	m := &MockDiscovery{
		devices: make(map[string]*DiscoveredDevice),
		networks: []NetworkInterface{
			{
				Name:    "eth0",
				IPs:     []string{"192.168.1.100"},
				Subnets: []string{"192.168.1.0/24"},
			},
		},
	}

	for _, opt := range opts {
		opt(m)
	}

	return m
}

// MockWithDevices sets the initial devices for the mock.
func MockWithDevices(devices []*DiscoveredDevice) MockOption {
	return func(m *MockDiscovery) {
		for _, d := range devices {
			m.devices[d.DeviceID] = d
		}
	}
}

// MockWithNetworks sets the available networks.
func MockWithNetworks(networks []NetworkInterface) MockOption {
	return func(m *MockDiscovery) {
		m.networks = networks
	}
}

// MockWithScanDelay sets a delay for scan operations.
func MockWithScanDelay(delay time.Duration) MockOption {
	return func(m *MockDiscovery) {
		m.scanDelay = delay
	}
}

// MockWithScanError makes all scans return the specified error.
func MockWithScanError(err error) MockOption {
	return func(m *MockDiscovery) {
		m.scanError = err
	}
}

// MockWithScanCallback sets a custom callback for scan operations.
func MockWithScanCallback(cb func(*ScanRequest) (*ScanResult, error)) MockOption {
	return func(m *MockDiscovery) {
		m.scanCallback = cb
	}
}

// MockWithProbeDelay sets a delay for probe operations.
func MockWithProbeDelay(delay time.Duration) MockOption {
	return func(m *MockDiscovery) {
		m.probeDelay = delay
	}
}

// MockWithProbeError makes all probes return the specified error.
func MockWithProbeError(err error) MockOption {
	return func(m *MockDiscovery) {
		m.probeError = err
	}
}

// MockWithProbeCallback sets a custom callback for probe operations.
func MockWithProbeCallback(cb func(*ProbeRequest) (*ProbeResult, error)) MockOption {
	return func(m *MockDiscovery) {
		m.probeCallback = cb
	}
}

// MockWithSSHError makes SSH tests return the specified error.
func MockWithSSHError(err error) MockOption {
	return func(m *MockDiscovery) {
		m.sshTestError = err
	}
}

// --- Discovery Interface Implementation ---

// StartScan simulates a network scan.
func (m *MockDiscovery) StartScan(ctx context.Context, req *ScanRequest) (*ScanResult, error) {
	m.mu.Lock()
	m.scanCalls++
	m.mu.Unlock()

	// Apply delay if configured
	if m.scanDelay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(m.scanDelay):
		}
	}

	// Return error if configured
	if m.scanError != nil {
		return nil, m.scanError
	}

	// Use custom callback if set
	if m.scanCallback != nil {
		return m.scanCallback(req)
	}

	// Default behavior: return all configured devices
	m.mu.RLock()
	devices := make([]DiscoveredDevice, 0, len(m.devices))
	for _, d := range m.devices {
		devices = append(devices, *d)
	}
	m.mu.RUnlock()

	now := time.Now()
	result := &ScanResult{
		ScanID:      fmt.Sprintf("mock-scan-%d", time.Now().UnixNano()),
		Status:      ScanStatusCompleted,
		Profile:     req.Profile,
		Subnet:      req.Subnet,
		StartedAt:   now.Add(-m.scanDelay),
		CompletedAt: &now,
		Devices:     devices,
	}

	m.mu.Lock()
	m.stats.TotalScans++
	m.stats.LastScanTime = time.Now()
	m.mu.Unlock()

	return result, nil
}

// GetScanStatus returns the status of a mock scan.
func (m *MockDiscovery) GetScanStatus(scanID string) (*ScanResult, bool) {
	// Mock always returns completed immediately
	return &ScanResult{
		ScanID: scanID,
		Status: ScanStatusCompleted,
	}, true
}

// CancelScan is a no-op for the mock (returns nil to indicate success)
func (m *MockDiscovery) CancelScan(scanID string) error {
	// Nothing to cancel in the mock implementation
	return nil
}

// GetDevices returns all mock devices.
func (m *MockDiscovery) GetDevices() []*DiscoveredDevice {
	m.mu.RLock()
	defer m.mu.RUnlock()

	devices := make([]*DiscoveredDevice, 0, len(m.devices))
	for _, d := range m.devices {
		devices = append(devices, d)
	}
	return devices
}

// GetDevice returns a specific device by ID.
func (m *MockDiscovery) GetDevice(deviceID string) (*DiscoveredDevice, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.devices[deviceID]
	return d, ok
}

// GetDeviceByIP returns a device by IP address.
func (m *MockDiscovery) GetDeviceByIP(ip string) (*DiscoveredDevice, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, d := range m.devices {
		if d.IP == ip {
			return d, true
		}
	}
	return nil, false
}

// ProbeDevice simulates deep discovery.
func (m *MockDiscovery) ProbeDevice(ctx context.Context, req *ProbeRequest) (*ProbeResult, error) {
	m.mu.Lock()
	m.probeCalls++
	m.mu.Unlock()

	// Apply delay if configured
	if m.probeDelay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(m.probeDelay):
		}
	}

	// Return error if configured
	if m.probeError != nil {
		return nil, m.probeError
	}

	// Use custom callback if set
	if m.probeCallback != nil {
		return m.probeCallback(req)
	}

	// Default behavior: return simulated system info
	result := &ProbeResult{
		DeviceID: req.IP,
		IP:       req.IP,
		Status:   ProbeStatusDone,
		System: &SystemInfo{
			OS:           "linux",
			Distribution: "ubuntu",
			Version:      "22.04",
			Kernel:       "5.15.0-generic",
			Arch:         "x86_64",
			CPUCores:     4,
			MemoryMB:     8192,
			DiskGB:       100,
			DockerStatus: "running",
		},
	}

	// Update device in cache
	m.mu.Lock()
	if d, ok := m.devices[req.IP]; ok {
		d.System = result.System
		d.ProbeStatus = ProbeStatusDone
	}
	m.mu.Unlock()

	return result, nil
}

// TestSSH simulates SSH connection test.
func (m *MockDiscovery) TestSSH(ctx context.Context, host string, port int, username, password string) error {
	if m.sshTestError != nil {
		return m.sshTestError
	}
	return nil
}

// GetLocalNetworks returns mock network interfaces.
func (m *MockDiscovery) GetLocalNetworks() ([]NetworkInterface, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.networks, nil
}

// GetLocalSubnets returns mock subnet CIDRs.
func (m *MockDiscovery) GetLocalSubnets() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	subnets := make([]string, 0)
	for _, n := range m.networks {
		subnets = append(subnets, n.Subnets...)
	}
	return subnets, nil
}

// ClearCache clears all mock devices.
func (m *MockDiscovery) ClearCache() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices = make(map[string]*DiscoveredDevice)
}

// GetStats returns mock service statistics.
func (m *MockDiscovery) GetStats() ServiceStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return ServiceStats{
		CachedDevices: len(m.devices),
		ActiveScans:   0,
		LastScanTime:  m.stats.LastScanTime,
		TotalScans:    m.stats.TotalScans,
	}
}

// EnrichScan performs enrichment for a mock scan (no-op in mock, returns a completed status)
func (m *MockDiscovery) EnrichScan(scanID string) (*ScanResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	devices := make([]DiscoveredDevice, 0, len(m.devices))
	for _, d := range m.devices {
		devices = append(devices, *d)
	}

	return &ScanResult{
		ScanID:  scanID,
		Status:  ScanStatusCompleted,
		Devices: devices,
	}, nil
}

// --- Mock-specific Methods ---

// AddDevice adds a device to the mock cache.
func (m *MockDiscovery) AddDevice(device *DiscoveredDevice) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if device.DeviceID == "" {
		device.DeviceID = GenerateDeviceID(device.IP, device.MAC)
	}
	if device.FirstSeen.IsZero() {
		device.FirstSeen = time.Now()
	}
	device.LastSeen = time.Now()

	m.devices[device.DeviceID] = device
}

// RemoveDevice removes a device from the mock cache.
func (m *MockDiscovery) RemoveDevice(deviceID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.devices[deviceID]; ok {
		delete(m.devices, deviceID)
		return true
	}
	return false
}

// SetDevices replaces all devices.
func (m *MockDiscovery) SetDevices(devices []*DiscoveredDevice) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.devices = make(map[string]*DiscoveredDevice)
	for _, d := range devices {
		m.devices[d.DeviceID] = d
	}
}

// GetScanCalls returns the number of StartScan calls.
func (m *MockDiscovery) GetScanCalls() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.scanCalls
}

// GetProbeCalls returns the number of ProbeDevice calls.
func (m *MockDiscovery) GetProbeCalls() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.probeCalls
}

// Reset clears all state and call counts.
func (m *MockDiscovery) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.devices = make(map[string]*DiscoveredDevice)
	m.scanCalls = 0
	m.probeCalls = 0
	m.stats = ServiceStats{}
}

// Ensure MockDiscovery implements Discovery interface.
var _ Discovery = (*MockDiscovery)(nil)

// --- Simulation Helpers for KombiSim ---

// SimulatedDevice creates a device with realistic simulated data.
func SimulatedDevice(ip, hostname string, role DeviceRole) *DiscoveredDevice {
	// Generate pseudo-MAC from IP string bytes
	ipBytes := []byte(ip)
	var b1, b2, b3 byte
	if len(ipBytes) >= 3 {
		b1 = ipBytes[len(ipBytes)-3]
		b2 = ipBytes[len(ipBytes)-2]
		b3 = ipBytes[len(ipBytes)-1]
	}
	mac := fmt.Sprintf("52:54:00:%02x:%02x:%02x", b1, b2, b3)

	services := []DiscoveredService{
		{Name: "ssh", Port: 22},
	}

	switch role {
	case RoleMain:
		services = append(services,
			DiscoveredService{Name: "docker", Port: 2375},
			DiscoveredService{Name: "k8s-api", Port: 6443},
		)
	case RoleWorker:
		services = append(services,
			DiscoveredService{Name: "docker", Port: 2375},
		)
	case RoleStorage:
		services = append(services,
			DiscoveredService{Name: "nfs", Port: 2049},
		)
	}

	device := &DiscoveredDevice{
		DeviceID:         GenerateDeviceID(ip, mac),
		IP:               ip,
		MAC:              mac,
		Hostname:         hostname,
		Services:         services,
		RolesSuggested:   []DeviceRole{role},
		Confidence:       0.85,
		DiscoveryProfile: ProfileActive,
		ProbeStatus:      ProbeStatusPending,
		FirstSeen:        time.Now(),
		LastSeen:         time.Now(),
	}

	return device
}

// SimulatedCluster creates a set of devices representing a typical cluster.
func SimulatedCluster(baseIP string, nodeCount int) []*DiscoveredDevice {
	devices := make([]*DiscoveredDevice, 0, nodeCount+1)

	// Main node
	devices = append(devices, SimulatedDevice(
		baseIP+".10",
		"main-node",
		RoleMain,
	))

	// Worker nodes
	for i := 1; i <= nodeCount; i++ {
		devices = append(devices, SimulatedDevice(
			fmt.Sprintf("%s.%d", baseIP, 10+i),
			fmt.Sprintf("worker-%d", i),
			RoleWorker,
		))
	}

	return devices
}
