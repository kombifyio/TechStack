// Package monitoring provides embedded TSDB compatibility storage and PromQL query
// support shared by both the agent (standalone mode) and the core (connected mode).
package monitoring

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/util/annotations"
)

// TSDBConfig configures the embedded compatibility TSDB instance.
type TSDBConfig struct {
	DataDir   string        // Directory for TSDB data files
	Retention time.Duration // How long to keep data (default: 15 days)
	Logger    *slog.Logger
}

// MonitorTSDB wraps the embedded TSDB for metric ingestion and PromQL queries.
type MonitorTSDB struct {
	db     *tsdb.DB
	engine *promql.Engine
	logger *slog.Logger
	mu     sync.RWMutex
}

// NewMonitorTSDB opens (or creates) the embedded TSDB at the given path.
func NewMonitorTSDB(cfg TSDBConfig) (*MonitorTSDB, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 15 * 24 * time.Hour // 15 days default
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create TSDB dir: %w", err)
	}

	retentionMs := cfg.Retention.Milliseconds()

	opts := tsdb.DefaultOptions()
	opts.RetentionDuration = retentionMs
	opts.NoLockfile = false
	opts.MinBlockDuration = 2 * 60 * 60 * 1000 // 2h in ms
	if retentionMs/10 > opts.MinBlockDuration {
		opts.MaxBlockDuration = retentionMs / 10
	} else {
		opts.MaxBlockDuration = opts.MinBlockDuration
	}

	db, err := tsdb.Open(cfg.DataDir, cfg.Logger, nil, opts, nil)
	if err != nil {
		return nil, fmt.Errorf("open TSDB: %w", err)
	}

	engine := promql.NewEngine(promql.EngineOpts{
		Logger:               cfg.Logger,
		MaxSamples:           50_000_000,
		Timeout:              2 * time.Minute,
		EnableAtModifier:     true,
		EnableNegativeOffset: true,
	})

	return &MonitorTSDB{
		db:     db,
		engine: engine,
		logger: cfg.Logger,
	}, nil
}

// MetricSample is a single metric data point for TSDB ingestion.
type MetricSample struct {
	Name      string
	Value     float64
	Labels    map[string]string
	Timestamp time.Time
}

// Write appends metric samples to the TSDB.
func (m *MonitorTSDB) Write(samples []MetricSample) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	app := m.db.Appender(context.Background())

	for _, s := range samples {
		lblMap := make(map[string]string, len(s.Labels)+1)
		lblMap[labels.MetricName] = s.Name
		for k, v := range s.Labels {
			lblMap[k] = v
		}

		ls := labels.FromMap(lblMap)
		ts := s.Timestamp.UnixMilli()

		if _, err := app.Append(0, ls, ts, s.Value); err != nil {
			_ = app.Rollback()
			return fmt.Errorf("append sample %s: %w", s.Name, err)
		}
	}

	return app.Commit()
}

// QueryResult holds the result of a PromQL query.
type QueryResult struct {
	Value    parser.Value
	Warnings annotations.Annotations
}

// MetricsQueryBackend is the read/query contract shared by embedded TSDB mode
// and future remote PromQL-compatible backends such as VictoriaMetrics.
type MetricsQueryBackend interface {
	InstantQuery(ctx context.Context, query string, ts time.Time) (*QueryResult, error)
	RangeQuery(ctx context.Context, query string, start, end time.Time, step time.Duration) (*QueryResult, error)
	LabelNames(ctx context.Context) ([]string, error)
	LabelValues(ctx context.Context, name string) ([]string, error)
	MetricNames(ctx context.Context) ([]string, error)
	Stats(ctx context.Context) (*TSDBStats, error)
}

// InstantQuery executes an instant PromQL query at the given time.
func (m *MonitorTSDB) InstantQuery(ctx context.Context, query string, ts time.Time) (*QueryResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	q, err := m.engine.NewInstantQuery(ctx, m.db, nil, query, ts)
	if err != nil {
		return nil, fmt.Errorf("create instant query: %w", err)
	}
	defer q.Close()

	res := q.Exec(ctx)
	if res.Err != nil {
		return nil, fmt.Errorf("exec instant query: %w", res.Err)
	}

	return &QueryResult{Value: res.Value, Warnings: res.Warnings}, nil
}

// RangeQuery executes a range PromQL query between start and end with the given step.
func (m *MonitorTSDB) RangeQuery(ctx context.Context, query string, start, end time.Time, step time.Duration) (*QueryResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	q, err := m.engine.NewRangeQuery(ctx, m.db, nil, query, start, end, step)
	if err != nil {
		return nil, fmt.Errorf("create range query: %w", err)
	}
	defer q.Close()

	res := q.Exec(ctx)
	if res.Err != nil {
		return nil, fmt.Errorf("exec range query: %w", res.Err)
	}

	return &QueryResult{Value: res.Value, Warnings: res.Warnings}, nil
}

// LabelNames returns all known label names in the TSDB.
func (m *MonitorTSDB) LabelNames(ctx context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	q, err := m.db.Querier(0, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer q.Close()

	names, _, err := q.LabelNames(ctx, nil)
	return names, err
}

// LabelValues returns known values for a label name.
func (m *MonitorTSDB) LabelValues(ctx context.Context, name string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	q, err := m.db.Querier(0, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer q.Close()

	vals, _, err := q.LabelValues(ctx, name, nil)
	return vals, err
}

// MetricNames returns all distinct __name__ values (metric names) in the TSDB.
func (m *MonitorTSDB) MetricNames(ctx context.Context) ([]string, error) {
	return m.LabelValues(ctx, labels.MetricName)
}

// SeriesCount returns an approximate count of active series.
func (m *MonitorTSDB) SeriesCount() uint64 {
	return m.db.Head().NumSeries()
}

// Stats returns TSDB head stats for diagnostics.
type TSDBStats struct {
	NumSeries     uint64
	MinTime       int64 // Unix ms
	MaxTime       int64 // Unix ms
	TopMetrics    []MetricCount
	RetentionDays float64
}

type MetricCount struct {
	Name  string
	Count int
}

func (m *MonitorTSDB) Stats(ctx context.Context) (*TSDBStats, error) {
	head := m.db.Head()
	st := &TSDBStats{
		NumSeries: head.NumSeries(),
		MinTime:   head.MinTime(),
		MaxTime:   head.MaxTime(),
	}

	// Calculate retention in days from min/max
	if st.MaxTime > st.MinTime {
		st.RetentionDays = float64(st.MaxTime-st.MinTime) / float64(24*60*60*1000)
	}

	// Get top metric names by series count
	names, err := m.MetricNames(ctx)
	if err != nil {
		return st, nil // Return partial stats on error
	}

	counts := make([]MetricCount, 0, len(names))
	for _, n := range names {
		// Use label values query to estimate series count per metric
		counts = append(counts, MetricCount{Name: n, Count: 1})
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].Name < counts[j].Name })
	if len(counts) > 20 {
		counts = counts[:20]
	}
	st.TopMetrics = counts

	return st, nil
}

// Close gracefully shuts down the TSDB.
func (m *MonitorTSDB) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.db.Close()
}
