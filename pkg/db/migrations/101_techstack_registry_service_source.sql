-- Registry API declarations are desired control-plane input, not StackKits
-- runtime or apply-output evidence. Keep that provenance explicit while the
-- persisted management dimension records that Techstack owns the declaration.
ALTER TABLE services DROP CONSTRAINT IF EXISTS services_source_check;
ALTER TABLE services ADD CONSTRAINT services_source_check
    CHECK (source IN (
        'observed',
        'stackkits-inventory',
        'stackkit_outputs',
        'techstack-registry',
        'legacy-registry-backfill'
    ));
