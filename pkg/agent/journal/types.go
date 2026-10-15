package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SchemaVersion is the on-disk format this build reads and writes. A journal
// written by a newer format is never opened for writing.
const SchemaVersion = 1

// State is the derived durable state of one command.
type State string

const (
	StateIntent         State = "intent"
	StateDispatched     State = "dispatched"
	StateResultPending  State = "result_pending"
	StateOutcomeUnknown State = "outcome_unknown"
	StateAcked          State = "acked"
)

// Report states carried by a ResultRecord. They mirror the wire contract.
const (
	ReportTerminal       = "terminal"
	ReportOutcomeUnknown = "outcome_unknown"
	ReportRejected       = "rejected"
	ReportResolved       = "resolved"
)

// Denial reasons. Each one fails new mutations closed and keeps read-only
// diagnosis working.
const (
	ReasonCorrupt     = "journal_corrupt"
	ReasonSchema      = "journal_schema_incompatible"
	ReasonFull        = "journal_full"
	ReasonWriteFailed = "journal_write_failed"
	ReasonPermissions = "journal_permissions"
	ReasonUnavailable = "journal_unavailable"
)

var (
	// ErrLocked means another process holds the single-writer lock.
	ErrLocked = errors.New("journal is locked by another process")
	// ErrTombstoned means the command was already acknowledged and compacted.
	ErrTombstoned = errors.New("command was already completed and compacted")
	// ErrDigestConflict means a known command ID arrived with another payload.
	ErrDigestConflict = errors.New("command identity is known with a different payload digest")
	// ErrEntryTooLarge means a record exceeds its bound.
	ErrEntryTooLarge = errors.New("journal entry exceeds its size bound")
	// ErrInvalidTransition means the requested step is not permitted from the
	// command's durable state.
	ErrInvalidTransition = errors.New("invalid journal transition")
	// ErrNotFound means the command has no journal entry.
	ErrNotFound = errors.New("journal entry not found")
)

// DeniedError reports that the journal refuses new writes.
type DeniedError struct {
	Reason string
	Detail string
}

func (e *DeniedError) Error() string {
	if e.Detail == "" {
		return e.Reason
	}
	return e.Reason + ": " + e.Detail
}

// Intent is the minimum durable identity of an admitted command. It carries
// digests and a small reconciliation hint, never a reusable secret or the
// command payload.
type Intent struct {
	CommandID          string          `json:"command_id"`
	TenantID           string          `json:"tenant_id"`
	WorkerID           string          `json:"worker_id"`
	Operation          string          `json:"operation"`
	Mutating           bool            `json:"mutating"`
	Generation         int64           `json:"generation"`
	PayloadDigest      string          `json:"payload_digest"`
	AuthorityExpiresAt time.Time       `json:"authority_expires_at"`
	ContractVersion    string          `json:"contract_version"`
	ResourceKey        string          `json:"resource_key"`
	Hint               json.RawMessage `json:"hint,omitempty"`
}

// ExecutorRef identifies the executor process group so a restarted Guard can
// prove the previous executor is gone before it admits conflicting work.
type ExecutorRef struct {
	PID        int    `json:"pid"`
	PGID       int    `json:"pgid"`
	StartTicks uint64 `json:"start_ticks,omitempty"`
}

// ResultRecord is one immutable outcome revision.
type ResultRecord struct {
	Revision         int64           `json:"revision"`
	State            string          `json:"state"`
	Digest           string          `json:"digest"`
	PriorDigest      string          `json:"prior_digest,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	Resolution       string          `json:"resolution,omitempty"`
	Truncated        bool            `json:"truncated,omitempty"`
	SensitiveOmitted bool            `json:"sensitive_omitted,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	At               time.Time       `json:"at"`
}

// Open reports whether the revision leaves the outcome unsettled: an
// outcome_unknown, or a resolution that only escalated to an operator.
func (r ResultRecord) Open() bool {
	return r.State == ReportOutcomeUnknown || (r.State == ReportResolved && r.Resolution == "operator_escalation")
}

// Entry is a read-only snapshot of one command.
type Entry struct {
	Intent        Intent
	State         State
	IntentAt      time.Time
	DispatchedAt  time.Time
	Executor      *ExecutorRef
	Results       []ResultRecord
	AckedRevision int64
	// Rejected is true when the control plane permanently refused the final
	// delivery; the evidence stays for diagnosis.
	Rejected bool
}

// Latest returns the newest result revision.
func (e Entry) Latest() (ResultRecord, bool) {
	if len(e.Results) == 0 {
		return ResultRecord{}, false
	}
	return e.Results[len(e.Results)-1], true
}

// NextUnacked returns the lowest revision not yet acknowledged.
func (e Entry) NextUnacked() (ResultRecord, bool) {
	for _, result := range e.Results {
		if result.Revision > e.AckedRevision {
			return result, true
		}
	}
	return ResultRecord{}, false
}

// Health is the operator-facing summary of the journal.
type Health struct {
	State             string // "ok" or "denied"
	Reason            string
	Detail            string
	SchemaVersion     int
	Bytes             int64
	BudgetBytes       int64
	Entries           int
	InFlight          int
	UnackedResults    int
	UnresolvedUnknown int
	TornTailRepaired  bool
}

// Options bounds and instruments a Store. Zero values select the defaults.
type Options struct {
	// MaxBytes is the disk budget of the log file.
	MaxBytes int64
	// MaxResultBytes bounds one persisted result payload; a slot of this size
	// is reserved before a command is admitted.
	MaxResultBytes int64
	// MaxIntentBytes bounds one intent record.
	MaxIntentBytes int64
	// ClockMargin extends the deduplication horizon beyond authority expiry.
	ClockMargin time.Duration
	// MinTombstone is the shortest time a tombstone outlives its acknowledgement.
	MinTombstone time.Duration
	// CompactAfterBytes triggers compaction when the log grows past it.
	CompactAfterBytes int64
	Now               func() time.Time
	// FreeBytes reports free space for the journal directory; nil uses the OS.
	FreeBytes func(dir string) (uint64, error)
}

func (o Options) withDefaults() Options {
	if o.MaxBytes <= 0 {
		o.MaxBytes = 256 << 20
	}
	if o.MaxResultBytes <= 0 {
		o.MaxResultBytes = 24 << 20
	}
	if o.MaxIntentBytes <= 0 {
		o.MaxIntentBytes = 64 << 10
	}
	if o.ClockMargin <= 0 {
		o.ClockMargin = 15 * time.Minute
	}
	if o.MinTombstone <= 0 {
		o.MinTombstone = time.Hour
	}
	if o.CompactAfterBytes <= 0 {
		o.CompactAfterBytes = 8 << 20
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	return o
}

func (e Entry) String() string {
	return fmt.Sprintf("%s(%s)", e.Intent.CommandID, e.State)
}
