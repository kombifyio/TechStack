-- Align the port-inventory exposure vocabulary with the compiler-owned
-- StackKits ResolvedPlan contract. Port inventory was not wired when migration
-- 064 shipped, so legacy values are foundation-only data and can be normalized
-- without inferring network meaning from bind addresses or provider facts.

ALTER TABLE server_port_reservation_claims
    DROP CONSTRAINT IF EXISTS server_port_reservation_claims_exposure_check;

UPDATE server_port_reservation_claims
SET exposure = CASE exposure
    WHEN 'loopback' THEN 'local'
    WHEN 'private' THEN 'remote-private'
    ELSE exposure
END
WHERE exposure IN ('loopback', 'private');

ALTER TABLE server_port_reservation_claims
    ADD CONSTRAINT server_port_reservation_claims_exposure_check
    CHECK (exposure IN ('local', 'remote-private', 'public'));

ALTER TABLE server_port_runtime_facts
    DROP CONSTRAINT IF EXISTS server_port_runtime_facts_exposure_check;

UPDATE server_port_runtime_facts
SET exposure = CASE exposure
    WHEN 'loopback' THEN 'local'
    WHEN 'private' THEN 'remote-private'
    ELSE exposure
END
WHERE exposure IN ('loopback', 'private');

ALTER TABLE server_port_runtime_facts
    ADD CONSTRAINT server_port_runtime_facts_exposure_check
    CHECK (exposure IN ('local', 'remote-private', 'public'));
