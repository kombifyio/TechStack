-- 044_homelabs.sql
-- Homelab umbrella over kit deployments (ADR-0036).
-- A homelab groups every kit deployment (= stacks row) one owner operates
-- inside a tenant. B2C keeps exactly one active homelab per (tenant, owner);
-- the partial unique index enforces that singleton. stacks.homelab_id is the
-- forward link: backfilled here, maintained by the wizard-run path from
-- phase 3 on — readers must not require it before then.
-- Per 001: no central Homelab member directory; owner_subject_id stays an
-- external subject reference, never a users-table FK.

CREATE TABLE IF NOT EXISTS homelabs (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
    owner_subject_id text NOT NULL,
    name text NOT NULL,
    intent_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CHECK (BTRIM(id) <> ''),
    CHECK (BTRIM(owner_subject_id) <> ''),
    CHECK (BTRIM(name) <> '')
);

CREATE INDEX IF NOT EXISTS idx_homelabs_tenant ON homelabs (tenant_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_homelabs_owner_singleton
    ON homelabs (tenant_id, owner_subject_id)
    WHERE deleted_at IS NULL;
-- Composite-FK target so stacks.homelab_id can never point across tenants
-- (FK checks bypass RLS; 024 established this pattern for servers -> stacks).
CREATE UNIQUE INDEX IF NOT EXISTS uq_homelabs_tenant_id ON homelabs (tenant_id, id);

-- RESTRICT instead of SET NULL: homelabs are soft-deleted (deleted_at), a hard
-- DELETE is not a product code path, and multi-column SET NULL would null
-- tenant_id as well.
ALTER TABLE stacks ADD COLUMN IF NOT EXISTS homelab_id text;
ALTER TABLE stacks DROP CONSTRAINT IF EXISTS fk_stacks_homelab_tenant;
ALTER TABLE stacks
    ADD CONSTRAINT fk_stacks_homelab_tenant
    FOREIGN KEY (tenant_id, homelab_id)
    REFERENCES homelabs (tenant_id, id)
    ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_stacks_homelab
    ON stacks (homelab_id)
    WHERE homelab_id IS NOT NULL;

-- Backfill: one homelab per (tenant, owner) that already operates live stacks.
-- Deterministic, injective ids (hash per component, no ambiguous separator)
-- keep both statements idempotent. Migrations run without app.tenant_id while
-- stacks AND techstack_tenants FORCE tenant RLS (002/006), so suspend RLS on
-- both only inside this migration transaction (016 pattern) — otherwise the
-- join reads zero rows and the backfill silently no-ops. homelabs gets its
-- RLS enabled after the backfill for the same reason. The tenants join skips
-- orphaned stack rows whose tenant no longer exists (stacks.tenant_id carries
-- no FK), so the homelabs.tenant_id FK cannot fail. The stacks updated_at
-- trigger is suspended so linking does not rewrite every stack's timestamp.
SET LOCAL lock_timeout = '5s';

ALTER TABLE stacks NO FORCE ROW LEVEL SECURITY;
ALTER TABLE stacks DISABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_tenants NO FORCE ROW LEVEL SECURITY;
ALTER TABLE techstack_tenants DISABLE ROW LEVEL SECURITY;
ALTER TABLE stacks DISABLE TRIGGER set_stacks_updated_at;

INSERT INTO homelabs (id, tenant_id, owner_subject_id, name)
SELECT 'hl-' || md5(md5(s.tenant_id) || md5(s.owner_subject_id)),
       s.tenant_id,
       s.owner_subject_id,
       'homelab'
FROM stacks s
JOIN techstack_tenants t ON t.id = s.tenant_id
WHERE s.deleted_at IS NULL
  AND s.owner_subject_id IS NOT NULL
  AND BTRIM(s.owner_subject_id) <> ''
GROUP BY s.tenant_id, s.owner_subject_id
ON CONFLICT DO NOTHING;

UPDATE stacks s
SET homelab_id = h.id
FROM homelabs h
WHERE s.deleted_at IS NULL
  AND s.homelab_id IS NULL
  AND h.deleted_at IS NULL
  AND h.tenant_id = s.tenant_id
  AND h.owner_subject_id = s.owner_subject_id;

ALTER TABLE stacks ENABLE TRIGGER set_stacks_updated_at;
ALTER TABLE techstack_tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE techstack_tenants FORCE ROW LEVEL SECURITY;
ALTER TABLE stacks ENABLE ROW LEVEL SECURITY;
ALTER TABLE stacks FORCE ROW LEVEL SECURITY;

ALTER TABLE homelabs ENABLE ROW LEVEL SECURITY;
ALTER TABLE homelabs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON homelabs;
CREATE POLICY tenant_isolation ON homelabs
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP TRIGGER IF EXISTS set_homelabs_updated_at ON homelabs;
CREATE TRIGGER set_homelabs_updated_at BEFORE UPDATE ON homelabs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
