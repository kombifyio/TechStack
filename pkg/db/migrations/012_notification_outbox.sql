-- Durable product-notification delivery for Monitoring and RIL transitions.
-- The worker claims across tenants; recipient and tenant binding are persisted
-- with the event so retries cannot accidentally target another account.

CREATE TABLE IF NOT EXISTS techstack_notification_outbox (
    idempotency_key text PRIMARY KEY,
    tenant_id text,
    auth0_user_id text NOT NULL,
    topic_slug text NOT NULL,
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    dispatch_id text,
    feed_item_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    CHECK (status IN ('pending', 'retrying', 'delivered', 'failed')),
    CHECK (attempts >= 0),
    CHECK (auth0_user_id <> ''),
    CHECK (topic_slug <> '')
);

CREATE INDEX IF NOT EXISTS idx_techstack_notification_outbox_due
    ON techstack_notification_outbox (status, next_attempt_at, updated_at)
    WHERE status IN ('pending', 'retrying');
