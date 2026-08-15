package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/collectors"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// MetricsPusher runs the metric collectors on their own intervals and ships
// batches to Core via the PushMetrics RPC (Monitoring 2.0 ingest). Push
// failures are logged and retried on the next tick — a broken metrics path
// must never take the agent session down; connection liveness is the
// heartbeat's job.
type MetricsPusher struct {
	client     *Client
	collectors []collectors.Collector
	log        *slog.Logger
}

// NewMetricsPusher creates a pusher for the given collectors.
func NewMetricsPusher(client *Client, cols []collectors.Collector, log *slog.Logger) *MetricsPusher {
	if log == nil {
		log = nopLogger
	}
	return &MetricsPusher{
		client:     client,
		collectors: cols,
		log:        log.With("component", "metrics-pusher"),
	}
}

// Run drives every collector on its own interval until the context is
// canceled. Blocks; run in a goroutine.
func (p *MetricsPusher) Run(ctx context.Context) {
	for _, col := range p.collectors {
		go p.runCollector(ctx, col)
	}
	<-ctx.Done()
}

func (p *MetricsPusher) runCollector(ctx context.Context, col collectors.Collector) {
	ticker := time.NewTicker(col.Interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			samples, err := col.Collect(ctx)
			if err != nil {
				p.log.Warn("collector_failed", "collector", col.Name(), "error", err.Error())
				continue
			}
			if len(samples) == 0 {
				continue
			}
			if err := p.client.PushMetrics(ctx, toProtoSamples(samples)); err != nil {
				p.log.Warn("metrics_push_failed",
					"collector", col.Name(),
					"samples", len(samples),
					"error", err.Error(),
				)
			}
		}
	}
}

func toProtoSamples(samples []collectors.Sample) []*agentpb.MetricSample {
	out := make([]*agentpb.MetricSample, 0, len(samples))
	for _, s := range samples {
		out = append(out, &agentpb.MetricSample{
			Name:          s.Name,
			Value:         s.Value,
			Labels:        s.Labels,
			Type:          toProtoMetricType(s.Type),
			TimestampUnix: s.Timestamp.UnixMilli(),
		})
	}
	return out
}

func toProtoMetricType(t collectors.MetricType) agentpb.MetricType {
	switch t {
	case collectors.Counter:
		return agentpb.MetricType_METRIC_COUNTER
	case collectors.Histogram:
		return agentpb.MetricType_METRIC_HISTOGRAM
	default:
		return agentpb.MetricType_METRIC_GAUGE
	}
}
