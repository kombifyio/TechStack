// Package backup provides backup functionality including S3 storage and scheduling.
package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/pocketbase/pocketbase/core"
)

// SchedulerConfig holds configuration for the backup scheduler.
type SchedulerConfig struct {
	Enabled   bool
	Interval  time.Duration
	Retention int
	BackupDir string

	// S3 settings (optional)
	S3Enabled   bool
	S3Bucket    string
	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Region    string
	S3Prefix    string
}

// NewSchedulerConfigFromConfig creates scheduler config from the main config.
func NewSchedulerConfigFromConfig(cfg *config.Config) SchedulerConfig {
	return SchedulerConfig{
		Enabled:     cfg.Backup.Enabled,
		Interval:    cfg.Backup.BackupIntervalDuration(),
		Retention:   cfg.Backup.Retention,
		BackupDir:   cfg.Backup.BackupDir,
		S3Enabled:   cfg.Backup.S3Enabled,
		S3Bucket:    cfg.Backup.S3Bucket,
		S3Endpoint:  cfg.Backup.S3Endpoint,
		S3AccessKey: cfg.Backup.S3AccessKey,
		S3SecretKey: cfg.Backup.S3SecretKey,
		S3Region:    cfg.Backup.S3Region,
		S3Prefix:    cfg.Backup.S3Prefix,
	}
}

// Scheduler handles automated backup scheduling.
type Scheduler struct {
	config    SchedulerConfig
	app       core.App
	s3Client  *S3Client
	log       *logger.Logger
	stopCh    chan struct{}
	wg        sync.WaitGroup
	mu        sync.RWMutex
	running   bool
	lastRun   time.Time
	lastError error
}

// NewScheduler creates a new backup scheduler.
func NewScheduler(app core.App, cfg SchedulerConfig) (*Scheduler, error) {
	log := logger.Get().WithComponent("backup-scheduler")

	// Validate config
	if cfg.Interval < time.Minute {
		cfg.Interval = 24 * time.Hour
		log.Warn("Backup interval too short, using default", "interval", cfg.Interval)
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 7
	}
	if cfg.BackupDir == "" {
		cfg.BackupDir = "./backups"
	}

	// Ensure backup directory exists
	if err := os.MkdirAll(cfg.BackupDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	s := &Scheduler{
		config: cfg,
		app:    app,
		log:    log,
		stopCh: make(chan struct{}),
	}

	// Initialize S3 client if enabled
	if cfg.S3Enabled {
		s3Cfg := S3Config{
			Bucket:    cfg.S3Bucket,
			Endpoint:  cfg.S3Endpoint,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			Region:    cfg.S3Region,
			Prefix:    cfg.S3Prefix,
		}

		client, err := NewS3Client(s3Cfg)
		if err != nil {
			log.Error("Failed to initialize S3 client", "error", err)
			// Continue without S3 - will only do local backups
		} else {
			s.s3Client = client
			log.Info("S3 backup enabled", "bucket", cfg.S3Bucket)
		}
	}

	return s, nil
}

// Start begins the backup scheduler.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	s.log.Info("Starting backup scheduler",
		"interval", s.config.Interval,
		"retention", s.config.Retention,
		"s3_enabled", s.config.S3Enabled,
	)

	s.wg.Add(1)
	go s.run()
}

// Stop gracefully stops the backup scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()

	s.log.Info("Stopping backup scheduler")
	close(s.stopCh)
	s.wg.Wait()
	s.log.Info("Backup scheduler stopped")
}

// IsRunning returns whether the scheduler is running.
func (s *Scheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// LastRun returns the time of the last backup run.
func (s *Scheduler) LastRun() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRun
}

// LastError returns the error from the last backup run (if any).
func (s *Scheduler) LastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastError
}

// Status returns the current scheduler status.
func (s *Scheduler) Status() SchedulerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nextRun := time.Time{}
	if s.running && !s.lastRun.IsZero() {
		nextRun = s.lastRun.Add(s.config.Interval)
	}

	var lastErrorStr string
	if s.lastError != nil {
		lastErrorStr = s.lastError.Error()
	}

	return SchedulerStatus{
		Running:   s.running,
		Interval:  s.config.Interval.String(),
		Retention: s.config.Retention,
		S3Enabled: s.s3Client != nil,
		LastRun:   s.lastRun,
		NextRun:   nextRun,
		LastError: lastErrorStr,
	}
}

// SchedulerStatus represents the current status of the scheduler.
type SchedulerStatus struct {
	Running   bool      `json:"running"`
	Interval  string    `json:"interval"`
	Retention int       `json:"retention"`
	S3Enabled bool      `json:"s3_enabled"`
	LastRun   time.Time `json:"last_run,omitempty"`
	NextRun   time.Time `json:"next_run,omitempty"`
	LastError string    `json:"last_error,omitempty"`
}

// run is the main scheduler loop.
func (s *Scheduler) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	// Run initial backup after a short delay
	initialDelay := time.NewTimer(30 * time.Second)
	defer initialDelay.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-initialDelay.C:
			s.runBackup()
		case <-ticker.C:
			s.runBackup()
		}
	}
}

// runBackup performs a single backup iteration.
func (s *Scheduler) runBackup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	s.log.Info("Starting scheduled backup")
	startTime := time.Now()

	// Create local backup
	backup, err := s.createBackup(ctx)
	if err != nil {
		s.mu.Lock()
		s.lastRun = startTime
		s.lastError = err
		s.mu.Unlock()

		s.log.Error("Scheduled backup failed", "error", err)
		return
	}

	s.log.Info("Local backup created",
		"name", backup.Name,
		"size", backup.SizeHuman,
		"path", backup.Path,
	)

	// Upload to S3 if enabled
	if s.s3Client != nil {
		s.log.Info("Uploading backup to S3", "name", backup.Name)
		if err := s.s3Client.Upload(ctx, backup.Path, backup.Name); err != nil {
			s.log.Error("Failed to upload backup to S3", "error", err)
			// Don't fail the whole backup, just log the S3 error
		} else {
			s.log.Info("Backup uploaded to S3", "name", backup.Name)
		}
	}

	// Apply retention policy
	deleted, err := s.applyRetention()
	if err != nil {
		s.log.Warn("Failed to apply retention policy", "error", err)
	} else if len(deleted) > 0 {
		s.log.Info("Applied retention policy", "deleted", len(deleted))
	}

	// Apply S3 retention if enabled
	if s.s3Client != nil {
		if err := s.applyS3Retention(ctx); err != nil {
			s.log.Warn("Failed to apply S3 retention policy", "error", err)
		}
	}

	s.mu.Lock()
	s.lastRun = startTime
	s.lastError = nil
	s.mu.Unlock()

	duration := time.Since(startTime)
	s.log.Info("Scheduled backup completed",
		"duration", duration.Round(time.Second),
		"name", backup.Name,
	)
}

// RunNow triggers an immediate backup.
func (s *Scheduler) RunNow(ctx context.Context) (*BackupInfo, error) {
	s.log.Info("Running manual backup")

	backup, err := s.createBackup(ctx)
	if err != nil {
		return nil, err
	}

	// Upload to S3 if enabled
	if s.s3Client != nil {
		if err := s.s3Client.Upload(ctx, backup.Path, backup.Name); err != nil {
			s.log.Warn("Failed to upload manual backup to S3", "error", err)
			// Don't fail the whole backup
		} else {
			backup.S3Path = s.s3Client.Prefix() + backup.Name
		}
	}

	return backup, nil
}

// BackupInfo represents metadata about a backup.
type BackupInfo struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	SizeHuman string    `json:"size_human"`
	CreatedAt time.Time `json:"created_at"`
	Path      string    `json:"path"`
	S3Path    string    `json:"s3_path,omitempty"`
}

// createBackup creates a new local backup.
func (s *Scheduler) createBackup(ctx context.Context) (*BackupInfo, error) {
	timestamp := time.Now().Format("20060102_150405")
	backupName := fmt.Sprintf("techstack_backup_%s.tar.gz", timestamp)
	backupPath := filepath.Join(s.config.BackupDir, backupName)

	// Find the database file
	dataDir := s.app.DataDir()
	dbFile := filepath.Join(dataDir, "data.db")

	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		// Try alternative names
		for _, name := range []string{"techstack.db", "pb.db"} {
			alt := filepath.Join(dataDir, name)
			if _, err := os.Stat(alt); err == nil {
				dbFile = alt
				break
			}
		}
	}

	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("database file not found in %s", dataDir)
	}

	// Create backup using SQLite backup command if available
	tempBackup := filepath.Join(s.config.BackupDir, fmt.Sprintf("temp_%s.db", timestamp))
	defer os.Remove(tempBackup)

	// Copy database (PocketBase handles WAL internally)
	if err := copyFile(dbFile, tempBackup); err != nil {
		return nil, fmt.Errorf("failed to copy database: %w", err)
	}

	// Create gzipped archive
	if err := createTarGz(backupPath, tempBackup); err != nil {
		return nil, fmt.Errorf("failed to create archive: %w", err)
	}

	// Get file info
	info, err := os.Stat(backupPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat backup: %w", err)
	}

	return &BackupInfo{
		Name:      backupName,
		Size:      info.Size(),
		SizeHuman: formatBytes(info.Size()),
		CreatedAt: info.ModTime(),
		Path:      backupPath,
	}, nil
}

// applyRetention removes old local backups exceeding the retention count.
func (s *Scheduler) applyRetention() ([]string, error) {
	return applyLocalRetention(s.config.BackupDir, s.config.Retention)
}

// applyS3Retention removes old S3 backups exceeding the retention count.
func (s *Scheduler) applyS3Retention(ctx context.Context) error {
	if s.s3Client == nil {
		return nil
	}

	// List all backups in S3
	objects, err := s.s3Client.List(ctx, "techstack_backup_")
	if err != nil {
		return fmt.Errorf("failed to list S3 backups: %w", err)
	}

	if len(objects) <= s.config.Retention {
		return nil
	}

	// Sort by last modified (newest first)
	// Objects are typically returned in order, but let's be safe
	// Delete oldest backups
	toDelete := objects[s.config.Retention:]
	var deletePaths []string
	for _, obj := range toDelete {
		// Strip prefix from key to get relative path
		key := obj.Key
		if s.s3Client.Prefix() != "" && len(key) > len(s.s3Client.Prefix()) {
			key = key[len(s.s3Client.Prefix()):]
		}
		deletePaths = append(deletePaths, key)
	}

	if err := s.s3Client.DeleteMultiple(ctx, deletePaths); err != nil {
		return fmt.Errorf("failed to delete old S3 backups: %w", err)
	}

	s.log.Info("Applied S3 retention policy", "deleted", len(deletePaths))
	return nil
}

// GetS3Client returns the S3 client if configured.
func (s *Scheduler) GetS3Client() *S3Client {
	return s.s3Client
}
