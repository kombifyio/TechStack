-- Guard heartbeats and inventory projections apply a server event: they update
-- servers and insert a server_registry_outbox row. Three AFTER trigger
-- functions turned each such event into a tenant-wide lock held until commit,
-- so one inventory projection that stays open for seconds stalled the
-- heartbeats of every other server in its tenant:
--   * provider_control_refresh_stale_capacity_recovery_tenant (093) took the
--     tenant's stale-capacity advisory lock and rebuilt its candidates on every
--     update of servers;
--   * server_registry_wake_sweep_tenant (072, authority from 083) upserted the
--     tenant's sweep directory row, a row lock, on every heartbeat;
--   * server_registry_wake_outbox_prune_tenant (072, authority from 083)
--     upserted the tenant's prune directory row on every outbox insert.
-- The advisory lock is taken first, so only it shows in pg_locks; lifting it
-- alone would move the wait to the two directory rows.
--
-- The candidate set reads servers only through tenant_id, id, lease_id,
-- desired_state, lifecycle_state and decommissioned_at, so an update of servers
-- that changes none of them leaves it unchanged and no longer refreshes it. The
-- tenant checks still run first. Inserts, deletes and the other four source
-- tables refresh exactly as before, and all their writers run READ COMMITTED,
-- so the candidates stay exactly what 093 maintained.
--
-- A heartbeat of a server that was already tracked no longer writes the sweep
-- directory while the tenant entry already wakes the sweeper no later than the
-- new heartbeat. That read takes no row lock. Compaction reads the committed,
-- tracked OLD row, so it keeps an entry no later than OLD.last_heartbeat_at;
-- servers entering the tracked set still upsert exactly as before.
--
-- An outbox insert no longer writes the prune directory while the tenant entry
-- is already no later than the new row; the oldest tracked row only moves
-- through prune compaction, which recomputes it. Both skips read without a row
-- lock. A compaction that commits a stale result between that read and the
-- event's commit leaves the tenant late or absent until its next server event,
-- which rewrites the entry; the previous upsert had the same stale-overwrite
-- race when compaction committed after it. Compaction deletes a prune entry
-- only for a tenant whose outbox it saw empty, so a tenant that then goes idle
-- again can stay off the prune and projection-tenant pages until it next
-- reports.
--
-- Only the functions change. Re-scoping the triggers would need SHARE ROW
-- EXCLUSIVE on servers, which the long projection transactions held off past
-- the lock timeout most of the time (measured 2026-10-21); replacing a function
-- takes no table lock.
SET LOCAL lock_timeout = '5s';

DO $servers_trigger_scope_owner$
DECLARE owner_oid oid := (SELECT oid FROM pg_catalog.pg_roles WHERE rolname = current_user);
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class WHERE oid = 'public.servers'::regclass
            AND relowner = owner_oid AND relrowsecurity AND relforcerowsecurity)
        OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class
            WHERE oid = 'public.server_registry_sweep_tenants'::regclass AND relowner = owner_oid)
        OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class
            WHERE oid = 'public.server_registry_outbox_prune_tenants'::regclass AND relowner = owner_oid)
        OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_proc
            WHERE oid = 'public.server_registry_wake_outbox_prune_tenant()'::regprocedure
              AND proowner = owner_oid AND prosecdef)
        OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_proc
            WHERE oid = 'public.provider_control_refresh_stale_capacity_recovery_tenant()'::regprocedure
              AND proowner = owner_oid AND prosecdef)
        OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_proc
            WHERE oid = 'public.server_registry_wake_sweep_tenant()'::regprocedure
              AND proowner = owner_oid AND prosecdef)
        OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = 'public' AND nspowner = owner_oid)
        OR EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE oid = owner_oid AND (rolsuper OR rolbypassrls)) THEN
        RAISE EXCEPTION 'servers trigger scope requires the exact non-bypass migration owner and FORCE RLS'
            USING ERRCODE = '42501';
    END IF;
END $servers_trigger_scope_owner$;

CREATE OR REPLACE FUNCTION public.provider_control_refresh_stale_capacity_recovery_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    recovery_tenant_id text := COALESCE(NEW.tenant_id, OLD.tenant_id);
    scoped_tenant_id text := NULLIF(current_setting('app.tenant_id', true), '');
BEGIN
    IF recovery_tenant_id IS NULL OR BTRIM(recovery_tenant_id) = '' THEN
        RAISE EXCEPTION 'stale capacity recovery refresh requires an exact tenant'
            USING ERRCODE = '55000';
    END IF;
    IF scoped_tenant_id IS DISTINCT FROM recovery_tenant_id THEN
        RAISE EXCEPTION 'stale capacity recovery refresh requires the exact tenant scope'
            USING ERRCODE = '42501';
    END IF;

    -- plpgsql compiles a trigger function per table, so the servers columns
    -- are resolved only on servers.
    IF TG_TABLE_NAME = 'servers' AND TG_OP = 'UPDATE' THEN
        IF OLD.tenant_id IS NOT DISTINCT FROM NEW.tenant_id
            AND OLD.id IS NOT DISTINCT FROM NEW.id
            AND OLD.lease_id IS NOT DISTINCT FROM NEW.lease_id
            AND OLD.desired_state IS NOT DISTINCT FROM NEW.desired_state
            AND OLD.lifecycle_state IS NOT DISTINCT FROM NEW.lifecycle_state
            AND OLD.decommissioned_at IS NOT DISTINCT FROM NEW.decommissioned_at THEN
            RETURN NEW;
        END IF;
    END IF;

    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended(
            'providercontrol.stale-capacity-recovery/v1:' || recovery_tenant_id,
            0
        )
    );

    DELETE FROM provider_stale_capacity_recovery_candidates
    WHERE tenant_id = recovery_tenant_id;

    INSERT INTO provider_stale_capacity_recovery_candidates (
        tenant_id,
        lease_id,
        owner_subject_id,
        stack_id,
        refreshed_at
    )
    SELECT
        reservation.tenant_id,
        reservation.lease_id,
        reservation.owner_subject_id,
        lease.lease_json->'metadata'->>'stack_id',
        clock_timestamp()
    FROM managed_runtime_capacity_reservations AS reservation
    JOIN techstack_vm_leases AS lease
      ON lease.tenant_id = reservation.tenant_id
     AND lease.id = reservation.lease_id
     AND lease.resource_generation_id = reservation.resource_generation_id
    JOIN runtime_lease_execution_authorities AS authority
      ON authority.tenant_id = lease.tenant_id
     AND authority.lease_id = lease.id
     AND authority.execution_authority = 'techstack_provider_control'
    WHERE reservation.tenant_id = recovery_tenant_id
      AND NOT EXISTS (
          SELECT 1
          FROM managed_runtime_capacity_release_facts AS release
          WHERE release.tenant_id = reservation.tenant_id
            AND release.lease_id = reservation.lease_id
            AND release.resource_generation_id = reservation.resource_generation_id
      )
      AND (
          lease.cancelled_at IS NOT NULL
          OR lease.desired_state = 'absent'
          OR EXISTS (
              SELECT 1
              FROM servers AS server
              WHERE server.tenant_id = lease.tenant_id
                AND server.id = lease.server_id
                AND server.lease_id = lease.id
                AND server.desired_state = 'absent'
                AND (
                    server.lifecycle_state IN ('decommissioning', 'decommissioned')
                    OR server.decommissioned_at IS NOT NULL
                )
          )
      )
      AND COALESCE(lease.lease_json->'metadata'->>'stack_id', '') <> '';

    RETURN COALESCE(NEW, OLD);
END;
$$;

CREATE OR REPLACE FUNCTION public.server_registry_wake_sweep_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF TG_OP = 'UPDATE'
        AND OLD.tenant_id = NEW.tenant_id
        AND OLD.last_heartbeat_at IS NOT NULL
        AND OLD.connection_state IN ('connected', 'degraded', 'stale')
        AND OLD.lifecycle_state NOT IN ('decommissioning', 'decommissioned')
        AND EXISTS (
            SELECT 1
            FROM public.server_registry_sweep_tenants AS directory
            WHERE directory.tenant_id = NEW.tenant_id
              AND directory.earliest_heartbeat_at <= NEW.last_heartbeat_at
        ) THEN
        RETURN NEW;
    END IF;
    INSERT INTO public.server_registry_sweep_tenants (tenant_id, earliest_heartbeat_at, refreshed_at)
    VALUES (NEW.tenant_id, NEW.last_heartbeat_at, clock_timestamp())
    ON CONFLICT (tenant_id) DO UPDATE
      SET earliest_heartbeat_at = LEAST(
              server_registry_sweep_tenants.earliest_heartbeat_at,
              EXCLUDED.earliest_heartbeat_at
          ),
          refreshed_at = EXCLUDED.refreshed_at;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.server_registry_wake_outbox_prune_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.server_registry_outbox_prune_tenants AS directory
        WHERE directory.tenant_id = NEW.tenant_id
          AND directory.oldest_created_at <= NEW.created_at
    ) THEN
        RETURN NEW;
    END IF;
    INSERT INTO public.server_registry_outbox_prune_tenants (tenant_id, oldest_created_at, refreshed_at)
    VALUES (NEW.tenant_id, NEW.created_at, clock_timestamp())
    ON CONFLICT (tenant_id) DO UPDATE
      SET oldest_created_at = LEAST(
              server_registry_outbox_prune_tenants.oldest_created_at,
              EXCLUDED.oldest_created_at
          ),
          refreshed_at = EXCLUDED.refreshed_at;
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION public.provider_control_refresh_stale_capacity_recovery_tenant() IS
    'Refreshes stale native-capacity recovery candidates for one exact tenant under its FORCE-RLS scope; a servers update that changes none of the columns the candidates read is skipped.';
COMMENT ON FUNCTION public.server_registry_wake_sweep_tenant() IS
    'Wakes the observation sweeper when a server enters the tracked set or would be swept later than its heartbeat; steady heartbeats take no directory row lock.';
COMMENT ON FUNCTION public.server_registry_wake_outbox_prune_tenant() IS
    'Wakes the outbox prune for a tenant that has no entry or an entry later than the new outbox row; other inserts take no directory row lock.';
