// Package providercontrol is TechStack's internal provider lifecycle authority.
//
// # Authority
//
// The package selects admitted adapters and immutable execution profiles and
// owns provider commands, generation-bound claims, receipts, resource graphs,
// evidence, reconciliation, and cleanup custody.
//
// # Non-authority
//
// It does not own user desired state, lease validity, Guard health or
// inventory, billing, catalog publication, or Cloud product decisions.
//
// # Side effects
//
// Provider calls are allowed only after a durable command and resource intent
// exist. All operation-specific execution paths remain under the single
// techstack_provider_control authority; their safety modes are TechStack
// catalog and ledger state, not providerexecutor receipt-wire fields.
// Crash-recoverable adapters may replay an exact head only through their
// admitted recovery mechanism. An at-most-once provision whose current head is
// handle-free pending/accepted may advance mutably only to resources_bound and
// requires the execute permit returned by the transaction that atomically
// creates its immutable dispatch guard and first claim. A claim alone never
// permits that dispatch; an existing guard can lead only to read-only discovery
// or Operator Reconcile. Simulate and legacy executors are never production
// execution lanes. Claim access is derived from the operation and receipt
// phase, never from the registered adapter: a read-only poll receives no
// provider-mutation authority, while every new side-effecting claim revalidates
// its exact versioned credential handle under the ledger transaction and
// database time.
//
// # Persistence and concurrency
//
// PostgreSQL is the production ledger. Every operation records the immutable
// techstack_provider_control authority and binds the exact lease revision,
// runtime server, resource-generation UUID, provider, profile hashes, receipt
// head, and execution claim. Receipt heads and claims use compare-and-swap;
// expired, superseded, or lost claims cannot append completion receipts. A
// dispatch guard outlives claim release and expiry and is never reset or
// re-armed. Resources-bound provision work advances to present through
// read-only polling. The append-only discovery/decision store keeps zero
// candidates blocked, adopts exactly one only through expected-revision CAS,
// and quarantines multiple candidates. Its admin-authorized application,
// certified discovery adapter, policy, and read projection are not present
// yet, so production activation remains closed.
//
// Heartbeat renewal and receipt append retain the already-issued claim
// capability and do not reselect credentials. Revocation prevents a new claim
// or takeover but cannot erase provider-resource and cleanup custody after a
// request may already have crossed the network boundary.
//
// # Secrets
//
// Commands, receipts, evidence, and logs contain only versioned opaque custody
// references and hashes. Raw credentials never cross this boundary.
package providercontrol
