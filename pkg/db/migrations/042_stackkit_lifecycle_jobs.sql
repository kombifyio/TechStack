ALTER TABLE jobs
    DROP CONSTRAINT IF EXISTS jobs_type_check;

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
        'stackkit_lifecycle'
    ));
