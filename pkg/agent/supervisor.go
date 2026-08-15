package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// Supervisor owns the agent lifecycle: it dials Core, registers, runs the
// heartbeat and command stream, executes queued commands, and — the part that
// was missing before it existed — tears the session down and reconnects with
// capped exponential backoff when anything dies. The Client itself never
// re-dials (Connect refuses on an existing conn); the Supervisor is the only
// component allowed to call reset() between sessions.
//
// systemd Restart=always is the outer belt for process death; the Supervisor
// is the inner belt for connection death.
type Supervisor struct {
	client            *Client
	backoff           *Backoff
	log               *slog.Logger
	heartbeatInterval time.Duration
	stableAfter       time.Duration

	sampler   *ResourceSampler
	pusher    *MetricsPusher
	prechecks *PreCheckRunner
	router    CommandRouter
}

// CommandRouter executes a queued command and returns its result. The default
// router dispatches diagnostic commands to the CommandExecutor; anything else
// is answered with a structured failure instead of silence.
type CommandRouter interface {
	Route(ctx context.Context, cmd Command) CommandResult
}

// SupervisorConfig configures a Supervisor. Client is required; everything
// else has working defaults or is optional.
type SupervisorConfig struct {
	Client *Client
	Logger *slog.Logger

	// Backoff paces reconnect attempts. Defaults to NewBackoff()
	// (1s base, factor 2, 5min cap, full jitter).
	Backoff *Backoff

	// HeartbeatInterval overrides the Core-provided interval (0 = use config).
	HeartbeatInterval time.Duration

	// StableAfter resets the backoff once a session survived this long.
	// Default 60s.
	StableAfter time.Duration

	// Sampler, when set, provides real host resource usage for heartbeats and
	// is run for the Supervisor's whole lifetime (not per session).
	Sampler *ResourceSampler

	// Pusher, when set, ships collector metrics per session.
	Pusher *MetricsPusher

	// PreChecks, when set, run once after every successful registration.
	PreChecks *PreCheckRunner

	// Router executes queued commands. Defaults to a diagnostics-only router
	// backed by CommandExecutor.
	Router CommandRouter
}

// NewSupervisor validates the config and applies defaults.
func NewSupervisor(cfg SupervisorConfig) (*Supervisor, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("client is required")
	}
	log := cfg.Logger
	if log == nil {
		log = nopLogger
	}
	log = log.With("component", "agent-supervisor")

	backoff := cfg.Backoff
	if backoff == nil {
		backoff = NewBackoff()
	}
	stableAfter := cfg.StableAfter
	if stableAfter <= 0 {
		stableAfter = 60 * time.Second
	}
	router := cfg.Router
	if router == nil {
		exec, err := NewCommandExecutor(ExecutorConfig{Logger: log})
		if err != nil {
			return nil, fmt.Errorf("create default command executor: %w", err)
		}
		router = &defaultRouter{exec: exec}
	}

	return &Supervisor{
		client:            cfg.Client,
		backoff:           backoff,
		log:               log,
		heartbeatInterval: cfg.HeartbeatInterval,
		stableAfter:       stableAfter,
		sampler:           cfg.Sampler,
		pusher:            cfg.Pusher,
		prechecks:         cfg.PreChecks,
		router:            router,
	}, nil
}

// Run supervises the agent until the context is canceled. It never returns on
// session errors — those trigger a backoff and a new session.
func (s *Supervisor) Run(ctx context.Context) error {
	if s.sampler != nil {
		s.client.SetResourceProvider(s.sampler.Snapshot)
		go s.sampler.Run(ctx)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		started := time.Now()
		err := s.runSession(ctx)
		sessionDuration := time.Since(started)

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if sessionDuration >= s.stableAfter {
			s.backoff.Reset()
		}
		delay := s.backoff.Next()
		s.log.Warn("agent_session_ended",
			"error", errString(err),
			"session_duration", sessionDuration.Round(time.Second).String(),
			"reconnect_in", delay.Round(time.Millisecond).String(),
			"attempt", s.backoff.Attempt(),
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// runSession runs one connect→register→work cycle and returns when the
// session dies for any reason. The client connection is always torn down on
// exit so the next cycle re-dials cleanly.
func (s *Supervisor) runSession(ctx context.Context) error {
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer s.client.reset()

	dialCtx, dialCancel := context.WithTimeout(sessionCtx, 30*time.Second)
	err := s.client.Connect(dialCtx)
	dialCancel()
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if regErr := s.client.Register(sessionCtx); regErr != nil {
		return fmt.Errorf("register: %w", regErr)
	}
	s.log.Info("agent_session_started", "agent_id", s.client.AgentID())

	if s.prechecks != nil {
		go func() {
			if _, pcErr := s.client.RunAndReportPreChecks(sessionCtx, s.prechecks); pcErr != nil {
				s.log.Warn("precheck_report_failed", "error", pcErr.Error())
			}
		}()
	}
	if s.pusher != nil {
		go s.pusher.Run(sessionCtx)
	}
	go s.pumpCommands(sessionCtx)

	// The session lives as long as heartbeat AND command stream live. The
	// first fatal error wins and cancels the other via sessionCtx.
	errCh := make(chan error, 2)
	go func() {
		errCh <- s.client.StartHeartbeat(sessionCtx, s.heartbeatInterval)
	}()
	go func() {
		errCh <- s.client.HandleCommands(sessionCtx)
	}()

	err = <-errCh
	cancel()
	// Drain the second goroutine so nothing leaks into the next session.
	<-errCh
	return err
}

// pumpCommands executes queued commands and reports results until the session
// ends. Commands must never block the session goroutines.
func (s *Supervisor) pumpCommands(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-s.client.Commands():
			if !ok {
				return
			}
			go func(cmd Command) {
				result := s.router.Route(ctx, cmd)
				result.CommandID = cmd.ID
				s.client.SendResult(result)
			}(cmd)
		}
	}
}

// defaultRouter handles the diagnostic command classes (health_check,
// get_logs) via CommandExecutor. Tofu/terramate over the command stream keep
// their dedicated dispatch (server-side jobs today); until that path is wired
// through the agent, such commands get an explicit structured failure instead
// of vanishing.
type defaultRouter struct {
	exec *CommandExecutor
}

func (r *defaultRouter) Route(ctx context.Context, cmd Command) CommandResult {
	started := time.Now()

	pbCmd := &agentpb.ExecuteCommand{
		CommandId:        cmd.ID,
		Command:          commandString(cmd),
		Args:             cmd.Args,
		Environment:      cmd.Environment,
		WorkingDirectory: cmd.WorkDir,
	}
	if cmd.Timeout > 0 {
		pbCmd.TimeoutSeconds = clampIntToInt32(int(cmd.Timeout / time.Second))
	}

	pbResult, err := r.exec.Execute(ctx, pbCmd)
	if pbResult == nil {
		return CommandResult{
			CommandID:  cmd.ID,
			ExitCode:   1,
			Stderr:     errString(err),
			StartedAt:  started,
			FinishedAt: time.Now(),
			Error:      err,
		}
	}
	return CommandResult{
		CommandID:  cmd.ID,
		ExitCode:   int(pbResult.ExitCode),
		Stdout:     pbResult.Stdout,
		Stderr:     pbResult.Stderr,
		StartedAt:  time.Unix(pbResult.StartedAtUnix, 0),
		FinishedAt: time.Unix(pbResult.FinishedAtUnix, 0),
		Error:      err,
	}
}

func commandString(cmd Command) string {
	if cmd.Command != "" {
		return cmd.Command
	}
	return cmd.Type
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "context canceled"
	}
	return err.Error()
}
