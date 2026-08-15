-- 008_ril_workflows.sql
--
-- RIL durable workflow engine persistence (epic kombify-Techstack-a7x.1).
-- Postgres successor to the in-test PocketBase collections (ril_workflow_*),
-- mirroring the field set the engine's RunStore reads/writes (see
-- pkg/ril/workflow/store.go + store_test.go).
--
-- Global tables (no tenant RLS): the worker polls runnable runs and due timers
-- across all tenants; per-tenant scoping lives in the run's owner_id/input.

CREATE TABLE IF NOT EXISTS ril_workflow_runs (
    run_id          text PRIMARY KEY,
    type            text NOT NULL CHECK (type IN (
                        'action_card_remediation', 'drift_correction',
                        'cert_rotation', 'rolling_update', 'service_migration',
                        'provider_incident_advisory')),
    status          text NOT NULL CHECK (status IN (
                        'pending', 'running', 'suspended', 'completed',
                        'failed', 'compensating', 'cancelled')),
    current_step    integer NOT NULL DEFAULT 0,
    input           jsonb NOT NULL DEFAULT '{}'::jsonb,
    context         jsonb NOT NULL DEFAULT '{}'::jsonb,
    owner_id        text NOT NULL DEFAULT '',
    server_id       text NOT NULL DEFAULT '',
    card_id         text NOT NULL DEFAULT '',
    awaiting_signal text NOT NULL DEFAULT '',
    error           text NOT NULL DEFAULT '',
    started_at      timestamptz,
    finished_at     timestamptz,
    created         timestamptz NOT NULL DEFAULT now(),
    updated         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ril_runs_status_updated
    ON ril_workflow_runs (status, updated);
CREATE INDEX IF NOT EXISTS idx_ril_runs_awaiting_signal
    ON ril_workflow_runs (awaiting_signal) WHERE awaiting_signal <> '';

CREATE TABLE IF NOT EXISTS ril_workflow_steps (
    id              text PRIMARY KEY,
    run_id          text NOT NULL REFERENCES ril_workflow_runs(run_id) ON DELETE CASCADE,
    step_index      integer NOT NULL,
    name            text NOT NULL DEFAULT '',
    status          text NOT NULL CHECK (status IN (
                        'pending', 'running', 'completed', 'failed',
                        'skipped', 'compensated')),
    attempt         integer NOT NULL DEFAULT 0,
    input           jsonb NOT NULL DEFAULT '{}'::jsonb,
    output          jsonb NOT NULL DEFAULT '{}'::jsonb,
    error           text NOT NULL DEFAULT '',
    idempotency_key text NOT NULL DEFAULT '',
    started_at      timestamptz,
    finished_at     timestamptz,
    created         timestamptz NOT NULL DEFAULT now(),
    updated         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, step_index)
);

CREATE INDEX IF NOT EXISTS idx_ril_steps_run
    ON ril_workflow_steps (run_id, step_index);

CREATE TABLE IF NOT EXISTS ril_workflow_timers (
    id          text PRIMARY KEY,
    run_id      text NOT NULL REFERENCES ril_workflow_runs(run_id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN (
                    'escalation', 'reminder', 'retry', 'schedule')),
    fire_at     timestamptz NOT NULL,
    fired       boolean NOT NULL DEFAULT false,
    signal_key  text NOT NULL DEFAULT '',
    created     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ril_timers_due
    ON ril_workflow_timers (fired, fire_at);
