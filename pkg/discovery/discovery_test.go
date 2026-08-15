package discovery

import (
	"context"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config == nil {
		t.Fatal("DefaultConfig returned nil")
	}

	if config.DefaultProfile != ProfileActive {
		t.Errorf("expected default profile to be active, got %s", config.DefaultProfile)
	}

	if config.DefaultTimeout != 2 {
		t.Errorf("expected default timeout to be 2, got %d", config.DefaultTimeout)
	}

	if config.MaxConcurrent != 50 {
		t.Errorf("expected max concurrent to be 50, got %d", config.MaxConcurrent)
	}

	if len(config.ActivePorts) == 0 {
		t.Error("expected active ports to be non-empty")
	}
}

func TestDefaultActivePorts(t *testing.T) {
	ports := DefaultActivePorts()

	expectedPorts := map[int]bool{
		22:   true, // SSH
		80:   true, // HTTP
		443:  true, // HTTPS
		2375: true, // Docker
		8006: true, // Proxmox
	}

	for port := range expectedPorts {
		found := false
		for _, p := range ports {
			if p == port {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected port %d to be in default active ports", port)
		}
	}
}

func TestGenerateDeviceID(t *testing.T) {
	tests := []struct {
		mac      string
		ip       string
		expected string
	}{
		{"00:11:22:33:44:55", "192.168.1.1", "mac-00:11:22:33:44:55"},
		{"", "192.168.1.1", "ip-192.168.1.1"},
		{"invalid-mac", "192.168.1.1", "ip-192.168.1.1"},
	}

	for _, test := range tests {
		result := GenerateDeviceID(test.mac, test.ip)
		if result != test.expected {
			t.Errorf("GenerateDeviceID(%q, %q) = %q, expected %q",
				test.mac, test.ip, result, test.expected)
		}
	}
}

func TestSuggestRoles(t *testing.T) {
	// Device with Docker service
	device := &DiscoveredDevice{
		Services: []DiscoveredService{
			{Name: "docker", Port: 2375},
		},
	}

	roles := SuggestRoles(device)
	if len(roles) == 0 {
		t.Error("expected at least one role for device with Docker")
	}

	hasWorker := false
	for _, role := range roles {
		if role == RoleWorker {
			hasWorker = true
			break
		}
	}
	if !hasWorker {
		t.Error("expected worker role for device with Docker")
	}
}

func TestSuggestRoles_HighSpecs(t *testing.T) {
	// Device with high specs
	device := &DiscoveredDevice{
		System: &SystemInfo{
			CPUCores: 8,
			MemoryMB: 16384,
			DiskGB:   1000,
		},
	}

	roles := SuggestRoles(device)

	hasMain := false
	hasStorage := false
	for _, role := range roles {
		if role == RoleMain {
			hasMain = true
		}
		if role == RoleStorage {
			hasStorage = true
		}
	}

	if !hasMain {
		t.Error("expected main role for high-spec device")
	}
	if !hasStorage {
		t.Error("expected storage role for device with large disk")
	}
}

func TestSuggestRoles_Gateway(t *testing.T) {
	tests := []struct {
		name     string
		device   *DiscoveredDevice
		expected DeviceRole
	}{
		{
			name: "gateway by IP .1",
			device: &DiscoveredDevice{
				IP:    "192.168.1.1",
				Ports: []int{53, 67}, // DNS + DHCP
			},
			expected: RoleGateway,
		},
		{
			name: "gateway by IP .254 with DNS",
			device: &DiscoveredDevice{
				IP:    "192.168.1.254",
				Ports: []int{53, 1900}, // DNS + UPnP
			},
			expected: RoleGateway,
		},
		{
			name: "gateway by hostname fritzbox",
			device: &DiscoveredDevice{
				IP:       "192.168.178.1",
				Hostname: "fritz.box",
			},
			expected: RoleGateway,
		},
		{
			name: "gateway by hostname unifi",
			device: &DiscoveredDevice{
				IP:       "10.0.0.1",
				Hostname: "UniFi-Gateway",
			},
			expected: RoleGateway,
		},
		{
			name: "gateway by services only (DHCP)",
			device: &DiscoveredDevice{
				IP:    "192.168.0.50",
				Ports: []int{67, 53},
			},
			expected: RoleGateway,
		},
		{
			name: "not gateway - regular server on .1",
			device: &DiscoveredDevice{
				IP:       "192.168.1.1",
				Hostname: "server01",
				Services: []DiscoveredService{{Name: "ssh", Port: 22}},
			},
			expected: RoleWorker, // Only gateway IP, no gateway services → worker because SSH
		},
		{
			name: "worker with docker",
			device: &DiscoveredDevice{
				IP:       "192.168.1.100",
				Services: []DiscoveredService{{Name: "docker", Port: 2375}},
			},
			expected: RoleWorker,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roles := SuggestRoles(tt.device)
			if len(roles) == 0 {
				t.Fatal("expected at least one role")
			}
			if roles[0] != tt.expected {
				t.Errorf("expected role %s, got %s", tt.expected, roles[0])
			}
		})
	}
}

func TestIsGatewayIP(t *testing.T) {
	tests := []struct {
		ip       string
		expected bool
	}{
		{"192.168.1.1", true},
		{"192.168.1.254", true},
		{"10.0.0.1", true},
		{"192.168.1.100", false},
		{"192.168.1.2", false},
		{"invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			result := isGatewayIP(tt.ip)
			if result != tt.expected {
				t.Errorf("isGatewayIP(%s) = %v, expected %v", tt.ip, result, tt.expected)
			}
		})
	}
}

func TestHasGatewayHostname(t *testing.T) {
	tests := []struct {
		hostname string
		expected bool
	}{
		{"fritz.box", true},
		{"FritzBox-7590", true},
		{"UniFi-Gateway", true},
		{"router.local", true},
		{"gateway", true},
		{"openwrt", true},
		{"pfsense.local", true},
		{"server01", false},
		{"workstation", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.hostname, func(t *testing.T) {
			result := hasGatewayHostname(tt.hostname)
			if result != tt.expected {
				t.Errorf("hasGatewayHostname(%s) = %v, expected %v", tt.hostname, result, tt.expected)
			}
		})
	}
}

func TestCalculateConfidence(t *testing.T) {
	// Minimal device
	minimal := &DiscoveredDevice{}
	confMin := CalculateConfidence(minimal)
	if confMin > 0.1 {
		t.Errorf("expected low confidence for minimal device, got %f", confMin)
	}

	// Well-probed device
	full := &DiscoveredDevice{
		Hostname: "server01",
		MAC:      "00:11:22:33:44:55",
		Services: []DiscoveredService{
			{Name: "ssh", Port: 22},
			{Name: "docker", Port: 2375},
			{Name: "http", Port: 80},
			{Name: "https", Port: 443},
		},
		System: &SystemInfo{
			OS:           "linux",
			Distribution: "ubuntu",
			CPUCores:     4,
			MemoryMB:     8192,
			DockerStatus: "running",
		},
	}

	confFull := CalculateConfidence(full)
	if confFull < 0.8 {
		t.Errorf("expected high confidence for well-probed device, got %f", confFull)
	}
}

func TestNewService(t *testing.T) {
	service := NewService(nil)
	if service == nil {
		t.Fatal("NewService returned nil")
	}

	if service.scanner == nil {
		t.Error("expected scanner to be initialized")
	}

	if service.prober == nil {
		t.Error("expected prober to be initialized")
	}

	if service.cache == nil {
		t.Error("expected cache to be initialized")
	}
}

func TestService_GetStats(t *testing.T) {
	service := NewService(nil)

	stats := service.GetStats()
	if stats.CachedDevices != 0 {
		t.Errorf("expected 0 cached devices, got %d", stats.CachedDevices)
	}

	if stats.ActiveScans != 0 {
		t.Errorf("expected 0 active scans, got %d", stats.ActiveScans)
	}
}

func TestService_ClearCache(t *testing.T) {
	service := NewService(nil)

	// Add a device to cache
	device := DiscoveredDevice{
		DeviceID: "test-device",
		IP:       "192.168.1.1",
	}
	service.updateCache([]DiscoveredDevice{device})

	// Verify device is in cache
	devices := service.GetDevices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device in cache, got %d", len(devices))
	}

	// Clear cache
	service.ClearCache()

	// Verify cache is empty
	devices = service.GetDevices()
	if len(devices) != 0 {
		t.Errorf("expected 0 devices after clear, got %d", len(devices))
	}
}

func TestService_GetDevice(t *testing.T) {
	service := NewService(nil)

	// Add a device to cache
	device := DiscoveredDevice{
		DeviceID: "test-device-123",
		IP:       "192.168.1.100",
		Hostname: "testhost",
	}
	service.updateCache([]DiscoveredDevice{device})

	// Get by ID
	found, ok := service.GetDevice("test-device-123")
	if !ok {
		t.Fatal("expected device to be found")
	}

	if found.IP != "192.168.1.100" {
		t.Errorf("expected IP 192.168.1.100, got %s", found.IP)
	}

	// Get non-existent
	_, ok = service.GetDevice("non-existent")
	if ok {
		t.Error("expected device not to be found")
	}
}

func TestService_GetDeviceByIP(t *testing.T) {
	service := NewService(nil)

	device := DiscoveredDevice{
		DeviceID: "device-by-ip",
		IP:       "10.0.0.50",
		Hostname: "myserver",
	}
	service.updateCache([]DiscoveredDevice{device})

	found, ok := service.GetDeviceByIP("10.0.0.50")
	if !ok {
		t.Fatal("expected device to be found by IP")
	}

	if found.Hostname != "myserver" {
		t.Errorf("expected hostname myserver, got %s", found.Hostname)
	}
}

func TestScanner_GetLocalInterfaces(t *testing.T) {
	scanner := NewScanner(nil)

	interfaces, err := scanner.GetLocalInterfaces()
	if err != nil {
		t.Fatalf("GetLocalInterfaces failed: %v", err)
	}

	// Should have at least one interface (unless running in very isolated environment)
	t.Logf("Found %d local interfaces", len(interfaces))
	for _, iface := range interfaces {
		t.Logf("  - %s: %v (subnets: %v)", iface.Name, iface.IPs, iface.Subnets)
	}
}

func TestScanner_GetLocalSubnets(t *testing.T) {
	scanner := NewScanner(nil)

	subnets, err := scanner.GetLocalSubnets()
	if err != nil {
		t.Fatalf("GetLocalSubnets failed: %v", err)
	}

	t.Logf("Found %d local subnets: %v", len(subnets), subnets)
}

func TestExpandSubnet(t *testing.T) {
	tests := []struct {
		cidr     string
		minHosts int
		maxHosts int
	}{
		{"192.168.1.0/30", 2, 2},  // /30 = 4 IPs, minus network and broadcast = 2
		{"10.0.0.0/29", 6, 6},     // /29 = 8 IPs, minus network and broadcast = 6
		{"172.16.0.0/28", 14, 14}, // /28 = 16 IPs, minus network and broadcast = 14
	}

	for _, test := range tests {
		hosts, err := expandSubnet(test.cidr)
		if err != nil {
			t.Errorf("expandSubnet(%q) failed: %v", test.cidr, err)
			continue
		}

		if len(hosts) < test.minHosts || len(hosts) > test.maxHosts {
			t.Errorf("expandSubnet(%q) = %d hosts, expected %d-%d",
				test.cidr, len(hosts), test.minHosts, test.maxHosts)
		}
	}
}

func TestExpandSubnet_Invalid(t *testing.T) {
	_, err := expandSubnet("invalid")
	if err == nil {
		t.Error("expected error for invalid CIDR")
	}
}

func TestIdentifyService(t *testing.T) {
	tests := []struct {
		port     int
		expected string
	}{
		{22, "ssh"},
		{80, "http"},
		{443, "https"},
		{2375, "docker"},
		{8006, "proxmox"},
		{9999, "unknown-9999"},
	}

	for _, test := range tests {
		svc := identifyService(test.port)
		if svc.Name != test.expected {
			t.Errorf("identifyService(%d) = %q, expected %q",
				test.port, svc.Name, test.expected)
		}
		if svc.Port != test.port {
			t.Errorf("identifyService(%d).Port = %d, expected %d",
				test.port, svc.Port, test.port)
		}
	}
}

func TestProber_QuickProbe_InvalidHost(t *testing.T) {
	prober := NewProber(2 * time.Second)

	// This should fail quickly
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := prober.QuickProbe(ctx, "192.0.2.1", 22, "user", "pass", "")
	if err == nil {
		t.Error("expected error for unreachable host")
	}
}

// Benchmark tests

func BenchmarkGenerateDeviceID_MAC(b *testing.B) {
	for i := 0; i < b.N; i++ {
		GenerateDeviceID("00:11:22:33:44:55", "192.168.1.1")
	}
}

func BenchmarkGenerateDeviceID_IP(b *testing.B) {
	for i := 0; i < b.N; i++ {
		GenerateDeviceID("", "192.168.1.1")
	}
}

func BenchmarkCalculateConfidence(b *testing.B) {
	device := &DiscoveredDevice{
		Hostname: "server01",
		MAC:      "00:11:22:33:44:55",
		Services: []DiscoveredService{
			{Name: "ssh", Port: 22},
			{Name: "docker", Port: 2375},
		},
		System: &SystemInfo{
			OS:       "linux",
			CPUCores: 4,
			MemoryMB: 8192,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalculateConfidence(device)
	}
}

// ============================================
// Interface and Mock Tests
// ============================================

func TestNew_CreatesDiscoveryInterface(t *testing.T) {
	// Test that New() returns a Discovery interface
	var disc = New()

	// Verify it's the right type underneath
	_, ok := disc.(*Service)
	if !ok {
		t.Error("New() should return a *Service implementing Discovery")
	}
}

func TestNew_WithOptions(t *testing.T) {
	config := &DiscoveryConfig{
		DefaultProfile:  ProfilePassive,
		DefaultTimeout:  5,
		MaxConcurrent:   10,
		CacheTTLSeconds: 60,
	}

	disc := New(
		WithConfig(config),
		WithProbeTimeout(10*time.Second),
		WithCacheTTL(time.Minute),
	)
	if _, ok := disc.(*Service); !ok {
		t.Fatal("New() with options should return a *Service implementing Discovery")
	}
}

func TestMockDiscovery_ImplementsInterface(t *testing.T) {
	// Verify mock implements Discovery interface
	var disc Discovery = NewMock()
	if _, ok := disc.(*MockDiscovery); !ok {
		t.Fatal("NewMock() should return a *MockDiscovery implementing Discovery")
	}
}

func TestMockDiscovery_WithDevices(t *testing.T) {
	devices := []*DiscoveredDevice{
		{DeviceID: "test-1", IP: "192.168.1.10", Hostname: "server1"},
		{DeviceID: "test-2", IP: "192.168.1.11", Hostname: "server2"},
	}

	mock := NewMock(MockWithDevices(devices))

	// GetDevices should return both
	result := mock.GetDevices()
	if len(result) != 2 {
		t.Errorf("expected 2 devices, got %d", len(result))
	}

	// GetDevice should find by ID
	d, ok := mock.GetDevice("test-1")
	if !ok {
		t.Error("GetDevice(test-1) not found")
	}
	if d.Hostname != "server1" {
		t.Errorf("expected hostname server1, got %s", d.Hostname)
	}

	// GetDeviceByIP should work
	d, ok = mock.GetDeviceByIP("192.168.1.11")
	if !ok {
		t.Error("GetDeviceByIP(192.168.1.11) not found")
	}
	if d.DeviceID != "test-2" {
		t.Errorf("expected device test-2, got %s", d.DeviceID)
	}
}

func TestMockDiscovery_StartScan(t *testing.T) {
	devices := []*DiscoveredDevice{
		{DeviceID: "test-1", IP: "192.168.1.10"},
	}

	mock := NewMock(MockWithDevices(devices))

	result, err := mock.StartScan(context.Background(), &ScanRequest{
		Profile: ProfileActive,
		Subnet:  "192.168.1.0/24",
	})

	if err != nil {
		t.Fatalf("StartScan failed: %v", err)
	}

	if result.Status != ScanStatusCompleted {
		t.Errorf("expected completed status, got %s", result.Status)
	}

	if len(result.Devices) != 1 {
		t.Errorf("expected 1 device in result, got %d", len(result.Devices))
	}

	if mock.GetScanCalls() != 1 {
		t.Errorf("expected 1 scan call, got %d", mock.GetScanCalls())
	}
}

func TestMockDiscovery_ProbeDevice(t *testing.T) {
	mock := NewMock()
	mock.AddDevice(&DiscoveredDevice{
		DeviceID:    "test-1",
		IP:          "192.168.1.10",
		ProbeStatus: ProbeStatusPending,
	})

	result, err := mock.ProbeDevice(context.Background(), &ProbeRequest{
		IP:      "192.168.1.10",
		SSHUser: "admin",
	})

	if err != nil {
		t.Fatalf("ProbeDevice failed: %v", err)
	}

	if result.System == nil {
		t.Error("expected system info in result")
	}

	if result.System.OS != "linux" {
		t.Errorf("expected linux OS, got %s", result.System.OS)
	}

	if mock.GetProbeCalls() != 1 {
		t.Errorf("expected 1 probe call, got %d", mock.GetProbeCalls())
	}
}

func TestMockDiscovery_WithScanError(t *testing.T) {
	mock := NewMock(MockWithScanError(context.DeadlineExceeded))

	_, err := mock.StartScan(context.Background(), &ScanRequest{})
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded error, got %v", err)
	}
}

func TestMockDiscovery_Reset(t *testing.T) {
	mock := NewMock()
	mock.AddDevice(&DiscoveredDevice{DeviceID: "test-1", IP: "192.168.1.10"})

	_, _ = mock.StartScan(context.Background(), &ScanRequest{})
	_, _ = mock.ProbeDevice(context.Background(), &ProbeRequest{IP: "192.168.1.10"})

	mock.Reset()

	if len(mock.GetDevices()) != 0 {
		t.Error("expected empty devices after reset")
	}
	if mock.GetScanCalls() != 0 {
		t.Error("expected 0 scan calls after reset")
	}
	if mock.GetProbeCalls() != 0 {
		t.Error("expected 0 probe calls after reset")
	}
}

func TestSimulatedDevice(t *testing.T) {
	device := SimulatedDevice("192.168.1.10", "test-server", RoleWorker)

	if device.IP != "192.168.1.10" {
		t.Errorf("expected IP 192.168.1.10, got %s", device.IP)
	}

	if device.Hostname != "test-server" {
		t.Errorf("expected hostname test-server, got %s", device.Hostname)
	}

	if len(device.RolesSuggested) != 1 || device.RolesSuggested[0] != RoleWorker {
		t.Errorf("expected role worker, got %v", device.RolesSuggested)
	}

	// Worker should have ssh and docker services
	hasSSH := false
	hasDocker := false
	for _, svc := range device.Services {
		if svc.Name == "ssh" {
			hasSSH = true
		}
		if svc.Name == "docker" {
			hasDocker = true
		}
	}

	if !hasSSH {
		t.Error("expected ssh service for worker")
	}
	if !hasDocker {
		t.Error("expected docker service for worker")
	}
}

func TestSimulatedCluster(t *testing.T) {
	devices := SimulatedCluster("192.168.1", 3)

	if len(devices) != 4 { // 1 main + 3 workers
		t.Errorf("expected 4 devices, got %d", len(devices))
	}

	// First should be main
	if len(devices[0].RolesSuggested) == 0 || devices[0].RolesSuggested[0] != RoleMain {
		t.Errorf("expected first device to be main, got %v", devices[0].RolesSuggested)
	}

	// Rest should be workers
	for i := 1; i < len(devices); i++ {
		if len(devices[i].RolesSuggested) == 0 || devices[i].RolesSuggested[0] != RoleWorker {
			t.Errorf("expected device %d to be worker, got %v", i, devices[i].RolesSuggested)
		}
	}
}

func TestDiscoveryHandler_UsesInterface(t *testing.T) {
	// This tests that any Discovery implementation works with the handler pattern
	mock := NewMock(MockWithDevices([]*DiscoveredDevice{
		{DeviceID: "test-1", IP: "192.168.1.10"},
	}))

	// This simulates what the routes package does
	handleGetDevices := func(disc Discovery) []*DiscoveredDevice {
		return disc.GetDevices()
	}

	devices := handleGetDevices(mock)
	if len(devices) != 1 {
		t.Errorf("expected 1 device, got %d", len(devices))
	}
}
