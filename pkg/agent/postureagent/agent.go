// Package postureagent is the Guard side of kombify Guard posture. On a fixed
// cadence it observes the host through the privileged executor's read-only
// StackKits operations (the host security baseline and the backup repository),
// reports what it saw to the control plane over the Guard's authenticated
// channel, and answers the control plane's request for an immediate observation.
//
// It sends what StackKits produced and nothing it judged itself: the control
// plane reads the evidence, recomputes the verdict and keeps the time. A
// StackKits release without the command is reported as unsupported, which the
// control plane shows as unknown, never as secure.
package postureagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/guardexec"
	"github.com/kombifyio/techstack/pkg/guardposture"
)

const (
	// DefaultInterval is the routine observation cadence until the control
	// plane says otherwise.
	DefaultInterval = 15 * time.Minute
	// DefaultCheckIn is how often the Guard asks whether a fresh observation was
	// requested. A check-in carries no observation and costs one small request.
	DefaultCheckIn = time.Minute

	minInterval  = 5 * time.Minute
	maxInterval  = 6 * time.Hour
	errorBackoff = 5 * time.Minute
	responseMax  = 16 << 10

	hostSecurityCommand = "stackkit host security verify"
	backupStatusCommand = "stackkit backup status"
	commandResultSchema = "stackkit.command-result/v1"
)

// Observer is the executor's read-only observation surface.
type Observer interface {
	HostSecurityVerify(ctx context.Context) (guardexec.Observation, error)
	BackupStatus(ctx context.Context) (guardexec.Observation, error)
}

// Config configures an Agent.
type Config struct {
	// URL is the control plane's posture endpoint for this worker.
	URL string
	// HTTPClient carries the Guard's key-bound transport, or plain HTTPS for a
	// Guard still on the bearer credential.
	HTTPClient *http.Client
	// AgentToken and TenantID authenticate a bearer Guard; a key-bound transport
	// replaces the header.
	AgentToken string
	TenantID   string
	Observer   Observer
	// Version is the running Guard's version; Signed reports whether its update
	// path admits only signed releases.
	Version string
	Signed  func() bool
	// Interval and CheckIn override the cadences. Tests only.
	Interval, CheckIn time.Duration
	Logger            *slog.Logger
	Now               func() time.Time
}

// Agent runs the observation loop.
type Agent struct {
	cfg      Config
	log      *slog.Logger
	epoch    string
	sequence int64
}

// New validates the configuration.
func New(cfg Config) (*Agent, error) {
	if strings.TrimSpace(cfg.URL) == "" || cfg.HTTPClient == nil || cfg.Observer == nil {
		return nil, errors.New("kombify Guard posture needs the control plane URL, a client and an observer")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.CheckIn <= 0 {
		cfg.CheckIn = DefaultCheckIn
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	return &Agent{cfg: cfg, log: logger.With("component", "guard-posture"), epoch: hex.EncodeToString(random)}, nil
}

// Run observes until the context ends. The first observation is immediate.
func (a *Agent) Run(ctx context.Context) {
	interval := a.cfg.Interval
	var lastObserved, lastBackup time.Time
	failures := 0
	requested := true
	// requestID is the token of the request the next observation answers. It is
	// learned from a directive, so the observation that carries it always starts
	// after the Guard heard of the request; one already in flight when the
	// request arrived finishes without it and the answer to that report is what
	// brings the token here.
	requestID := ""
	for ctx.Err() == nil {
		now := a.cfg.Now()
		var directive *guardposture.Directive
		var err error
		if requested || lastObserved.IsZero() || now.Sub(lastObserved) >= interval {
			withBackup := requested || lastBackup.IsZero() || now.Sub(lastBackup) >= 2*interval
			directive, err = a.report(ctx, withBackup, requestID)
			if err == nil {
				lastObserved = now
				if withBackup {
					lastBackup = now
				}
			}
		} else {
			directive, err = a.send(ctx, guardposture.Report{CheckIn: true})
		}
		wait := a.cfg.CheckIn
		if err != nil {
			failures++
			wait = backoff(failures, a.cfg.CheckIn)
			a.log.Warn("guard_posture_exchange_failed", "error", err.Error(), "retry_in", wait.String())
		} else {
			failures = 0
			requested = directive.VerifyRequested
			requestID = ""
			if requested {
				requestID = directive.VerifyRequestID
			}
			if seconds := time.Duration(directive.VerifyIntervalSeconds) * time.Second; seconds > 0 {
				interval = min(max(seconds, minInterval), maxInterval)
			}
			if requested {
				wait = 0
			}
		}
		if wait > 0 && !sleep(ctx, wait) {
			return
		}
	}
}

func backoff(failures int, base time.Duration) time.Duration {
	wait := base
	for i := 1; i < failures && wait < errorBackoff; i++ {
		wait *= 2
	}
	return min(wait, errorBackoff)
}

func sleep(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// report observes the host and sends the result, echoing the request token the
// observation answers.
func (a *Agent) report(ctx context.Context, withBackup bool, requestID string) (*guardposture.Directive, error) {
	report := guardposture.Report{VerifyRequestID: requestID}
	if observation, err := a.cfg.Observer.HostSecurityVerify(ctx); err != nil {
		report.Security = failedSecurity(err)
	} else {
		report.Security = SecurityReport(observation)
	}
	if withBackup {
		if observation, err := a.cfg.Observer.BackupStatus(ctx); err != nil {
			report.Backup = &guardposture.BackupReport{Status: guardposture.ObservationFailed, ReasonCode: reasonFor(err)}
		} else {
			report.Backup = BackupReport(observation)
		}
	}
	return a.send(ctx, report)
}

func (a *Agent) send(ctx context.Context, report guardposture.Report) (*guardposture.Directive, error) {
	a.sequence++
	report.SchemaVersion = guardposture.ReportSchema
	report.SourceEpoch, report.Sequence, report.ObservedAt = a.epoch, a.sequence, a.cfg.Now()
	report.GuardVersion = a.cfg.Version
	if a.cfg.Signed != nil {
		report.GuardSigned = a.cfg.Signed()
	}
	body, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(a.cfg.AgentToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant := strings.TrimSpace(a.cfg.TenantID); tenant != "" {
		req.Header.Set("X-Kombify-Tenant-ID", tenant)
	}
	response, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, responseMax))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("control plane answered HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Data struct {
			Posture struct {
				State string `json:"state"`
				guardposture.Directive
			} `json:"posture"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil, errors.New("control plane returned an unusable posture answer")
	}
	if envelope.Data.Posture.State == "disabled" {
		// Posture is off here: check in rarely.
		return &guardposture.Directive{VerifyIntervalSeconds: int(maxInterval / time.Second)}, nil
	}
	return &envelope.Data.Posture.Directive, nil
}

func reasonFor(err error) string {
	switch {
	case guardexec.IsCode(err, guardexec.CodeUnavailable):
		return "executor_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "observation_timed_out"
	}
	return guardposture.ReasonObservationFailed
}

func failedSecurity(err error) *guardposture.SecurityReport {
	return &guardposture.SecurityReport{Status: guardposture.ObservationFailed, ReasonCode: reasonFor(err)}
}

// unwrap returns the data of a successful stackkit.command-result/v1 envelope
// for the named command. Anything else is not a document the Guard forwards.
func unwrap(stdout []byte, command string) (json.RawMessage, bool) {
	var envelope struct {
		SchemaVersion string          `json:"schemaVersion"`
		Command       string          `json:"command"`
		Status        string          `json:"status"`
		Data          json.RawMessage `json:"data"`
	}
	if json.Unmarshal(stdout, &envelope) != nil || envelope.SchemaVersion != commandResultSchema ||
		envelope.Command != command || envelope.Status != "success" || len(envelope.Data) == 0 {
		return nil, false
	}
	return envelope.Data, true
}

// SecurityReport turns the executor's observation into the report section. A
// CLI without the command is unsupported and a run that produced no readable
// success document is a failure; neither carries evidence.
func SecurityReport(observation guardexec.Observation) *guardposture.SecurityReport {
	switch observation.Reason {
	case guardexec.ReasonCLIUnsupported:
		return &guardposture.SecurityReport{Status: guardposture.ObservationUnsupported, ReasonCode: guardposture.ReasonCLIUnsupported}
	case "":
	default:
		return &guardposture.SecurityReport{Status: guardposture.ObservationUnavailable, ReasonCode: boundedReason(observation.Reason)}
	}
	if observation.ExitCode == 0 {
		if data, ok := unwrap(observation.Stdout, hostSecurityCommand); ok {
			return &guardposture.SecurityReport{Status: guardposture.ObservationObserved, Evidence: data}
		}
	}
	return &guardposture.SecurityReport{Status: guardposture.ObservationFailed, ReasonCode: guardposture.ReasonObservationFailed}
}

// BackupReport reduces `stackkit backup status` to the receipt states and times
// the control plane judges. Nothing else of the repository leaves the host.
func BackupReport(observation guardexec.Observation) *guardposture.BackupReport {
	if observation.Reason != "" {
		return &guardposture.BackupReport{Status: guardposture.ObservationUnavailable, ReasonCode: boundedReason(observation.Reason)}
	}
	if observation.ExitCode == 0 {
		if data, ok := unwrap(observation.Stdout, backupStatusCommand); ok {
			var status struct {
				Ready   bool `json:"ready"`
				History *struct {
					Snapshot      receipt `json:"snapshot"`
					StagedRestore receipt `json:"stagedRestore"`
				} `json:"history"`
			}
			if json.Unmarshal(data, &status) == nil && status.History != nil {
				return &guardposture.BackupReport{
					Status: guardposture.ObservationObserved, Ready: status.Ready,
					SnapshotState: status.History.Snapshot.State, LastSnapshotAt: status.History.Snapshot.RecordedAt,
					RestoreState: status.History.StagedRestore.State, LastRestoreAt: status.History.StagedRestore.RecordedAt,
				}
			}
		}
	}
	return &guardposture.BackupReport{Status: guardposture.ObservationFailed, ReasonCode: guardposture.ReasonObservationFailed}
}

// boundedReason keeps a reason code a bounded token.
func boundedReason(reason string) string {
	if len(reason) == 0 || len(reason) > 64 || strings.ToLower(reason) != reason || strings.ContainsAny(reason, " \t\n-.:/") {
		return guardposture.ReasonObservationFailed
	}
	return reason
}

type receipt struct {
	State      string     `json:"state"`
	RecordedAt *time.Time `json:"recordedAt"`
}
