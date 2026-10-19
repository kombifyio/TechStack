// Package guardtelemetry is kombify Guard's telemetry on the managed transport.
//
// What a Guard may send is the data contract of STACKKITS-PRODUCT-BOUNDARY-STANDARD.md §14.9: host resources (CPU, memory, swap, load, disk per mount, uptime)
// and container resource usage, plus structured hardware drive observations.
// Drive health is independent of capacity gauges. The contract is code here: a
// metric name and its label keys are on an allowlist, every label value is
// bounded, and the same filter runs on the Guard before a sample is spooled and
// on the control plane before it is stored, so neither a bug in a collector nor
// a caller of the loopback OTLP sink can widen what leaves the host.
//
// During an outage the Guard spools batches to a bounded local file and sends
// them, oldest first, once the control plane answers again. Sequence numbers
// make a replay harmless and a gap explicit: data that could not be kept is
// reported as a gap marker, never dropped without trace.
package guardtelemetry

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/drivehealth"
)

// BatchSchema is the wire version of a telemetry batch.
const BatchSchema = "kombify.guard-telemetry/v1"

// Bounds of one batch, enforced by the Guard when it builds one and by the
// control plane when it reads one.
const (
	MaxBatchBytes       = 1 << 20
	MaxRecordsPerBatch  = 64
	MaxSamplesPerRecord = 600
	MaxSamplesPerBatch  = 6000
	maxLabelValueBytes  = 128
	maxClockSkew        = 5 * time.Minute
	maxRecordAge        = 15 * 24 * time.Hour
	maxGapReasonBytes   = 32
	gapMetric           = "guard_telemetry_gap_seconds"
	GapMetricName       = gapMetric
	OriginGuard         = "guard"
	OriginOTLP          = "otlp"
	labelOrigin         = "origin"
	labelCore           = "core"
	labelMount          = "mount"
	labelContainerName  = "container_name"
)

// contract maps each metric the Guard may send to the label keys it may carry.
// A metric or a label key that is not here must not be emitted; adding one
// means amending section 9 of the standard first.
var contract = map[string][]string{
	// Host resources.
	"node_cpu_usage_percent":      {labelCore, labelOrigin},
	"node_load_1":                 {labelOrigin},
	"node_load_5":                 {labelOrigin},
	"node_load_15":                {labelOrigin},
	"node_memory_total_bytes":     {labelOrigin},
	"node_memory_used_bytes":      {labelOrigin},
	"node_memory_available_bytes": {labelOrigin},
	"node_memory_cached_bytes":    {labelOrigin},
	"node_memory_buffers_bytes":   {labelOrigin},
	"node_memory_usage_percent":   {labelOrigin},
	"node_swap_total_bytes":       {labelOrigin},
	"node_swap_used_bytes":        {labelOrigin},
	"node_swap_usage_percent":     {labelOrigin},
	"node_disk_total_bytes":       {labelMount, labelOrigin},
	"node_disk_used_bytes":        {labelMount, labelOrigin},
	"node_disk_free_bytes":        {labelMount, labelOrigin},
	"node_disk_usage_percent":     {labelMount, labelOrigin},
	"node_uptime_seconds":         {labelOrigin},
	// Container resource usage.
	"container_running":              {labelContainerName, labelOrigin},
	"container_cpu_usage_percent":    {labelContainerName, labelOrigin},
	"container_memory_usage_bytes":   {labelContainerName, labelOrigin},
	"container_memory_limit_bytes":   {labelContainerName, labelOrigin},
	"container_memory_usage_percent": {labelContainerName, labelOrigin},
}

var (
	coreValue          = regexp.MustCompile(`^(total|[0-9]{1,4})$`)
	containerNameValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// ContractMetrics lists the allowed metric names, for documentation and tests.
func ContractMetrics() []string {
	names := make([]string, 0, len(contract))
	for name := range contract {
		names = append(names, name)
	}
	return names
}

// Sample is one data point.
type Sample struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// validLabel reports whether a label value is acceptable for its key.
func validLabel(key, value string) bool {
	switch key {
	case labelCore:
		return coreValue.MatchString(value)
	case labelContainerName:
		return containerNameValue.MatchString(value)
	case labelMount:
		return value != "" && len(value) <= maxLabelValueBytes && strings.HasPrefix(value, "/") && !strings.ContainsAny(value, "\x00\n\r")
	case labelOrigin:
		return value == OriginGuard || value == OriginOTLP
	}
	return false
}

// Admit reports whether a sample is on the contract: a listed metric, a finite
// value, only listed label keys with bounded values. A sample without an origin
// is not admitted; the emitter stamps it.
func Admit(sample Sample) bool {
	allowed, ok := contract[sample.Name]
	if !ok || math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) || len(sample.Labels) > len(allowed) {
		return false
	}
	for key, value := range sample.Labels {
		listed := false
		for _, name := range allowed {
			if name == key {
				listed = true
			}
		}
		if !listed || !validLabel(key, value) {
			return false
		}
	}
	_, hasOrigin := sample.Labels[labelOrigin]
	return hasOrigin
}

// Filter keeps the admitted samples and counts what it dropped.
func Filter(samples []Sample) (kept []Sample, dropped int) {
	kept = make([]Sample, 0, len(samples))
	for _, sample := range samples {
		if Admit(sample) {
			kept = append(kept, sample)
		} else {
			dropped++
		}
	}
	return kept, dropped
}

// Gap is an explicit marker that telemetry between From and To was not kept.
type Gap struct {
	From         time.Time `json:"from"`
	To           time.Time `json:"to"`
	FromSequence int64     `json:"from_sequence"`
	ToSequence   int64     `json:"to_sequence"`
	// Reason is a bounded token: spool_budget, rejected or restart.
	Reason string `json:"reason"`
}

// Gap reasons.
const (
	GapSpoolBudget = "spool_budget"
	GapRejected    = "rejected"
)

// Record is one collection cycle, or a gap marker.
type Record struct {
	Sequence int64     `json:"sequence"`
	At       time.Time `json:"at"`
	Samples  []Sample  `json:"samples,omitempty"`
	Gap      *Gap      `json:"gap,omitempty"`
	// DriveHealth is a bounded observation envelope, separate from filesystem
	// resource gauges. Null readings never become numeric zeroes.
	DriveHealth *drivehealth.Collection `json:"drive_health,omitempty"`
}

// Batch is what the Guard posts.
type Batch struct {
	SchemaVersion string   `json:"schema_version"`
	SourceEpoch   string   `json:"source_epoch"`
	Records       []Record `json:"records"`
}

// Ack is the control plane's answer: every record up to AckedSequence is stored.
type Ack struct {
	State         string `json:"state,omitempty"`
	AckedSequence int64  `json:"acked_sequence"`
	Accepted      int    `json:"accepted"`
	Dropped       int    `json:"dropped"`
}

// StateDisabled is the answer of a control plane that stores no telemetry.
const StateDisabled = "disabled"

// ErrBatchInvalid marks a batch the control plane refuses.
var ErrBatchInvalid = errors.New("guardtelemetry: the telemetry batch is not acceptable")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBatchInvalid, fmt.Sprintf(format, args...))
}

var gapReasonValue = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// Validate checks the envelope and the bounds of a batch. Samples are not
// checked here: the contract filter drops what is off it and reports the count.
func (b *Batch) Validate(now time.Time) error {
	if b.SchemaVersion != BatchSchema {
		return invalid("schema %q is not %s", b.SchemaVersion, BatchSchema)
	}
	b.SourceEpoch = strings.TrimSpace(b.SourceEpoch)
	if b.SourceEpoch == "" || len(b.SourceEpoch) > 64 {
		return invalid("the source epoch is missing")
	}
	if len(b.Records) == 0 || len(b.Records) > MaxRecordsPerBatch {
		return invalid("a batch carries between 1 and %d records", MaxRecordsPerBatch)
	}
	total := 0
	var last int64
	for _, record := range b.Records {
		if record.Sequence <= last {
			return invalid("record sequences must increase")
		}
		last = record.Sequence
		if record.At.IsZero() || record.At.After(now.Add(maxClockSkew)) || now.Sub(record.At) > maxRecordAge {
			return invalid("record %d has no usable time", record.Sequence)
		}
		if len(record.Samples) > MaxSamplesPerRecord {
			return invalid("record %d carries %d samples", record.Sequence, len(record.Samples))
		}
		if record.DriveHealth != nil {
			if err := record.DriveHealth.Validate(now); err != nil {
				return invalid("record %d has invalid drive observations: %v", record.Sequence, err)
			}
		}
		total += len(record.Samples)
		if gap := record.Gap; gap != nil {
			if len(record.Samples) != 0 || record.DriveHealth != nil || !gapReasonValue.MatchString(gap.Reason) || gap.From.IsZero() || gap.To.Before(gap.From) ||
				gap.ToSequence < gap.FromSequence || gap.To.Sub(gap.From) > maxRecordAge {
				return invalid("record %d holds a malformed gap marker", record.Sequence)
			}
		}
	}
	if total > MaxSamplesPerBatch {
		return invalid("the batch carries %d samples", total)
	}
	return nil
}
