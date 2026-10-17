-- Migration 156 created techstack_subject_ref(text) for the subject directory's
-- generated column but left PostgreSQL's default PUBLIC EXECUTE on it. Only the
-- SECURITY DEFINER directory functions write that table, so no login needs the
-- helper; the application posture check admits no routine outside its exact
-- capability set, so the PUBLIC grant blocks the separated application role.
DO $subject_ref_posture$
BEGIN
    EXECUTE format('REVOKE ALL ON FUNCTION %I.techstack_subject_ref(text) FROM PUBLIC', current_schema());
END $subject_ref_posture$;
