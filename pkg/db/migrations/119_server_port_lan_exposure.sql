-- The StackKits ResolvedPlan exposure vocabulary (#ServiceExposureV2) also
-- contains `lan`: Basement core binds its LAN DNS resolver on the site network.
-- Port inventory stores the compiler-owned value unchanged.

ALTER TABLE server_port_reservation_claims
    DROP CONSTRAINT IF EXISTS server_port_reservation_claims_exposure_check;

ALTER TABLE server_port_reservation_claims
    ADD CONSTRAINT server_port_reservation_claims_exposure_check
    CHECK (exposure IN ('local', 'lan', 'remote-private', 'public'));

ALTER TABLE server_port_runtime_facts
    DROP CONSTRAINT IF EXISTS server_port_runtime_facts_exposure_check;

ALTER TABLE server_port_runtime_facts
    ADD CONSTRAINT server_port_runtime_facts_exposure_check
    CHECK (exposure IN ('local', 'lan', 'remote-private', 'public'));
