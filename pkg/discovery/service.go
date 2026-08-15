package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Service coordinates network discovery operations.
// It implements the Discovery interface for use across kombifyTechstack components.
type Service struct {
	config  *DiscoveryConfig
	scanner *Scanner
	prober  *Prober
	logger  *slog.Logger

	// Cache configuration
	cacheTTL time.Duration

	// In-memory cache of discovered devices
	cache    map[string]*DiscoveredDevice
	cacheMu  sync.RWMutex
	cacheExp time.Time

	// Active scans
	scans      map[string]*ScanResult
	scansMu    sync.RWMutex
	totalScans int
	// Cancellation handles for running scans
	scanCancels map[string]context.CancelFunc
	// maxScans limits how many scan results are kept in memory (default: 100)
	maxScans int
	// scanTTL is how long scan results are kept before cleanup (default: 1 hour)
	scanTTL time.Duration
}

// NewService creates a new discovery service.
// Deprecated: Use New() with options instead for better configurability.
func NewService(config *DiscoveryConfig) *Service {
	if config == nil {
		config = DefaultConfig()
	}

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

// StartScan initiates a network discovery scan and returns immediately with
// a running ScanResult (scan_id). The actual scan is executed asynchronously
// and progress updates are stored in-memory and available via GetScanStatus.
func (s *Service) StartScan(ctx context.Context, req *ScanRequest) (*ScanResult, error) {
	if req == nil {
		req = &ScanRequest{}
	}
	// Keep scan history bounded.
	s.CleanupOldScans()

	if req.Profile == "" {
		req.Profile = s.config.DefaultProfile
	}

	// Create initial running result and store it
	scanID := generateScanID()
	initial := &ScanResult{
		ScanID:    scanID,
		Status:    ScanStatusRunning,
		Profile:   req.Profile,
		Subnet:    req.Subnet,
		StartedAt: time.Now(),
		Devices:   make([]DiscoveredDevice, 0),
		Progress:  0,
	}

	// Estimate total hosts early so UI can display a reasonable denominator
	var totalEstimate int
	if req.Subnet != "" {
		if hosts, err := expandSubnet(req.Subnet); err == nil {
			totalEstimate = len(hosts)
		}
	} else {
		if subs, err := s.scanner.GetLocalSubnets(); err == nil && len(subs) > 0 {
			if hosts, err := expandSubnet(subs[0]); err == nil {
				totalEstimate = len(hosts)
			}
		}
	}
	initial.TotalHosts = totalEstimate

	s.scansMu.Lock()
	s.scans[scanID] = initial
	s.totalScans++
	s.scansMu.Unlock()

	scanParent := ctx
	if scanParent == nil {
		scanParent = context.Background()
	}

	// Start the actual scan in a background goroutine
	go func(id string, r *ScanRequest, parent context.Context) {
		scanCtx, cancel := context.WithCancel(parent)
		// Store cancel func so it can be triggered by CancelScan
		s.scansMu.Lock()
		s.scanCancels[id] = cancel
		s.scansMu.Unlock()

		defer func() {
			s.scansMu.Lock()
			delete(s.scanCancels, id)
			s.scansMu.Unlock()
		}()

		// define progress callback
		onProgress := func(p *ScanResult) {
			s.scansMu.Lock()
			// update partial result stored in service
			current := s.scans[id]
			if current == nil {
				current = &ScanResult{ScanID: id}
			}
			current.Devices = p.Devices
			current.ScannedHosts = p.ScannedHosts
			current.TotalHosts = p.TotalHosts
			current.Progress = p.Progress
			current.Status = p.Status
			current.Errors = p.Errors
			s.scans[id] = current
			s.scansMu.Unlock()
		}

		var final *ScanResult
		var err error
		s.logger.Debug("Async scan started", "scan_id", id, "profile", r.Profile)
		switch r.Profile {
		case ProfilePassive:
			final, err = s.scanner.ScanPassive(scanCtx, r, onProgress)
		case ProfileActive, ProfileDeep:
			final, err = s.scanner.ScanActive(scanCtx, r, onProgress)
		default:
			final, err = s.scanner.ScanActive(scanCtx, r, onProgress)
		}

		if err != nil {
			s.logger.Error("Scan failed", "scan_id", id, "error", err)
			// store failed state
			if final == nil {
				final = &ScanResult{ScanID: id}
			}
			final.Status = ScanStatusFailed
			final.Errors = append(final.Errors, err.Error())
		}

		// Update cache and store final result
		if final != nil {
			s.updateCache(final.Devices)
		}

		s.scansMu.Lock()
		if final != nil {
			s.scans[id] = final
		} else {
			// ensure at least status is failed
			cur := s.scans[id]
			if cur == nil {
				cur = &ScanResult{ScanID: id}
			}
			cur.Status = ScanStatusFailed
			s.scans[id] = cur
		}
		s.scansMu.Unlock()

		s.logger.Info("Async scan finished", "scan_id", id, "devices_found", len(final.Devices))
	}(scanID, req, scanParent)

	return initial, nil
}

// GetScanStatus returns the status of a running or completed scan.
func (s *Service) GetScanStatus(scanID string) (*ScanResult, bool) {
	s.scansMu.RLock()
	defer s.scansMu.RUnlock()

	result, ok := s.scans[scanID]
	return result, ok
}

// EnrichScan triggers on-demand enrichment (for example ARP cache lookups)
// and updates the stored ScanResult.
func (s *Service) EnrichScan(scanID string) (*ScanResult, error) {
	s.scansMu.Lock()
	defer s.scansMu.Unlock()

	res, ok := s.scans[scanID]
	if !ok {
		return nil, fmt.Errorf("scan not found")
	}

	arp, err := GetARPCache()
	if err != nil {
		res.Errors = append(res.Errors, "arp: "+err.Error())
		s.scans[scanID] = res
		return res, nil
	}

	for i := range res.Devices {
		if res.Devices[i].MAC == "" {
			if mac, ok := arp[res.Devices[i].IP]; ok {
				res.Devices[i].MAC = mac
				res.Devices[i].DeviceID = GenerateDeviceID(mac, res.Devices[i].IP)
			}
		}
	}

	s.scans[scanID] = res
	return res, nil
}

// CancelScan attempts to cancel a running scan and mark it as canceled.
func (s *Service) CancelScan(scanID string) error {
	s.scansMu.Lock()
	cancel, ok := s.scanCancels[scanID]
	if ok {
		cancel()
		delete(s.scanCancels, scanID)
		// mark as canceled
		if res, exists := s.scans[scanID]; exists {
			res.Status = ScanStatusCancelled
			res.Errors = append(res.Errors, "canceled by user")
			s.scans[scanID] = res
		}
		s.scansMu.Unlock()
		return nil
	}
	s.scansMu.Unlock()
	return fmt.Errorf("scan not found or already finished")
}

// GetDevices returns all cached discovered devices.
func (s *Service) GetDevices() []*DiscoveredDevice {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	// Check cache expiration
	if s.cacheTTL > 0 && time.Now().After(s.cacheExp) {
		return nil
	}

	devices := make([]*DiscoveredDevice, 0, len(s.cache))
	for _, device := range s.cache {
		devices = append(devices, device)
	}

	return devices
}

// GetDevice returns a specific discovered device by ID.
func (s *Service) GetDevice(deviceID string) (*DiscoveredDevice, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	device, ok := s.cache[deviceID]
	if !ok {
		return nil, false
	}

	return device, true
}

// GetDeviceByIP returns a discovered device by IP address.
func (s *Service) GetDeviceByIP(ip string) (*DiscoveredDevice, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	for _, device := range s.cache {
		if device.IP == ip {
			return device, true
		}
	}

	return nil, false
}

// ProbeDevice performs deep discovery on a specific device.
func (s *Service) ProbeDevice(ctx context.Context, req *ProbeRequest) (*ProbeResult, error) {
	s.logger.Debug("Probing device", "ip", req.IP, "port", req.SSHPort)

	result, err := s.prober.Probe(ctx, req)
	if err != nil {
		// Update device probe status in cache
		s.updateDeviceProbeStatus(req.IP, ProbeStatusFailed, err.Error())
		return result, err
	}

	// Update cache with probe results
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	for id, device := range s.cache {
		if device.IP == req.IP {
			device.System = result.System
			device.ProbeStatus = ProbeStatusDone
			device.DiscoveryProfile = ProfileDeep
			device.RolesSuggested = SuggestRoles(device)
			device.Confidence = CalculateConfidence(device)
			device.LastSeen = time.Now()
			s.cache[id] = device
			break
		}
	}

	s.logger.Info("Device probed successfully",
		"ip", req.IP,
		"os", result.System.OS,
		"docker", result.System.DockerStatus,
	)

	return result, nil
}

// TestSSH tests SSH connectivity without full probe.
func (s *Service) TestSSH(ctx context.Context, host string, port int, username, password string) error {
	return s.prober.QuickProbe(ctx, host, port, username, password, "")
}

// TestSSHWithKey tests SSH connectivity using a private key.
func (s *Service) TestSSHWithKey(ctx context.Context, host string, port int, username, privateKey string) error {
	return s.prober.QuickProbe(ctx, host, port, username, "", privateKey)
}

// ClearCache removes all cached devices.
func (s *Service) ClearCache() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	s.cache = make(map[string]*DiscoveredDevice)
	s.cacheExp = time.Time{}

	s.logger.Debug("Discovery cache cleared")
}

// ClearScans removes all stored scan results.
func (s *Service) ClearScans() {
	s.scansMu.Lock()
	defer s.scansMu.Unlock()

	s.scans = make(map[string]*ScanResult)
}

// CleanupOldScans removes completed scans older than TTL and enforces maxScans limit.
// This should be called periodically to prevent memory leaks.
func (s *Service) CleanupOldScans() {
	s.scansMu.Lock()
	defer s.scansMu.Unlock()

	now := time.Now()
	toDelete := make([]string, 0)

	// Find scans older than TTL (only completed/failed/canceled)
	for id, scan := range s.scans {
		if scan.Status != ScanStatusRunning && scan.Status != ScanStatusPending {
			if scan.CompletedAt != nil && now.Sub(*scan.CompletedAt) > s.scanTTL {
				toDelete = append(toDelete, id)
			}
		}
	}

	// Delete old scans
	for _, id := range toDelete {
		delete(s.scans, id)
	}

	// If still over limit, delete oldest completed scans
	completedCount := 0
	for _, scan := range s.scans {
		if scan.Status != ScanStatusRunning && scan.Status != ScanStatusPending {
			completedCount++
		}
	}

	if completedCount > s.maxScans {
		// Find and delete excess scans (oldest first)
		type scanAge struct {
			id  string
			age time.Time
		}
		completed := make([]scanAge, 0)
		for id, scan := range s.scans {
			if scan.Status != ScanStatusRunning && scan.Status != ScanStatusPending {
				age := scan.StartedAt
				if scan.CompletedAt != nil {
					age = *scan.CompletedAt
				}
				completed = append(completed, scanAge{id: id, age: age})
			}
		}
		// Sort by age (oldest first) using optimized sort
		sort.Slice(completed, func(i, j int) bool {
			return completed[i].age.Before(completed[j].age)
		})
		// Delete excess
		excess := len(completed) - s.maxScans
		for i := 0; i < excess && i < len(completed); i++ {
			delete(s.scans, completed[i].id)
		}
	}

	if len(toDelete) > 0 {
		s.logger.Debug("Cleaned up old scans", "count", len(toDelete))
	}
}

// GetLocalNetworks returns available local network interfaces and subnets.
func (s *Service) GetLocalNetworks() ([]NetworkInterface, error) {
	return s.scanner.GetLocalInterfaces()
}

// GetLocalSubnets returns a list of local subnet CIDRs.
func (s *Service) GetLocalSubnets() ([]string, error) {
	return s.scanner.GetLocalSubnets()
}

// GetStats returns current service statistics.
func (s *Service) GetStats() ServiceStats {
	s.cacheMu.RLock()
	cachedDevices := len(s.cache)
	s.cacheMu.RUnlock()

	s.scansMu.RLock()
	activeScans := 0
	var lastScanTime time.Time
	for _, scan := range s.scans {
		if scan.Status == ScanStatusRunning {
			activeScans++
		}
		if scan.CompletedAt != nil && scan.CompletedAt.After(lastScanTime) {
			lastScanTime = *scan.CompletedAt
		}
	}
	totalScans := s.totalScans
	s.scansMu.RUnlock()

	return ServiceStats{
		CachedDevices: cachedDevices,
		ActiveScans:   activeScans,
		LastScanTime:  lastScanTime,
		TotalScans:    totalScans,
	}
}

// AddDevice manually adds a device to the cache.
// Useful for KombiSim integration or importing known devices.
func (s *Service) AddDevice(device *DiscoveredDevice) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if device.DeviceID == "" {
		device.DeviceID = GenerateDeviceID(device.IP, device.MAC)
	}
	if device.FirstSeen.IsZero() {
		device.FirstSeen = time.Now()
	}
	device.LastSeen = time.Now()

	s.cache[device.DeviceID] = device
}

// RemoveDevice removes a device from the cache.
func (s *Service) RemoveDevice(deviceID string) bool {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if _, ok := s.cache[deviceID]; ok {
		delete(s.cache, deviceID)
		return true
	}
	return false
}

// updateCache updates the device cache with new discoveries.
func (s *Service) updateCache(devices []DiscoveredDevice) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	for _, device := range devices {
		existing, ok := s.cache[device.DeviceID]
		if ok {
			// Merge with existing device
			device.FirstSeen = existing.FirstSeen
			if existing.System != nil && device.System == nil {
				device.System = existing.System
			}
			if existing.ProbeStatus == ProbeStatusDone {
				device.ProbeStatus = existing.ProbeStatus
			}
		}
		s.cache[device.DeviceID] = &device
	}

	// Update cache expiration
	if s.cacheTTL > 0 {
		s.cacheExp = time.Now().Add(s.cacheTTL)
	}
}

// updateDeviceProbeStatus updates the probe status for a device by IP.
func (s *Service) updateDeviceProbeStatus(ip string, status ProbeStatus, errMsg string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	for _, device := range s.cache {
		if device.IP == ip {
			device.ProbeStatus = status
			device.ProbeError = errMsg
			break
		}
	}
}
