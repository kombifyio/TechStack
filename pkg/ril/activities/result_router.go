// Package activities holds the side-effecting building blocks RIL workflow steps
// dispatch into, plus the result-router that correlates asynchronous agent
// command results back to the suspended runs awaiting them. The package is
// deliberately decoupled from the gRPC server and PocketBase: it depends only on
// the workflow engine's public types, so it is unit-testable without a network
// or database. Wiring adapters (cmd/techstack) bridge the concrete grpcserver /
// store types onto the small interfaces defined here.
package activities

import (
	"context"
	"errors"
	"log/slog"

	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

// AgentResultSignalPrefix + command id is the signal a workflow step awaits after
// dispatching an agent command. The dispatcher MUST set the command id equal to
// the dispatching step's idempotency key so the suspended step and its result
// correlate deterministically across retries and restarts.
const AgentResultSignalPrefix = "agent_result:"

// AgentResultSignalKey returns the await/deliver signal key for a command id.
func AgentResultSignalKey(commandID string) string {
	return AgentResultSignalPrefix + commandID
}

// CommandResult is the minimal shape the router needs from an agent command
// result. A wiring adapter maps the concrete grpcserver.CommandResult onto it.
type CommandResult struct {
	CommandID string
	AgentID   string
	ExitCode  int
	Stdout    string
	Stderr    string
	Err       string
}

// SignalDeliverer routes an external signal to the suspended run awaiting it. It
// is satisfied by *workflow.Engine.
type SignalDeliverer interface {
	Deliver(ctx context.Context, sig workflow.Signal) error
}

// ResultRouter forwards agent command results to the suspended workflow runs
// awaiting them. A result with no awaiting run is a benign race (the run already
// resumed, or the command was not workflow-driven) and is dropped.
type ResultRouter struct {
	deliverer SignalDeliverer
	log       *slog.Logger
}

// NewResultRouter builds a router over the given deliverer.
func NewResultRouter(deliverer SignalDeliverer) *ResultRouter {
	return &ResultRouter{
		deliverer: deliverer,
		log:       slog.Default().With("component", "ril.activities.result_router"),
	}
}

// Route delivers one command result to its awaiting run. A not-found delivery
// (no run awaiting the signal) is swallowed as benign; any other delivery error
// is returned so the caller can decide how to react.
func (r *ResultRouter) Route(ctx context.Context, res CommandResult) error {
	if res.CommandID == "" {
		return errors.New("result_router: command result missing command id")
	}
	sig := workflow.Signal{
		Key: AgentResultSignalKey(res.CommandID),
		Payload: map[string]any{
			"command_id": res.CommandID,
			"agent_id":   res.AgentID,
			"exit_code":  res.ExitCode,
			"stdout":     res.Stdout,
			"stderr":     res.Stderr,
			"error":      res.Err,
			"ok":         res.Err == "" && res.ExitCode == 0,
		},
	}
	if err := r.deliverer.Deliver(ctx, sig); err != nil && !errors.Is(err, workflow.ErrNotFound) {
		return err
	}
	return nil
}

// Run consumes results until the channel closes or ctx is canceled, routing each
// and logging (without stopping) any non-benign delivery error.
func (r *ResultRouter) Run(ctx context.Context, results <-chan CommandResult) {
	for {
		select {
		case <-ctx.Done():
			return
		case res, ok := <-results:
			if !ok {
				return
			}
			if err := r.Route(ctx, res); err != nil {
				r.log.Error("ril_result_route_failed", "command_id", res.CommandID, "error", err.Error())
			}
		}
	}
}
