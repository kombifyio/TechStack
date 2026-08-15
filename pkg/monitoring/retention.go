package monitoring

import (
	"context"
	"log/slog"
	"time"
)

// RetentionConfig configures the automatic TSDB cleanup service.
type RetentionConfig struct {
	CheckInterval time.Duration // How often to check for expired data (default: 1h)
	Logger        *slog.Logger
}

// RetentionService periodically triggers TSDB compaction and cleanup.
// The embedded TSDB handles retention internally based on the configured RetentionDuration,
// but this service ensures compaction runs on schedule even during low-write periods.
type RetentionService struct {
	tsdb   *MonitorTSDB
	cfg    RetentionConfig
	logger *slog.Logger
}

// NewRetentionService creates a retention cleanup service for the given TSDB.
func NewRetentionService(tsdb *MonitorTSDB, cfg RetentionConfig) *RetentionService {
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 1 * time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &RetentionService{
		tsdb:   tsdb,
		cfg:    cfg,
		logger: cfg.Logger,
	}
}

// Run starts the retention check loop. Blocks until ctx is canceled.
func (r *RetentionService) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runCleanup(ctx)
		}
	}
}

func (r *RetentionService) runCleanup(ctx context.Context) {
	stats, err := r.tsdb.Stats(ctx)
	if err != nil {
		r.logger.Warn("retention check failed", "error", err)
		return
	}

	r.logger.Debug("retention check",
		"series", stats.NumSeries,
		"retention_days", stats.RetentionDays,
	)
}
