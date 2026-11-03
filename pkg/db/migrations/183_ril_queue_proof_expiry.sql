SELECT pg_catalog.set_config('search_path',pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp',true);
-- Existing queued proof ciphertext may outlive its sender. The projection holds
-- only tenant, command identity and expiry, never proof material or history.
LOCK TABLE typed_agent_commands IN SHARE ROW EXCLUSIVE MODE;
CREATE TABLE techstack_ril_queue_expiry_index (
  tenant_id text NOT NULL,
  command_id text NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,command_id)
);
REVOKE ALL ON techstack_ril_queue_expiry_index FROM PUBLIC;
CREATE INDEX techstack_ril_queue_expiry_due ON techstack_ril_queue_expiry_index (expires_at,tenant_id,command_id);

-- A temporary owner-only SELECT policy establishes the complete preexisting
-- snapshot without relaxing FORCE RLS for the runtime or any other principal.
DO $ril_queue_snapshot_admission$
DECLARE owner_oid oid:=(SELECT oid FROM pg_roles WHERE rolname=current_user);
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.oid='typed_agent_commands'::regclass AND c.relowner=owner_oid AND c.relrowsecurity AND c.relforcerowsecurity)
     OR EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid='typed_agent_commands'::regclass AND ((NOT p.polpermissive AND p.polcmd IN ('r','*')) OR p.polname='techstack_ril_queue_snapshot')) THEN
    RAISE EXCEPTION 'RIL queue snapshot requires the exact migration owner and unrestricted SELECT' USING ERRCODE='42501';
  END IF;
  EXECUTE format('CREATE POLICY techstack_ril_queue_snapshot ON typed_agent_commands FOR SELECT TO %I USING (true)',current_user);
END $ril_queue_snapshot_admission$;
INSERT INTO techstack_ril_queue_expiry_index (tenant_id,command_id,expires_at)
  SELECT tenant_id,command_id,ril_proof_expires_at FROM typed_agent_commands WHERE ril_proof_expires_at IS NOT NULL;
DO $ril_queue_snapshot_readback$
BEGIN
  IF EXISTS ((SELECT tenant_id,command_id,ril_proof_expires_at FROM typed_agent_commands WHERE ril_proof_expires_at IS NOT NULL
      EXCEPT SELECT tenant_id,command_id,expires_at FROM techstack_ril_queue_expiry_index)
    UNION ALL (SELECT tenant_id,command_id,expires_at FROM techstack_ril_queue_expiry_index
      EXCEPT SELECT tenant_id,command_id,ril_proof_expires_at FROM typed_agent_commands WHERE ril_proof_expires_at IS NOT NULL)) THEN
    RAISE EXCEPTION 'RIL queue expiry projection backfill incomplete' USING ERRCODE='55000';
  END IF;
END $ril_queue_snapshot_readback$;
DROP POLICY techstack_ril_queue_snapshot ON typed_agent_commands;

CREATE FUNCTION techstack_project_ril_queue_expiry() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
DECLARE scoped text:=NULLIF(current_setting('app.tenant_id',true),'');
BEGIN
  IF TG_OP <> 'INSERT' AND OLD.tenant_id IS DISTINCT FROM scoped THEN
    RAISE EXCEPTION 'RIL queue expiry projection requires exact tenant scope' USING ERRCODE='42501';
  END IF;
  IF TG_OP <> 'DELETE' AND NEW.tenant_id IS DISTINCT FROM scoped THEN
    RAISE EXCEPTION 'RIL queue expiry projection requires exact tenant scope' USING ERRCODE='42501';
  END IF;
  IF TG_OP <> 'INSERT' THEN
    DELETE FROM techstack_ril_queue_expiry_index WHERE tenant_id=OLD.tenant_id AND command_id=OLD.command_id;
  END IF;
  IF TG_OP <> 'DELETE' AND NEW.ril_proof_expires_at IS NOT NULL THEN
    INSERT INTO techstack_ril_queue_expiry_index (tenant_id,command_id,expires_at)
    VALUES (NEW.tenant_id,NEW.command_id,NEW.ril_proof_expires_at)
    ON CONFLICT (tenant_id,command_id) DO UPDATE SET expires_at=EXCLUDED.expires_at;
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION techstack_project_ril_queue_expiry() FROM PUBLIC;
CREATE TRIGGER ril_queue_expiry_insert AFTER INSERT ON typed_agent_commands FOR EACH ROW
WHEN (NEW.ril_proof_expires_at IS NOT NULL) EXECUTE FUNCTION techstack_project_ril_queue_expiry();
CREATE TRIGGER ril_queue_expiry_update AFTER UPDATE OF ril_proof_ciphertext,ril_proof_expires_at,tenant_id,command_id ON typed_agent_commands FOR EACH ROW
WHEN (OLD.ril_proof_expires_at IS NOT NULL OR NEW.ril_proof_expires_at IS NOT NULL) EXECUTE FUNCTION techstack_project_ril_queue_expiry();
CREATE TRIGGER ril_queue_expiry_delete AFTER DELETE ON typed_agent_commands FOR EACH ROW
WHEN (OLD.ril_proof_expires_at IS NOT NULL) EXECUTE FUNCTION techstack_project_ril_queue_expiry();
CREATE FUNCTION techstack_list_due_ril_queue_proofs() RETURNS TABLE(tenant_id text,command_id text)
LANGUAGE sql SECURITY DEFINER SET search_path FROM CURRENT AS $$
  SELECT e.tenant_id,e.command_id FROM techstack_ril_queue_expiry_index e
  WHERE e.expires_at<=clock_timestamp() ORDER BY e.expires_at,e.tenant_id,e.command_id LIMIT 100
$$;
REVOKE ALL ON FUNCTION techstack_list_due_ril_queue_proofs() FROM PUBLIC;
DO $ril_queue_search_path$
DECLARE n text:=current_schema(); signature text;
BEGIN
  FOREACH signature IN ARRAY ARRAY['techstack_project_ril_queue_expiry()','techstack_list_due_ril_queue_proofs()'] LOOP
    EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog,%I,pg_temp',n,signature,n);
  END LOOP;
END $ril_queue_search_path$;
