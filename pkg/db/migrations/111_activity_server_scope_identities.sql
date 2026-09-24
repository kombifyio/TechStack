-- Extend the server scope backfill from migration 077 to every identity an
-- activity producer may carry. 077 only recognised server_id and node_id, so
-- rows whose details named the server by worker, agent or lease kept a NULL
-- server_scope_key and were only reachable through read-time alias matching.
--
-- The alias order here is the same authority as normalizeActivityEvent in
-- pkg/controlplane/store.go. Hostname is deliberately not an alias: it is a
-- display name, not an identity, it is not unique across servers and it
-- survives a rename.

SET LOCAL lock_timeout = '5s';

UPDATE activity_log
SET server_scope_key = NULLIF(BTRIM(COALESCE(
        details_json->>'worker_id',
        details_json->>'agent_id',
        details_json->>'agent',
        details_json->>'lease_id',
        details_json->>'runtime_lease_id'
    )), '')
WHERE server_scope_key IS NULL
  AND NULLIF(BTRIM(COALESCE(
        details_json->>'worker_id',
        details_json->>'agent_id',
        details_json->>'agent',
        details_json->>'lease_id',
        details_json->>'runtime_lease_id'
    )), '') IS NOT NULL;

-- runtime_scope_key derives from server scope, so rows that just gained one
-- and had no runtime scope follow the same rule as migration 077.
UPDATE activity_log
SET runtime_scope_key = 'server:' || server_scope_key
WHERE runtime_scope_key IS NULL
  AND server_scope_key IS NOT NULL;
