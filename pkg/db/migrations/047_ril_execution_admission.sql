-- Bind every newly started governed action to the exact current Runtime
-- Inventory and RuntimeLease heads without widening the provider-free RIL
-- request or public evidence contracts.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

ALTER TABLE ril_action_cards
    ADD COLUMN IF NOT EXISTS admission_inventory_revision bigint,
    ADD COLUMN IF NOT EXISTS admission_server_revision bigint,
    ADD COLUMN IF NOT EXISTS admission_server_generation bigint,
    ADD COLUMN IF NOT EXISTS admission_lease_id text,
    ADD COLUMN IF NOT EXISTS admission_lease_revision bigint,
    ADD COLUMN IF NOT EXISTS admission_resource_generation_id uuid,
    ADD COLUMN IF NOT EXISTS execution_admission_digest text;

ALTER TABLE ril_action_cards
    DROP CONSTRAINT IF EXISTS ril_action_cards_execution_admission_complete;
ALTER TABLE ril_action_cards
    ADD CONSTRAINT ril_action_cards_execution_admission_complete CHECK (
        (
            admission_inventory_revision IS NULL
            AND admission_server_revision IS NULL
            AND admission_server_generation IS NULL
            AND admission_lease_id IS NULL
            AND admission_lease_revision IS NULL
            AND admission_resource_generation_id IS NULL
            AND execution_admission_digest IS NULL
        )
        OR (
            admission_inventory_revision IS NOT NULL
            AND admission_inventory_revision > 0
            AND admission_server_revision IS NOT NULL
            AND admission_server_revision > 0
            AND admission_server_generation IS NOT NULL
            AND admission_server_generation > 0
            AND admission_lease_id IS NOT NULL
            AND NULLIF(BTRIM(admission_lease_id), '') IS NOT NULL
            AND admission_lease_revision IS NOT NULL
            AND admission_lease_revision BETWEEN 1 AND 9007199254740991
            AND admission_resource_generation_id IS NOT NULL
            AND execution_admission_digest IS NOT NULL
            AND execution_admission_digest ~ '^sha256:[a-f0-9]{64}$'
        )
    ) NOT VALID;

-- Historical terminal cards may predate this binding. Every newly inserted or
-- updated execution-bearing card must nevertheless carry the complete
-- admission tuple; NOT VALID skips only the one-time historical table scan.
ALTER TABLE ril_action_cards
    DROP CONSTRAINT IF EXISTS ril_action_cards_execution_status_requires_admission;
ALTER TABLE ril_action_cards
    ADD CONSTRAINT ril_action_cards_execution_status_requires_admission CHECK (
        status NOT IN ('executing', 'verifying', 'completed', 'failed')
        OR (
            admission_inventory_revision IS NOT NULL
            AND admission_inventory_revision > 0
            AND admission_server_revision IS NOT NULL
            AND admission_server_revision > 0
            AND admission_server_generation IS NOT NULL
            AND admission_server_generation > 0
            AND admission_lease_id IS NOT NULL
            AND NULLIF(BTRIM(admission_lease_id), '') IS NOT NULL
            AND admission_lease_revision IS NOT NULL
            AND admission_lease_revision BETWEEN 1 AND 9007199254740991
            AND admission_resource_generation_id IS NOT NULL
            AND execution_admission_digest IS NOT NULL
            AND execution_admission_digest ~ '^sha256:[a-f0-9]{64}$'
        )
    ) NOT VALID;

ALTER TABLE ril_action_execution_ledger
    ADD COLUMN IF NOT EXISTS execution_admission_digest text;

ALTER TABLE ril_action_execution_ledger
    DROP CONSTRAINT IF EXISTS ril_action_execution_ledger_admission_digest_valid;
ALTER TABLE ril_action_execution_ledger
    ADD CONSTRAINT ril_action_execution_ledger_admission_digest_valid CHECK (
        execution_admission_digest IS NOT NULL
        AND execution_admission_digest ~ '^sha256:[a-f0-9]{64}$'
    ) NOT VALID;
