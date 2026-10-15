package controlplane

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

// This file is the read/write model behind the private Cloud monitoring
// cockpit: bounded host-metric history, state-change events and backup facts.
// Every read takes a policy-issued InventoryReadScope and applies it in the
// database query, exactly like the inventory collection reads.

const (
	// MetricFineResolution and MetricCoarseResolution are the two bucket widths
	// the sample store keeps. They must match the CHECK in migration 146.
	MetricFineResolution   = 60 * time.Second
	MetricCoarseResolution = 15 * time.Minute

	// MetricFineRetention and MetricCoarseRetention bound how long each
	// resolution is kept. The writer prunes past these cutoffs.
	MetricFineRetention   = 24 * time.Hour
	MetricCoarseRetention = 30 * 24 * time.Hour

	// MaxInventoryTransitionEvents bounds one events read.
	MaxInventoryTransitionEvents = 200
	// DefaultInventoryTransitionEvents is the page size when none is asked for.
	DefaultInventoryTransitionEvents = 50
	// InventoryTransitionWindow is how far back the events read looks.
	InventoryTransitionWindow = 30 * 24 * time.Hour

	// InventoryMonitoringServerLimit bounds the servers one monitoring read
	// covers; it equals the runtime summary page size.
	InventoryMonitoringServerLimit = 50
	// BackupRunWindow is how many recent days of backup runs a facts read keeps.
	BackupRunWindow = 7
)

// MetricRange is one history window a client may ask for, with the bucket
// width the answer is aggregated to and the stored resolution it reads from.
type MetricRange struct {
	Name       string
	Window     time.Duration
	Step       time.Duration
	Resolution time.Duration
}

var metricRanges = []MetricRange{
	{Name: "1h", Window: time.Hour, Step: time.Minute, Resolution: MetricFineResolution},
	{Name: "24h", Window: 24 * time.Hour, Step: 5 * time.Minute, Resolution: MetricFineResolution},
	{Name: "7d", Window: 7 * 24 * time.Hour, Step: 15 * time.Minute, Resolution: MetricCoarseResolution},
	{Name: "30d", Window: 30 * 24 * time.Hour, Step: time.Hour, Resolution: MetricCoarseResolution},
}

// ParseMetricRange resolves the closed range vocabulary.
func ParseMetricRange(name string) (MetricRange, bool) {
	name = strings.TrimSpace(name)
	for _, candidate := range metricRanges {
		if candidate.Name == name {
			return candidate, true
		}
	}
	return MetricRange{}, false
}

// MetricRetentionCutoffs returns the oldest bucket start still retained for
// each resolution at the given time.
func MetricRetentionCutoffs(now time.Time) (fine, coarse time.Time) {
	now = now.UTC()
	return now.Add(-MetricFineRetention), now.Add(-MetricCoarseRetention)
}

// MetricBucketStart is the start of the bucket a sample at t falls into.
func MetricBucketStart(t time.Time, resolution time.Duration) time.Time {
	return t.UTC().Truncate(resolution)
}

// MetricSample is one host report. A nil figure was not reported; it is never
// stored as zero.
type MetricSample struct {
	CPU    *float64
	Memory *float64
	Disk   *float64
}

// HostMetricSample extracts the sample from the Guard inventory host map using
// the same derivation as the live metrics projection (a direct percent first,
// otherwise used over total). It reports false when the host map carries no
// evidence of an observed host: a Guard that sent neither a memory nor a disk
// total sent zero-valued placeholders, and a placeholder is not a reading.
func HostMetricSample(host map[string]any) (MetricSample, bool) {
	memoryTotal, _ := metricFloat(host["memory_total_bytes"])
	diskTotal, _ := metricFloat(host["disk_total_bytes"])
	if memoryTotal <= 0 && diskTotal <= 0 {
		return MetricSample{}, false
	}
	sample := MetricSample{
		CPU:    percentFigure(host, "cpu_percent", "", ""),
		Memory: percentFigure(host, "memory_percent", "memory_used_bytes", "memory_total_bytes"),
		Disk:   percentFigure(host, "disk_percent", "disk_used_bytes", "disk_total_bytes"),
	}
	if sample.CPU == nil && sample.Memory == nil && sample.Disk == nil {
		return MetricSample{}, false
	}
	return sample, true
}

func percentFigure(host map[string]any, directKey, usedKey, totalKey string) *float64 {
	if value, ok := metricFloat(host[directKey]); ok {
		return clampPercent(value)
	}
	if usedKey == "" {
		return nil
	}
	used, usedOK := metricFloat(host[usedKey])
	total, totalOK := metricFloat(host[totalKey])
	if usedOK && totalOK && used >= 0 && total > 0 {
		return clampPercent(used / total * 100)
	}
	return nil
}

func clampPercent(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return nil
	}
	if value > 100 {
		value = 100
	}
	value = math.Round(value*10) / 10
	return &value
}

func metricFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	default:
		return 0, false
	}
}

// MetricBucket is one stored bucket: a sum and a sample count per metric.
type MetricBucket struct {
	Start       time.Time
	CPUSum      float64
	CPUCount    int
	MemorySum   float64
	MemoryCount int
	DiskSum     float64
	DiskCount   int
}

// Add folds one report into the bucket.
func (b *MetricBucket) Add(sample MetricSample) {
	if sample.CPU != nil {
		b.CPUSum += *sample.CPU
		b.CPUCount++
	}
	if sample.Memory != nil {
		b.MemorySum += *sample.Memory
		b.MemoryCount++
	}
	if sample.Disk != nil {
		b.DiskSum += *sample.Disk
		b.DiskCount++
	}
}

// MetricPoint is one aggregated history point. A nil figure had no samples in
// the point's interval; gaps stay gaps and are never interpolated.
type MetricPoint struct {
	At     time.Time
	CPU    *float64
	Memory *float64
	Disk   *float64
}

// ServerMetricSeries is the history of one server.
type ServerMetricSeries struct {
	ServerID string
	Points   []MetricPoint
}

// DownsampleMetricBuckets merges stored buckets into points of the given step,
// aligned to the UTC epoch, oldest first. Intervals without any sample produce
// no point. Averages are weighted by sample count, so merging 15-minute
// buckets into hours equals averaging the underlying reports.
func DownsampleMetricBuckets(buckets []MetricBucket, step time.Duration) []MetricPoint {
	if step <= 0 {
		return nil
	}
	merged := make(map[time.Time]*MetricBucket, len(buckets))
	for _, bucket := range buckets {
		at := bucket.Start.UTC().Truncate(step)
		target, ok := merged[at]
		if !ok {
			target = &MetricBucket{Start: at}
			merged[at] = target
		}
		target.CPUSum += bucket.CPUSum
		target.CPUCount += bucket.CPUCount
		target.MemorySum += bucket.MemorySum
		target.MemoryCount += bucket.MemoryCount
		target.DiskSum += bucket.DiskSum
		target.DiskCount += bucket.DiskCount
	}
	points := make([]MetricPoint, 0, len(merged))
	for at, bucket := range merged {
		point := MetricPoint{
			At:     at,
			CPU:    bucketMean(bucket.CPUSum, bucket.CPUCount),
			Memory: bucketMean(bucket.MemorySum, bucket.MemoryCount),
			Disk:   bucketMean(bucket.DiskSum, bucket.DiskCount),
		}
		if point.CPU == nil && point.Memory == nil && point.Disk == nil {
			continue
		}
		points = append(points, point)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].At.Before(points[j].At) })
	return points
}

func bucketMean(sum float64, count int) *float64 {
	if count <= 0 {
		return nil
	}
	value := math.Round(sum/float64(count)*10) / 10
	return &value
}

// InventoryTransitionEvent is one state change of a server or service.
type InventoryTransitionEvent struct {
	At         time.Time
	Kind       string // "server" | "service"
	ID         string
	Name       string
	ServerID   string
	Field      string // "health" | "connection" | "lifecycle"
	From       string
	To         string
	ReasonCode string
}

// BackupRunFact is the end of one backup job of a stack.
type BackupRunFact struct {
	At    time.Time
	State string // "completed" | "failed"
}

// BackupDay is the outcome of the newest backup run of one UTC day.
type BackupDay struct {
	Date  string // YYYY-MM-DD, UTC
	State string
}

// SummarizeBackupRuns reduces finished backup runs to one entry per UTC day,
// newest day first, keeping the state of the day's newest run and only the
// last `days` days that have a run. A day without a run has no entry: the
// absence of a record is not a failure.
func SummarizeBackupRuns(runs []BackupRunFact, days int) []BackupDay {
	sorted := append([]BackupRunFact(nil), runs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].At.After(sorted[j].At) })
	seen := make(map[string]struct{}, days)
	out := make([]BackupDay, 0, days)
	for _, run := range sorted {
		date := run.At.UTC().Format("2006-01-02")
		if _, ok := seen[date]; ok {
			continue
		}
		if len(out) >= days {
			break
		}
		seen[date] = struct{}{}
		out = append(out, BackupDay{Date: date, State: run.State})
	}
	return out
}

// BackupScheduleFact is the projected backup cadence of a stack.
type BackupScheduleFact struct {
	Cadence   string
	Enabled   bool
	LastRunAt *time.Time
}

// InventoryBackupFacts is what the control plane itself knows about the backups
// of one visible server. Guard-observed freshness is joined by the route from
// the Guard posture store; nothing here is a verdict.
type InventoryBackupFacts struct {
	ServerID string
	StackID  string
	WorkerID string
	// OffsiteConfigured is true when a managed off-site store is bound to the
	// server's stack and its token is not revoked.
	OffsiteConfigured bool
	// StoredBytes is the measured size of the stack's off-site repository (all
	// snapshots together), not the size of one run.
	StoredBytes     *int64
	UsageMeasuredAt *time.Time
	Schedule        *BackupScheduleFact
	Runs            []BackupRunFact
}

// InventoryMonitoringReader is the optional authorization-scoped read seam of
// the monitoring cockpit. Implementations must apply the policy-issued scope in
// the query; callers must never read broadly and filter in Go.
type InventoryMonitoringReader interface {
	// ListInventoryMetricHistory returns one series per visible server (an
	// empty series for a server without samples in the window).
	ListInventoryMetricHistory(ctx context.Context, scope InventoryReadScope, window MetricRange, now time.Time) ([]ServerMetricSeries, error)
	// ListInventoryTransitions returns the newest state changes of the visible
	// servers and their services, newest first.
	ListInventoryTransitions(ctx context.Context, scope InventoryReadScope, since time.Time, limit int) ([]InventoryTransitionEvent, error)
	// ListInventoryBackupFacts returns the backup facts of the visible servers.
	ListInventoryBackupFacts(ctx context.Context, scope InventoryReadScope, now time.Time) ([]InventoryBackupFacts, error)
}

// ServerMetricRecorder persists one host report. It is best-effort by contract:
// the registry write that produced the report never depends on it.
type ServerMetricRecorder interface {
	RecordServerMetricSample(ctx context.Context, tenantID, serverID string, at time.Time, sample MetricSample) error
}

// clampTransitionLimit applies the default and the cap of an events read.
func clampTransitionLimit(limit int) int {
	if limit <= 0 {
		return DefaultInventoryTransitionEvents
	}
	if limit > MaxInventoryTransitionEvents {
		return MaxInventoryTransitionEvents
	}
	return limit
}

// monitoringTransitionField maps a stored transition dimension of a kind to the
// field name clients see, or "" when that dimension is not a client event.
func monitoringTransitionField(kind, dimension string) string {
	switch kind {
	case "server":
		switch dimension {
		case "health", "connection", "lifecycle":
			return dimension
		}
	case "service":
		switch dimension {
		case "health":
			return "health"
		case "observed":
			return "lifecycle"
		}
	}
	return ""
}
