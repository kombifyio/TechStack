-- Canonical runtime activity scoping.
--
-- Activity remains an audit/event lane, separate from raw runtime logs. These
-- keys make both lanes address the same tenant/server/service identities
-- without parsing display messages or provider-specific metadata at read time.

SET LOCAL lock_timeout = '5s';

ALTER TABLE activity_log ADD COLUMN IF NOT EXISTS runtime_scope_key text;
ALTER TABLE activity_log ADD COLUMN IF NOT EXISTS server_scope_key text;
ALTER TABLE activity_log ADD COLUMN IF NOT EXISTS service_scope_key text;
ALTER TABLE activity_log ADD COLUMN IF NOT EXISTS correlation_id text;

UPDATE activity_log
SET server_scope_key = NULLIF(BTRIM(COALESCE(
        details_json->>'server_scope_key',
        details_json->>'server_id',
        details_json->>'node_id'
    )), '')
WHERE server_scope_key IS NULL;

UPDATE activity_log
SET service_scope_key = NULLIF(BTRIM(COALESCE(
        details_json->>'service_scope_key',
        details_json->>'service_id'
    )), '')
WHERE service_scope_key IS NULL;

UPDATE activity_log
SET runtime_scope_key = NULLIF(BTRIM(COALESCE(
        details_json->>'runtime_scope_key',
        CASE WHEN NULLIF(BTRIM(details_json->>'runtime_target_id'), '') IS NOT NULL
            THEN 'managed_target:' || BTRIM(details_json->>'runtime_target_id') END,
        CASE WHEN server_scope_key IS NOT NULL
            THEN 'server:' || server_scope_key END,
        CASE WHEN stack_id IS NOT NULL
            THEN 'stack:' || stack_id END
    )), '')
WHERE runtime_scope_key IS NULL;

UPDATE activity_log
SET correlation_id = NULLIF(BTRIM(COALESCE(
        details_json->>'correlation_id',
        details_json->>'trace_id',
        details_json->>'runtime_action_id',
        details_json->>'job_id'
    )), '')
WHERE correlation_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_activity_log_tenant_runtime_created
    ON activity_log (tenant_id, runtime_scope_key, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_activity_log_tenant_server_created
    ON activity_log (tenant_id, server_scope_key, created_at DESC, id DESC)
    WHERE server_scope_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_activity_log_tenant_service_created
    ON activity_log (tenant_id, service_scope_key, created_at DESC, id DESC)
    WHERE service_scope_key IS NOT NULL;
