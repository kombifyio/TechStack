CREATE TABLE home_assistant_owner_bindings (
 tenant_id text NOT NULL REFERENCES techstack_tenants(id),
 binding_ref text NOT NULL,
 stack_id text NOT NULL,
 runtime_agent_id text NOT NULL,
 plan_hash text NOT NULL,
 origin text NOT NULL CHECK(origin IN ('new','existing','imported')),
 management_scope text NOT NULL CHECK(management_scope IN ('observed','managed')),
 request_json jsonb NOT NULL,
 endpoint text,
 token_enc text,
 settings_json jsonb NOT NULL DEFAULT '{}'::jsonb,
 configure_granted boolean NOT NULL DEFAULT false,
 lease_id text,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,binding_ref),
 FOREIGN KEY(tenant_id,stack_id) REFERENCES stacks(tenant_id,id),
 FOREIGN KEY(tenant_id,runtime_agent_id) REFERENCES workers(tenant_id,id)
);
ALTER TABLE home_assistant_owner_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE home_assistant_owner_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON home_assistant_owner_bindings
 USING(tenant_id=current_setting('app.tenant_id',true))
 WITH CHECK(tenant_id=current_setting('app.tenant_id',true));
