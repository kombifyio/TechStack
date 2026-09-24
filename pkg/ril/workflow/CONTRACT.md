# pkg/ril/workflow — Component Contract

**Status:** binding for the package. Companion to the RIL Orchestration ADR
(`docs/ADR/`). Last updated 2026-08-26.

## Purpose

A durable, **embedded** workflow-orchestration engine for the Runtime
Intelligence Layer. It drives the multi-step, long-running, human-in-the-loop
flows the RIL needs — Action-Card remediation, drift correction, cert rotation,
rolling updates — with crash/restart safety, retries, durable timers, and saga
compensation (rollback).

It is the durable **executor** substrate. Approval and execution today live in
`internal/routes/ril_governed_actions.go` and
`internal/rilactionexecution/service.go`, with the ledger in
`pkg/controlplane/ril_action_execution_ledger.go`; this engine adds
checkpoint/resume and saga compensation on top of that path.

## Design: checkpoint-per-step state machine (NOT replay)

A workflow is an ordered list of named steps (`WorkflowDefinition.Steps()`).
The engine:

1. Persists the run's **current mutable state** in `ril_workflow_runs`
   (`current_step` = source of truth) and one row per step in
   `ril_workflow_steps`.
2. Runs `Steps()[current_step]`, checkpoints the step result, then advances
   `current_step`. A restart between those writes observes the completed step
   and advances without executing it again.
3. On restart, reads `current_step` and **continues from there**. Completed
   steps are NOT re-executed (idempotency keys guard side effects).
4. A step may **suspend** the run (`SuspendDirective`) to await an external
   signal (approval, agent-result) and/or arm a durable timer
   (escalation/reminder). The run goes `suspended`; a `Signal` or fired
   `Timer` resumes it. A delivery failure leaves the timer due and retryable;
   only a successful delivery or an already-resolved signal marks it fired.
5. On unrecoverable step failure, the engine runs each completed step's
   `Compensate` in reverse order (saga), then marks the run `failed`.

## Reused existing infrastructure (do NOT duplicate)

- `pkg/ril/actions` — governed action-card authority and lifecycle over
  Postgres. The remediation workflow **drives this authority**; it does not own
  a second card state machine.
- `internal/rilactionexecution` plus `pkg/grpcserver` — the governed action
  activity reuses the exact-once execution ledger and the typed
  `SendStackKitCommand` path. `StackKitCommandHandler` binds each result to the
  exact command, agent, and pinned release before the activity can checkpoint
  public-safe evidence. The legacy generic `Server.Results()` channel is not a
  governed StackKits result authority and must not be wired into RIL.
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
- **Approval is not execution.** `/approve` records the decision; the explicit
  `/execute` command creates or resumes the deterministic remediation run. A
  failed action may report a separately approved recovery primitive, but the
  workflow never auto-executes that new authority.

## Tables (migration 008)

- `ril_workflow_runs` — run state (SoT). `status ∈ {pending, running,
  suspended, completed, failed, compensating, cancelled}`.
- `ril_workflow_steps` — per-step checkpoint. `status ∈ {pending, running,
  completed, failed, skipped, compensated}`. `(run_id, step_index)` unique;
  `idempotency_key` unique.
- `ril_workflow_timers` — durable timers. `kind ∈ {escalation, reminder,
  retry, schedule}`; swept by the worker, deliver a `signal_key` on fire.

## Audit and observability

- Every Postgres run, step, and timer mutation commits its current state and a
  sanitized append-only record in the existing central `audit_events` table in
  one transaction. Audit records never contain workflow input, context, output,
  raw errors, idempotency keys, credentials, or connector material.
- `GET /v1/ril/workflow-runs/{runId}` exposes the sanitized run, step, and audit
  history only when both owner and tenant match the authenticated inventory
  scope. Missing or foreign runs are indistinguishable and the response is
  `private, no-store`.
- The central `/metrics` registry publishes bounded `techstack_ril_workflow_*`
  transition, duration, and timer counters/histograms. Labels are limited to
  closed run type, step name, status, timer kind, and outcome values; run,
  tenant, owner, card, signal, and server identifiers are forbidden labels.

## Testability

Engine tests use an in-memory `fakeStore` (fake_store_test.go) that validates
all state-machine transitions via `ValidateRunTransition`/`ValidateStepTransition`.
`PgStore` integration tests run against a real Postgres instance when
`TECHSTACK_TEST_POSTGRES_URL` is set (see pg_store_test.go).
The durable-timer invariant reconstructs the engine over the same store before
the worker sweep, so restart recovery is tested instead of an in-memory-only
callback path. `mise run test:race:core` includes the engine, definitions, and
activities in the repository's existing CGO/Linux race lane.
