-- Migration 151 bound its SECURITY DEFINER functions with SET search_path FROM
-- CURRENT, which records the owning schema first. The provider-control posture
-- check admits only pg_catalog, <owning schema>, pg_temp, as migration 150
-- installs for its boundary functions; normalize the two RIL functions to it.
DO $ril_authority_posture$
DECLARE active_schema text:=current_schema(); owner_role text:=current_user; signature text;
BEGIN
    FOREACH signature IN ARRAY ARRAY['techstack_project_ril_workflow_tenant()',
        'techstack_list_ril_workflow_tenants(text,integer)'] LOOP
        EXECUTE format('ALTER FUNCTION %I.%s OWNER TO %I',active_schema,signature,owner_role);
        EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog,%I,pg_temp',active_schema,signature,active_schema);
        EXECUTE format('REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC',active_schema,signature);
    END LOOP;
END $ril_authority_posture$;
