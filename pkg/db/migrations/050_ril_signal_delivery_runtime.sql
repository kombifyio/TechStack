-- Cross-tenant wake-up index for the process-wide RIL signal publisher.
-- It contains tenant identifiers only; payloads stay in the forced-RLS outbox
-- and are claimed after the worker establishes app.tenant_id.

CREATE TABLE IF NOT EXISTS ril_signal_delivery_tenants (
    tenant_id text PRIMARY KEY,
    pending_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

ALTER TABLE ril_signal_outbox
    ADD COLUMN IF NOT EXISTS action_card_id text;

UPDATE ril_signal_outbox
SET action_card_id = 'ril-action-card:' || signal_id
WHERE action_card_id IS NULL OR BTRIM(action_card_id) = '';

ALTER TABLE ril_signal_outbox
    ALTER COLUMN action_card_id SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_ril_signal_outbox_action_card
    ON ril_signal_outbox (tenant_id, action_card_id);

CREATE OR REPLACE FUNCTION wake_ril_signal_delivery_tenant()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    INSERT INTO public.ril_signal_delivery_tenants (tenant_id, pending_at)
    VALUES (NEW.tenant_id, clock_timestamp())
    ON CONFLICT (tenant_id) DO UPDATE
      SET pending_at = LEAST(ril_signal_delivery_tenants.pending_at, EXCLUDED.pending_at);
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS ril_signal_outbox_wake_delivery ON ril_signal_outbox;
CREATE TRIGGER ril_signal_outbox_wake_delivery
AFTER INSERT OR UPDATE OF available_at, delivered_at, failed_at
ON ril_signal_outbox
FOR EACH ROW
WHEN (NEW.delivered_at IS NULL AND NEW.failed_at IS NULL)
EXECUTE FUNCTION wake_ril_signal_delivery_tenant();

INSERT INTO ril_signal_delivery_tenants (tenant_id, pending_at)
SELECT tenant_id, MIN(available_at)
FROM ril_signal_outbox
WHERE delivered_at IS NULL AND failed_at IS NULL
GROUP BY tenant_id
ON CONFLICT (tenant_id) DO UPDATE
SET pending_at = LEAST(ril_signal_delivery_tenants.pending_at, EXCLUDED.pending_at);
