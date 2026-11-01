-- A managed target's SSH host-key pin belongs to the managed server whose
-- bootstrap observed the key, not to its address forever (platform-cesjv.2).
-- Providers recycle public addresses: centron gave a new server the address
-- of a server decommissioned earlier, and the address pin of migration 174,
-- which no runtime role can remove, refused every bootstrap of the new server
-- ("runtime target host key changed") until an operator deleted the row.
--
-- One managed lease is one provider server (a recreate creates a new lease,
-- and a lease's resource generation is immutable), so a pin is bound to the
-- lease that took it:
--
-- runtime_host_key_pins.lease_binding: an opaque digest of the tenant and
-- lease whose bootstrap took the pin, so the tenant-free known_hosts table
-- names no tenant. The writer requests a binding with the transaction-local
-- setting app.runtime_host_key_lease inside the lease's tenant scope; the
-- admit trigger keeps it only for a lease of that tenant whose
-- provider-attested address is the pinned host, refuses a pin for a lease
-- already proven absent, and ignores any binding a writer supplies itself.
-- The runtime has no UPDATE, so a binding never changes afterwards.
--
-- Retirement: when a decommission operation of the lease reaches
-- succeeded/absent for the lease's current resource generation and a
-- provider_api absence observation exists for it, the SECURITY DEFINER
-- trigger on provider_operations deletes the pins bound to that lease. The
-- server that presented the key is then proven gone at the provider, so the
-- next server the provider gives the address starts a fresh pin. No runtime
-- role receives DELETE on the pins or EXECUTE on these functions. A pin whose
-- server is not proven absent keeps refusing a changed key as before. The
-- proof is the provider ledger the capacity release already trusts: a role
-- that can forge a tenant's absence observation can retire that tenant's pins.
--
-- runtime_host_key_pin_retirements: one row per retired pin with the absence
-- evidence that retired it. A retired pin can survive outside the database in
-- the pin file of a disk-backed instance; the runtime reads this table to
-- drop such a file pin instead of trusting it, and the admit trigger refuses
-- to re-import a retired pin from a disk, whatever binary runs the import.
--
-- Backfill of the pins taken before this migration (all unbound). Only a
-- lease that existed when the pin was taken can have taken it. A pin whose
-- address every such lease records only while proven absent at the provider
-- is retired; a pin whose address exactly one such lease records without that
-- proof is bound to it, so it retires with it. Every other pin stays unbound.
--
-- Additive: a binary that predates this migration inserts unbound pins, which
-- the table still accepts, and ignores the retirement table.

SET LOCAL lock_timeout = '5s';
SELECT pg_catalog.set_config(
    'search_path',
    pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',
    true
);

ALTER TABLE runtime_host_key_pins ADD COLUMN IF NOT EXISTS lease_binding text;
ALTER TABLE runtime_host_key_pins DROP CONSTRAINT IF EXISTS runtime_host_key_pins_lease_binding;
ALTER TABLE runtime_host_key_pins ADD CONSTRAINT runtime_host_key_pins_lease_binding
    CHECK (lease_binding IS NULL OR lease_binding ~ '^[0-9a-f]{64}$');
CREATE INDEX IF NOT EXISTS runtime_host_key_pins_lease_binding_idx
    ON runtime_host_key_pins (lease_binding)
    WHERE lease_binding IS NOT NULL;

CREATE TABLE IF NOT EXISTS runtime_host_key_pin_retirements (
    address text NOT NULL,
    fingerprint text NOT NULL,
    source text NOT NULL,
    pinned_at timestamptz NOT NULL,
    lease_binding text NOT NULL CHECK (lease_binding ~ '^[0-9a-f]{64}$'),
    evidence_ref text NOT NULL CHECK (evidence_ref LIKE 'provider-evidence://%'),
    reason text NOT NULL CHECK (reason IN ('provider_absence_verified', 'backfill_provider_absence_verified')),
    retired_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (address, fingerprint, pinned_at)
);
REVOKE ALL ON TABLE runtime_host_key_pin_retirements FROM PUBLIC;

CREATE OR REPLACE FUNCTION runtime_host_key_pin_lease_binding(p_tenant_id text, p_lease_id text)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
SET search_path FROM CURRENT
AS $$
    SELECT pg_catalog.encode(
        pg_catalog.sha256(pg_catalog.convert_to(p_tenant_id || pg_catalog.chr(31) || p_lease_id, 'UTF8')),
        'hex'
    )
$$;

-- The host part of a normalized pin address is compared with the addresses
-- the lease authority recorded for the lease (provider control projects the
-- provider's public IPv4 into runtime_public_ip and runtime_ssh_host). Reads
-- run as the owner under the writer's tenant scope (FORCE RLS), so a writer
-- cannot see or bind another tenant's lease.
CREATE OR REPLACE FUNCTION runtime_host_key_pin_admit()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    pinned_host text := pg_catalog.split_part(NEW.address, ':', 1);
    scoped_tenant text := NULLIF(pg_catalog.current_setting('app.tenant_id', true), '');
    requested_lease text := NULLIF(btrim(pg_catalog.current_setting('app.runtime_host_key_lease', true)), '');
BEGIN
    NEW.lease_binding := NULL;
    IF NEW.source = 'disk_import' THEN
        IF EXISTS (
            SELECT 1
            FROM runtime_host_key_pin_retirements AS retired
            WHERE retired.address = NEW.address
              AND retired.fingerprint = NEW.fingerprint
        ) THEN
            RETURN NULL;
        END IF;
        RETURN NEW;
    END IF;
    IF requested_lease IS NULL OR scoped_tenant IS NULL THEN
        RETURN NEW;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM techstack_vm_leases AS lease
        JOIN provider_operations AS operation
          ON operation.tenant_id = lease.tenant_id
         AND operation.lease_id = lease.id
         AND operation.operation = 'decommission'
         AND operation.status = 'succeeded'
         AND operation.phase = 'absent'
         AND operation.command_json #>> '{command,resource_generation_id}' = lease.resource_generation_id::text
        JOIN provider_absence_observations AS observation
          ON observation.tenant_id = operation.tenant_id
         AND observation.operation_id = operation.operation_id
         AND observation.observation = 'absent'
         AND observation.source = 'provider_api'
        WHERE lease.tenant_id = scoped_tenant
          AND lease.id = requested_lease
    ) THEN
        RAISE EXCEPTION 'runtime host key pin refused: lease % is proven absent at the provider', requested_lease
            USING ERRCODE = '55000';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM techstack_vm_leases AS lease
        WHERE lease.tenant_id = scoped_tenant
          AND lease.id = requested_lease
          AND pinned_host IN (
              lower(btrim(lease.lease_json #>> '{metadata,runtime_public_ip}')),
              lower(btrim(lease.lease_json #>> '{metadata,runtime_ssh_host}')),
              lower(btrim(lease.lease_json #>> '{metadata,node_public_ip}')),
              lower(btrim(lease.lease_json #>> '{metadata,public_ip}'))
          )
    ) THEN
        NEW.lease_binding := runtime_host_key_pin_lease_binding(scoped_tenant, requested_lease);
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS runtime_host_key_pin_admit ON runtime_host_key_pins;
CREATE TRIGGER runtime_host_key_pin_admit
BEFORE INSERT ON runtime_host_key_pins
FOR EACH ROW EXECUTE FUNCTION runtime_host_key_pin_admit();

-- Runs in the provider-control transaction that records the terminal
-- receipt, under that transaction's tenant scope: the owner reads the lease
-- and the absence observation through the same tenant policy, so it acts
-- only on the writer's own tenant.
CREATE OR REPLACE FUNCTION runtime_host_key_pins_retire_absent_lease()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
    absence_ref text;
    binding text;
BEGIN
    IF NEW.operation <> 'decommission' OR NEW.status <> 'succeeded' OR NEW.phase <> 'absent' THEN
        RETURN NULL;
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.status = NEW.status AND OLD.phase = NEW.phase THEN
        RETURN NULL;
    END IF;
    IF NEW.tenant_id IS DISTINCT FROM NULLIF(pg_catalog.current_setting('app.tenant_id', true), '') THEN
        RETURN NULL;
    END IF;
    SELECT observation.evidence_ref INTO absence_ref
    FROM provider_absence_observations AS observation
    JOIN techstack_vm_leases AS lease
      ON lease.tenant_id = NEW.tenant_id
     AND lease.id = NEW.lease_id
    WHERE observation.tenant_id = NEW.tenant_id
      AND observation.operation_id = NEW.operation_id
      AND observation.observation = 'absent'
      AND observation.source = 'provider_api'
      AND NEW.command_json #>> '{command,resource_generation_id}' = lease.resource_generation_id::text
    ORDER BY observation.binding_id, observation.evidence_ref
    LIMIT 1;
    IF absence_ref IS NULL THEN
        RETURN NULL;
    END IF;
    binding := runtime_host_key_pin_lease_binding(NEW.tenant_id, NEW.lease_id);
    WITH retired AS (
        DELETE FROM runtime_host_key_pins AS pin
        WHERE pin.lease_binding = binding
        RETURNING pin.address, pin.fingerprint, pin.source, pin.pinned_at
    )
    INSERT INTO runtime_host_key_pin_retirements (
        address, fingerprint, source, pinned_at, lease_binding, evidence_ref, reason
    )
    SELECT address, fingerprint, source, pinned_at, binding, absence_ref, 'provider_absence_verified'
    FROM retired
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS runtime_host_key_pins_retire_absent_lease ON provider_operations;
CREATE TRIGGER runtime_host_key_pins_retire_absent_lease
AFTER INSERT OR UPDATE OF status, phase ON provider_operations
FOR EACH ROW EXECUTE FUNCTION runtime_host_key_pins_retire_absent_lease();

-- Backfill. The leases recording a pinned host are read through a transient
-- owner-only SELECT policy that is removed again before anything is decided;
-- each lease's absence proof is then read under its own tenant scope, so
-- FORCE RLS stays in force for the provider ledger.
DO $pin_lifecycle_snapshot$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_class AS c
        WHERE c.oid = pg_catalog.to_regclass('techstack_vm_leases')
          AND c.relowner = (SELECT oid FROM pg_catalog.pg_roles WHERE rolname = current_user)
          AND c.relrowsecurity
          AND c.relforcerowsecurity
    ) THEN
        RAISE EXCEPTION 'host key pin backfill requires the exact migration owner and FORCE RLS'
            USING ERRCODE = '42501';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_catalog.pg_policy AS policy
        WHERE policy.polrelid = pg_catalog.to_regclass('techstack_vm_leases')
          AND ((NOT policy.polpermissive AND policy.polcmd IN ('r', '*'))
               OR policy.polname = 'runtime_host_key_pin_lifecycle_snapshot')
    ) THEN
        RAISE EXCEPTION 'host key pin backfill snapshot authority is ambiguous or restrictive'
            USING ERRCODE = '42501';
    END IF;
END
$pin_lifecycle_snapshot$;
-- Static, so the expand-only lint sees this file create the policy it drops below.
CREATE POLICY runtime_host_key_pin_lifecycle_snapshot ON techstack_vm_leases FOR SELECT TO CURRENT_USER USING (true);

CREATE TEMP TABLE runtime_host_key_pin_lifecycle_candidates ON COMMIT DROP AS
SELECT pin.address, lease.tenant_id, lease.id AS lease_id, lease.resource_generation_id,
       NULL::text AS absence_ref
FROM runtime_host_key_pins AS pin
JOIN techstack_vm_leases AS lease
  ON lease.created_at <= pin.pinned_at
 AND pg_catalog.split_part(pin.address, ':', 1) IN (
      lower(btrim(lease.lease_json #>> '{metadata,runtime_public_ip}')),
      lower(btrim(lease.lease_json #>> '{metadata,runtime_ssh_host}')),
      lower(btrim(lease.lease_json #>> '{metadata,node_public_ip}')),
      lower(btrim(lease.lease_json #>> '{metadata,public_ip}'))
  )
WHERE pin.lease_binding IS NULL;
REVOKE ALL ON TABLE runtime_host_key_pin_lifecycle_candidates FROM PUBLIC;
DROP POLICY runtime_host_key_pin_lifecycle_snapshot ON techstack_vm_leases;

DO $pin_lifecycle_absence$
DECLARE
    scope record;
    previous_tenant text := current_setting('app.tenant_id', true);
BEGIN
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_policy WHERE polname = 'runtime_host_key_pin_lifecycle_snapshot') THEN
        RAISE EXCEPTION 'host key pin backfill transient read authority not removed' USING ERRCODE = '42501';
    END IF;
    FOR scope IN SELECT DISTINCT tenant_id FROM runtime_host_key_pin_lifecycle_candidates ORDER BY tenant_id LOOP
        PERFORM set_config('app.tenant_id', scope.tenant_id, true);
        UPDATE runtime_host_key_pin_lifecycle_candidates AS candidate
        SET absence_ref = proof.evidence_ref
        FROM (
            SELECT DISTINCT ON (operation.lease_id, operation.command_json #>> '{command,resource_generation_id}')
                   operation.lease_id, observation.evidence_ref,
                   operation.command_json #>> '{command,resource_generation_id}' AS resource_generation_id
            FROM provider_operations AS operation
            JOIN provider_absence_observations AS observation
              ON observation.tenant_id = operation.tenant_id
             AND observation.operation_id = operation.operation_id
             AND observation.observation = 'absent'
             AND observation.source = 'provider_api'
            WHERE operation.tenant_id = scope.tenant_id
              AND operation.operation = 'decommission'
              AND operation.status = 'succeeded'
              AND operation.phase = 'absent'
            ORDER BY operation.lease_id, operation.command_json #>> '{command,resource_generation_id}',
                     operation.updated_at DESC, observation.binding_id, observation.evidence_ref
        ) AS proof
        WHERE candidate.tenant_id = scope.tenant_id
          AND candidate.lease_id = proof.lease_id
          AND candidate.resource_generation_id::text = proof.resource_generation_id;
    END LOOP;
    PERFORM set_config('app.tenant_id', COALESCE(previous_tenant, ''), true);
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tenant_id', COALESCE(previous_tenant, ''), true);
    RAISE;
END
$pin_lifecycle_absence$;

DO $pin_lifecycle_backfill$
DECLARE
    retired_count integer;
    bound_count integer;
BEGIN
    WITH decided AS (
        SELECT candidate.address
        FROM runtime_host_key_pin_lifecycle_candidates AS candidate
        GROUP BY candidate.address
        HAVING bool_and(candidate.absence_ref IS NOT NULL)
    ),
    evidence AS (
        SELECT DISTINCT ON (candidate.address)
               candidate.address, candidate.tenant_id, candidate.lease_id, candidate.absence_ref
        FROM runtime_host_key_pin_lifecycle_candidates AS candidate
        JOIN decided ON decided.address = candidate.address
        ORDER BY candidate.address, candidate.tenant_id, candidate.lease_id
    ),
    retired AS (
        DELETE FROM runtime_host_key_pins AS pin
        USING evidence
        WHERE pin.address = evidence.address
          AND pin.lease_binding IS NULL
        RETURNING pin.address, pin.fingerprint, pin.source, pin.pinned_at,
                  runtime_host_key_pin_lease_binding(evidence.tenant_id, evidence.lease_id) AS lease_binding,
                  evidence.absence_ref
    ),
    recorded AS (
        INSERT INTO runtime_host_key_pin_retirements (
            address, fingerprint, source, pinned_at, lease_binding, evidence_ref, reason
        )
        SELECT address, fingerprint, source, pinned_at, lease_binding, absence_ref,
               'backfill_provider_absence_verified'
        FROM retired
        ON CONFLICT DO NOTHING
        RETURNING 1
    )
    SELECT count(*) INTO retired_count FROM recorded;

    WITH holder AS (
        SELECT candidate.address, min(candidate.tenant_id) AS tenant_id, min(candidate.lease_id) AS lease_id
        FROM runtime_host_key_pin_lifecycle_candidates AS candidate
        WHERE candidate.absence_ref IS NULL
        GROUP BY candidate.address
        HAVING count(*) = 1
    ),
    bound AS (
        UPDATE runtime_host_key_pins AS pin
        SET lease_binding = runtime_host_key_pin_lease_binding(holder.tenant_id, holder.lease_id)
        FROM holder
        WHERE pin.address = holder.address
          AND pin.lease_binding IS NULL
        RETURNING 1
    )
    SELECT count(*) INTO bound_count FROM bound;

    RAISE NOTICE 'runtime host key pin lifecycle backfill: retired=% bound=% unbound=%',
        retired_count, bound_count,
        (SELECT count(*) FROM runtime_host_key_pins WHERE lease_binding IS NULL);
END
$pin_lifecycle_backfill$;

DO $pin_lifecycle_posture$
DECLARE
    active_schema text := pg_catalog.current_schema();
    owner_role text := current_user;
    application_role text := 'techstack_application';
    signature text;
BEGIN
    FOREACH signature IN ARRAY ARRAY[
        'runtime_host_key_pin_lease_binding(text,text)',
        'runtime_host_key_pin_admit()',
        'runtime_host_key_pins_retire_absent_lease()'
    ] LOOP
        EXECUTE pg_catalog.format('ALTER FUNCTION %I.%s OWNER TO %I', active_schema, signature, owner_role);
        EXECUTE pg_catalog.format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog,%I,pg_temp',
            active_schema, signature, active_schema);
        EXECUTE pg_catalog.format('REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC', active_schema, signature);
    END LOOP;
    IF pg_catalog.to_regrole(application_role) IS NULL THEN
        RETURN;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_catalog.pg_roles
        WHERE rolname = application_role
          AND (rolsuper OR rolbypassrls)
    ) THEN
        RAISE EXCEPTION 'host key pin lifecycle grants refuse a superuser or BYPASSRLS role'
            USING ERRCODE = '42501';
    END IF;
    EXECUTE pg_catalog.format('GRANT SELECT ON TABLE %I.runtime_host_key_pin_retirements TO %I',
        active_schema, application_role);
END
$pin_lifecycle_posture$;
