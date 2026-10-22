package journal

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"

	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
)

const (
	logName      = "journal.log"
	lockName     = "journal.lock"
	magic        = "KGJRNL1\n"
	headerSize   = 16
	recHeaderLen = 8
	// maxRecordExtra is the headroom over MaxResultBytes for the JSON envelope.
	maxRecordExtra = 1 << 20
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

type tombstone struct {
	Digest    string    `json:"digest"`
	ExpiresAt time.Time `json:"expires_at"`
}

type record struct {
	Kind       string        `json:"k"`
	ID         string        `json:"id,omitempty"`
	At         time.Time     `json:"at"`
	Intent     *Intent       `json:"intent,omitempty"`
	Executor   *ExecutorRef  `json:"executor,omitempty"`
	Result     *ResultRecord `json:"result,omitempty"`
	Revision   int64         `json:"rev,omitempty"`
	Rejected   bool          `json:"rejected,omitempty"`
	Generation int64         `json:"gen,omitempty"`
	Tombstone  *tombstone    `json:"tombstone,omitempty"`
}

type entryState struct {
	Entry
	ackedAt time.Time
}

func (e *entryState) recompute() {
	if last, ok := e.Latest(); ok {
		switch {
		case last.Open():
			e.State = StateOutcomeUnknown
		case e.AckedRevision >= last.Revision:
			e.State = StateAcked
		default:
			e.State = StateResultPending
		}
		return
	}
	if !e.DispatchedAt.IsZero() {
		e.State = StateDispatched
		return
	}
	e.State = StateIntent
}

func (e *entryState) snapshot() Entry {
	out := e.Entry
	out.Intent.Hint = append(json.RawMessage(nil), e.Intent.Hint...)
	if e.Executor != nil {
		ref := *e.Executor
		out.Executor = &ref
	}
	out.Results = append([]ResultRecord(nil), e.Results...)
	return out
}

// Store is one open journal. All methods are safe for concurrent use.
type Store struct {
	mu           sync.Mutex
	dir          string
	root         *os.Root
	opts         Options
	lock         *os.File
	file         *os.File
	size         int64
	entries      map[string]*entryState
	tombs        map[string]tombstone
	highGen      int64
	denied       *DeniedError
	tornRepaired bool
	schema       int
}

// Open acquires the single-writer lock and replays the journal in dir. It
// returns an error only when the journal cannot be owned at all (another
// process holds it, or the directory is unusable). A corrupt, incompatible or
// unwritable journal opens in a denied state: Health and Entries still work,
// every mutation fails closed, and the file is left untouched.
func Open(dir string, opts Options) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("journal directory is required")
	}
	root, err := privatefs.Open(dir)
	if err != nil {
		return nil, err
	}
	return openRoot(root, opts)
}

// OpenRoot uses the existing journal with a clone of admitted node custody.
// It never reopens the configured pathname after a directory replacement.
func OpenRoot(custody *os.Root, opts Options) (*Store, error) {
	root, err := custody.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if err := privatefs.CheckRoot(root); err != nil {
		root.Close()
		return nil, err
	}
	return openRoot(root, opts)
}

func openRoot(root *os.Root, opts Options) (*Store, error) {
	opts = opts.withDefaults()
	s := &Store{dir: root.Name(), root: root, opts: opts, entries: map[string]*entryState{}, tombs: map[string]tombstone{}, schema: SchemaVersion}
	lock, err := acquireLock(root, lockName)
	if err != nil {
		root.Close()
		return nil, err
	}
	s.lock = lock
	s.load()
	return s, nil
}

func (s *Store) load() {
	path := logName
	data, err := privatefs.ReadFile(s.root, path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := s.createLog(path); err != nil {
			s.denied = &DeniedError{Reason: ReasonWriteFailed, Detail: err.Error()}
		}
		return
	case err != nil:
		s.denied = &DeniedError{Reason: ReasonUnavailable, Detail: err.Error()}
		return
	}

	if len(data) < headerSize || string(data[:len(magic)]) != magic {
		if len(data) < headerSize {
			// A crash while the header was being created leaves a file that
			// never held a record; nothing durable is lost by starting over.
			if err := s.createLog(path); err != nil {
				s.denied = &DeniedError{Reason: ReasonWriteFailed, Detail: err.Error()}
			}
			return
		}
		s.denied = &DeniedError{Reason: ReasonCorrupt, Detail: "unrecognized journal header"}
		return
	}
	schema := int(binary.LittleEndian.Uint32(data[len(magic) : len(magic)+4]))
	s.schema = schema
	if schema > SchemaVersion || schema < 1 {
		s.denied = &DeniedError{Reason: ReasonSchema, Detail: fmt.Sprintf("journal schema %d, this Guard supports %d", schema, SchemaVersion)}
		return
	}
	good, torn, err := s.replay(data)
	if err != nil {
		s.entries, s.tombs, s.highGen = map[string]*entryState{}, map[string]tombstone{}, 0
		// Keep what replayed cleanly readable for diagnosis.
		s.replayPrefixOnly(data)
		s.denied = &DeniedError{Reason: ReasonCorrupt, Detail: err.Error()}
		return
	}
	file, err := privatefs.OpenFile(s.root, path, os.O_RDWR, 0o600)
	if err != nil {
		s.denied = &DeniedError{Reason: ReasonUnavailable, Detail: err.Error()}
		return
	}
	if torn {
		// The tail was never acknowledged to a caller: an append is durable
		// only after its fsync returns, and this record failed that test.
		if err := file.Truncate(good); err != nil {
			_ = file.Close()
			s.denied = &DeniedError{Reason: ReasonWriteFailed, Detail: "truncate torn tail: " + err.Error()}
			return
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			s.denied = &DeniedError{Reason: ReasonWriteFailed, Detail: "sync torn tail repair: " + err.Error()}
			return
		}
		s.tornRepaired = true
	}
	s.file = file
	s.size = good
}

// replayPrefixOnly rebuilds the readable part of a corrupt journal for
// read-only diagnosis. It never writes.
func (s *Store) replayPrefixOnly(data []byte) {
	offset := int64(headerSize)
	for offset+recHeaderLen <= int64(len(data)) {
		rec, next, err := decodeRecord(data, offset, s.opts.MaxResultBytes+maxRecordExtra)
		if err != nil {
			return
		}
		if s.apply(rec) != nil {
			return
		}
		offset = next
	}
}

func (s *Store) createLog(path string) error {
	file, err := privatefs.OpenFile(s.root, path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	header := make([]byte, headerSize)
	copy(header, magic)
	binary.LittleEndian.PutUint32(header[len(magic):], SchemaVersion)
	if _, err := file.WriteAt(header, 0); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := syncDir(s.root); err != nil {
		_ = file.Close()
		return err
	}
	s.file = file
	s.size = headerSize
	return nil
}

func decodeRecord(data []byte, offset, maxRecord int64) (record, int64, error) {
	total := int64(len(data))
	if total-offset < recHeaderLen {
		return record{}, offset, errTornTail
	}
	length := int64(binary.LittleEndian.Uint32(data[offset : offset+4]))
	sum := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
	if length > maxRecord {
		return record{}, offset, fmt.Errorf("record at offset %d declares %d bytes", offset, length)
	}
	end := offset + recHeaderLen + length
	if end > total {
		return record{}, offset, errTornTail
	}
	payload := data[offset+recHeaderLen : end]
	if crc32.Checksum(payload, crcTable) != sum {
		if end == total {
			return record{}, offset, errTornTail
		}
		return record{}, offset, fmt.Errorf("checksum mismatch at offset %d", offset)
	}
	var rec record
	if err := json.Unmarshal(payload, &rec); err != nil {
		return record{}, offset, fmt.Errorf("record at offset %d is not valid: %v", offset, err)
	}
	return rec, end, nil
}

var errTornTail = errors.New("torn tail")

// replay applies every record. It returns the size of the durable prefix and
// whether an incomplete final record follows it.
func (s *Store) replay(data []byte) (good int64, torn bool, err error) {
	offset := int64(headerSize)
	maxRecord := s.opts.MaxResultBytes + maxRecordExtra
	for offset < int64(len(data)) {
		rec, next, decodeErr := decodeRecord(data, offset, maxRecord)
		if errors.Is(decodeErr, errTornTail) {
			return offset, true, nil
		}
		if decodeErr != nil {
			return 0, false, decodeErr
		}
		if applyErr := s.apply(rec); applyErr != nil {
			return 0, false, fmt.Errorf("record at offset %d: %w", offset, applyErr)
		}
		offset = next
	}
	return offset, false, nil
}

func (s *Store) apply(rec record) error {
	switch rec.Kind {
	case "gen":
		if rec.Generation > s.highGen {
			s.highGen = rec.Generation
		}
	case "intent":
		if rec.Intent == nil || rec.Intent.CommandID == "" {
			return errors.New("intent without identity")
		}
		if _, exists := s.entries[rec.Intent.CommandID]; exists {
			return errors.New("duplicate intent")
		}
		e := &entryState{Entry: Entry{Intent: *rec.Intent, IntentAt: rec.At}}
		e.recompute()
		s.entries[rec.Intent.CommandID] = e
	case "dispatched":
		e, ok := s.entries[rec.ID]
		if !ok {
			return errors.New("dispatched marker without intent")
		}
		e.DispatchedAt = rec.At
		e.recompute()
	case "executor":
		e, ok := s.entries[rec.ID]
		if !ok || rec.Executor == nil {
			return errors.New("executor record without intent")
		}
		ref := *rec.Executor
		e.Executor = &ref
	case "result":
		e, ok := s.entries[rec.ID]
		if !ok || rec.Result == nil || rec.Result.Revision != int64(len(e.Results))+1 {
			return errors.New("result record out of order")
		}
		e.Results = append(e.Results, *rec.Result)
		e.recompute()
	case "ack":
		e, ok := s.entries[rec.ID]
		if !ok || rec.Revision < 1 || rec.Revision > int64(len(e.Results)) {
			return errors.New("ack for unknown revision")
		}
		if rec.Revision > e.AckedRevision {
			e.AckedRevision = rec.Revision
			e.ackedAt = rec.At
		}
		if rec.Rejected {
			e.Rejected = true
		}
		e.recompute()
	case "tombstone":
		if rec.Tombstone == nil || rec.ID == "" {
			return errors.New("tombstone without identity")
		}
		delete(s.entries, rec.ID)
		s.tombs[rec.ID] = *rec.Tombstone
	default:
		return fmt.Errorf("unknown record kind %q", rec.Kind)
	}
	if rec.Kind == "intent" && rec.Intent != nil && rec.Intent.Generation > s.highGen {
		s.highGen = rec.Intent.Generation
	}
	return nil
}

func encodeRecord(rec record) ([]byte, error) {
	payload, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	if len(payload) > math.MaxUint32 {
		return nil, ErrEntryTooLarge
	}
	buf := make([]byte, recHeaderLen+len(payload))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(payload))) // #nosec G115 -- bounded above.
	binary.LittleEndian.PutUint32(buf[4:8], crc32.Checksum(payload, crcTable))
	copy(buf[recHeaderLen:], payload)
	return buf, nil
}

// appendLocked makes one record durable and applies it. The in-memory state
// changes only after fsync returned, so a caller is told "durable" only when
// it is.
func (s *Store) appendLocked(rec record) error {
	if s.denied != nil {
		return s.denied
	}
	rec.At = s.opts.Now()
	buf, err := encodeRecord(rec)
	if err != nil {
		return err
	}
	if int64(len(buf)) > s.opts.MaxResultBytes+maxRecordExtra {
		return ErrEntryTooLarge
	}
	if _, err := s.file.WriteAt(buf, s.size); err != nil {
		return s.failWriteLocked(err)
	}
	if err := s.file.Sync(); err != nil {
		return s.failWriteLocked(err)
	}
	if err := s.apply(rec); err != nil {
		// The record was validated by the caller; a failure here is a logic
		// error. The record is already durable, so stop accepting writes.
		s.denied = &DeniedError{Reason: ReasonCorrupt, Detail: err.Error()}
		return s.denied
	}
	s.size += int64(len(buf))
	return nil
}

func (s *Store) failWriteLocked(cause error) error {
	// Remove a possibly partial record so the durable prefix stays exact. If
	// even that fails the file can no longer be trusted for appends.
	if truncErr := s.file.Truncate(s.size); truncErr != nil {
		s.denied = &DeniedError{Reason: ReasonWriteFailed, Detail: cause.Error() + "; " + truncErr.Error()}
		return s.denied
	}
	reason := ReasonWriteFailed
	if isNoSpace(cause) {
		reason = ReasonFull
	}
	s.denied = &DeniedError{Reason: reason, Detail: cause.Error()}
	return s.denied
}

// Close releases the writer lock. There is no buffered state: a process that
// dies has the same durable state as one that calls Close.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.file != nil {
		err = s.file.Close()
		s.file = nil
	}
	if s.lock != nil {
		err = errors.Join(err, releaseLock(s.lock))
		s.lock = nil
	}
	if s.denied == nil {
		s.denied = &DeniedError{Reason: ReasonUnavailable, Detail: "journal closed"}
	}
	if s.root != nil {
		err = errors.Join(err, s.root.Close())
		s.root = nil
	}
	return err
}

// Denied returns the reason new writes are refused, or nil.
func (s *Store) Denied() *DeniedError {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.denied
}

// HighestGeneration is the highest credential generation this journal has seen.
func (s *Store) HighestGeneration() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.highGen
}

// NoteGeneration persists a newly observed credential generation. Generations
// only move forward, which is what lets a later stale dispatch be recognized.
func (s *Store) NoteGeneration(generation int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation <= s.highGen {
		return nil
	}
	return s.appendLocked(record{Kind: "gen", Generation: generation})
}

func validIntent(in Intent) error {
	if strings.TrimSpace(in.CommandID) == "" || strings.TrimSpace(in.PayloadDigest) == "" ||
		strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.WorkerID) == "" ||
		strings.TrimSpace(in.Operation) == "" || strings.TrimSpace(in.ResourceKey) == "" {
		return errors.New("intent requires command, tenant, worker, operation, resource and payload digest")
	}
	return nil
}

// Admit persists the intent of a command before any effect. A command that is
// already known returns its existing entry and existing=true without writing:
// the caller must continue from that state, never execute again.
func (s *Store) Admit(in Intent) (entry Entry, existing bool, err error) {
	if err := validIntent(in); err != nil {
		return Entry{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tomb, ok := s.tombs[in.CommandID]; ok {
		if tomb.Digest != in.PayloadDigest {
			return Entry{}, false, ErrDigestConflict
		}
		return Entry{}, false, ErrTombstoned
	}
	if known, ok := s.entries[in.CommandID]; ok {
		if known.Intent.PayloadDigest != in.PayloadDigest {
			return Entry{}, false, ErrDigestConflict
		}
		return known.snapshot(), true, nil
	}
	if s.denied != nil {
		return Entry{}, false, s.denied
	}
	if int64(len(in.Hint)) > s.opts.MaxIntentBytes {
		return Entry{}, false, ErrEntryTooLarge
	}
	if err := s.reserveLocked(); err != nil {
		return Entry{}, false, err
	}
	if err := s.appendLocked(record{Kind: "intent", Intent: &in}); err != nil {
		return Entry{}, false, err
	}
	return s.entries[in.CommandID].snapshot(), false, nil
}

// reserveLocked guarantees space for this command's result, and for the
// results of every admitted command that has none yet, before it is admitted.
// It never drops unacknowledged or uncertain work to make room.
func (s *Store) reserveLocked() error {
	reserve := func() int64 {
		pending := int64(0)
		for _, e := range s.entries {
			if len(e.Results) == 0 {
				pending++
			}
		}
		return (pending + 1) * s.opts.MaxResultBytes
	}
	if s.size+reserve() > s.opts.MaxBytes {
		if err := s.compactLocked(); err != nil {
			return err
		}
		if s.size+reserve() > s.opts.MaxBytes {
			return &DeniedError{Reason: ReasonFull, Detail: fmt.Sprintf("journal %d of %d bytes with unacknowledged work", s.size, s.opts.MaxBytes)}
		}
	}
	var available uint64
	var err error
	if s.opts.FreeBytes != nil {
		available, err = s.opts.FreeBytes(s.dir)
	} else {
		available, err = freeBytes(s.root)
	}
	if err == nil && available < uint64(reserve()+s.opts.MaxResultBytes/4) { // #nosec G115 -- a positive, bounded byte count.
		return &DeniedError{Reason: ReasonFull, Detail: fmt.Sprintf("%d bytes free on the journal volume", available)}
	}
	return nil
}

// MarkDispatched writes the marker that must be durable before the executor is
// invoked. From here until a result exists the outcome is uncertain.
func (s *Store) MarkDispatched(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return ErrNotFound
	}
	if e.State != StateIntent {
		return fmt.Errorf("%w: dispatch from %s", ErrInvalidTransition, e.State)
	}
	return s.appendLocked(record{Kind: "dispatched", ID: id})
}

// RecordExecutor stores the executor process group after it started. It is
// best effort for exclusion on restart; the dispatched marker already makes the
// command uncertain.
func (s *Store) RecordExecutor(id string, ref ExecutorRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return ErrNotFound
	}
	if e.State != StateDispatched {
		return fmt.Errorf("%w: executor record from %s", ErrInvalidTransition, e.State)
	}
	return s.appendLocked(record{Kind: "executor", ID: id, Executor: &ref})
}

// RecordResult makes one immutable outcome revision durable before it is
// delivered.
func (s *Store) RecordResult(id string, result ResultRecord) error {
	if result.State != ReportTerminal && result.State != ReportOutcomeUnknown && result.State != ReportRejected && result.State != ReportResolved {
		return fmt.Errorf("%w: unknown report state %q", ErrInvalidTransition, result.State)
	}
	if strings.TrimSpace(result.Digest) == "" {
		return errors.New("result requires a digest")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if int64(len(result.Payload)) > s.opts.MaxResultBytes {
		return ErrEntryTooLarge
	}
	e, ok := s.entries[id]
	if !ok {
		return ErrNotFound
	}
	if result.Revision != int64(len(e.Results))+1 {
		return fmt.Errorf("%w: revision %d after %d", ErrInvalidTransition, result.Revision, len(e.Results))
	}
	if result.Revision == 1 {
		switch result.State {
		case ReportRejected:
			if e.State != StateIntent {
				return fmt.Errorf("%w: rejection after dispatch", ErrInvalidTransition)
			}
		case ReportResolved:
			return fmt.Errorf("%w: a first revision cannot be a resolution", ErrInvalidTransition)
		default:
			if e.State != StateDispatched {
				return fmt.Errorf("%w: result before dispatch", ErrInvalidTransition)
			}
		}
	} else {
		prior := e.Results[len(e.Results)-1]
		if !prior.Open() || result.State == ReportRejected || result.PriorDigest != prior.Digest {
			return fmt.Errorf("%w: a resolution must supersede an outcome_unknown revision", ErrInvalidTransition)
		}
	}
	return s.appendLocked(record{Kind: "result", ID: id, Result: &result})
}

// Ack records that the control plane committed the revision. Acknowledging an
// older revision again is a no-op. rejected marks a permanent refusal of the
// delivery; the evidence is retained.
func (s *Store) Ack(id string, revision int64, rejected bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return ErrNotFound
	}
	if revision < 1 || revision > int64(len(e.Results)) {
		return fmt.Errorf("%w: ack of revision %d", ErrInvalidTransition, revision)
	}
	if revision <= e.AckedRevision && !rejected {
		return nil
	}
	if err := s.appendLocked(record{Kind: "ack", ID: id, Revision: revision, Rejected: rejected}); err != nil {
		return err
	}
	if s.size > s.opts.CompactAfterBytes {
		// Compaction failure must not turn a durable ack into an error.
		_ = s.compactLocked()
	}
	return nil
}

// MaxResultBytes is the bound of one persisted result payload.
func (s *Store) MaxResultBytes() int64 { return s.opts.MaxResultBytes }

// Entry returns one command's snapshot.
func (s *Store) Entry(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return Entry{}, false
	}
	return e.snapshot(), true
}

// Entries returns snapshots, oldest first, of the commands for which keep
// returns true (all when keep is nil).
func (s *Store) Entries(keep func(Entry) bool) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		snap := e.snapshot()
		if keep == nil || keep(snap) {
			out = append(out, snap)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IntentAt.Equal(out[j].IntentAt) {
			return out[i].Intent.CommandID < out[j].Intent.CommandID
		}
		return out[i].IntentAt.Before(out[j].IntentAt)
	})
	return out
}

// Fenced returns the mutating commands on resourceKey whose effect is not
// settled: dispatched or of unknown outcome. A command that only has an intent
// has started nothing. A new mutating command on the same resource must wait.
func (s *Store) Fenced(resourceKey, exceptID string) []Entry {
	return s.Entries(func(e Entry) bool {
		if e.Intent.CommandID == exceptID || !e.Intent.Mutating || e.Intent.ResourceKey != resourceKey {
			return false
		}
		return e.State == StateDispatched || e.State == StateOutcomeUnknown
	})
}

// Health summarizes the journal for heartbeat and diagnostics.
func (s *Store) Health() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := Health{State: "ok", SchemaVersion: s.schema, Bytes: s.size, BudgetBytes: s.opts.MaxBytes, Entries: len(s.entries), TornTailRepaired: s.tornRepaired}
	if s.denied != nil {
		h.State, h.Reason, h.Detail = "denied", s.denied.Reason, s.denied.Detail
	}
	for _, e := range s.entries {
		switch e.State {
		case StateIntent, StateDispatched:
			h.InFlight++
		case StateResultPending:
			h.UnackedResults++
		case StateOutcomeUnknown:
			h.UnresolvedUnknown++
			if e.AckedRevision < int64(len(e.Results)) {
				h.UnackedResults++
			}
		}
	}
	return h
}

// Decommission ends the journal of a server whose identity the control plane
// revoked. Acknowledged entries and every deduplication tombstone are removed:
// the control plane holds the receipts, and nothing will be admitted again.
// Evidence that was never acknowledged is the only thing kept, because it
// cannot be re-created; it stays until it is acknowledged or handed off (the
// operator exports or discards the directory). When nothing is left the log is
// destroyed. It returns how many unacknowledged entries remain.
func (s *Store) Decommission() (kept int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.denied != nil {
		// A journal that could not be read or written keeps its evidence untouched.
		return len(s.entries), s.denied
	}
	for id, e := range s.entries {
		if e.State == StateAcked {
			delete(s.entries, id)
		}
	}
	s.tombs = map[string]tombstone{}
	if len(s.entries) > 0 {
		if s.file == nil {
			return len(s.entries), errors.New("journal is closed")
		}
		return len(s.entries), s.compactLocked()
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	path := logName
	if info, statErr := s.root.Stat(path); statErr == nil && info.Mode().IsRegular() && info.Size() <= 256<<20 {
		if handle, openErr := privatefs.OpenFile(s.root, path, os.O_WRONLY, 0); openErr == nil {
			_, _ = handle.Write(make([]byte, info.Size()))
			_ = handle.Sync()
			_ = handle.Close()
		}
	}
	if removeErr := s.root.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return 0, removeErr
	}
	s.size = 0
	return 0, nil
}

// Compact drops acknowledged entries into deduplication tombstones, drops
// tombstones whose horizon passed, and rewrites the log atomically.
func (s *Store) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compactLocked()
}

func (s *Store) horizon(e *entryState) time.Time {
	byAuthority := e.Intent.AuthorityExpiresAt.Add(s.opts.ClockMargin)
	byAck := e.ackedAt.Add(s.opts.MinTombstone)
	if byAck.After(byAuthority) {
		return byAck
	}
	return byAuthority
}

func (s *Store) compactLocked() error {
	if s.denied != nil {
		return s.denied
	}
	now := s.opts.Now()
	var records []record
	if s.highGen > 0 {
		records = append(records, record{Kind: "gen", At: now, Generation: s.highGen})
	}
	tombs := map[string]tombstone{}
	for id, t := range s.tombs {
		if now.Before(t.ExpiresAt) {
			tombs[id] = t
		}
	}
	ids := make([]string, 0, len(s.entries))
	for id := range s.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := s.entries[id]
		if e.State == StateAcked {
			if horizon := s.horizon(e); now.Before(horizon) {
				tombs[id] = tombstone{Digest: e.Intent.PayloadDigest, ExpiresAt: horizon}
			}
			continue
		}
		intent := e.Intent
		records = append(records, record{Kind: "intent", At: e.IntentAt, Intent: &intent})
		if !e.DispatchedAt.IsZero() {
			records = append(records, record{Kind: "dispatched", ID: id, At: e.DispatchedAt})
		}
		if e.Executor != nil {
			ref := *e.Executor
			records = append(records, record{Kind: "executor", ID: id, At: e.DispatchedAt, Executor: &ref})
		}
		for _, result := range e.Results {
			result := result
			if result.Revision <= e.AckedRevision {
				result.Payload = nil
			}
			records = append(records, record{Kind: "result", ID: id, At: result.At, Result: &result})
		}
		if e.AckedRevision > 0 {
			records = append(records, record{Kind: "ack", ID: id, At: e.ackedAt, Revision: e.AckedRevision, Rejected: e.Rejected})
		}
	}
	tombIDs := make([]string, 0, len(tombs))
	for id := range tombs {
		tombIDs = append(tombIDs, id)
	}
	sort.Strings(tombIDs)
	for _, id := range tombIDs {
		t := tombs[id]
		records = append(records, record{Kind: "tombstone", ID: id, At: now, Tombstone: &t})
	}

	tmp, tmpPath, err := privatefs.CreateTemp(s.root, logName+".tmp-", 0o600)
	if err != nil {
		return err
	}
	cleanup := func() { _ = tmp.Close(); _ = s.root.Remove(tmpPath) }
	header := make([]byte, headerSize)
	copy(header, magic)
	binary.LittleEndian.PutUint32(header[len(magic):], SchemaVersion)
	offset := int64(0)
	write := func(b []byte) error {
		_, err := tmp.WriteAt(b, offset)
		offset += int64(len(b))
		return err
	}
	if err := write(header); err != nil {
		cleanup()
		return err
	}
	for _, rec := range records {
		buf, err := encodeRecord(rec)
		if err != nil {
			cleanup()
			return err
		}
		if err := write(buf); err != nil {
			cleanup()
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := s.root.Rename(tmpPath, logName); err != nil {
		cleanup()
		return err
	}
	if err := syncDir(s.root); err != nil {
		// The rename happened; the new file is complete either way.
		_ = tmp.Close()
		return err
	}
	old := s.file
	s.file = tmp
	s.size = offset
	if old != nil {
		_ = old.Close()
	}
	// Rebuild derived state from what was written so memory equals disk.
	s.entries, s.tombs = map[string]*entryState{}, map[string]tombstone{}
	highGen := s.highGen
	s.highGen = 0
	for _, rec := range records {
		if err := s.apply(rec); err != nil {
			s.denied = &DeniedError{Reason: ReasonCorrupt, Detail: "compaction produced an inconsistent journal: " + err.Error()}
			return s.denied
		}
	}
	if highGen > s.highGen {
		s.highGen = highGen
	}
	return nil
}
