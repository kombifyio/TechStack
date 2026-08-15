# pkg/ril/workflow — Component Contract

**Status:** binding for the package. Companion to the RIL Orchestration ADR
(`docs/adr/`). Last updated 2026-05-28.

## Purpose

A durable, **embedded** workflow-orchestration engine for the Runtime
Intelligence Layer. It drives the multi-step, long-running, human-in-the-loop
flows the RIL needs — Action-Card remediation, drift correction, cert rotation,
rolling updates — with crash/restart safety, retries, durable timers, and saga
compensation (rollback).

It is the missing **executor**: today `internal/routes/ril_actions.go`
approves an action card but nothing drives `approved → executing →
completed/failed`. This engine fills that gap.

## Design: checkpoint-per-step state machine (NOT replay)

A workflow is an ordered list of named steps (`WorkflowDefinition.Steps()`).
The engine:

1. Persists the run's **current mutable state** in `ril_workflow_runs`
   (`current_step` = source of truth) and one row per step in
   `ril_workflow_steps`.
2. Runs `Steps()[current_step]`, then checkpoints: writes the step's
   result + advances `current_step`, atomically.
3. On restart, reads `current_step` and **continues from there**. Completed
   steps are NOT re-executed (idempotency keys guard side effects).
4. A step may **suspend** the run (`SuspendDirective`) to await an external
   signal (approval, agent-result) and/or arm a durable timer
   (escalation/reminder). The run goes `suspended`; a `Signal` or fired
   `Timer` resumes it.
5. On unrecoverable step failure, the engine runs each completed step's
   `Compensate` in reverse order (saga), then marks the run `failed`.

## Reused existing infrastructure (do NOT duplicate)

- `pkg/ril/actions` — `ActionCard` model + `CardStatus` state machine
  (`ValidateTransition`) + PocketBase store. The remediation workflow **drives
  these store methods**; it does not own a second card state machine.
- `pkg/grpcserver` — async agent transport. `DispatchAgentCommand` wraps
  `Server.SendCommandWithPersistence`; command results arrive on
  `Server.Results()` and are correlated back to a suspended run by
  `command_id` (a signal), NOT by a blocking wait.
- `pkg/drift` — drift detection, consumed by the drift-correction workflow.
- `pkg/db/migrations` — Postgres schema via numbered SQL migrations (008).

## Non-goals (binding)

- **No event-sourcing, no deterministic replay.** State is mutable in the
  store; the audit trail is append-only and is NEVER read to reconstruct
  run state. (This is what separates "adopt Temporal patterns" from "clone
  Temporal".)
- **No generic, user-defined workflows.** Those are kombify-AI v1.0.0
  "Workflows" (ADK, SaaS-side) — a different domain. This engine runs a
  closed, operator-defined set of Go workflows.
- **No external service dependency.** No Temporal cluster. Runs embedded
  on the product's own Postgres — D11: RIL is an OSS, self-hostable product
  standard.
- **Not the provisioning queue.** `pkg/jobs.Queue` (in-memory, 24h TTL,
  single-step) stays the substrate for short provisioning/deploy/drift jobs.
  This engine is separate and durable for long-running RIL workflows.

## Tables (migration 008)

- `ril_workflow_runs` — run state (SoT). `status ∈ {pending, running,
  suspended, completed, failed, compensating, cancelled}`.
- `ril_workflow_steps` — per-step checkpoint. `status ∈ {pending, running,
  completed, failed, skipped, compensated}`. `(run_id, step_index)` unique;
  `idempotency_key` unique.
- `ril_workflow_timers` — durable timers. `kind ∈ {escalation, reminder,
  retry, schedule}`; swept by the worker, deliver a `signal_key` on fire.

## Testability

Engine tests use an in-memory `fakeStore` (fake_store_test.go) that validates
all state-machine transitions via `ValidateRunTransition`/`ValidateStepTransition`.
`PgStore` integration tests run against a real Postgres instance when
`TECHSTACK_TEST_POSTGRES_URL` is set (see pg_store_test.go).
