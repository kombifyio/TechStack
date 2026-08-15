# Techstack RIL action contract

This package is Techstack's authoritative, provider-free wire contract for an
already approved StackKits action handoff. It binds the exact action card,
execution, tenant, stack, primitive contract, current ResolvedPlan, approval,
grant, scoped target, validity window, nonce, idempotency key, and opaque
evidence/channel references.

The request cannot represent provider selection, executor selection,
credentials, endpoints, shell commands, arbitrary paths, or raw runtime
authority. Techstack owns the action card, approval and durable execution
ledger. The pinned StackKits CLI remains the only beta-core executor.

Evidence is bound to the complete request digest and carries only closed
verification, recovery, and summary codes. Raw logs and diagnostics are not
representable; failures may retain one opaque `diagnostic:` reference.

`ExecutionLedger` defines the atomic reservation/replay boundary. An acquired
reservation returns a fencing token that must match the final evidence commit;
replay returns the already persisted evidence without dispatching again.
