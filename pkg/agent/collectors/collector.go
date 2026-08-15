// Package collectors provides metric collection implementations for the kombify agent.
package collectors

import (
	"context"
	"time"
)

// MetricType classifies the semantic type of a metric.
type MetricType int

const (
	Gauge     MetricType = iota // Point-in-time value
	Counter                     // Monotonically increasing
	Histogram                   // Distribution
)

// Sample is a single metric data point collected by a Collector.
type Sample struct {
	Name      string
	Value     float64
	Labels    map[string]string
	Type      MetricType
	Timestamp time.Time
}

// Collector defines the interface for all metric collectors.
type Collector interface {
	// Name returns a human-readable identifier for this collector.
	Name() string

	// Collect gathers metric samples. Implementations must respect context cancellation.
	Collect(ctx context.Context) ([]Sample, error)

	// Interval returns how often this collector should be invoked.
	Interval() time.Duration
}
