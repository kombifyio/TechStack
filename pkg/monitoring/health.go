package monitoring

import "time"

// IngestLaneHealth describes the live health and cumulative counters for one
// monitoring ingest lane.
type IngestLaneHealth struct {
	Status           string     `json:"status"`
	Requirement      string     `json:"requirement"`
	FreshnessTTL     string     `json:"freshnessTTL,omitempty"`
	QueueMode        string     `json:"queueMode"`
	QueueDepth       int        `json:"queueDepth"`
	QueueCapacity    int        `json:"queueCapacity"`
	RequestsAccepted uint64     `json:"requestsAccepted"`
	SamplesAccepted  uint64     `json:"samplesAccepted"`
	RequestsRejected uint64     `json:"requestsRejected"`
	SamplesRejected  uint64     `json:"samplesRejected"`
	ParseErrors      uint64     `json:"parseErrors"`
	LastSuccessAt    *time.Time `json:"lastSuccessAt,omitempty"`
	LastFailureAt    *time.Time `json:"lastFailureAt,omitempty"`
	LastError        string     `json:"lastError,omitempty"`
}

// IngestHealthSnapshot describes the live monitoring ingest state across all
// supported ingest lanes.
type IngestHealthSnapshot struct {
	OTLP       IngestLaneHealth `json:"otlp"`
	LegacyPush IngestLaneHealth `json:"legacyPush"`
}

// IngestHealthProvider exposes the current monitoring ingest state for API and
// operator diagnostics.
type IngestHealthProvider interface {
	MonitoringIngestHealth() IngestHealthSnapshot
}
