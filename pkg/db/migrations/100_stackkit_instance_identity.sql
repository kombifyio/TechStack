-- Separate Techstack's kit-deployment identity from the StackKits-owned
-- StackInstance identity carried by StackSpec/ResolvedPlan stackId.
--
-- `stacks.id` remains the Techstack authority used by jobs, servers, leases,
-- and routes. `stackkit_instance_id` is the optional external contract
-- identity of that deployment. StackKits scopes uniqueness to one Fleet, which
-- ADR-036 maps to homelabs.id.

SET LOCAL lock_timeout = '5s';

ALTER TABLE stacks
    ADD COLUMN IF NOT EXISTS stackkit_instance_id text;

ALTER TABLE stacks DROP CONSTRAINT IF EXISTS stacks_stackkit_instance_id_not_blank;
ALTER TABLE stacks
    ADD CONSTRAINT stacks_stackkit_instance_id_not_blank
    CHECK (stackkit_instance_id IS NULL OR BTRIM(stackkit_instance_id) <> '')
    NOT VALID;

-- Existing wizard v2 deployments already retain the exact StackSpec in
-- config_json. Backfill only identities that are unambiguous within their
-- Homelab. A historical collision remains NULL instead of making startup fail
-- or inventing a replacement StackKits identity.
ALTER TABLE stacks NO FORCE ROW LEVEL SECURITY;
ALTER TABLE stacks DISABLE ROW LEVEL SECURITY;
ALTER TABLE stacks DISABLE TRIGGER set_stacks_updated_at;

WITH candidates AS (
    SELECT id,
           tenant_id,
           homelab_id,
           COALESCE(
               NULLIF(BTRIM(config_json #>> '{stack_spec_v2,metadata,stackId}'), ''),
               NULLIF(BTRIM(config_json #>> '{stack_spec_v2,metadata,name}'), '')
           ) AS stackkit_instance_id
    FROM stacks
    WHERE deleted_at IS NULL
      AND homelab_id IS NOT NULL
      AND stackkit_instance_id IS NULL
), unambiguous AS (
    SELECT id, stackkit_instance_id
    FROM (
        SELECT id,
               stackkit_instance_id,
               count(*) OVER (
                   PARTITION BY tenant_id, homelab_id, stackkit_instance_id
               ) AS identity_count
        FROM candidates
        WHERE stackkit_instance_id IS NOT NULL
    ) ranked
    WHERE identity_count = 1
)
UPDATE stacks target
SET stackkit_instance_id = unambiguous.stackkit_instance_id
FROM unambiguous
WHERE target.id = unambiguous.id;

ALTER TABLE stacks ENABLE TRIGGER set_stacks_updated_at;
ALTER TABLE stacks ENABLE ROW LEVEL SECURITY;
ALTER TABLE stacks FORCE ROW LEVEL SECURITY;

CREATE UNIQUE INDEX IF NOT EXISTS idx_stacks_homelab_stackkit_instance_active
    ON stacks (tenant_id, homelab_id, stackkit_instance_id)
    WHERE deleted_at IS NULL
      AND homelab_id IS NOT NULL
      AND stackkit_instance_id IS NOT NULL;
