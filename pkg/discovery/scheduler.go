// Package discovery provides network device discovery functionality for kombifyTechstack.
// scheduler.go implements periodic automatic network scanning.
package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// SchedulerConfig defines configuration for the discovery scheduler.
type SchedulerConfig struct {
	// Enabled determines if the scheduler should run.
	Enabled bool `json:"enabled"`

	// Interval between scans (default: 30 minutes).
	Interval time.Duration `json:"interval"`

	// Profile determines scan depth (default: passive).
	Profile ScanProfile `json:"profile"`

	// Networks to scan in CIDR notation (empty = auto-detect).
	Networks []string `json:"networks"`

	// MaxConcurrent limits parallel scans (default: 1).
	MaxConcurrent int `json:"maxConcurrent"`

	// QuietHours defines periods when no scans should run.
	QuietHours *QuietHours `json:"quietHours"`
}

// QuietHours defines a time window when scans should not run.
type QuietHours struct {
	// Start time in "HH:MM" format (e.g., "22:00").
	Start string `json:"start"`
	// End time in "HH:MM" format (e.g., "07:00").
	End string `json:"end"`
}

// SchedulerStatus represents the current state of the scheduler.
type SchedulerStatus struct {
	// Running indicates if the scheduler is currently active.
	Running bool `json:"running"`

	// NextScanAt is the scheduled time for the next scan.
	NextScanAt *time.Time `json:"nextScanAt,omitempty"`

	// LastScanAt is when the last scan was started.
	LastScanAt *time.Time `json:"lastScanAt,omitempty"`

	// LastScanID is the ID of the most recent scan.
	LastScanID string `json:"lastScanId,omitempty"`

	// CurrentScanID is the ID of a currently running scan (if any).
	CurrentScanID string `json:"currentScanId,omitempty"`

	// ScanCount is the total number of scans performed.
	ScanCount int `json:"scanCount"`

	// InQuietHours indicates if currently in quiet hours period.
	InQuietHours bool `json:"inQuietHours"`

	// Config is the current scheduler configuration.
	Config SchedulerConfig `json:"config"`
}

// ScanEventCallbacks defines callbacks for scan lifecycle events.
type ScanEventCallbacks struct {
	// OnScanStart is called when a scheduled scan begins.
	OnScanStart func(scanID string, profile ScanProfile)

	// OnScanComplete is called when a scan finishes (success or failure).
	OnScanComplete func(result *ScanResult)

	// OnDeviceFound is called for each device discovered during a scan.
	OnDeviceFound func(device *DiscoveredDevice)
}

// Scheduler manages periodic network discovery scans.
type Scheduler struct {
	service   *Service
	config    SchedulerConfig
	callbacks ScanEventCallbacks
	logger    *slog.Logger

	// State management
	mu            sync.RWMutex
	running       bool
	stopCh        chan struct{}
	doneCh        chan struct{}
	triggerCh     chan struct{}
	lastScanAt    *time.Time
	lastScanID    string
	currentScanID string
	nextScanAt    *time.Time
	scanCount     int
}

// DefaultSchedulerConfig returns sensible defaults for the scheduler.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		Enabled:       true,
		Interval:      30 * time.Minute,
		Profile:       ProfilePassive,
		Networks:      nil, // auto-detect
		MaxConcurrent: 1,
		QuietHours:    nil,
	}
}

// NewScheduler creates a new scheduler for the given discovery service.
func NewScheduler(service *Service, config SchedulerConfig) *Scheduler {
	// Apply defaults for zero values
	if config.Interval == 0 {
		config.Interval = 30 * time.Minute
	}
	if config.Profile == "" {
		config.Profile = ProfilePassive
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = 1
	}

	return &Scheduler{
		service:   service,
		config:    config,
		logger:    slog.Default().With("component", "discovery-scheduler"),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		triggerCh: make(chan struct{}, 1), // buffered to avoid blocking
	}
}

// SetCallbacks sets the event callbacks for scan lifecycle events.
func (s *Scheduler) SetCallbacks(callbacks ScanEventCallbacks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callbacks = callbacks
}

// Start begins the scheduler loop. It blocks until Stop() is called or
// the context is canceled.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: already running")
	}
	if !s.config.Enabled {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: disabled in config")
	}

	s.running = true
	// Reset channels for a fresh start
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.triggerCh = make(chan struct{}, 1)
	s.mu.Unlock()

	s.logger.Info("scheduler starting",
		"interval", s.config.Interval,
		"profile", s.config.Profile,
		"networks", s.config.Networks,
	)

	go s.runLoop(ctx)

	return nil
}

// Stop gracefully stops the scheduler and waits for it to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	s.logger.Info("scheduler stopping")

	// Signal the loop to stop
	close(s.stopCh)

	// Wait for the loop to finish
	<-s.doneCh

	s.mu.Lock()
	s.running = false
	s.nextScanAt = nil
	s.mu.Unlock()

	s.logger.Info("scheduler stopped")
}

// TriggerNow requests an immediate scan outside the regular schedule.
// Returns an error if the scheduler is not running or a scan is already in progress.
func (s *Scheduler) TriggerNow() error {
	s.mu.RLock()
	if !s.running {
		s.mu.RUnlock()
		return fmt.Errorf("scheduler: not running")
	}
	if s.currentScanID != "" {
		s.mu.RUnlock()
		return fmt.Errorf("scheduler: scan already in progress")
	}
	s.mu.RUnlock()

	// Non-blocking send to trigger channel
	select {
	case s.triggerCh <- struct{}{}:
		s.logger.Info("manual scan triggered")
	default:
		return fmt.Errorf("scheduler: trigger already pending")
	}

	return nil
}

// SetInterval dynamically changes the scan interval.
func (s *Scheduler) SetInterval(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if d < time.Minute {
		s.logger.Warn("interval too short, using minimum of 1 minute", "requested", d)
		d = time.Minute
	}

	s.config.Interval = d
	s.logger.Info("interval changed", "new_interval", d)
}

// SetProfile dynamically changes the scan profile.
func (s *Scheduler) SetProfile(profile ScanProfile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.Profile = profile
	s.logger.Info("profile changed", "new_profile", profile)
}

// SetNetworks dynamically changes the target networks.
func (s *Scheduler) SetNetworks(networks []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.Networks = networks
	s.logger.Info("networks changed", "new_networks", networks)
}

// GetStatus returns the current scheduler status.
func (s *Scheduler) GetStatus() SchedulerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return SchedulerStatus{
		Running:       s.running,
		NextScanAt:    s.nextScanAt,
		LastScanAt:    s.lastScanAt,
		LastScanID:    s.lastScanID,
		CurrentScanID: s.currentScanID,
		ScanCount:     s.scanCount,
		InQuietHours:  s.isQuietHoursLocked(),
		Config:        s.config,
	}
}

// IsRunning returns whether the scheduler is currently active.
func (s *Scheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// runLoop is the main scheduler loop that runs in a goroutine.
func (s *Scheduler) runLoop(ctx context.Context) {
	defer close(s.doneCh)

	// Run an initial scan immediately (unless in quiet hours)
	s.performScanIfAllowed(ctx)

	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	for {
		// Update next scan time
		s.mu.Lock()
		nextTime := time.Now().Add(s.config.Interval)
		s.nextScanAt = &nextTime
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			s.logger.Info("scheduler context canceled")
			return

		case <-s.stopCh:
			return

		case <-ticker.C:
			s.performScanIfAllowed(ctx)
			// Reset ticker if interval changed
			s.mu.RLock()
			currentInterval := s.config.Interval
			s.mu.RUnlock()
			ticker.Reset(currentInterval)

		case <-s.triggerCh:
			// Manual trigger bypasses quiet hours check
			s.performScan(ctx, true)
		}
	}
}

// performScanIfAllowed runs a scan if not in quiet hours.
func (s *Scheduler) performScanIfAllowed(ctx context.Context) {
	s.mu.RLock()
	inQuiet := s.isQuietHoursLocked()
	s.mu.RUnlock()

	if inQuiet {
		s.logger.Debug("skipping scan during quiet hours")
		return
	}

	s.performScan(ctx, false)
}

// performScan executes a network scan.
func (s *Scheduler) performScan(ctx context.Context, manual bool) {
	s.mu.Lock()
	// Check if a scan is already running
	if s.currentScanID != "" {
		s.mu.Unlock()
		s.logger.Debug("scan already in progress, skipping")
		return
	}

	profile := s.config.Profile
	networks := s.config.Networks
	callbacks := s.callbacks
	s.mu.Unlock()

	// Determine subnet to scan
	subnet := ""
	if len(networks) > 0 {
		subnet = networks[0] // Use first network for now
	}

	req := &ScanRequest{
		Profile: profile,
		Subnet:  subnet,
	}

	s.logger.Info("starting scheduled scan",
		"profile", profile,
		"subnet", subnet,
		"manual", manual,
	)

	// Start the scan
	result, err := s.service.StartScan(ctx, req)
	if err != nil {
		s.logger.Error("failed to start scan", "error", err)
		return
	}

	// Update state
	now := time.Now()
	s.mu.Lock()
	s.currentScanID = result.ScanID
	s.lastScanAt = &now
	s.lastScanID = result.ScanID
	s.scanCount++
	s.mu.Unlock()

	// Fire OnScanStart callback
	if callbacks.OnScanStart != nil {
		callbacks.OnScanStart(result.ScanID, profile)
	}

	// Wait for scan completion in a separate goroutine
	go s.waitForScanCompletion(ctx, result.ScanID, callbacks)
}

// waitForScanCompletion polls for scan completion and fires callbacks.
func (s *Scheduler) waitForScanCompletion(ctx context.Context, scanID string, callbacks ScanEventCallbacks) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	timeout := time.After(30 * time.Minute) // Maximum scan duration

	for {
		select {
		case <-ctx.Done():
			return

		case <-timeout:
			s.logger.Warn("scan timeout exceeded", "scan_id", scanID)
			s.mu.Lock()
			s.currentScanID = ""
			s.mu.Unlock()
			return

		case <-ticker.C:
			result, ok := s.service.GetScanStatus(scanID)
			if !ok {
				s.logger.Warn("scan not found", "scan_id", scanID)
				s.mu.Lock()
				s.currentScanID = ""
				s.mu.Unlock()
				return
			}

			// Check if scan is complete
			if result.Status == ScanStatusCompleted ||
				result.Status == ScanStatusFailed ||
				result.Status == ScanStatusCancelled {

				s.logger.Info("scan completed",
					"scan_id", scanID,
					"status", result.Status,
					"devices_found", len(result.Devices),
				)

				// Clear current scan
				s.mu.Lock()
				s.currentScanID = ""
				s.mu.Unlock()

				// Fire OnDeviceFound for each device
				if callbacks.OnDeviceFound != nil {
					for i := range result.Devices {
						callbacks.OnDeviceFound(&result.Devices[i])
					}
				}

				// Fire OnScanComplete callback
				if callbacks.OnScanComplete != nil {
					callbacks.OnScanComplete(result)
				}

				return
			}
		}
	}
}

// isQuietHoursLocked checks if current time is within quiet hours.
// Must be called with at least a read lock held.
func (s *Scheduler) isQuietHoursLocked() bool {
	if s.config.QuietHours == nil {
		return false
	}

	qh := s.config.QuietHours
	if qh.Start == "" || qh.End == "" {
		return false
	}

	now := time.Now()
	startTime, err := parseTimeOfDay(qh.Start)
	if err != nil {
		s.logger.Warn("invalid quiet hours start time", "start", qh.Start, "error", err)
		return false
	}

	endTime, err := parseTimeOfDay(qh.End)
	if err != nil {
		s.logger.Warn("invalid quiet hours end time", "end", qh.End, "error", err)
		return false
	}

	// Set to today's date
	startTime = time.Date(now.Year(), now.Month(), now.Day(),
		startTime.Hour(), startTime.Minute(), 0, 0, now.Location())
	endTime = time.Date(now.Year(), now.Month(), now.Day(),
		endTime.Hour(), endTime.Minute(), 0, 0, now.Location())

	// Handle overnight quiet hours (e.g., 22:00 to 07:00)
	if endTime.Before(startTime) {
		// We're in overnight mode
		// Quiet if now >= start OR now < end
		return now.After(startTime) || now.Equal(startTime) || now.Before(endTime)
	}

	// Normal same-day quiet hours
	return (now.After(startTime) || now.Equal(startTime)) && now.Before(endTime)
}

// IsQuietHours returns whether the current time is within quiet hours.
func (s *Scheduler) IsQuietHours() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isQuietHoursLocked()
}

// parseTimeOfDay parses a "HH:MM" string into a time.Time (date is zeroed).
func parseTimeOfDay(timeStr string) (time.Time, error) {
	return time.Parse("15:04", timeStr)
}
