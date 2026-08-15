-- A connected server exists before an operator assigns it to a StackKit.
-- Tenant, worker, and server identity remain mandatory; only the later stack
-- placement binding is optional during canonical enrollment.

ALTER TABLE nodes
    ALTER COLUMN stack_id DROP NOT NULL;
