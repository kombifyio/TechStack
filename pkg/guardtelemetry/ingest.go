package guardtelemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/db/tenantcontext"
	"github.com/kombifyio/techstack/pkg/drivehealth"
)

// SourceLabel marks the series a Guard's telemetry is stored in. It is distinct
// from the heartbeat's series, so a backfilled record never collides with the
// newer heartbeat samples of the same metric.
const SourceLabel = "guard-telemetry"

// SinkSample is one stored data point.
type SinkSample struct {
	Name      string
	Value     float64
	Labels    map[string]string
	Timestamp time.Time
}

// Sink stores samples. ErrSampleRejected from it means the store will never
// accept them (a replay older than the series head); the record then counts as
// stored, since it already is or can no longer be.
type Sink interface {
	WriteSamples(samples []SinkSample) error
}

// DriveSink persists drive lifecycle and alerts under the authenticated worker.
// A drive record is acknowledged only after this durable write succeeds.
type DriveSink interface {
	Ingest(ctx context.Context, identity Identity, epoch string, sequence int64, collection drivehealth.Collection) error
}

// ErrPartialBatch accompanies an Ack when the store failed part way through a
// batch: the records up to the Ack are stored, the rest must be sent again.
var ErrPartialBatch = errors.New("guardtelemetry: the batch was stored only in part")

// ErrSampleRejected marks samples the store refuses for good.
var ErrSampleRejected = errors.New("guardtelemetry: the store refused the samples for good")

// Cursor is the highest sequence accepted for one epoch of one Guard.
type Cursor struct {
	Epoch        string
	LastSequence int64
}

// CursorStore keeps the cursors, tenant scoped.
type CursorStore interface {
	Get(ctx context.Context, tenantID, workerID string) (Cursor, error)
	Advance(ctx context.Context, tenantID, workerID string, cursor Cursor, at time.Time) error
}

// Identity is who a batch comes from, taken from the authenticated worker and
// never from the batch.
type Identity struct {
	TenantID string
	WorkerID string
	ServerID string
	Hostname string
	Provider string
}

// Ingestor reads a Guard's batches into the store.
type Ingestor struct {
	Sink    Sink
	Drives  DriveSink
	Cursors CursorStore
	Now     func() time.Time
}

// DriveHealthSupported advertises the additive envelope only when a durable
// drive sink is configured; older peers can continue resource-only telemetry.
func (i *Ingestor) DriveHealthSupported() bool {
	return i != nil && i.Drives != nil
}

func (i *Ingestor) now() time.Time {
	if i.Now != nil {
		return i.Now().UTC()
	}
	return time.Now().UTC()
}

// Ingest stores one batch and answers which sequences are now safe. Only what
// was stored (or was a replay of what is stored) is acknowledged, so the Guard
// keeps everything else. Samples off the contract are dropped and counted; a
// batch that is not a batch is refused.
func (i *Ingestor) Ingest(ctx context.Context, identity Identity, body []byte) (Ack, error) {
	if len(body) == 0 || len(body) > MaxBatchBytes {
		return Ack{}, invalid("the body is empty or exceeds %d bytes", MaxBatchBytes)
	}
	var batch Batch
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&batch); err != nil {
		return Ack{}, invalid("it does not decode: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Ack{}, invalid("it holds more than one document")
	}
	now := i.now()
	if err := batch.Validate(now); err != nil {
		return Ack{}, err
	}
	cursor, err := i.Cursors.Get(ctx, identity.TenantID, identity.WorkerID)
	if err != nil {
		return Ack{}, err
	}
	last := cursor.LastSequence
	if cursor.Epoch != batch.SourceEpoch {
		last = 0
	}
	ack := Ack{AckedSequence: last}
	advance := func() error {
		if ack.AckedSequence == last && cursor.Epoch == batch.SourceEpoch {
			return nil
		}
		return i.Cursors.Advance(ctx, identity.TenantID, identity.WorkerID, Cursor{Epoch: batch.SourceEpoch, LastSequence: ack.AckedSequence}, now)
	}
	for _, record := range batch.Records {
		if record.Sequence <= last {
			continue // a replay of what is already stored
		}
		if record.DriveHealth != nil {
			var driveErr error
			if i.Drives == nil {
				driveErr = errors.New("guardtelemetry: drive observation storage is unavailable")
			} else {
				driveErr = i.Drives.Ingest(ctx, identity, batch.SourceEpoch, record.Sequence, *record.DriveHealth)
			}
			if driveErr != nil {
				if advanceErr := advance(); advanceErr != nil {
					return ack, advanceErr
				}
				if ack.AckedSequence > last {
					return ack, fmt.Errorf("%w: %v", ErrPartialBatch, driveErr)
				}
				return Ack{}, driveErr
			}
		}
		stored, dropped := i.sinkSamples(identity, record)
		ack.Dropped += dropped
		if len(stored) > 0 {
			if err := i.Sink.WriteSamples(stored); err != nil && !errors.Is(err, ErrSampleRejected) {
				if advanceErr := advance(); advanceErr != nil {
					return ack, advanceErr
				}
				if ack.AckedSequence > last {
					return ack, fmt.Errorf("%w: %v", ErrPartialBatch, err)
				}
				return Ack{}, err
			}
			ack.Accepted += len(stored)
		}
		ack.AckedSequence = record.Sequence
	}
	return ack, advance()
}

func commonLabels(identity Identity) map[string]string {
	labels := map[string]string{
		"agent_id": identity.WorkerID, "worker_id": identity.WorkerID, "source": SourceLabel,
		"tenant_id": identity.TenantID, "server_id": identity.ServerID, "node_id": identity.ServerID,
	}
	if identity.Hostname != "" {
		labels["hostname"] = identity.Hostname
	}
	if identity.Provider != "" {
		labels["provider"] = identity.Provider
	}
	return labels
}

// sinkSamples turns one record into stored samples, re-filtering against the
// contract: the control plane never relies on the Guard having done it.
func (i *Ingestor) sinkSamples(identity Identity, record Record) ([]SinkSample, int) {
	if record.Gap != nil {
		labels := commonLabels(identity)
		delete(labels, "hostname")
		delete(labels, "provider")
		labels["reason"] = record.Gap.Reason
		return []SinkSample{{Name: GapMetricName, Value: record.Gap.To.Sub(record.Gap.From).Seconds(), Labels: labels, Timestamp: record.Gap.To.UTC()}}, 0
	}
	kept, dropped := Filter(record.Samples)
	out := make([]SinkSample, 0, len(kept))
	base := commonLabels(identity)
	for _, sample := range kept {
		labels := make(map[string]string, len(base)+len(sample.Labels))
		for key, value := range base {
			labels[key] = value
		}
		for key, value := range sample.Labels {
			labels[key] = value
		}
		out = append(out, SinkSample{Name: sample.Name, Value: sample.Value, Labels: labels, Timestamp: record.At.UTC()})
	}
	return out, dropped
}

// MemoryCursors is the in-memory CursorStore.
type MemoryCursors struct {
	mu      sync.Mutex
	cursors map[string]Cursor
}

// NewMemoryCursors returns an empty store.
func NewMemoryCursors() *MemoryCursors { return &MemoryCursors{cursors: map[string]Cursor{}} }

func (m *MemoryCursors) Get(_ context.Context, tenantID, workerID string) (Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursors[tenantID+"\x00"+workerID], nil
}

func (m *MemoryCursors) Advance(_ context.Context, tenantID, workerID string, cursor Cursor, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantID + "\x00" + workerID
	if existing := m.cursors[key]; existing.Epoch == cursor.Epoch && existing.LastSequence > cursor.LastSequence {
		return nil
	}
	m.cursors[key] = cursor
	return nil
}

// PostgresCursors is the CursorStore over the control-plane database.
type PostgresCursors struct{ db *sql.DB }

// NewPostgresCursors wraps a control-plane database handle.
func NewPostgresCursors(db *sql.DB) (*PostgresCursors, error) {
	if db == nil {
		return nil, errors.New("guardtelemetry: database handle is required")
	}
	return &PostgresCursors{db: db}, nil
}

func (p *PostgresCursors) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tenantcontext.ApplyTenant(ctx, tx, tenantID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *PostgresCursors) Get(ctx context.Context, tenantID, workerID string) (Cursor, error) {
	tenantID, workerID = strings.TrimSpace(tenantID), strings.TrimSpace(workerID)
	var cursor Cursor
	err := p.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT source_epoch, last_sequence FROM guard_telemetry_cursors WHERE tenant_id = $1 AND worker_id = $2`,
			tenantID, workerID).Scan(&cursor.Epoch, &cursor.LastSequence)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return cursor, err
}

func (p *PostgresCursors) Advance(ctx context.Context, tenantID, workerID string, cursor Cursor, at time.Time) error {
	tenantID, workerID = strings.TrimSpace(tenantID), strings.TrimSpace(workerID)
	return p.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO guard_telemetry_cursors (tenant_id, worker_id, source_epoch, last_sequence, last_received_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, worker_id) DO UPDATE SET
				last_sequence = CASE WHEN guard_telemetry_cursors.source_epoch = EXCLUDED.source_epoch
					THEN GREATEST(guard_telemetry_cursors.last_sequence, EXCLUDED.last_sequence) ELSE EXCLUDED.last_sequence END,
				source_epoch = EXCLUDED.source_epoch, last_received_at = EXCLUDED.last_received_at
		`, tenantID, workerID, cursor.Epoch, cursor.LastSequence, at)
		return err
	})
}
