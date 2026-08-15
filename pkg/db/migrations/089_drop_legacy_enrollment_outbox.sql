-- The Simulate enrollment lane was permanently disabled by migration 019.
-- Its immutable outbox has no runtime readers or writers, and rows without a
-- surviving lease are not custody evidence. Remove the retired queue instead
-- of retaining an ever-growing second lifecycle projection.

DROP TRIGGER IF EXISTS techstack_vm_lease_enrollment_outbox_reject_mutation
    ON techstack_vm_lease_enrollment_outbox;
DROP TABLE IF EXISTS techstack_vm_lease_enrollment_outbox;
DROP FUNCTION IF EXISTS techstack_vm_lease_enrollment_outbox_reject_mutation();
