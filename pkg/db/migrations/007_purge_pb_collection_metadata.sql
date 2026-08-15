-- 007_purge_pb_collection_metadata.sql
--
-- Remove PocketBase _collections entries for tables whose Postgres schema is
-- now owned by pkg/db (migration 006). PocketBase stored metadata for these
-- collections in its _collections table; that metadata no longer matches the
-- actual table schemas and causes the startup tenant-backfill check to abort
-- with "missing_tenant_field" because PocketBase's field list doesn't include
-- the tenant_id column that migration 006 added.
--
-- Safe to run: _collections entries are only metadata; the actual Postgres
-- tables (nodes, auth_sessions, feature_audit) already have the correct schema
-- from migration 006. Removing the _collections entries makes
-- app.FindCollectionByNameOrId() return "not found" so the backfill check
-- skips these collections entirely.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema()
          AND table_name = '_collections'
    ) THEN
        DELETE FROM "_collections"
        WHERE name IN ('nodes', 'auth_sessions', 'feature_audit');
    END IF;
END $$;
