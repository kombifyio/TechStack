-- A native Windows client joins the existing browser OIDC login with a
-- short-lived, single-use exchange. Only hashes and verified identity claims
-- are stored; no browser cookie or provider token is persisted here.
CREATE TABLE IF NOT EXISTS native_login_handoffs (
    id text PRIMARY KEY,
    client_key_hash text NOT NULL,
    verifier_hash text NOT NULL,
    callback_port integer NOT NULL CHECK (callback_port BETWEEN 63690 AND 63694),
    callback_state text NOT NULL,
    ticket_hash text UNIQUE,
    claims_json jsonb,
    session_expires_at bigint,
    origin_expires_at bigint,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (verifier_hash ~ '^[a-f0-9]{64}$'),
    CHECK (ticket_hash IS NULL OR ticket_hash ~ '^[a-f0-9]{64}$'),
    CHECK (expires_at <= created_at + interval '5 minutes')
);

CREATE INDEX IF NOT EXISTS idx_native_login_handoffs_expiry
    ON native_login_handoffs (expires_at);
CREATE INDEX IF NOT EXISTS idx_native_login_handoffs_client_active
    ON native_login_handoffs (client_key_hash, expires_at)
    WHERE consumed_at IS NULL;
