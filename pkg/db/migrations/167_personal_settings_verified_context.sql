-- Personal settings are individual answers, preferences and consents, not
-- organization-delegated resources. The hosted application must prove both
-- the exact tenant and subject through the canonical Common verifier.
-- Reuse 166's fixed login boundary: local/system principals retain the
-- existing tenant-isolation policies; no runtime flag or GUC exempts the
-- hosted login. This narrows existing tables and is deliberately nonadditive.
SET LOCAL lock_timeout = '5s';

-- The existing Common role registry classifies this distinct private login.
INSERT INTO kombify_ctx.role_class(role_name,class) VALUES ('techstack_backup_worker','system');

ALTER TABLE feature_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE feature_preferences FORCE ROW LEVEL SECURITY;
ALTER TABLE feature_consents ENABLE ROW LEVEL SECURITY;
ALTER TABLE feature_consents FORCE ROW LEVEL SECURITY;
ALTER TABLE operator_self_disclosures ENABLE ROW LEVEL SECURITY;
ALTER TABLE operator_self_disclosures FORCE ROW LEVEL SECURITY;

CREATE POLICY feature_preferences_verified_identity ON feature_preferences
    AS RESTRICTIVE FOR ALL TO PUBLIC
    USING (
        session_user <> 'techstack_application'
        OR (
            tenant_id = (SELECT kombify_ctx.org_id())
            AND subject_id = (SELECT kombify_ctx.subject())
        )
    )
    WITH CHECK (
        session_user <> 'techstack_application'
        OR (
            tenant_id = (SELECT kombify_ctx.org_id())
            AND subject_id = (SELECT kombify_ctx.subject())
        )
    );

CREATE POLICY feature_consents_verified_identity ON feature_consents
    AS RESTRICTIVE FOR ALL TO PUBLIC
    USING (
        session_user <> 'techstack_application'
        OR (
            tenant_id = (SELECT kombify_ctx.org_id())
            AND subject_id = (SELECT kombify_ctx.subject())
        )
    )
    WITH CHECK (
        session_user <> 'techstack_application'
        OR (
            tenant_id = (SELECT kombify_ctx.org_id())
            AND subject_id = (SELECT kombify_ctx.subject())
        )
    );

CREATE POLICY operator_self_disclosures_verified_identity ON operator_self_disclosures
    AS RESTRICTIVE FOR ALL TO PUBLIC
    USING (
        session_user <> 'techstack_application'
        OR (
            tenant_id = (SELECT kombify_ctx.org_id())
            AND owner_subject_id = (SELECT kombify_ctx.subject())
        )
    )
    WITH CHECK (
        session_user <> 'techstack_application'
        OR (
            tenant_id = (SELECT kombify_ctx.org_id())
            AND owner_subject_id = (SELECT kombify_ctx.subject())
        )
    );

-- The separate private backup worker uses the existing jobs authority. Its
-- payload reads and writes are backup-only. Only the bounded definer below
-- may inspect other executions' resource identities; SET ROLE/DDL is absent.
CREATE POLICY backup_worker_job_read ON jobs AS RESTRICTIVE FOR SELECT TO PUBLIC
    USING (session_user <> 'techstack_backup_worker'
        OR current_user <> 'techstack_backup_worker' OR type = 'backup');
CREATE POLICY backup_worker_job_insert ON jobs AS RESTRICTIVE FOR INSERT TO PUBLIC
    WITH CHECK (session_user <> 'techstack_backup_worker' OR type = 'backup');
CREATE POLICY backup_worker_job_update ON jobs AS RESTRICTIVE FOR UPDATE TO PUBLIC
    USING (session_user <> 'techstack_backup_worker' OR type = 'backup')
    WITH CHECK (session_user <> 'techstack_backup_worker' OR type = 'backup');

CREATE POLICY backup_worker_typed_commands ON typed_agent_commands AS RESTRICTIVE FOR ALL TO PUBLIC
    USING (session_user <> 'techstack_backup_worker' OR
        (command_family = 'stackkit' AND command_json->>'operation' = 'STACKKIT_OPERATION_BACKUP_RUN'))
    WITH CHECK (session_user <> 'techstack_backup_worker' OR
        (command_family = 'stackkit' AND command_json->>'operation' = 'STACKKIT_OPERATION_BACKUP_RUN'));

CREATE FUNCTION public.techstack_backup_execution_conflicts(p_tenant text,p_job text)
RETURNS TABLE(id text,stack_id text,type text,targets_json jsonb)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
    IF session_user <> 'techstack_backup_worker' OR p_tenant IS NULL OR p_tenant = ''
        OR p_tenant IS DISTINCT FROM current_setting('app.tenant_id',true)
        OR p_job IS NULL OR p_job = '' THEN
        RAISE EXCEPTION 'backup conflict projection requires its exact worker and tenant' USING ERRCODE='42501';
    END IF;
    IF (SELECT count(*) FROM public.jobs j WHERE j.tenant_id=p_tenant AND j.id<>p_job
        AND (j.state='running' OR (j.type='workload_relocation' AND j.result_json->'workload_relocation'->>'state'
            IN ('transferring','destination_verification_required','transfer_receipt_incomplete')))) > 500 THEN
        RAISE EXCEPTION 'backup conflict projection exceeds its bounded execution set' USING ERRCODE='54000';
    END IF;
    RETURN QUERY SELECT j.id,COALESCE(j.stack_id,''),j.type,
        jsonb_build_object('agent_id',j.payload_json->'agent_id',
            'execution_targets',CASE WHEN jsonb_typeof(j.payload_json->'execution_targets')='array' THEN
                (SELECT jsonb_agg(jsonb_build_object('stack_id',target->'stack_id','agent_id',target->'agent_id'))
                    FROM jsonb_array_elements(j.payload_json->'execution_targets') target) ELSE NULL END,
            'workload_relocation',jsonb_build_object(
                'source',jsonb_build_object('stack_id',j.payload_json#>'{workload_relocation,source,stack_id}',
                    'agent_id',j.payload_json#>'{workload_relocation,source,agent_id}'),
                'destination',jsonb_build_object('stack_id',j.payload_json#>'{workload_relocation,destination,stack_id}',
                    'agent_id',j.payload_json#>'{workload_relocation,destination,agent_id}')))
        FROM public.jobs j WHERE j.tenant_id=p_tenant AND j.id<>p_job
        AND (j.state='running' OR (j.type='workload_relocation' AND j.result_json->'workload_relocation'->>'state'
            IN ('transferring','destination_verification_required','transfer_receipt_incomplete')));
END $$;
REVOKE ALL ON FUNCTION public.techstack_backup_execution_conflicts(text,text) FROM PUBLIC;
