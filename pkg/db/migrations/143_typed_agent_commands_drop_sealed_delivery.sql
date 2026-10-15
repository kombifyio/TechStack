-- A sealed host delivery (backup target custody for a node) rides in a typed
-- agent command as ciphertext only, short-lived and single-use. Once the command
-- settles it has no further use, so the stored command never keeps it: no
-- completion path can forget to drop it, because the row itself does.
CREATE OR REPLACE FUNCTION typed_agent_commands_drop_sealed_delivery() RETURNS trigger AS $$
BEGIN
    IF NEW.state IN ('completed', 'failed') AND NEW.command_json ? 'sealedDelivery' THEN
        NEW.command_json := NEW.command_json - 'sealedDelivery';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS typed_agent_commands_drop_sealed_delivery ON typed_agent_commands;
CREATE TRIGGER typed_agent_commands_drop_sealed_delivery
    BEFORE UPDATE ON typed_agent_commands
    FOR EACH ROW EXECUTE FUNCTION typed_agent_commands_drop_sealed_delivery();
