package postureagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardposture"
)

const (
	ledgerFile    = "local-repairs.json"
	ledgerSchema  = 1
	ledgerMax     = 256
	ledgerRetain  = 48 * time.Hour
	restartReason = "guard_restarted_during_repair"
)

// Ledger is the Guard's durable record of the repairs it ran on its own under
// a local policy. It follows the journal's order: the operation is recorded
// before any effect, its outcome after, and the record stays until the control
// plane acknowledged the outcome. An operation that was recorded but has no
// outcome when the ledger is opened again (the Guard stopped during the
// effect) is outcome_unknown, never run again. Because the record survives a
// restart, so do the cooldown, the attempt budget and the circuit breaker it
// feeds.
type Ledger struct {
	root *os.Root
	now  func() time.Time

	mu      sync.Mutex
	entries []ledgerEntry
	err     error
}

type ledgerEntry struct {
	// Evidence is the revision to deliver; until the effect finished it has
	// revision 0 and holds the operation's binding only.
	Evidence      guardposture.LocalOperationEvidence `json:"evidence"`
	FinishedAt    *time.Time                          `json:"finished_at,omitempty"`
	AckedRevision int64                               `json:"acked_revision,omitempty"`
	// Rejected is set when the control plane refused the evidence; it is kept
	// for diagnosis and still counts toward the bounds.
	Rejected bool `json:"rejected,omitempty"`
}

type ledgerDocument struct {
	Schema  int           `json:"schema"`
	Entries []ledgerEntry `json:"entries"`
}

// OpenLedger opens the ledger in the Guard's custody directory. A ledger that
// cannot be read is not discarded: it refuses new local repairs, so the
// bounds it holds are never reset by a damaged file.
func OpenLedger(root *os.Root, now func() time.Time) *Ledger {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	l := &Ledger{root: root, now: now}
	if root == nil {
		l.err = errors.New("no custody directory for the local repair ledger")
		return l
	}
	data, err := root.ReadFile(ledgerFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return l
	case err != nil:
		l.err = err
		return l
	}
	var doc ledgerDocument
	if err := json.Unmarshal(data, &doc); err != nil || doc.Schema != ledgerSchema {
		l.err = fmt.Errorf("local repair ledger is unreadable or of another schema")
		return l
	}
	l.entries = doc.Entries
	at := l.now()
	changed := false
	for i := range l.entries {
		if l.entries[i].Evidence.Report.Revision == 0 {
			// Recorded before the effect, no outcome since: the effect may
			// have started.
			l.settleLocked(i, unknownOutcome(restartReason), at)
			changed = true
		}
	}
	if changed {
		l.err = l.saveLocked()
	}
	return l
}

// Err is non-nil when the ledger refuses new local repairs.
func (l *Ledger) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Repairs are the recorded local repairs as the bounds count them.
func (l *Ledger) Repairs() []guardposture.Repair {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]guardposture.Repair, 0, len(l.entries))
	for _, entry := range l.entries {
		e := entry.Evidence
		status := guardposture.RepairDispatched
		switch e.Report.State {
		case guardbinding.ReportTerminal:
			status = guardposture.RepairOutcomeStatus(e.Outcome)
		case guardbinding.ReportOutcomeUnknown:
			status = guardposture.RepairOutcomeUnknown
		}
		out = append(out, guardposture.Repair{
			Mode: guardposture.RepairLocal, Controls: e.Controls, RequestedAt: e.StartedAt, FinishedAt: entry.FinishedAt, Status: status,
			Local: &guardposture.LocalBinding{OperationID: e.OperationID, PolicyID: e.PolicyID, PolicyVersion: e.PolicyVersion},
		})
	}
	return out
}

// Fenced reports whether an earlier local repair has an unknown outcome the
// control plane has not acknowledged: new local repairs wait for it.
func (l *Ledger) Fenced() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if entry.Evidence.Report.State == guardbinding.ReportOutcomeUnknown && entry.AckedRevision < entry.Evidence.Report.Revision && !entry.Rejected {
			return true
		}
	}
	return false
}

// Begin durably records an operation before its effect.
func (l *Ledger) Begin(evidence guardposture.LocalOperationEvidence) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	l.pruneLocked()
	if len(l.entries) >= ledgerMax {
		return errors.New("local repair ledger is full")
	}
	evidence.Report = guardbinding.ResultReport{}
	l.entries = append(l.entries, ledgerEntry{Evidence: evidence})
	if err := l.saveLocked(); err != nil {
		l.entries = l.entries[:len(l.entries)-1]
		return err
	}
	return nil
}

// Abandon removes an operation whose effect never started.
func (l *Ledger) Abandon(operationID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, entry := range l.entries {
		if entry.Evidence.OperationID == operationID && entry.Evidence.Report.Revision == 0 {
			l.entries = append(l.entries[:i], l.entries[i+1:]...)
			return l.saveLocked()
		}
	}
	return nil
}

// outcome is what a finished local repair produced.
type outcome struct {
	state   string
	reason  string
	outcome string
	steps   []guardposture.RepairStep
}

func unknownOutcome(reason string) outcome {
	return outcome{state: guardbinding.ReportOutcomeUnknown, reason: reason}
}

// Finish durably records the first outcome revision of an operation.
func (l *Ledger) Finish(operationID string, result outcome) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, entry := range l.entries {
		if entry.Evidence.OperationID == operationID && entry.Evidence.Report.Revision == 0 {
			l.settleLocked(i, result, l.now())
			return l.saveLocked()
		}
	}
	return fmt.Errorf("local repair %s is not awaiting an outcome", operationID)
}

func (l *Ledger) settleLocked(i int, result outcome, at time.Time) {
	e := l.entries[i].Evidence
	e.Report = guardbinding.ResultReport{Revision: 1, State: result.state, Reason: result.reason}
	e.Outcome, e.Steps = result.outcome, result.steps
	if digest, err := e.ComputeDigest(); err == nil {
		e.Report.Digest = digest
	}
	finished := at.UTC()
	l.entries[i].Evidence, l.entries[i].FinishedAt = e, &finished
}

// Pending is the evidence the control plane has not acknowledged yet.
func (l *Ledger) Pending() []guardposture.LocalOperationEvidence {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []guardposture.LocalOperationEvidence
	for _, entry := range l.entries {
		if rev := entry.Evidence.Report.Revision; rev > 0 && rev > entry.AckedRevision && !entry.Rejected {
			out = append(out, entry.Evidence)
		}
	}
	return out
}

// Acknowledge records the control plane's answer to one evidence revision.
func (l *Ledger) Acknowledge(operationID string, revision int64, rejected bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.entries {
		if l.entries[i].Evidence.OperationID != operationID {
			continue
		}
		if rejected {
			l.entries[i].Rejected = true
		} else if revision > l.entries[i].AckedRevision {
			l.entries[i].AckedRevision = revision
		}
		return l.saveLocked()
	}
	return nil
}

// pruneLocked drops settled records past the retention; nothing unacknowledged
// is ever dropped.
func (l *Ledger) pruneLocked() {
	cutoff := l.now().Add(-ledgerRetain)
	kept := l.entries[:0]
	for _, entry := range l.entries {
		settled := entry.Rejected || (entry.Evidence.Report.Revision > 0 && entry.AckedRevision >= entry.Evidence.Report.Revision)
		if settled && entry.Evidence.StartedAt.Before(cutoff) {
			continue
		}
		kept = append(kept, entry)
	}
	l.entries = kept
}

// saveLocked writes the ledger atomically: a temporary file, fsync, rename.
func (l *Ledger) saveLocked() error {
	entries := l.entries
	if entries == nil {
		entries = []ledgerEntry{}
	}
	data, err := json.Marshal(ledgerDocument{Schema: ledgerSchema, Entries: entries})
	if err != nil {
		return err
	}
	tmp := ledgerFile + ".tmp"
	file, err := l.root.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := l.root.Rename(tmp, ledgerFile); err != nil {
		return err
	}
	if dir, err := l.root.Open("."); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
