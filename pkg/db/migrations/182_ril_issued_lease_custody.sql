SELECT pg_catalog.set_config('search_path',pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',true);
-- The tenant-scoped execution stores only short-lived encrypted authority.
ALTER TABLE ril_action_cards ADD COLUMN IF NOT EXISTS issued_lease_ciphertext bytea;
ALTER TABLE ril_action_cards ADD COLUMN IF NOT EXISTS issued_lease_expires_at timestamptz;
ALTER TABLE ril_action_cards ADD CONSTRAINT ril_action_cards_issued_lease_pair CHECK (
  (issued_lease_ciphertext IS NULL AND issued_lease_expires_at IS NULL)
  OR (issued_lease_ciphertext IS NOT NULL AND issued_lease_expires_at IS NOT NULL)
);

-- A secret-free index permits bounded crash recovery without granting the
-- runtime cross-tenant access to the RLS-protected cards themselves.
CREATE TABLE techstack_ril_lease_expiry_index (
  tenant_id text NOT NULL,
  card_id text NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, card_id)
);
REVOKE ALL ON techstack_ril_lease_expiry_index FROM PUBLIC;
CREATE INDEX techstack_ril_lease_expiry_due ON techstack_ril_lease_expiry_index (expires_at, tenant_id, card_id);
CREATE FUNCTION techstack_project_ril_lease_expiry() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
DECLARE scoped text := NULLIF(current_setting('app.tenant_id',true),'');
BEGIN
  IF TG_OP <> 'INSERT' AND OLD.tenant_id IS DISTINCT FROM scoped THEN
    RAISE EXCEPTION 'RIL expiry projection requires exact tenant scope' USING ERRCODE='42501';
  END IF;
  IF TG_OP <> 'DELETE' AND NEW.tenant_id IS DISTINCT FROM scoped THEN
    RAISE EXCEPTION 'RIL expiry projection requires exact tenant scope' USING ERRCODE='42501';
  END IF;
  IF TG_OP <> 'INSERT' THEN
    DELETE FROM techstack_ril_lease_expiry_index WHERE tenant_id=OLD.tenant_id AND card_id=OLD.id;
  END IF;
  IF TG_OP <> 'DELETE' AND NEW.issued_lease_expires_at IS NOT NULL THEN
    INSERT INTO techstack_ril_lease_expiry_index (tenant_id,card_id,expires_at)
    VALUES (NEW.tenant_id,NEW.id,NEW.issued_lease_expires_at)
    ON CONFLICT (tenant_id,card_id) DO UPDATE SET expires_at=EXCLUDED.expires_at;
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION techstack_project_ril_lease_expiry() FROM PUBLIC;
CREATE TRIGGER ril_lease_expiry_insert AFTER INSERT ON ril_action_cards FOR EACH ROW
WHEN (NEW.issued_lease_expires_at IS NOT NULL) EXECUTE FUNCTION techstack_project_ril_lease_expiry();
CREATE TRIGGER ril_lease_expiry_update AFTER UPDATE OF issued_lease_ciphertext, issued_lease_expires_at, tenant_id, id ON ril_action_cards FOR EACH ROW
WHEN (OLD.issued_lease_expires_at IS NOT NULL OR NEW.issued_lease_expires_at IS NOT NULL) EXECUTE FUNCTION techstack_project_ril_lease_expiry();
CREATE TRIGGER ril_lease_expiry_delete AFTER DELETE ON ril_action_cards FOR EACH ROW
WHEN (OLD.issued_lease_expires_at IS NOT NULL) EXECUTE FUNCTION techstack_project_ril_lease_expiry();
CREATE FUNCTION techstack_list_due_ril_leases() RETURNS TABLE(tenant_id text,card_id text)
LANGUAGE sql SECURITY DEFINER SET search_path FROM CURRENT AS $$
  SELECT e.tenant_id,e.card_id FROM techstack_ril_lease_expiry_index e
  WHERE e.expires_at<=clock_timestamp() ORDER BY e.expires_at,e.tenant_id,e.card_id LIMIT 100
$$;
REVOKE ALL ON FUNCTION techstack_list_due_ril_leases() FROM PUBLIC;

DO $ril_expiry_search_path$
DECLARE n text:=current_schema(); signature text;
BEGIN
  FOREACH signature IN ARRAY ARRAY['techstack_project_ril_lease_expiry()','techstack_list_due_ril_leases()'] LOOP
    EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog,%I,pg_temp',n,signature,n);
  END LOOP;
END $ril_expiry_search_path$;
