package journal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
)

func testIntent(id string) Intent {
	return Intent{
		CommandID: id, TenantID: "tenant-1", WorkerID: "worker-1", Operation: "STACKKIT_OPERATION_APPLY",
		Mutating: true, Generation: 3, PayloadDigest: "d-" + id, ContractVersion: "guard.command-binding/v1",
		AuthorityExpiresAt: time.Now().Add(15 * time.Minute), ResourceKey: "tenant-1/worker-1",
	}
}

func open(t *testing.T, dir string, opts Options) *Store {
	t.Helper()
	store, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// A process that dies at any step leaves exactly the durable state of the
// steps that completed, and a known command is never admitted twice.
func TestCrashAtEveryStepKeepsOneEffectOrUnknown(t *testing.T) {
	dir := t.TempDir()
	reopen := func(s *Store) *Store {
		_ = s.Close() // Close has no buffered state: equal to a crash.
		return open(t, dir, Options{})
	}
	s := open(t, dir, Options{})
	if _, existing, err := s.Admit(testIntent("c1")); err != nil || existing {
		t.Fatalf("admit: existing=%v err=%v", existing, err)
	}

	// Crash before the dispatched marker: no effect began, the intent survives.
	s = reopen(s)
	entry, existing, err := s.Admit(testIntent("c1"))
	if err != nil || !existing || entry.State != StateIntent {
		t.Fatalf("after crash before marker: %v existing=%v err=%v", entry, existing, err)
	}
	if err := s.MarkDispatched("c1"); err != nil {
		t.Fatal(err)
	}

	// Crash after the marker, before any result: uncertain, never re-runnable.
	s = reopen(s)
	entry, existing, _ = s.Admit(testIntent("c1"))
	if !existing || entry.State != StateDispatched {
		t.Fatalf("after crash after marker: %v", entry)
	}
	if err := s.MarkDispatched("c1"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("a dispatched command must not be dispatched again: %v", err)
	}
	if err := s.RecordResult("c1", ResultRecord{Revision: 1, State: ReportOutcomeUnknown, Digest: "u1", Reason: "interrupted"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ack("c1", 1, false); err != nil {
		t.Fatal(err)
	}

	// outcome_unknown is non-terminal even when acknowledged; a resolution
	// must name the evidence it supersedes.
	s = reopen(s)
	if entry, _ = s.Entry("c1"); entry.State != StateOutcomeUnknown || len(s.Fenced("tenant-1/worker-1", "")) != 1 {
		t.Fatalf("unknown must keep fencing: %v", entry)
	}
	if err := s.RecordResult("c1", ResultRecord{Revision: 2, State: ReportTerminal, Digest: "r2", PriorDigest: "wrong"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("resolution with wrong prior evidence accepted: %v", err)
	}
	payload := []byte(`{"command_id":"c1","success":true}`)
	if err := s.RecordResult("c1", ResultRecord{Revision: 2, State: ReportTerminal, Digest: "r2", PriorDigest: "u1", Resolution: "observed_converged", Payload: payload}); err != nil {
		t.Fatal(err)
	}

	// Crash after the result is durable and before its ack: redeliver the same
	// bytes, never execute.
	s = reopen(s)
	entry, _ = s.Entry("c1")
	next, ok := entry.NextUnacked()
	if entry.State != StateResultPending || !ok || next.Revision != 2 || !bytes.Equal(next.Payload, payload) {
		t.Fatalf("unacked result lost: %v %+v", entry, next)
	}
	if err := s.Ack("c1", 2, false); err != nil {
		t.Fatal(err)
	}
	if len(s.Fenced("tenant-1/worker-1", "")) != 0 {
		t.Fatal("an acknowledged terminal resolution must release the fence")
	}

	// After compaction the command is a tombstone: replay stays rejected.
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	s = reopen(s)
	if _, _, err := s.Admit(testIntent("c1")); !errors.Is(err, ErrTombstoned) {
		t.Fatalf("replay of a compacted command admitted: %v", err)
	}
	conflicting := testIntent("c1")
	conflicting.PayloadDigest = "other"
	if _, _, err := s.Admit(conflicting); !errors.Is(err, ErrDigestConflict) {
		t.Fatalf("altered replay admitted: %v", err)
	}
}

// A torn tail is repaired; corruption before the tail denies new mutations,
// keeps the evidence readable and leaves the file untouched.
func TestTornTailRepairedAndCorruptionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	for _, id := range []string{"a", "b"} {
		if _, _, err := s.Admit(testIntent(id)); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()
	path := filepath.Join(dir, logName)
	intact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A half-written record after two durable ones.
	if err := os.WriteFile(path, append(append([]byte(nil), intact...), 0x40, 0, 0, 0, 1, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir, Options{})
	if h := s.Health(); h.State != "ok" || !h.TornTailRepaired || h.Entries != 2 {
		t.Fatalf("torn tail not repaired: %+v", h)
	}
	if _, _, err := s.Admit(testIntent("c")); err != nil {
		t.Fatalf("repaired journal refused a write: %v", err)
	}
	_ = s.Close()

	// Flip one byte inside the first record: durable history is damaged.
	damaged, _ := os.ReadFile(path)
	damaged[headerSize+recHeaderLen+5] ^= 0xff
	if err := os.WriteFile(path, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir, Options{})
	defer s.Close()
	if h := s.Health(); h.State != "denied" || h.Reason != ReasonCorrupt {
		t.Fatalf("corruption not denied: %+v", h)
	}
	var denied *DeniedError
	if _, _, err := s.Admit(testIntent("d")); !errors.As(err, &denied) || denied.Reason != ReasonCorrupt {
		t.Fatalf("mutation admitted on a corrupt journal: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, damaged) {
		t.Fatal("a corrupt journal was modified")
	}
}

// Backpressure never prunes unacknowledged work, and only one process owns the
// journal.
func TestBudgetDeniesNewWorkWithoutDroppingUnackedAndSingleWriter(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{MaxBytes: 4096, MaxResultBytes: 1024})
	var denied *DeniedError
	admitted := 0
	for i := 0; i < 20; i++ {
		id := string(rune('a' + i))
		if _, _, err := s.Admit(testIntent(id)); err != nil {
			if !errors.As(err, &denied) || denied.Reason != ReasonFull {
				t.Fatalf("unexpected denial: %v", err)
			}
			break
		}
		admitted++
	}
	if denied == nil || admitted == 0 {
		t.Fatalf("budget never applied (admitted %d)", admitted)
	}
	if got := len(s.Entries(nil)); got != admitted {
		t.Fatalf("unacknowledged entries were dropped: %d of %d", got, admitted)
	}
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second writer opened the journal: %v", err)
	}
}

// A revoked server's journal drops what the control plane acknowledged and
// every replay tombstone, keeps only evidence nobody acknowledged, and is
// destroyed once nothing is left.
func TestDecommissionKeepsOnlyUnacknowledgedEvidence(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "state")
	root, err := privatefs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	s := open(t, dir, Options{})
	moved := filepath.Join(parent, "admitted")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(parent, "outside")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("unrelated journal custody")
	if err := os.WriteFile(filepath.Join(other, logName), sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"acked", "pending"} {
		if _, _, err := s.Admit(testIntent(id)); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkDispatched(id); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordResult(id, ResultRecord{Revision: 1, State: ReportTerminal, Digest: "d-" + id, Payload: []byte(`{"ok":true}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Ack("acked", 1, false); err != nil {
		t.Fatal(err)
	}
	kept, err := s.Decommission()
	if err != nil || kept != 1 {
		t.Fatalf("Decommission = %d, %v; want the one unacknowledged entry kept", kept, err)
	}
	if _, ok := s.Entry("acked"); ok {
		t.Fatal("an acknowledged entry survived")
	}
	_ = s.Close()

	s, err = OpenRoot(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.Entry("pending"); !ok {
		t.Fatal("unacknowledged evidence was lost")
	}
	if err := s.Ack("pending", 1, false); err != nil {
		t.Fatal(err)
	}
	if kept, err = s.Decommission(); err != nil || kept != 0 {
		t.Fatalf("second Decommission = %d, %v", kept, err)
	}
	if _, statErr := os.Stat(filepath.Join(moved, logName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the journal log survived with nothing left to keep: %v", statErr)
	}
	if got, err := os.ReadFile(filepath.Join(other, logName)); err != nil || !bytes.Equal(got, sentinel) {
		t.Fatalf("journal changed replacement custody: %v", err)
	}
}
