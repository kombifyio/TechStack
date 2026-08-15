package grpcserver

import (
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/monitoring"
)

type monitorIngestHealthTracker struct {
	mu         sync.RWMutex
	otlp       ingestLaneState
	legacyPush ingestLaneState
}

type ingestLaneState struct {
	requestsAccepted uint64
	samplesAccepted  uint64
	requestsRejected uint64
	samplesRejected  uint64
	parseErrors      uint64
	lastSuccessAt    time.Time
	lastFailureAt    time.Time
	lastError        string
}

func newMonitorIngestHealthTracker() *monitorIngestHealthTracker {
	return &monitorIngestHealthTracker{}
}

func (s *Server) MonitoringIngestHealth() monitoring.IngestHealthSnapshot {
	if s == nil || s.monitorIngestHealth == nil {
		return monitoring.IngestHealthSnapshot{}
	}
	return s.monitorIngestHealth.snapshot()
}

func (t *monitorIngestHealthTracker) recordOTLPSuccess(samples int) {
	t.recordSuccess(&t.otlp, samples)
}

func (t *monitorIngestHealthTracker) recordOTLPRejected(samples int, err string) {
	t.recordRejected(&t.otlp, samples, err, false)
}

func (t *monitorIngestHealthTracker) recordLegacySuccess(samples int) {
	t.recordSuccess(&t.legacyPush, samples)
}

func (t *monitorIngestHealthTracker) recordLegacyRejected(samples int, err string) {
	t.recordRejected(&t.legacyPush, samples, err, false)
}

func (t *monitorIngestHealthTracker) recordLegacyParseError(err string) {
	t.recordRejected(&t.legacyPush, 0, err, true)
}

func (t *monitorIngestHealthTracker) recordSuccess(state *ingestLaneState, samples int) {
	if t == nil {
		return
	}
	now := time.Now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	state.requestsAccepted++
	if samples > 0 {
		state.samplesAccepted += uint64(samples)
	}
	state.lastSuccessAt = now
	state.lastError = ""
}

func (t *monitorIngestHealthTracker) recordRejected(state *ingestLaneState, samples int, err string, parseError bool) {
	if t == nil {
		return
	}
	now := time.Now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	state.requestsRejected++
	if samples > 0 {
		state.samplesRejected += uint64(samples)
	}
	if parseError {
		state.parseErrors++
	}
	state.lastFailureAt = now
	state.lastError = err
}

func (t *monitorIngestHealthTracker) snapshot() monitoring.IngestHealthSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return monitoring.IngestHealthSnapshot{
		OTLP:       snapshotLaneState(t.otlp),
		LegacyPush: snapshotLaneState(t.legacyPush),
	}
}

func snapshotLaneState(state ingestLaneState) monitoring.IngestLaneHealth {
	return monitoring.IngestLaneHealth{
		Status:           laneStatus(state),
		QueueMode:        "synchronous",
		QueueDepth:       0,
		QueueCapacity:    0,
		RequestsAccepted: state.requestsAccepted,
		SamplesAccepted:  state.samplesAccepted,
		RequestsRejected: state.requestsRejected,
		SamplesRejected:  state.samplesRejected,
		ParseErrors:      state.parseErrors,
		LastSuccessAt:    cloneTimePtr(state.lastSuccessAt),
		LastFailureAt:    cloneTimePtr(state.lastFailureAt),
		LastError:        state.lastError,
	}
}

func laneStatus(state ingestLaneState) string {
	if state.lastError != "" {
		if state.requestsAccepted == 0 {
			return "error"
		}
		return "degraded"
	}
	if state.requestsAccepted == 0 && state.requestsRejected == 0 && state.parseErrors == 0 {
		return "idle"
	}
	return "ok"
}

func cloneTimePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}
