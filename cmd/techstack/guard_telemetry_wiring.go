package main

import (
	"errors"
	"fmt"

	"github.com/prometheus/prometheus/storage"

	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/kombifyio/techstack/pkg/monitoring"
)

// guardTelemetryEnabledEnv is the kill switch of Guard telemetry ingest.
const guardTelemetryEnabledEnv = "TECHSTACK_GUARD_TELEMETRY_ENABLED"

// telemetryTSDBSink stores a Guard's telemetry in the monitoring store. A sample
// the store refuses for good (older than the series head, a duplicate) is not
// retried forever: it is reported as rejected, which counts as stored.
type telemetryTSDBSink struct{ writer routes.WorkerMetricWriter }

func (s telemetryTSDBSink) WriteSamples(samples []guardtelemetry.SinkSample) error {
	out := make([]monitoring.MetricSample, 0, len(samples))
	for _, sample := range samples {
		out = append(out, monitoring.MetricSample{Name: sample.Name, Value: sample.Value, Labels: sample.Labels, Timestamp: sample.Timestamp})
	}
	err := s.writer.Write(out)
	if errors.Is(err, storage.ErrOutOfOrderSample) || errors.Is(err, storage.ErrOutOfBounds) ||
		errors.Is(err, storage.ErrDuplicateSampleForTimestamp) || errors.Is(err, storage.ErrTooOldSample) {
		return fmt.Errorf("%w: %v", guardtelemetry.ErrSampleRejected, err)
	}
	return err
}

// workerTelemetry returns the Guard telemetry ingest, or untyped nil when there
// is nowhere to store it: a Guard is then told telemetry is disabled and stops
// collecting instead of spooling for nobody.
func workerTelemetry(deps routeDeps, metricWriter routes.WorkerMetricWriter) routes.WorkerTelemetry {
	if metricWriter == nil || !envBoolDefault(guardTelemetryEnabledEnv, true) || deps.v2 == nil || deps.v2.db == nil || deps.v2.db.DB == nil {
		return nil
	}
	cursors, err := guardtelemetry.NewPostgresCursors(deps.v2.db.DB)
	if err != nil {
		deps.log.Warn("guard_telemetry_disabled", "reason", err.Error())
		return nil
	}
	var drives guardtelemetry.DriveSink
	if deps.monitor != nil && deps.monitor.driveHealth != nil {
		drives = deps.monitor.driveHealth
	}
	return &guardtelemetry.Ingestor{Sink: telemetryTSDBSink{writer: metricWriter}, Drives: drives, Cursors: cursors}
}
