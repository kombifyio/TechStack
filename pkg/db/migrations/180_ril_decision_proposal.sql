-- A proposed effect is frozen before asking the owner; legacy cards remain unprepared.
ALTER TABLE ril_action_cards ADD COLUMN IF NOT EXISTS decision_card_revision BIGINT NOT NULL DEFAULT 1 CHECK (decision_card_revision > 0);
ALTER TABLE ril_action_cards ADD COLUMN IF NOT EXISTS decision_proposal_json JSONB;
CREATE OR REPLACE FUNCTION ril_decision_card_revision_advance() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.action_template_json IS DISTINCT FROM OLD.action_template_json OR NEW.server_id IS DISTINCT FROM OLD.server_id OR NEW.owner_subject_id IS DISTINCT FROM OLD.owner_subject_id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id THEN
    NEW.decision_card_revision := OLD.decision_card_revision + 1;
    NEW.decision_proposal_json := NULL;
  ELSIF NEW.decision_card_revision IS DISTINCT FROM OLD.decision_card_revision THEN
    RAISE EXCEPTION 'decision card revision is managed';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER ril_decision_card_revision_advance BEFORE UPDATE ON ril_action_cards FOR EACH ROW EXECUTE FUNCTION ril_decision_card_revision_advance();
