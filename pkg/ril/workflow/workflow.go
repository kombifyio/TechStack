// Package workflow is the durable, embedded orchestration engine for the
// Runtime Intelligence Layer (RIL). It drives multi-step, long-running,
// human-in-the-loop flows — Action-Card remediation, drift correction, cert
// rotation, rolling updates — with crash/restart safety, retries, durable
// timers, and saga compensation.
//
// Design: checkpoint-per-step state machine persisted in Postgres. NOT
// event-sourcing, NOT deterministic replay, NOT a generic user-workflow
// platform, NOT an external service. See CONTRACT.md.
package workflow

import (
	"context"
	"time"
)

// RunType identifies a registered workflow definition. The values match the
// `type` column in the ril_workflow_runs table (migration 008).
type RunType string

const (
	TypeActionCardRemediation RunType = "action_card_remediation"
	TypeDriftCorrection       RunType = "drift_correction"
	TypeCertRotation          RunType = "cert_rotation"
	TypeRollingUpdate         RunType = "rolling_update"
	// TypeServiceMigration relocates a managed service between nodes. NOTE: the
	// ril_workflow_runs `type` column (migration 008) must include this value
	// before runs of this type can persist; that schema update lands with the
	// engine-wiring slice.
	TypeServiceMigration         RunType = "service_migration"
	TypeProviderIncidentAdvisory RunType = "provider_incident_advisory"
)

// RunStatus is the lifecycle state of a workflow run. Mirrors the `status`
// select-field in ril_workflow_runs.
type RunStatus string

const (
	RunPending      RunStatus = "pending"      // created, not yet started
	RunRunning      RunStatus = "running"      // a step is executing or ready to execute
	RunSuspended    RunStatus = "suspended"    // awaiting a signal and/or timer
	RunCompleted    RunStatus = "completed"    // all steps done
	RunFailed       RunStatus = "failed"       // unrecoverable; compensation (if any) done
	RunCompensating RunStatus = "compensating" // running saga rollback in reverse
	RunCancelled    RunStatus = "cancelled"    // operator-cancelled
)

// StepStatus is the lifecycle state of a single step execution. Mirrors the
// `status` select-field in ril_workflow_steps.
type StepStatus string

const (
	StepPending     StepStatus = "pending"
	StepRunning     StepStatus = "running"
	StepCompleted   StepStatus = "completed"
	StepFailed      StepStatus = "failed"
	StepSkipped     StepStatus = "skipped"
	StepCompensated StepStatus = "compensated"
)

// TimerKind classifies a durable timer. Mirrors the `kind` select-field in
// ril_workflow_timers.
type TimerKind string

const (
	TimerEscalation TimerKind = "escalation"
	TimerReminder   TimerKind = "reminder"
	TimerRetry      TimerKind = "retry"
	TimerSchedule   TimerKind = "schedule"
)

// Run is one workflow instance. The persisted row in ril_workflow_runs is the
// single source of truth for the run's state (current mutable state, NOT an
// event log to replay).
type Run struct {
	ID             string         `json:"id"`     // PocketBase record id
	RunID          string         `json:"run_id"` // stable external id (uuid)
	Type           RunType        `json:"type"`
	Status         RunStatus      `json:"status"`
	CurrentStep    int            `json:"current_step"`
	Input          map[string]any `json:"input,omitempty"`
	Context        map[string]any `json:"context,omitempty"` // engine/step scratch state
	OwnerID        string         `json:"owner_id,omitempty"`
	ServerID       string         `json:"server_id,omitempty"`
	CardID         string         `json:"card_id,omitempty"`         // links to ril_action_cards
	AwaitingSignal string         `json:"awaiting_signal,omitempty"` // signal key when suspended
	Error          string         `json:"error,omitempty"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
	Created        time.Time      `json:"created"`
	Updated        time.Time      `json:"updated"`
}

// Step is one step execution within a run. Persisted in ril_workflow_steps;
// the (run_id, step_index) pair is unique, and idempotency_key dedups
// side-effecting activities across retries/restarts.
type Step struct {
	ID             string         `json:"id"`
	RunID          string         `json:"run_id"`
	StepIndex      int            `json:"step_index"`
	Name           string         `json:"name"`
	Status         StepStatus     `json:"status"`
	Attempt        int            `json:"attempt"`
	Input          map[string]any `json:"input,omitempty"`
	Output         map[string]any `json:"output,omitempty"`
	Error          string         `json:"error,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
}

// Timer is a durable timer attached to a run. Persisted in ril_workflow_timers
// and swept by the worker; on fire it delivers SignalKey to the run.
type Timer struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Kind      TimerKind `json:"kind"`
	FireAt    time.Time `json:"fire_at"`
	Fired     bool      `json:"fired"`
	SignalKey string    `json:"signal_key,omitempty"`
}

// Signal is an external event that resumes a suspended run. Key correlates the
// signal to the run's AwaitingSignal (e.g. "approval:<cardID>" or
// "agent_result:<commandID>"). Payload carries event data (e.g. command result).
type Signal struct {
	Key       string         `json:"key"`
	Payload   map[string]any `json:"payload,omitempty"`
	TimedOut  bool           `json:"timed_out,omitempty"` // true when delivered by an escalation timer
	DeliverAt time.Time      `json:"deliver_at"`
}

// SuspendDirective is returned by a step (inside StepResult) to suspend the run
// until a signal arrives or an optional timer fires.
type SuspendDirective struct {
	// SignalKey the run will wait on. Required.
	SignalKey string
	// Timeout, if > 0, arms a durable timer that fires after this duration and
	// resumes the run with Signal.TimedOut = true.
	Timeout time.Duration
	// TimerKind classifies the timeout timer (default: TimerEscalation).
	TimerKind TimerKind
}

// StepResult is the successful outcome of a step. A non-nil Suspend tells the
// engine to suspend the run instead of advancing to the next step.
type StepResult struct {
	Output  map[string]any
	Suspend *SuspendDirective
}

// Activities is the side-effecting activity registry a step calls into. The
// runner provides a concrete implementation; steps never touch the network or
// PocketBase directly — they go through idempotency-keyed activities.
type Activities interface {
	Run(ctx context.Context, name string, input map[string]any, idempotencyKey string) (map[string]any, error)
}

// RunContext is passed to every StepFunc. It exposes the run, the resumed
// signal (nil unless resuming from suspend), and the activity registry.
type RunContext struct {
	Run        *Run
	Signal     *Signal
	Activities Activities
}

// StepFunc executes one step. Returning a StepResult with a non-nil Suspend
// suspends the run. Returning an error triggers retry (per RetryPolicy) and,
// once exhausted, saga compensation.
type StepFunc func(ctx context.Context, rc *RunContext) (StepResult, error)

// StepDef is one step in a workflow definition. Compensate is the optional saga
// rollback, run in reverse order over completed steps when the run fails.
type StepDef struct {
	Name       string
	Run        StepFunc
	Compensate StepFunc
}

// WorkflowDefinition is the contract a registered workflow implements. It is a
// declarative, ordered step list — NOT an imperative replayed function — so the
// engine can checkpoint and resume by step index without deterministic replay.
type WorkflowDefinition interface {
	Type() RunType
	Steps() []StepDef
	RetryPolicy() RetryPolicy
}
