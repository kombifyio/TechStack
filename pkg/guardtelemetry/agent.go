package guardtelemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kombifyio/techstack/pkg/drivehealth"
)

const (
	// DriveHealthCapabilityHeader is advertised by an authenticated successful
	// receiver that accepts structured drive observations in the v1 envelope.
	DriveHealthCapabilityHeader = "X-Kombify-Guard-Drive-Health"
	// DefaultInterval is the collection cadence.
	DefaultInterval = 30 * time.Second
	// disabledWait is how long a Guard stays quiet after the control plane said
	// it stores no telemetry.
	disabledWait = time.Hour
	flushBackoff = 5 * time.Minute
	responseMax  = 16 << 10
	// flushBytes keeps a batch well inside MaxBatchBytes once encoded.
	flushBytes = MaxBatchBytes / 4
	// maxPending bounds what the loopback sink may hand over between cycles.
	maxPending = 2 * MaxSamplesPerRecord
)

// SenderConfig configures how spooled records reach the control plane.
type SenderConfig struct {
	URL string
	// HTTPClient carries the Guard's key-bound transport, or plain HTTPS for a
	// Guard still on the bearer credential.
	HTTPClient *http.Client
	AgentToken string
	TenantID   string
	Spool      *Spool
}

// Sender posts spooled records, oldest first, and removes what the control
// plane acknowledged. A record leaves the spool only on an acknowledgement, or
// as a gap marker when the control plane refused it.
type Sender struct {
	cfg                  SenderConfig
	driveHealthSupported atomic.Bool
}

// DriveHealthSupported reports the last successful receiver's positive
// capability advertisement. New senders require a successful exchange first.
func (s *Sender) DriveHealthSupported() bool {
	return s != nil && s.driveHealthSupported.Load()
}

// NewSender validates the configuration.
func NewSender(cfg SenderConfig) (*Sender, error) {
	if strings.TrimSpace(cfg.URL) == "" || cfg.HTTPClient == nil || cfg.Spool == nil {
		return nil, errors.New("guardtelemetry: a sender needs the endpoint, a client and a spool")
	}
	return &Sender{cfg: cfg}, nil
}

// FlushResult is what one flush did.
type FlushResult struct {
	Sent int
	// Disabled means the control plane stores no telemetry; nothing was removed.
	Disabled bool
}

// Flush sends one batch. A nil error with Sent 0 means nothing was waiting.
func (s *Sender) Flush(ctx context.Context) (FlushResult, error) {
	records, err := s.cfg.Spool.Peek(MaxRecordsPerBatch, flushBytes)
	if err != nil || len(records) == 0 {
		return FlushResult{}, err
	}
	last := records[len(records)-1].Sequence
	body, err := json.Marshal(Batch{SchemaVersion: BatchSchema, SourceEpoch: s.cfg.Spool.Epoch(), Records: records})
	if err != nil {
		return FlushResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return FlushResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(s.cfg.AgentToken); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant := strings.TrimSpace(s.cfg.TenantID); tenant != "" {
		request.Header.Set("X-Kombify-Tenant-ID", tenant)
	}
	response, err := s.cfg.HTTPClient.Do(request)
	if err != nil {
		return FlushResult{}, err
	}
	defer func() { _ = response.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, responseMax))
	switch {
	case response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusRequestEntityTooLarge || response.StatusCode == http.StatusUnprocessableEntity:
		if response.Header.Get(DriveHealthCapabilityHeader) != "1" {
			for _, record := range records {
				if record.DriveHealth != nil {
					// A peer rollback must not erase already-collected hardware
					// evidence. Stop new collection and retain the whole record.
					s.driveHealthSupported.Store(false)
					return FlushResult{}, fmt.Errorf("drive telemetry remains queued: the receiver refused the batch without drive health capability (HTTP %d)", response.StatusCode)
				}
			}
		}
		// The control plane will never accept this batch. Keeping it would block
		// every later record, so it becomes an explicit gap.
		if discardErr := s.cfg.Spool.Discard(last, GapRejected); discardErr != nil {
			return FlushResult{}, discardErr
		}
		return FlushResult{}, fmt.Errorf("the control plane refused the batch (HTTP %d); its range is recorded as a gap", response.StatusCode)
	case response.StatusCode < 200 || response.StatusCode > 299:
		return FlushResult{}, fmt.Errorf("control plane answered HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Data struct {
			Telemetry Ack `json:"telemetry"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return FlushResult{}, errors.New("control plane returned an unusable telemetry answer")
	}
	ack := envelope.Data.Telemetry
	if ack.State == StateDisabled {
		s.driveHealthSupported.Store(false)
		for _, record := range records {
			if record.DriveHealth != nil {
				return FlushResult{}, errors.New("drive telemetry remains queued: the receiver disabled telemetry storage")
			}
		}
		return FlushResult{Disabled: true}, nil
	}
	// Never trust an acknowledgement beyond what was sent.
	acked := min(ack.AckedSequence, last)
	if acked <= 0 {
		return FlushResult{}, errors.New("the control plane acknowledged nothing")
	}
	s.driveHealthSupported.Store(response.Header.Get(DriveHealthCapabilityHeader) == "1")
	if err := s.cfg.Spool.Ack(acked); err != nil {
		return FlushResult{}, err
	}
	sent := 0
	for _, record := range records {
		if record.Sequence <= acked {
			sent++
		}
	}
	return FlushResult{Sent: sent}, nil
}

// AgentConfig configures an Agent.
type DriveCollector interface {
	Collect(context.Context) (drivehealth.Collection, error)
}

type AgentConfig struct {
	Host       Sampler
	Containers Sampler
	Drives     DriveCollector
	// DriveHealthEnabled optionally requires positive receiver negotiation.
	// Nil retains direct module collection behavior without a remote peer.
	DriveHealthEnabled func() bool
	// DriveEvery samples read-only hardware health on every Nth cycle. Zero
	// selects ten cycles (five minutes at the default resource interval).
	DriveEvery int
	// ContainerEvery samples containers on every Nth cycle; docker stats is the
	// costly reading. Zero means every second cycle.
	ContainerEvery int
	Spool          *Spool
	Sender         *Sender
	Interval       time.Duration
	Logger         *slog.Logger
	Now            func() time.Time
}

// Agent collects the contract's samples, spools them and sends them.
type Agent struct {
	cfg                AgentConfig
	log                *slog.Logger
	mu                 sync.Mutex
	pending            []Sample
	driveHealthEnabled atomic.Bool
}

// NewAgent validates the configuration.
func NewAgent(cfg AgentConfig) (*Agent, error) {
	if cfg.Spool == nil || cfg.Sender == nil || cfg.Host == nil {
		return nil, errors.New("guardtelemetry: an agent needs a host sampler, a spool and a sender")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.ContainerEvery <= 0 {
		cfg.ContainerEvery = 2
	}
	if cfg.DriveEvery <= 0 {
		cfg.DriveEvery = 10
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Agent{cfg: cfg, log: logger.With("component", "guard-telemetry")}, nil
}

// Submit hands samples from the loopback OTLP sink to the next cycle. They pass
// the same contract filter as every other sample.
func (a *Agent) Submit(samples []Sample) {
	kept, _ := Filter(samples)
	a.mu.Lock()
	defer a.mu.Unlock()
	if room := maxPending - len(a.pending); room > 0 {
		if len(kept) > room {
			kept = kept[:room]
		}
		a.pending = append(a.pending, kept...)
	}
}

// Collect runs one cycle: sample, filter to the contract, spool. It returns the
// spooled record, or false when there was nothing to keep.
func (a *Agent) Collect(ctx context.Context, cycle int) (Record, bool, error) {
	var samples []Sample
	var drives *drivehealth.Collection
	if hostSamples, err := a.cfg.Host.Sample(ctx); err == nil {
		samples = append(samples, hostSamples...)
	} else {
		a.log.Warn("guard_telemetry_host_sample_failed", "error", err.Error())
	}
	if a.cfg.Containers != nil && cycle%a.cfg.ContainerEvery == 0 {
		if containerSamples, err := a.cfg.Containers.Sample(ctx); err == nil {
			samples = append(samples, containerSamples...)
		}
	}
	driveHealthEnabled := true
	newlyEnabled := false
	if a.cfg.DriveHealthEnabled != nil {
		driveHealthEnabled = a.cfg.DriveHealthEnabled()
		previouslyEnabled := a.driveHealthEnabled.Swap(driveHealthEnabled)
		newlyEnabled = driveHealthEnabled && !previouslyEnabled
	}
	if a.cfg.Drives != nil && driveHealthEnabled && (cycle%a.cfg.DriveEvery == 0 || newlyEnabled) {
		collection, err := a.cfg.Drives.Collect(ctx)
		if err != nil {
			a.log.Warn("guard_drive_health_collection_failed", "reason", drivehealth.ErrorReason(err))
		}
		// Failed attempts carry their availability status to the receiver; they
		// must not disappear or become healthy zero-valued measurements.
		if validationErr := collection.Validate(a.cfg.Now()); validationErr == nil {
			drives = &collection
		} else {
			a.log.Warn("guard_drive_health_invalid_collection", "error", validationErr.Error())
			failed := drivehealth.Collection{ObservedAt: a.cfg.Now(), Status: drivehealth.Inaccessible, Reason: "invalid_collection", Drives: []drivehealth.Reading{}}
			drives = &failed
		}
	}
	a.mu.Lock()
	samples = append(samples, a.pending...)
	a.pending = nil
	a.mu.Unlock()
	kept, dropped := Filter(samples)
	if dropped > 0 {
		a.log.Warn("guard_telemetry_off_contract_samples_dropped", "count", dropped)
	}
	if len(kept) == 0 && drives == nil {
		return Record{}, false, nil
	}
	if len(kept) > MaxSamplesPerRecord {
		kept = kept[:MaxSamplesPerRecord]
	}
	record, err := a.cfg.Spool.AppendDrives(a.cfg.Now(), kept, drives)
	return record, err == nil, err
}

// Run collects and sends until the context ends. A control plane that stores no
// telemetry is asked again after an hour; nothing is collected meanwhile.
func (a *Agent) Run(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.Interval)
	defer ticker.Stop()
	var retryAt time.Time
	failures := 0
	for cycle := 0; ctx.Err() == nil; cycle++ {
		if _, _, err := a.Collect(ctx, cycle); err != nil {
			a.log.Warn("guard_telemetry_spool_failed", "error", err.Error())
		}
		if a.cfg.Now().After(retryAt) {
			result, err := a.flushAll(ctx)
			switch {
			case err != nil:
				failures++
				retryAt = a.cfg.Now().Add(min(time.Duration(failures)*a.cfg.Interval, flushBackoff))
				a.log.Warn("guard_telemetry_flush_failed", "error", err.Error(), "retry_after", retryAt.Sub(a.cfg.Now()).String())
			case result.Disabled:
				// Preserve managed drive evidence anywhere in the spool, even
				// when the rejected first batch contains resources only. The
				// established resource-only disabled behavior remains unchanged.
				a.log.Info("guard_telemetry_disabled_by_control_plane")
				if a.cfg.Drives == nil {
					if err := a.cfg.Spool.Ack(a.cfg.Spool.LastSequence()); err != nil {
						a.log.Warn("guard_telemetry_spool_clear_failed", "error", err.Error())
					}
				} else {
					a.log.Info("guard_telemetry_spool_preserved_for_drive_evidence")
				}
				if !sleepContext(ctx, disabledWait) {
					return
				}
				continue
			default:
				failures = 0
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// flushAll drains the spool in batches; it stops at the first failure.
func (a *Agent) flushAll(ctx context.Context) (FlushResult, error) {
	var total FlushResult
	for range 16 {
		result, err := a.cfg.Sender.Flush(ctx)
		if err != nil || result.Disabled {
			return result, err
		}
		if result.Sent == 0 {
			return total, nil
		}
		total.Sent += result.Sent
	}
	return total, nil
}

func sleepContext(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
