-- Admit the durable connect-remote enrollment job type.
--
-- createStoreRemoteEnrollmentJob (internal/routes/trust/pairing.go) has written
-- type='remote_enrollment' since the 0.11.5 connect-remote lane, but
-- jobs_type_check (042) never listed it. Against Postgres the mint failed closed
-- with a check-constraint violation, so the wizard answered "Failed to prepare
-- server registration" instead of preparing SSH enrollment. Confirmed against a
-- fresh migration chain on 2026-09-16.
--
-- reconcile_lease is deliberately NOT added here: that type stays fail-closed
-- until DurableReconciliationReady can truthfully return true (see
-- pkg/orchestrator).

SET LOCAL lock_timeout = '5s';

ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_type_check;

ALTER TABLE jobs
    ADD CONSTRAINT jobs_type_check
    CHECK (type IN (
        'provision',
        'destroy',
        'update',
        'restart',
        'deploy',
        'drift_check',
        'drift_resolve',
        'stackkit_lifecycle',
        'remote_enrollment'
    ));
