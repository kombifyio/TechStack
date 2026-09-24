package discovery

import (
	"testing"
)

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

func TestService_ClearCache(t *testing.T) {
	service := New()

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
	service := New()

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
	service := New()

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
		// A bare open port is not a Proxmox fingerprint: identifyService must
		// not claim Proxmox on 8006. The real claim comes from
		// FingerprintProxmox in the active scanner.
		{8006, "https"},
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
