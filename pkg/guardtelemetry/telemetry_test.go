package guardtelemetry

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sample(name string, value float64, pairs ...string) Sample {
	return labelled(name, value, OriginGuard, pairs...)
}

// Only what the telemetry contract lists is emitted or stored: a metric or a
// label key that is not listed, an unbounded or malformed label value, a value
// that is not a number and a caller-supplied identity label are all dropped, on
// the Guard and again on the control plane. What the real host sampler produces
// is entirely on the contract.
func TestTelemetryEmitsOnlyContractFields(t *testing.T) {
	good := []Sample{
		sample("node_cpu_usage_percent", 12.5, labelCore, "total"),
		sample("node_disk_used_bytes", 1, labelMount, "/var/lib/docker"),
		sample("container_running", 1, labelContainerName, "traefik"),
		sample("node_uptime_seconds", 99),
	}
	bad := []Sample{
		sample("node_network_receive_bytes_total", 1),
		sample("node_disk_read_bytes_total", 1),
		sample("node_info", 1),
		sample("node_cpu_usage_percent", 1, "hostname", "box"),
		sample("node_cpu_usage_percent", 1, "tenant_id", "someone-else"),
		sample("node_disk_used_bytes", 1, labelMount, "relative"),
		sample("node_disk_used_bytes", 1, labelMount, "/"+strings.Repeat("x", 200)),
		sample("container_running", 1, labelContainerName, "bad name; drop"),
		sample("node_load_1", math.NaN()),
		sample("node_load_5", math.Inf(1)),
		{Name: "node_load_15", Value: 1}, // no origin: nothing stamped it
		sample("node_cpu_usage_percent", 1, labelOrigin, "attacker"),
	}
	bad[len(bad)-1].Labels = map[string]string{labelOrigin: "attacker"}
	kept, dropped := Filter(append(append([]Sample{}, good...), bad...))
	if len(kept) != len(good) || dropped != len(bad) {
		t.Fatalf("kept %d dropped %d of %d good and %d bad", len(kept), dropped, len(good), len(bad))
	}

	real, err := (&HostSampler{}).Sample(context.Background())
	if err != nil {
		t.Skipf("the host cannot be read here: %v", err)
	}
	if kept, dropped := Filter(real); dropped != 0 || len(kept) != len(real) {
		t.Fatalf("the host sampler emitted %d off-contract samples", dropped)
	}

	// The control plane repeats the filter, and identity comes from the
	// authenticated worker, never from a label the Guard sent.
	sink := &memorySink{}
	ingestor := &Ingestor{Sink: sink, Cursors: NewMemoryCursors()}
	var encodable []Sample // NaN and Inf cannot even be encoded as JSON
	for _, s := range bad {
		if !math.IsNaN(s.Value) && !math.IsInf(s.Value, 0) {
			encodable = append(encodable, s)
		}
	}
	batch := Batch{SchemaVersion: BatchSchema, SourceEpoch: "e", Records: []Record{{Sequence: 1, At: time.Now().UTC(), Samples: append(append([]Sample{}, good...), encodable...)}}}
	body, _ := json.Marshal(batch)
	ack, err := ingestor.Ingest(context.Background(), Identity{TenantID: "tenant-1", WorkerID: "worker-1", ServerID: "server-1"}, body)
	if err != nil || ack.Accepted != len(good) || ack.Dropped != len(encodable) || ack.AckedSequence != 1 {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	for _, stored := range sink.all() {
		if stored.Labels["tenant_id"] != "tenant-1" || stored.Labels["source"] != SourceLabel {
			t.Fatalf("a stored series carries %v", stored.Labels)
		}
	}
}

type memorySink struct {
	mu      sync.Mutex
	samples []SinkSample
	fail    atomic.Bool
}

func (s *memorySink) WriteSamples(samples []SinkSample) error {
	if s.fail.Load() {
		return io.ErrUnexpectedEOF
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, samples...)
	return nil
}

func (s *memorySink) all() []SinkSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SinkSample(nil), s.samples...)
}

func bigSamples(n int) []Sample {
	out := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, sample("container_cpu_usage_percent", float64(i), labelContainerName, "c"+strings.Repeat("x", 40)))
	}
	return out
}

// A spool never loses unsent data without saying so: whatever the disk budget
// forces out is covered by a gap marker naming the sequences and the time, the
// sequence numbers keep counting across a restart, and a torn write is cut off
// without losing the records before it.
func TestSpoolNeverDropsUnsentDataWithoutAGapMarker(t *testing.T) {
	dir := t.TempDir()
	cfg := SpoolConfig{Dir: dir, MaxBytes: 40 << 10, SegmentBytes: 8 << 10}
	spool, err := OpenSpool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Hour)
	const total = 120
	var appended []int64
	for i := 0; i < total; i++ {
		record, err := spool.Append(start.Add(time.Duration(i)*30*time.Second), bigSamples(12))
		if err != nil {
			t.Fatal(err)
		}
		appended = append(appended, record.Sequence)
	}
	if _, bytes := spool.Backlog(); bytes > cfg.MaxBytes+cfg.SegmentBytes {
		t.Fatalf("the spool holds %d bytes beyond its %d budget", bytes, cfg.MaxBytes)
	}
	covered := func(spool *Spool) map[int64]bool {
		seen := map[int64]bool{}
		records, err := spool.Peek(10_000, 1<<30)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.Gap != nil {
				for seq := record.Gap.FromSequence; seq <= record.Gap.ToSequence; seq++ {
					seen[seq] = true
				}
				if record.Gap.Reason != GapSpoolBudget || record.Gap.To.Before(record.Gap.From) {
					t.Fatalf("gap marker = %+v", record.Gap)
				}
			}
			seen[record.Sequence] = true
		}
		return seen
	}
	seen := covered(spool)
	for _, seq := range appended {
		if !seen[seq] {
			t.Fatalf("sequence %d was dropped without a record or a gap marker", seq)
		}
	}
	if records, _ := spool.Peek(10_000, 1<<30); func() int {
		markers := 0
		for _, record := range records {
			if record.Gap != nil {
				markers++
			}
		}
		return markers
	}() != 1 {
		t.Fatalf("one contiguous loss must be one gap marker")
	}

	// A crash leaves a torn line; the records before it survive and the
	// sequence continues in the same epoch.
	epoch := spool.Epoch()
	last := spool.LastSequence()
	segments, _ := filesMatching(dir, segmentPrefix)
	if err := appendTorn(segments[len(segments)-1]); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSpool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Epoch() != epoch || reopened.LastSequence() != last {
		t.Fatalf("after restart: epoch %q (was %q), last %d (was %d)", reopened.Epoch(), epoch, reopened.LastSequence(), last)
	}
	next, err := reopened.Append(time.Now(), bigSamples(1))
	if err != nil || next.Sequence != last+1 {
		t.Fatalf("next record = %+v, %v", next, err)
	}
	if err := reopened.Ack(next.Sequence); err != nil {
		t.Fatal(err)
	}
	if records, bytes := reopened.Backlog(); records != 0 || bytes != 0 {
		t.Fatalf("an acknowledged spool still holds %d records, %d bytes", records, bytes)
	}
	again, err := reopened.Append(time.Now(), bigSamples(1))
	if err != nil || again.Sequence != next.Sequence+1 {
		t.Fatalf("a sequence was reused after the spool emptied: %+v, %v", again, err)
	}
}

// A 10 minute outage yields backfilled data or explicit gaps, never silent
// loss: records wait through failures, arrive in order when the control plane
// answers again, a lost acknowledgement replays without storing anything twice,
// and a batch the control plane refuses for good becomes a gap marker instead of
// blocking everything behind it.
func TestOutageBackfillIsOrderedIdempotentAndAcknowledgedOnlyWhenStored(t *testing.T) {
	sink := &memorySink{}
	cursors := NewMemoryCursors()
	ingestor := &Ingestor{Sink: sink, Cursors: cursors}
	var down atomic.Bool
	var reject atomic.Bool
	var loseAck atomic.Bool
	var lostOnce atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if reject.Load() {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		ack, err := ingestor.Ingest(r.Context(), Identity{TenantID: "t", WorkerID: "w", ServerID: "s"}, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if loseAck.Load() && lostOnce.CompareAndSwap(false, true) {
			// The batch is stored, but the answer never reaches the Guard.
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"telemetry": ack}})
	}))
	defer server.Close()

	spool, err := OpenSpool(SpoolConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := NewSender(SenderConfig{URL: server.URL, HTTPClient: server.Client(), Spool: spool})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-10 * time.Minute)
	cycle := func(i int) {
		if _, err := spool.Append(at.Add(time.Duration(i)*30*time.Second), []Sample{sample("node_load_1", float64(i))}); err != nil {
			t.Fatal(err)
		}
	}
	down.Store(true)
	for i := 0; i < 20; i++ {
		cycle(i)
		if _, err := sender.Flush(context.Background()); err == nil {
			t.Fatal("a flush to a control plane that is down reported success")
		}
	}
	if records, _ := spool.Backlog(); records != 20 {
		t.Fatalf("the spool holds %d of 20 records", records)
	}

	down.Store(false)
	loseAck.Store(true)
	if _, err := sender.Flush(context.Background()); err == nil {
		t.Fatal("a flush whose answer was lost reported success")
	}
	if records, _ := spool.Backlog(); records != 20 {
		t.Fatalf("a lost acknowledgement removed records: %d left", records)
	}
	result, err := sender.Flush(context.Background())
	if err != nil || result.Sent != 20 {
		t.Fatalf("replay = %+v, %v", result, err)
	}
	stored := sink.all()
	if len(stored) != 20 {
		t.Fatalf("%d samples were stored for 20 cycles: the replay stored something twice or lost it", len(stored))
	}
	for i, s := range stored {
		if s.Value != float64(i) || !s.Timestamp.Equal(at.Add(time.Duration(i)*30*time.Second)) {
			t.Fatalf("sample %d = %+v, not in order or not at its own time", i, s)
		}
	}
	if records, _ := spool.Backlog(); records != 0 {
		t.Fatalf("the spool still holds %d records", records)
	}

	// A refused batch is recorded as a gap, and what follows is still delivered.
	cycle(20)
	reject.Store(true)
	if _, err := sender.Flush(context.Background()); err == nil {
		t.Fatal("a refused batch reported success")
	}
	reject.Store(false)
	cycle(21)
	if _, err := sender.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	var gaps, samples int
	for _, s := range sink.all() {
		if s.Name == GapMetricName {
			gaps++
			if s.Labels["reason"] != GapRejected {
				t.Fatalf("gap labels = %v", s.Labels)
			}
		} else {
			samples++
		}
	}
	if gaps != 1 || samples != 21 {
		t.Fatalf("gaps %d samples %d: the refused cycle must be a gap, the next one delivered", gaps, samples)
	}
}

func filesMatching(dir, prefix string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), segmentSuffix) {
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func appendTorn(path string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = file.WriteString(`{"sequence":99999,"at":"2026-`)
	return err
}
