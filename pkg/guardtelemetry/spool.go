package guardtelemetry

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/kombifyio/techstack/pkg/drivehealth"
)

// Spool defaults.
const (
	DefaultSpoolBytes   = 8 << 20
	defaultSegmentBytes = 64 << 10
	segmentPrefix       = "seg-"
	segmentSuffix       = ".jsonl"
	stateFile           = "state.json"
	maxRecordLine       = 4 << 20
)

// SpoolConfig configures a Spool.
type SpoolConfig struct {
	// Dir is the spool directory, inside the Guard's protected state directory.
	Dir string
	// MaxBytes is the disk budget of everything not yet acknowledged.
	MaxBytes     int64
	SegmentBytes int64
}

type segment struct {
	path            string
	first, last     int64
	firstAt, lastAt time.Time
	size            int64
}

// Spool is the bounded, crash-safe, append-only file the Guard keeps telemetry
// in until the control plane acknowledges it. Sequence numbers never repeat
// within an epoch, and data that has to be dropped to stay within the budget is
// replaced by a gap marker that says which sequences and which time were lost.
type Spool struct {
	mu      sync.Mutex
	cfg     SpoolConfig
	root    *os.Root
	epoch   string
	lastSeq int64
	segs    []segment
	total   int64
}

type spoolState struct {
	Epoch   string `json:"epoch"`
	LastSeq int64  `json:"last_seq"`
}

// OpenSpool opens or creates the spool and recovers what a crash left.
func OpenSpool(cfg SpoolConfig) (*Spool, error) {
	if strings.TrimSpace(cfg.Dir) == "" {
		return nil, errors.New("guardtelemetry: a spool directory is required")
	}
	root, err := privatefs.Open(cfg.Dir)
	if err != nil {
		return nil, err
	}
	return openSpool(root, cfg)
}

// OpenSpoolRoot creates or reopens the telemetry child of admitted node custody.
func OpenSpoolRoot(custody *os.Root, name string, cfg SpoolConfig) (*Spool, error) {
	root, err := privatefs.OpenChild(custody, name)
	if err != nil {
		return nil, err
	}
	cfg.Dir = root.Name()
	return openSpool(root, cfg)
}

func openSpool(root *os.Root, cfg SpoolConfig) (*Spool, error) {
	if cfg.SegmentBytes <= 0 {
		cfg.SegmentBytes = defaultSegmentBytes
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultSpoolBytes
	}
	if cfg.MaxBytes < 4*cfg.SegmentBytes {
		cfg.MaxBytes = 4 * cfg.SegmentBytes
	}

	s := &Spool{cfg: cfg, root: root}
	admitted := false
	defer func() {
		if !admitted {
			_ = root.Close()
		}
	}()
	var state spoolState
	if raw, err := privatefs.ReadFile(s.root, stateFile); err == nil {
		_ = json.Unmarshal(raw, &state)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if state.Epoch == "" || len(state.Epoch) > 64 {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			return nil, err
		}
		state = spoolState{Epoch: hex.EncodeToString(random)}
	}
	s.epoch, s.lastSeq = state.Epoch, state.LastSeq
	entries, err := fs.ReadDir(s.root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), segmentPrefix) && strings.HasSuffix(entry.Name(), segmentSuffix) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		seg, records, err := s.scan(name)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			_ = s.root.Remove(seg.path)
			continue
		}
		s.segs = append(s.segs, seg)
		s.total += seg.size
		if seg.last > s.lastSeq {
			s.lastSeq = seg.last
		}
	}
	if err := s.writeState(); err != nil {
		return nil, err
	}
	admitted = true
	return s, nil
}

// Epoch names this spool's sequence space; it never changes for a spool directory.
func (s *Spool) Epoch() string { return s.epoch }

// scan reads one segment, keeps its valid records and truncates a torn tail.
func (s *Spool) scan(path string) (segment, []Record, error) {
	file, err := privatefs.OpenFile(s.root, path, os.O_RDWR, 0)
	if err != nil {
		return segment{}, nil, err
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	var records []Record
	var good int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' && len(line) <= maxRecordLine {
			var record Record
			if json.Unmarshal(line, &record) == nil && record.Sequence > 0 {
				records = append(records, record)
				good += int64(len(line))
			} else {
				break
			}
		}
		if readErr != nil {
			break
		}
	}
	if info, statErr := file.Stat(); statErr == nil && info.Size() != good {
		if err := file.Truncate(good); err != nil {
			_ = file.Close()
			return segment{}, nil, err
		}
	}
	_ = file.Close()
	seg := segment{path: path, size: good}
	if len(records) > 0 {
		seg.first, seg.last = records[0].Sequence, records[len(records)-1].Sequence
		seg.firstAt, seg.lastAt = records[0].At, records[len(records)-1].At
	}
	return seg, records, nil
}

func (s *Spool) writeState() error {
	raw, _ := json.Marshal(spoolState{Epoch: s.epoch, LastSeq: s.lastSeq})
	return privatefs.Replace(s.root, stateFile, raw, 0o600)
}

// Append stores one collection cycle and returns it with its sequence number.
// The record is durable when Append returns; if the disk budget is exceeded the
// oldest unacknowledged data is dropped and a gap marker takes its place.
func (s *Spool) Append(at time.Time, samples []Sample) (Record, error) {
	return s.AppendDrives(at, samples, nil)
}

// AppendDrives durably stores one collection cycle with optional drive readings.
func (s *Spool) AppendDrives(at time.Time, samples []Sample, drives *drivehealth.Collection) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.appendLocked(Record{At: at.UTC(), Samples: samples, DriveHealth: drives})
	if err != nil {
		return Record{}, err
	}
	return record, s.enforceBudgetLocked()
}

func (s *Spool) appendLocked(record Record) (Record, error) {
	s.lastSeq++
	record.Sequence = s.lastSeq
	line, err := json.Marshal(record)
	if err != nil {
		s.lastSeq--
		return Record{}, err
	}
	line = append(line, '\n')
	if len(line) > maxRecordLine {
		s.lastSeq--
		return Record{}, fmt.Errorf("guardtelemetry: a record of %d bytes exceeds its bound", len(line))
	}
	var active *segment
	if n := len(s.segs); n > 0 && s.segs[n-1].size+int64(len(line)) <= s.cfg.SegmentBytes {
		active = &s.segs[n-1]
	} else {
		s.segs = append(s.segs, segment{path: segmentPrefix + fmt.Sprintf("%020d", record.Sequence) + segmentSuffix, first: record.Sequence, firstAt: record.At})
		active = &s.segs[len(s.segs)-1]
	}
	file, err := privatefs.OpenFile(s.root, active.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		s.lastSeq--
		return Record{}, err
	}
	_, writeErr := file.Write(line)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		s.lastSeq--
		return Record{}, writeErr
	}
	active.last, active.lastAt, active.size = record.Sequence, record.At, active.size+int64(len(line))
	s.total += int64(len(line))
	return record, s.writeState()
}

// enforceBudgetLocked drops the oldest segments until the spool fits, then
// records one gap marker for everything dropped.
func (s *Spool) enforceBudgetLocked() error {
	if s.total <= s.cfg.MaxBytes || len(s.segs) < 2 {
		return nil
	}
	var gap *Gap
	for s.total > s.cfg.MaxBytes && len(s.segs) > 1 {
		seg := s.segs[0]
		_, records, err := s.scan(seg.path)
		if err != nil {
			return err
		}
		for _, record := range records {
			gap = mergeGap(gap, record, GapSpoolBudget)
		}
		if err := s.root.Remove(seg.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s.total -= seg.size
		s.segs = s.segs[1:]
	}
	if gap == nil {
		return nil
	}
	return s.placeGapLocked(gap)
}

// placeGapLocked puts the one gap marker at the head of the spool, under the
// lowest sequence it covers. Those sequence numbers are free once their data is
// gone, so the stream stays in order and there is only ever one marker per
// contiguous loss instead of one per enforcement.
func (s *Spool) placeGapLocked(gap *Gap) error {
	marker := Record{Sequence: gap.FromSequence, At: gap.To.UTC(), Gap: gap}
	line, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if len(s.segs) == 0 {
		path := segmentPrefix + fmt.Sprintf("%020d", marker.Sequence) + segmentSuffix
		if err := privatefs.Replace(s.root, path, line, 0o600); err != nil {
			return err
		}
		s.segs = append(s.segs, segment{path: path, first: marker.Sequence, last: marker.Sequence, firstAt: marker.At, lastAt: marker.At, size: int64(len(line))})
		s.total += int64(len(line))
		return nil
	}
	seg := s.segs[0]
	existing, err := privatefs.ReadFile(s.root, seg.path)
	if err != nil {
		return err
	}
	newPath := segmentPrefix + fmt.Sprintf("%020d", marker.Sequence) + segmentSuffix
	if err := privatefs.Replace(s.root, newPath, append(append([]byte{}, line...), existing...), 0o600); err != nil {
		return err
	}
	if newPath != seg.path {
		_ = s.root.Remove(seg.path)
	}
	s.total += int64(len(line))
	s.segs[0] = segment{path: newPath, first: marker.Sequence, last: seg.last, firstAt: marker.At, lastAt: seg.lastAt, size: seg.size + int64(len(line))}
	return nil
}

// mergeGap widens a gap marker by one dropped record; a dropped gap marker
// contributes the range it already described.
func mergeGap(gap *Gap, record Record, reason string) *Gap {
	from, to, fromSeq, toSeq := record.At, record.At, record.Sequence, record.Sequence
	if record.Gap != nil {
		from, to, fromSeq, toSeq = record.Gap.From, record.Gap.To, record.Gap.FromSequence, record.Gap.ToSequence
	}
	if gap == nil {
		return &Gap{From: from, To: to, FromSequence: fromSeq, ToSequence: toSeq, Reason: reason}
	}
	if from.Before(gap.From) {
		gap.From = from
	}
	if to.After(gap.To) {
		gap.To = to
	}
	gap.FromSequence, gap.ToSequence = min(gap.FromSequence, fromSeq), max(gap.ToSequence, toSeq)
	return gap
}

// Peek returns the oldest unacknowledged records, at most maxRecords and about
// maxBytes of encoded data, oldest first.
func (s *Spool) Peek(maxRecords, maxBytes int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	used := 0
	for _, seg := range s.segs {
		_, records, err := s.scan(seg.path)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			encoded, err := json.Marshal(record)
			if err != nil {
				return nil, err
			}
			size := len(encoded) + 1
			if len(out) >= maxRecords || (len(out) > 0 && used+size > maxBytes) {
				return out, nil
			}
			out = append(out, record)
			used += size
		}
	}
	return out, nil
}

// Ack removes every record up to and including seq.
func (s *Spool) Ack(seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ackLocked(seq)
}

func (s *Spool) ackLocked(seq int64) error {
	for len(s.segs) > 0 && s.segs[0].first <= seq {
		seg := s.segs[0]
		if seg.last <= seq {
			if err := s.root.Remove(seg.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			s.total -= seg.size
			s.segs = s.segs[1:]
			continue
		}
		// A partly acknowledged segment keeps only its newer records.
		_, records, err := s.scan(seg.path)
		if err != nil {
			return err
		}
		var kept []byte
		var first Record
		for _, record := range records {
			if record.Sequence > seq {
				if len(kept) == 0 {
					first = record
				}
				line, _ := json.Marshal(record)
				kept = append(append(kept, line...), '\n')
			}
		}
		newPath := segmentPrefix + fmt.Sprintf("%020d", first.Sequence) + segmentSuffix
		if err := privatefs.Replace(s.root, newPath, kept, 0o600); err != nil {
			return err
		}
		if newPath != seg.path {
			_ = s.root.Remove(seg.path)
		}
		s.total += int64(len(kept)) - seg.size
		s.segs[0] = segment{path: newPath, first: first.Sequence, last: seg.last, firstAt: first.At, lastAt: seg.lastAt, size: int64(len(kept))}
		break
	}
	return nil
}

// Discard drops every record up to seq that the control plane refused, and
// leaves a gap marker for them: refused data is reported, not forgotten.
func (s *Spool) Discard(seq int64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var gap *Gap
	for _, seg := range s.segs {
		if seg.first > seq {
			break
		}
		_, records, err := s.scan(seg.path)
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.Sequence <= seq {
				gap = mergeGap(gap, record, reason)
			}
		}
	}
	if err := s.ackLocked(seq); err != nil {
		return err
	}
	if gap == nil {
		return nil
	}
	return s.placeGapLocked(gap)
}

// Backlog reports how much is waiting to be acknowledged.
func (s *Spool) Backlog() (records int, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, seg := range s.segs {
		records += int(seg.last-seg.first) + 1
	}
	return records, s.total
}

// LastSequence is the highest sequence assigned so far.
func (s *Spool) LastSequence() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeq
}
