# providercontrol

`internal/providercontrol` is TechStack's production provider-lifecycle
authority. It has no public compatibility wrapper.

- `providerexecutor/v1beta1` is the neutral wire. Its normative module owner is
  `kombify-runtime-contracts-go`; Techstack consumes immutable release `v0.1.5`.
  Provider-control activation remains separately gated on adapter certification
  and explicit profile activation.
- TechStack resolves execution profiles, selects adapters, creates the initial
  receipt, seals every later receipt against the current ledger head, and
  durably retains resources and evidence.
- StackKits supplies provider-free architecture and host/OS conformance only.
- Simulate is never an admitted production adapter, boot dependency, fallback,
  callback target, credential custodian, or lifecycle authority.

An empty `Registry` is valid at boot. Starting an operation whose adapter is not
registered fails before any ledger row is created. `Coordinator.Advance` moves
exactly one legal state-machine step per call, which keeps recovery decisions
and partial cleanup state visible.

Adapter-bearing `Advance` calls acquire a short-lived, durable claim bound to
the exact receipt head. A second worker does not invoke the adapter while the
claim is live, and the claim token is consumed atomically with the receipt-head
compare-and-swap. Claim loss fails closed: the adapter context is cancelled and
its result is never appended without the still-live token. Claim expiry permits
takeover only when the operation-specific path is independently retry-safe; it
never reconstructs an at-most-once provision Execute Permit.

Claim access is classified from the operation and current receipt phase. Plan,
observe, presence polling, and absence polling are read-only; provision
dispatch, reconcile, and exact-handle delete are side-effecting. Before every
new side-effecting claim or takeover, the ledger locks and revalidates the exact
tenant/provider/mode/custody/connection handle, immutable hashes, revocation,
and validity window using PostgreSQL time. Same-token heartbeat renewal and
receipt append do not reselect credentials, so revocation cannot discard
resource or cleanup custody after a provider call may already have begun.

This serializes TechStack's invocation decision; it is not magic distributed
exactly-once delivery after a process crash. Crash-recoverable adapters
register one composite capability, but the Registry retains separate private
`CrashRecoverableReadOnlyExecutor` and `CrashRecoverableMutationExecutor`
method-value wrappers. No executable adapter view is exported, and a read-only
view cannot be recovered as the mutation interface. Both methods consume the
exact per-head `AdapterInvocation.Key`; admission permits either provider-native
idempotency or provider-persisted unique correlation with
recovery-by-correlation. An in-memory cache is never sufficient. Providers
without either guarantee use
the separately registered at-most-once provision interface: the catalog pin is
validated against that capability, the first create requires a non-replayable
Execute Permit, and later work is read-only or manual. In both paths the key
binds the immutable operation, resource-generation UUID, idempotency key,
receipt sequence, and receipt digest; only the crash-recoverable path may use
it for lease-expiry takeover after ambiguous provider acceptance.

The AMO append-only discovery/decision store and exact-candidate local custody
CAS are implemented. Manual reconcile is the hosted REST application's
provider-neutral workflow in two Admin+FGA-gated steps: immutable discovery
followed by an explicit append-only decision. It obtains candidates exclusively
from two fresh generation-tag reads, requires an exact operation confirmation,
and may adopt only the sole provider-produced graph. It has no create, delete,
guard-reset, or operator-supplied-handle capability. Customer UI/MCP access
and a general read projection remain unavailable.

## Centron Layer-2 demo handoff

Provider create admission follows the registered adapter and its discovered
capabilities. There is no separate deployment kill switch or per-server
operator approval in the normal lifecycle; exact-handle decommission remains
part of the same managed operation.

Read-only preflight must record:

1. the deployed Techstack version and full revision from `/api/v1/health`, plus
   `/api/v1/health/ready` and its applied migration;
2. active `centron` catalog coverage for both runtime offerings, adapter
   `centron-ccloud-v1`, dispatch mode
   `at_most_once_dispatch_manual_reconcile`, and the exact adapter manifest;
3. successful opaque bundle resolution without printing credential material;
4. positive Monthly Runtime, Cloud Kit, and Centron account entitlements, plus
   the signed capacity decision and `held < limit` for the exact owner;
5. all existing Centron leases, server bindings, open operations, dispatch
   guards, and capacity reservations. Stale custody is a blocker until provider
   absence is freshly proven; preflight never adopts a handle or writes a
   release fact.

Mutation authorization points are deliberately separate:

1. Deploy the reviewed full SHA and repeat the read-only preflight.
2. With explicit live-create approval, enable only the Centron create switch
   and have the authenticated owner submit one idempotent Wizard request.
3. If dispatch becomes `manual_reconcile_required`, do not retry Create. A
   separately authorized staff operator resolves it through the hosted
   Admin+FGA reconcile flow only with the exact tenant, operation, and
   operator subject: immutable discovery from two fresh generation-tag
   reads, then an explicit append-only adopt decision for exactly one
   provider-produced candidate.
4. Decommission is a later, independent approval. Capacity stays held until
   both provider resource roles have definitive absence evidence and the
   append-only release fact has committed.

The visible Easy Wizard sequence is:

`Stacks` -> selected stack -> `Register additional servers` -> `Add Server` ->
`Goals` -> `Server` (Kombify-managed, Centron, Standard or Premium, StackKit,
services) -> `Access` -> `Users` -> `Login` -> `Add server` -> Creating status ->
Dashboard server card -> Services inventory and actions.

The evidence packet must contain no secret values. It records source version
and revision; catalog version and adapter manifest; tenant, owner, stack,
runtime slot and generation; lease/revision; operation/idempotency key; every
receipt sequence, phase, digest, automation state, and secret-free automation
reason code; each resource binding's
ID, kind, native-reference hash, parent, ownership hash, disposition,
observation, and cleanup state; evidence ref/digest/source/subject hash,
collection time, and definitive flag; RuntimeTarget VM identity and public IP;
enrollment capability redemption, pairing and heartbeat timestamps; pinned
StackKit and observed services; and, after separately authorized teardown, the
decommission operation, two absence observations, RuntimeServer tombstone, and
capacity-release fact.

No legacy Simulate provider client, wire, callback, command route, or enrollment
worker is part of this package. Simulate may consume the neutral contract only
through deterministic fakes and conformance fixtures; it can never be a
real-provider adapter or emit authoritative provider custody.

## PostgreSQL authority boundary

Every new operation first proves an immutable lease authority of
`techstack_provider_control`. The durable `command_json` is a versioned
provider-control envelope containing that authority, the secret-free execution
profile snapshot, and the sealed `providerexecutor/v1beta1` command. Plain
legacy command JSON is rejected. The command and every receipt bind the exact
lease revision, runtime server, provider, capability snapshot, execution
profile, and resource-generation UUID. There is no automatic authority change,
fallback, or dual execution.

The migration role is trusted and owns the ledger tables, trigger functions,
and a controlled migration `search_path`. Provider Control accepts only an
explicit, separately credentialed runtime pool; it never falls back to the
migration or general application URL and never runs migrations. The canonical
runtime login is `techstack_provider_control_runtime`; it must be
`NOSUPERUSER NOINHERIT NOBYPASSRLS`, must not own objects, and must not have
database `TEMP`, `SET ROLE` memberships, schema creation, trigger replacement,
function replacement, migration, catalog-publication, or non-allowlisted table
sequence, or function privileges in any application schema. Its authenticated
`session_user` must equal its effective `current_user`; startup-role overrides
are rejected.

Migration 034 maintains a secret-free runnable-tenant directory atomically
with provider-operation changes. The runtime cannot read or write that table;
it can invoke only a hard-limited keyset function. Four existing privileged
ledger trigger functions plus the new directory/identity functions have a
fixed `pg_catalog, <trusted schema>, pg_temp` path and no `PUBLIC EXECUTE`.
Boot verifies their owner and grants, every required FORCE-RLS fence, and a
physical PostgreSQL `system_identifier` shared by the migration and runtime
DSNs. The Render pre-deploy command installs and proves this exact posture.
Failure disables Provider Control without a migration-URL fallback.

The bearer claim token is never persisted
in plaintext: the database stores only
`claim_token_digest` (SHA-256), while the raw token remains in worker memory and
transaction-local state. Reading a tenant-scoped claim row therefore cannot
recover a reusable capability; `claim_owner` is non-secret audit metadata.

Claim mutations bind the token and owner with transaction-local PostgreSQL
settings. Triggers compare that binding to the existing capability and use
`clock_timestamp()` for expiry, takeover, renewal, release, and consumption.
Caller-supplied timestamps therefore cannot steal or prematurely consume a
live claim.

Run the isolated PostgreSQL integration lane with:

```text
mise run test:providercontrol:postgres
```

It creates a unique schema and a temporary restricted runtime role, exercises
concurrent coordinators, expiry takeover, token secrecy, raw-SQL bypass
rejection, and tenant RLS, then removes only those generated test objects.
