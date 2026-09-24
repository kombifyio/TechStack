-- Admit the managed backup job type.
--
-- jobs_type_check (042, last widened in 113) enumerates every admissible job
-- type. JobTypeBackup would otherwise fail closed on insert with a
-- check-constraint violation, the same way remote_enrollment did before 113 --
-- the scheduler would enqueue nothing and report a database error rather than
-- a missing entitlement, which is the wrong diagnosis for the wrong party.
--
-- reconcile_lease stays out for the reason 113 records: it remains fail-closed
-- until DurableReconciliationReady can truthfully return true.

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
        'remote_enrollment',
        'backup'
    ));
