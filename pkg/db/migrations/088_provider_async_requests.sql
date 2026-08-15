-- Durable correlation for provider APIs that acknowledge mutations before
-- their asynchronous request has reached a terminal state.

CREATE TABLE IF NOT EXISTS provider_async_requests (
    tenant_id text NOT NULL,
    operation_id text NOT NULL,
    provider_id text NOT NULL,
    request_id text NOT NULL,
    status_ref text NOT NULL,
    target_native_ref text NOT NULL,
    state text NOT NULL,
    failure_code text,
    failure_message text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, operation_id),
    UNIQUE (provider_id, request_id),
    FOREIGN KEY (tenant_id, operation_id)
        REFERENCES provider_operations (tenant_id, operation_id) ON DELETE RESTRICT,
    CHECK (state IN ('QUEUED', 'RUNNING', 'DONE', 'FAILED'))
);

CREATE INDEX IF NOT EXISTS idx_provider_async_requests_active
    ON provider_async_requests (provider_id, state, updated_at)
    WHERE state IN ('QUEUED', 'RUNNING');
