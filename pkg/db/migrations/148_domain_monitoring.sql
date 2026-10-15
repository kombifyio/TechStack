-- Independent domain evidence and durable condition episodes. Migration executes
-- only through governed Delivery; collectors never apply schema themselves.
ALTER TABLE stack_routing_desired ADD COLUMN IF NOT EXISTS expectations_json jsonb NOT NULL DEFAULT '{"dns":[],"tls":{}}'::jsonb;
CREATE TABLE IF NOT EXISTS stack_domain_monitoring (
 tenant_id text NOT NULL,
 id text NOT NULL CHECK (btrim(id) <> ''),
 owner_subject_id text NOT NULL,
 stack_id text NOT NULL,
 server_id text NOT NULL,
 domain text NOT NULL CHECK (btrim(domain) <> ''),
 routing_revision bigint NOT NULL DEFAULT 0 CHECK (routing_revision >= 0),
 snapshot_json jsonb NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
 last_attempt_at timestamptz NOT NULL,
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,stack_id,owner_subject_id) REFERENCES stacks(tenant_id,id,owner_subject_id) ON DELETE CASCADE,
 FOREIGN KEY (tenant_id,server_id,stack_id,owner_subject_id) REFERENCES servers(tenant_id,id,stack_id,owner_subject_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS stack_domain_monitoring_owner ON stack_domain_monitoring(tenant_id,owner_subject_id,stack_id);
ALTER TABLE stack_domain_monitoring ENABLE ROW LEVEL SECURITY;
ALTER TABLE stack_domain_monitoring FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON stack_domain_monitoring;
CREATE POLICY tenant_isolation ON stack_domain_monitoring USING (tenant_id = current_setting('app.tenant_id',true)) WITH CHECK (tenant_id = current_setting('app.tenant_id',true));
