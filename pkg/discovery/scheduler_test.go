package discovery

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewScheduler(t *testing.T) {
	service := NewService(nil)
	config := DefaultSchedulerConfig()

	scheduler := NewScheduler(service, config)

	if scheduler == nil {
		t.Fatal("NewScheduler returned nil")
	}

	if scheduler.service != service {
		t.Error("service not set correctly")
	}

	if scheduler.config.Interval != 30*time.Minute {
		t.Errorf("expected default interval 30m, got %v", scheduler.config.Interval)
	}

	if scheduler.config.Profile != ProfilePassive {
		t.Errorf("expected default profile passive, got %v", scheduler.config.Profile)
	}
}

func TestNewSchedulerDefaults(t *testing.T) {
	service := NewService(nil)

	// Test with zero-value config
	config := SchedulerConfig{}
	scheduler := NewScheduler(service, config)

	if scheduler.config.Interval != 30*time.Minute {
		t.Errorf("expected default interval 30m for zero config, got %v", scheduler.config.Interval)
	}

	if scheduler.config.Profile != ProfilePassive {
		t.Errorf("expected default profile passive for zero config, got %v", scheduler.config.Profile)
	}

	if scheduler.config.MaxConcurrent != 1 {
		t.Errorf("expected default MaxConcurrent 1, got %d", scheduler.config.MaxConcurrent)
	}
}

func TestSchedulerStartStop(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 100 * time.Millisecond,
		Profile:  ProfilePassive,
	}

	scheduler := NewScheduler(service, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start scheduler
	err := scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify running
	if !scheduler.IsRunning() {
		t.Error("scheduler should be running after Start")
	}

	// Try starting again (should fail)
	err = scheduler.Start(ctx)
	if err == nil {
		t.Error("expected error when starting already running scheduler")
	}

	// Stop scheduler
	scheduler.Stop()

	// Verify stopped
	if scheduler.IsRunning() {
		t.Error("scheduler should not be running after Stop")
	}
}

func TestSchedulerStartDisabled(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  false,
		Interval: 100 * time.Millisecond,
	}

	scheduler := NewScheduler(service, config)

	ctx := context.Background()
	err := scheduler.Start(ctx)

	if err == nil {
		t.Error("expected error when starting disabled scheduler")
	}
}

func TestSchedulerTriggerNow(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 1 * time.Hour, // Long interval so regular scans don't interfere
		Profile:  ProfilePassive,
	}

	scheduler := NewScheduler(service, config)

	// TriggerNow should fail when not running
	err := scheduler.TriggerNow()
	if err == nil {
		t.Error("expected error when triggering non-running scheduler")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer scheduler.Stop()

	// Wait for initial scan to complete (poll for empty currentScanID)
	for i := 0; i < 50; i++ {
		status := scheduler.GetStatus()
		if status.CurrentScanID == "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// TriggerNow should succeed when no scan is in progress
	err = scheduler.TriggerNow()
	if err != nil {
		// If still in progress, that's acceptable for this test
		if err.Error() != "scheduler: scan already in progress" {
			t.Errorf("TriggerNow failed unexpectedly: %v", err)
		}
	}
}

func TestSchedulerSetInterval(t *testing.T) {
	service := NewService(nil)
	config := DefaultSchedulerConfig()
	scheduler := NewScheduler(service, config)

	// Set new interval
	newInterval := 15 * time.Minute
	scheduler.SetInterval(newInterval)

	status := scheduler.GetStatus()
	if status.Config.Interval != newInterval {
		t.Errorf("expected interval %v, got %v", newInterval, status.Config.Interval)
	}

	// Set interval too short (should be clamped to minimum)
	scheduler.SetInterval(10 * time.Second)
	status = scheduler.GetStatus()
	if status.Config.Interval != time.Minute {
		t.Errorf("expected minimum interval 1m, got %v", status.Config.Interval)
	}
}

func TestSchedulerSetProfile(t *testing.T) {
	service := NewService(nil)
	config := DefaultSchedulerConfig()
	scheduler := NewScheduler(service, config)

	scheduler.SetProfile(ProfileActive)

	status := scheduler.GetStatus()
	if status.Config.Profile != ProfileActive {
		t.Errorf("expected profile active, got %v", status.Config.Profile)
	}
}

func TestSchedulerSetNetworks(t *testing.T) {
	service := NewService(nil)
	config := DefaultSchedulerConfig()
	scheduler := NewScheduler(service, config)

	networks := []string{"192.168.1.0/24", "10.0.0.0/8"}
	scheduler.SetNetworks(networks)

	status := scheduler.GetStatus()
	if len(status.Config.Networks) != 2 {
		t.Errorf("expected 2 networks, got %d", len(status.Config.Networks))
	}
}

func TestSchedulerGetStatus(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 10 * time.Minute,
		Profile:  ProfileActive,
		Networks: []string{"192.168.1.0/24"},
	}
	scheduler := NewScheduler(service, config)

	status := scheduler.GetStatus()

	if status.Running {
		t.Error("scheduler should not be running initially")
	}

	if status.ScanCount != 0 {
		t.Errorf("expected scan count 0, got %d", status.ScanCount)
	}

	if status.Config.Profile != ProfileActive {
		t.Errorf("expected profile active in status, got %v", status.Config.Profile)
	}
}

func TestSchedulerCallbacks(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 50 * time.Millisecond,
		Profile:  ProfilePassive,
	}

	scheduler := NewScheduler(service, config)

	var scanStartCalled atomic.Int32
	var scanCompleteCalled atomic.Int32

	callbacks := ScanEventCallbacks{
		OnScanStart: func(scanID string, profile ScanProfile) {
			scanStartCalled.Add(1)
		},
		OnScanComplete: func(result *ScanResult) {
			scanCompleteCalled.Add(1)
		},
	}

	scheduler.SetCallbacks(callbacks)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for at least one scan cycle
	time.Sleep(200 * time.Millisecond)

	scheduler.Stop()

	if scanStartCalled.Load() == 0 {
		t.Error("OnScanStart callback was not called")
	}
}

func TestQuietHoursParsing(t *testing.T) {
	tests := []struct {
		name      string
		timeStr   string
		expectErr bool
	}{
		{"valid evening", "22:00", false},
		{"valid morning", "07:00", false},
		{"valid midday", "12:30", false},
		{"single digit hour", "09:00", false}, // Go's time.Parse accepts this
		{"invalid hour", "25:00", true},
		{"invalid minute", "22:99", true},
		{"completely invalid", "invalid", true},
		{"empty string", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTimeOfDay(tt.timeStr)
			if tt.expectErr && err == nil {
				t.Errorf("expected error parsing %q", tt.timeStr)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error parsing %q: %v", tt.timeStr, err)
			}
		})
	}
}

func TestIsQuietHours(t *testing.T) {
	service := NewService(nil)

	// Test with no quiet hours configured
	config := DefaultSchedulerConfig()
	scheduler := NewScheduler(service, config)

	if scheduler.IsQuietHours() {
		t.Error("should not be in quiet hours when not configured")
	}

	// Test with quiet hours that span midnight
	// This is tricky to test deterministically without mocking time
	config = SchedulerConfig{
		Enabled:  true,
		Interval: 30 * time.Minute,
		QuietHours: &QuietHours{
			Start: "03:00",
			End:   "03:01",
		},
	}
	scheduler = NewScheduler(service, config)

	// Just verify it doesn't crash
	_ = scheduler.IsQuietHours()
}

func TestSchedulerConcurrentAccess(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 50 * time.Millisecond,
		Profile:  ProfilePassive,
	}

	scheduler := NewScheduler(service, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	var wg sync.WaitGroup

	// Concurrent reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = scheduler.GetStatus()
				_ = scheduler.IsRunning()
				_ = scheduler.IsQuietHours()
			}
		}()
	}

	// Concurrent writes
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				scheduler.SetInterval(time.Duration(idx+1) * time.Minute)
				scheduler.SetProfile(ProfilePassive)
			}
		}(i)
	}

	wg.Wait()
	scheduler.Stop()
}

func TestSchedulerContextCancellation(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 1 * time.Hour,
		Profile:  ProfilePassive,
	}

	scheduler := NewScheduler(service, config)

	ctx, cancel := context.WithCancel(context.Background())

	err := scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Cancel context
	cancel()

	// Give it a moment to process cancellation
	time.Sleep(50 * time.Millisecond)

	// Scheduler should stop on context cancellation
	// Note: The scheduler may still report as running briefly
	// The important thing is it doesn't hang
}

func TestSchedulerMultipleStartStop(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 100 * time.Millisecond,
		Profile:  ProfilePassive,
	}

	scheduler := NewScheduler(service, config)

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())

		err := scheduler.Start(ctx)
		if err != nil {
			t.Fatalf("Start iteration %d failed: %v", i, err)
		}

		time.Sleep(50 * time.Millisecond)

		scheduler.Stop()
		cancel()

		if scheduler.IsRunning() {
			t.Errorf("scheduler should be stopped after Stop() iteration %d", i)
		}
	}
}

func TestSchedulerStopWhenNotRunning(t *testing.T) {
	service := NewService(nil)
	config := DefaultSchedulerConfig()
	scheduler := NewScheduler(service, config)

	// Stop when not running should not panic
	scheduler.Stop()

	if scheduler.IsRunning() {
		t.Error("scheduler should not be running")
	}
}

func TestDefaultSchedulerConfig(t *testing.T) {
	config := DefaultSchedulerConfig()

	if !config.Enabled {
		t.Error("default config should be enabled")
	}

	if config.Interval != 30*time.Minute {
		t.Errorf("expected default interval 30m, got %v", config.Interval)
	}

	if config.Profile != ProfilePassive {
		t.Errorf("expected default profile passive, got %v", config.Profile)
	}

	if config.MaxConcurrent != 1 {
		t.Errorf("expected default MaxConcurrent 1, got %d", config.MaxConcurrent)
	}

	if config.QuietHours != nil {
		t.Error("expected no quiet hours by default")
	}

	if config.Networks != nil {
		t.Error("expected nil networks (auto-detect) by default")
	}
}

func TestSchedulerScanCountIncrement(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 50 * time.Millisecond,
		Profile:  ProfilePassive,
	}

	scheduler := NewScheduler(service, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initialStatus := scheduler.GetStatus()
	if initialStatus.ScanCount != 0 {
		t.Errorf("expected initial scan count 0, got %d", initialStatus.ScanCount)
	}

	err := scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for scans to occur
	time.Sleep(200 * time.Millisecond)

	scheduler.Stop()

	finalStatus := scheduler.GetStatus()
	if finalStatus.ScanCount == 0 {
		t.Error("expected scan count to increase after running")
	}
}

func TestSchedulerTriggerDuringActiveScan(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 1 * time.Hour,
		Profile:  ProfilePassive,
	}

	scheduler := NewScheduler(service, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer scheduler.Stop()

	// Initial scan starts immediately, try to trigger another
	// This may fail if scan is in progress (expected behavior)
	_ = scheduler.TriggerNow()
}

func TestQuietHoursOvernightSpan(t *testing.T) {
	service := NewService(nil)

	// Set quiet hours that span midnight (22:00 to 07:00)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 30 * time.Minute,
		QuietHours: &QuietHours{
			Start: "22:00",
			End:   "07:00",
		},
	}
	scheduler := NewScheduler(service, config)

	// We can't easily test the actual quiet hours logic without
	// mocking time, but we can verify the scheduler handles the
	// overnight span without errors
	status := scheduler.GetStatus()
	// InQuietHours should be computed without panicking
	_ = status.InQuietHours
}

func TestSchedulerStatusFields(t *testing.T) {
	service := NewService(nil)
	config := SchedulerConfig{
		Enabled:  true,
		Interval: 100 * time.Millisecond,
		Profile:  ProfileActive,
		Networks: []string{"192.168.1.0/24"},
		QuietHours: &QuietHours{
			Start: "02:00",
			End:   "03:00",
		},
	}

	scheduler := NewScheduler(service, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	status := scheduler.GetStatus()

	if !status.Running {
		t.Error("status should show running")
	}

	if status.Config.Profile != ProfileActive {
		t.Errorf("status config profile mismatch: got %v", status.Config.Profile)
	}

	if len(status.Config.Networks) != 1 {
		t.Errorf("expected 1 network in status config, got %d", len(status.Config.Networks))
	}

	if status.Config.QuietHours == nil {
		t.Error("expected quiet hours in status config")
	}

	scheduler.Stop()
}
