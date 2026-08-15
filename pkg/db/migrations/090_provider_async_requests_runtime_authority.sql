-- Bind asynchronous provider-request correlation to the same tenant-scoped
-- provider-control runtime authority as the operation ledger.

ALTER TABLE provider_async_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_async_requests FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON provider_async_requests;
CREATE POLICY tenant_isolation ON provider_async_requests
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
