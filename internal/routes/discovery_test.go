package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/discovery"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestDiscoveryNetworksStopsAfterUnauthenticatedResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	event := &httpx.Event{
		Request:  httptest.NewRequest(http.MethodGet, "/api/v1/discovery/networks", nil),
		Response: rec,
	}
	disc := &fakeDiscovery{}
	handler := NewDiscoveryHandler(disc)
	defer handler.Stop()

	if err := handler.networks(event); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		t.Fatalf("networks returned error: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if disc.localNetworksCalls != 0 {
		t.Fatalf("GetLocalNetworks called %d times, want 0", disc.localNetworksCalls)
	}
}

func TestAuthenticatedDiscoveryOwnerStopsWhenPBHelperOnlyWritesResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	event := &httpx.Event{
		Request:  httptest.NewRequest(http.MethodGet, "/api/v1/discovery/devices", nil),
		Response: rec,
	}

	ownerID, ok, err := authenticatedDiscoveryOwner(event)
	if err == nil {
		t.Fatal("authenticatedDiscoveryOwner must return an error once it has written the refusal")
	}
	if ok || ownerID != "" {
		t.Fatalf("expected empty owner and ok=false, got ownerID=%q ok=%v", ownerID, ok)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestDiscoveryScanRequestDefaultsEmptyBody(t *testing.T) {
	req, err := decodeDiscoveryScanRequest(io.NopCloser(strings.NewReader("")))
	if err != nil {
		t.Fatalf("decodeDiscoveryScanRequest returned error: %v", err)
	}
	if req.Profile != discovery.ProfileActive {
		t.Fatalf("profile = %q, want %q", req.Profile, discovery.ProfileActive)
	}
}

func TestDiscoveryScanRequestRejectsMalformedJSON(t *testing.T) {
	_, err := decodeDiscoveryScanRequest(io.NopCloser(strings.NewReader("{")))
	if err == nil {
		t.Fatal("expected malformed JSON to be rejected")
	}
}

func TestValidateDiscoveryScanRequest(t *testing.T) {
	tests := []struct {
		name string
		req  discovery.ScanRequest
	}{
		{name: "bad subnet", req: discovery.ScanRequest{Subnet: "not-cidr"}},
		{name: "ipv6 subnet", req: discovery.ScanRequest{Subnet: "fd00::/64"}},
		{name: "bad target", req: discovery.ScanRequest{TargetIPs: []string{"not-ip"}}},
		{name: "bad exclude", req: discovery.ScanRequest{ExcludeIPs: []string{"not-ip"}}},
		{name: "negative timeout", req: discovery.ScanRequest{TimeoutSeconds: -1}},
		{name: "negative max concurrent", req: discovery.ScanRequest{MaxConcurrent: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateDiscoveryScanRequest(tt.req); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	valid := discovery.ScanRequest{
		Subnet:     "192.168.1.0/24",
		TargetIPs:  []string{"192.168.1.10"},
		ExcludeIPs: []string{"192.168.1.1"},
	}
	if err := validateDiscoveryScanRequest(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

func TestValidateDiscoveryProbeRequest(t *testing.T) {
	tests := []struct {
		name string
		req  discovery.ProbeRequest
	}{
		{name: "missing ip", req: discovery.ProbeRequest{SSHUser: "root"}},
		{name: "bad ip", req: discovery.ProbeRequest{IP: "not-ip", SSHUser: "root"}},
		{name: "missing ssh user", req: discovery.ProbeRequest{IP: "192.168.1.10"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateDiscoveryProbeRequest(tt.req); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	if err := validateDiscoveryProbeRequest(discovery.ProbeRequest{IP: "192.168.1.10", SSHUser: "root"}); err != nil {
		t.Fatalf("valid probe request rejected: %v", err)
	}
}

func TestDecodeDiscoveryTestSSHRequestDefaultsPort(t *testing.T) {
	req, err := decodeDiscoveryTestSSHRequest(io.NopCloser(strings.NewReader(`{"ip":"192.168.1.10","user":"root"}`)))
	if err != nil {
		t.Fatalf("decodeDiscoveryTestSSHRequest returned error: %v", err)
	}
	if req.Port != 22 {
		t.Fatalf("port = %d, want 22", req.Port)
	}
}

func TestDiscoveryStartScanUsesBackgroundContext(t *testing.T) {
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"subnet":"192.168.1.0/24"}`)
	req := authenticatedDiscoveryTestRequest(http.MethodPost, "/api/v1/discovery/scan", "owner-1", nil, body)
	event := &httpx.Event{Request: req, Response: rec}
	disc := &fakeDiscovery{startResult: &discovery.ScanResult{ScanID: "scan-1", Status: discovery.ScanStatusRunning}}
	handler := NewDiscoveryHandler(disc)
	defer handler.Stop()

	if err := handler.startScan(event); err != nil {
		t.Fatalf("startScan returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if disc.startCtx == nil {
		t.Fatal("StartScan was not called")
	}
	if disc.startCtx == req.Context() {
		t.Fatal("StartScan used request context, want detached background context")
	}
	if !handler.isScanOwner("scan-1", "owner-1") {
		t.Fatal("scan owner was not recorded")
	}
}

func TestDiscoveryDevicesFailClosedUntilOwned(t *testing.T) {
	device := &discovery.DiscoveredDevice{DeviceID: "device-1", IP: "192.168.1.10"}
	disc := &fakeDiscovery{devices: map[string]*discovery.DiscoveredDevice{device.DeviceID: device}}
	handler := NewDiscoveryHandler(disc)
	defer handler.Stop()

	listEvent, listRecorder := discoveryRouteTestEvent(http.MethodGet, "/api/v1/discovery/devices", "", "owner-1", nil, "")
	if err := handler.listDevices(listEvent); err != nil {
		t.Fatalf("listDevices returned error: %v", err)
	}
	var envelope struct {
		Data struct {
			Count int `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if envelope.Data.Count != 0 {
		t.Fatalf("unowned device count = %d, want 0", envelope.Data.Count)
	}

	getEvent, getRecorder := discoveryRouteTestEvent(http.MethodGet, "/api/v1/discovery/devices/device-1", "device-1", "owner-1", nil, "")
	if err := handler.getDevice(getEvent); err != nil {
		t.Fatalf("getDevice returned error: %v", err)
	}
	if getRecorder.Code != http.StatusNotFound {
		t.Fatalf("get status = %d, want 404", getRecorder.Code)
	}
}

func TestDiscoveryScanStatusAssignsCompletedDevicesToScanOwner(t *testing.T) {
	device := discovery.DiscoveredDevice{DeviceID: "device-1", IP: "192.168.1.10"}
	disc := &fakeDiscovery{scanStatusResult: &discovery.ScanResult{
		ScanID:  "scan-1",
		Status:  discovery.ScanStatusCompleted,
		Devices: []discovery.DiscoveredDevice{device},
	}}
	handler := NewDiscoveryHandler(disc)
	defer handler.Stop()
	handler.setScanOwner("scan-1", "owner-1")

	event, recorder := discoveryRouteTestEvent(http.MethodGet, "/api/v1/discovery/scan/scan-1", "scan-1", "owner-1", nil, "")
	if err := handler.scanStatus(event); err != nil {
		t.Fatalf("scanStatus returned error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !handler.isDeviceOwner("device-1", "owner-1") {
		t.Fatal("completed scan device was not assigned to scan owner")
	}
}

func TestDiscoveryProbeRequiresOwnedDeviceOrAdmin(t *testing.T) {
	device := &discovery.DiscoveredDevice{DeviceID: "device-1", IP: "192.168.1.10"}
	disc := &fakeDiscovery{devices: map[string]*discovery.DiscoveredDevice{device.DeviceID: device}}
	handler := NewDiscoveryHandler(disc)
	defer handler.Stop()

	unownedEvent, unownedRecorder := discoveryRouteTestEvent(http.MethodPost, "/api/v1/discovery/probe", "", "owner-1", nil, `{"ip":"192.168.1.10","ssh_user":"root"}`)
	if err := handler.probeDevice(unownedEvent); err != nil {
		t.Fatalf("probeDevice returned error: %v", err)
	}
	if unownedRecorder.Code != http.StatusNotFound {
		t.Fatalf("unowned status = %d, want 404", unownedRecorder.Code)
	}
	if disc.probeCalls != 0 {
		t.Fatalf("ProbeDevice calls = %d, want 0", disc.probeCalls)
	}

	handler.setDeviceOwner("device-1", "owner-1")
	ownedEvent, ownedRecorder := discoveryRouteTestEvent(http.MethodPost, "/api/v1/discovery/probe", "", "owner-1", nil, `{"ip":"192.168.1.10","ssh_user":"root"}`)
	if err := handler.probeDevice(ownedEvent); err != nil {
		t.Fatalf("owned probeDevice returned error: %v", err)
	}
	if ownedRecorder.Code != http.StatusOK {
		t.Fatalf("owned status = %d, want 200", ownedRecorder.Code)
	}

	adminEvent, adminRecorder := discoveryRouteTestEvent(http.MethodPost, "/api/v1/discovery/probe", "", "admin-1", []string{"admin"}, `{"ip":"10.0.0.5","ssh_user":"root"}`)
	if err := handler.probeDevice(adminEvent); err != nil {
		t.Fatalf("admin probeDevice returned error: %v", err)
	}
	if adminRecorder.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", adminRecorder.Code)
	}
}

func TestDiscoveryTestSSHRequiresOwnedDeviceOrAdmin(t *testing.T) {
	device := &discovery.DiscoveredDevice{DeviceID: "device-1", IP: "192.168.1.10"}
	disc := &fakeDiscovery{devices: map[string]*discovery.DiscoveredDevice{device.DeviceID: device}}
	handler := NewDiscoveryHandler(disc)
	defer handler.Stop()

	unownedEvent, unownedRecorder := discoveryRouteTestEvent(http.MethodPost, "/api/v1/discovery/test-ssh", "", "owner-1", nil, `{"ip":"192.168.1.10","user":"root"}`)
	if err := handler.testSSH(unownedEvent); err != nil {
		t.Fatalf("testSSH returned error: %v", err)
	}
	if unownedRecorder.Code != http.StatusNotFound {
		t.Fatalf("unowned status = %d, want 404", unownedRecorder.Code)
	}
	if disc.testSSHCalls != 0 {
		t.Fatalf("TestSSH calls = %d, want 0", disc.testSSHCalls)
	}

	handler.setDeviceOwner("device-1", "owner-1")
	ownedEvent, ownedRecorder := discoveryRouteTestEvent(http.MethodPost, "/api/v1/discovery/test-ssh", "", "owner-1", nil, `{"ip":"192.168.1.10","user":"root"}`)
	if err := handler.testSSH(ownedEvent); err != nil {
		t.Fatalf("owned testSSH returned error: %v", err)
	}
	if ownedRecorder.Code != http.StatusOK {
		t.Fatalf("owned status = %d, want 200", ownedRecorder.Code)
	}
}

func TestDiscoveryCacheAndStatsRequireAdmin(t *testing.T) {
	disc := &fakeDiscovery{}
	handler := NewDiscoveryHandler(disc)
	defer handler.Stop()

	cacheEvent, cacheRecorder := discoveryRouteTestEvent(http.MethodDelete, "/api/v1/discovery/cache", "", "owner-1", nil, "")
	if err := handler.clearCache(cacheEvent); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		t.Fatalf("clearCache returned error: %v", err)
	}
	if cacheRecorder.Code != http.StatusForbidden {
		t.Fatalf("clear cache status = %d, want 403", cacheRecorder.Code)
	}
	if disc.clearCacheCalls != 0 {
		t.Fatalf("ClearCache calls = %d, want 0", disc.clearCacheCalls)
	}

	statsEvent, statsRecorder := discoveryRouteTestEvent(http.MethodGet, "/api/v1/discovery/stats", "", "owner-1", nil, "")
	if err := handler.stats(statsEvent); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		t.Fatalf("stats returned error: %v", err)
	}
	if statsRecorder.Code != http.StatusForbidden {
		t.Fatalf("stats status = %d, want 403", statsRecorder.Code)
	}
	if disc.statsCalls != 0 {
		t.Fatalf("GetStats calls = %d, want 0", disc.statsCalls)
	}

	adminEvent, adminRecorder := discoveryRouteTestEvent(http.MethodDelete, "/api/v1/discovery/cache", "", "admin-1", []string{"admin"}, "")
	if err := handler.clearCache(adminEvent); err != nil {
		t.Fatalf("admin clearCache returned error: %v", err)
	}
	if adminRecorder.Code != http.StatusOK {
		t.Fatalf("admin clear cache status = %d, want 200", adminRecorder.Code)
	}
	if disc.clearCacheCalls != 1 {
		t.Fatalf("ClearCache calls = %d, want 1", disc.clearCacheCalls)
	}
}

type fakeDiscovery struct {
	localNetworksCalls int
	startCtx           context.Context
	startReq           *discovery.ScanRequest
	startResult        *discovery.ScanResult
	startErr           error
	testSSHErr         error
	devices            map[string]*discovery.DiscoveredDevice
	scanStatusResult   *discovery.ScanResult
	probeCalls         int
	testSSHCalls       int
	clearCacheCalls    int
	statsCalls         int
}

func (f *fakeDiscovery) StartScan(ctx context.Context, req *discovery.ScanRequest) (*discovery.ScanResult, error) {
	f.startCtx = ctx
	f.startReq = req
	if f.startErr != nil {
		return nil, f.startErr
	}
	if f.startResult != nil {
		return f.startResult, nil
	}
	return &discovery.ScanResult{ScanID: "scan-1", Status: discovery.ScanStatusRunning}, nil
}

func (f *fakeDiscovery) GetScanStatus(scanID string) (*discovery.ScanResult, bool) {
	if f.scanStatusResult != nil {
		return f.scanStatusResult, true
	}
	if f.startResult != nil && f.startResult.ScanID == scanID {
		return f.startResult, true
	}
	return nil, false
}

func (f *fakeDiscovery) CancelScan(string) error {
	return nil
}

func (f *fakeDiscovery) EnrichScan(string) (*discovery.ScanResult, error) {
	return nil, nil
}

func (f *fakeDiscovery) GetDevices() []*discovery.DiscoveredDevice {
	devices := make([]*discovery.DiscoveredDevice, 0, len(f.devices))
	for _, device := range f.devices {
		devices = append(devices, device)
	}
	return devices
}

func (f *fakeDiscovery) GetDevice(deviceID string) (*discovery.DiscoveredDevice, bool) {
	device, ok := f.devices[deviceID]
	return device, ok
}

func (f *fakeDiscovery) GetDeviceByIP(ip string) (*discovery.DiscoveredDevice, bool) {
	for _, device := range f.devices {
		if device.IP == ip {
			return device, true
		}
	}
	return nil, false
}

func (f *fakeDiscovery) ProbeDevice(context.Context, *discovery.ProbeRequest) (*discovery.ProbeResult, error) {
	f.probeCalls++
	return &discovery.ProbeResult{Status: discovery.ProbeStatusFailed}, errors.New("probe failed")
}

func (f *fakeDiscovery) TestSSH(context.Context, string, int, string, string) error {
	f.testSSHCalls++
	return f.testSSHErr
}

func (f *fakeDiscovery) GetLocalNetworks() ([]discovery.NetworkInterface, error) {
	f.localNetworksCalls++
	return []discovery.NetworkInterface{{Name: "eth0"}}, nil
}

func (f *fakeDiscovery) GetLocalSubnets() ([]string, error) {
	return nil, nil
}

func (f *fakeDiscovery) ClearCache() {
	f.clearCacheCalls++
}

func (f *fakeDiscovery) GetStats() discovery.ServiceStats {
	f.statsCalls++
	return discovery.ServiceStats{LastScanTime: time.Now()}
}

func discoveryRouteTestEvent(method, target, pathID, userID string, roles []string, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := authenticatedDiscoveryTestRequest(method, target, userID, roles, strings.NewReader(body))
	if pathID != "" {
		req.SetPathValue("id", pathID)
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func authenticatedDiscoveryTestRequest(method, target, userID string, roles []string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	if userID != "" {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{
			UserID: userID,
			Roles:  roles,
		}))
	}
	return req
}
