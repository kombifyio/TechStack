// Builds the StackKits compatibility receipt (techstack.depot-vm-run/v1) for
// one managed Cloud Kit lane: the product provisioned a real provider VPS,
// Guard ran the pinned StackKits CLI on it, and the stack was destroyed to
// provider absence. StackKits imports it with
// scripts/compat/import-managed-evidence.mjs and grades its public-vps row.
//
// Each lifecycle phase maps to the product proof that the managed job records:
//   install   Guard bootstrapped the pinned StackKits release on the target
//   init      Guard's StackKit prepare (stackkit init) completed on the node
//   generate  the pinned rollout artifacts were generated
//   apply     the StackKit rollout was applied
//   verify    the StackKit verification passed
//   backup    the native restore drill took its backup snapshot
//   restore   the native restore drill restored and verified it
// A phase without its proof is "failed", never "passed".

export const MANAGED_RECEIPT_SCHEMA = "techstack.depot-vm-run/v1";
export const MANAGED_EVIDENCE_SCOPE = "managed-provider-lifecycle";

type Json = Record<string, unknown>;

export interface ManagedLifecycleReceiptInput {
  attemptId: string;
  providerId: string;
  release: string;
  producerCommit: string;
  startedAt: string;
  finishedAt: string;
  job?: Json;
  cleanupReadback?: Json;
}

function record(value: unknown): Json {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Json)
    : {};
}

function lifecyclePhaseCompleted(lifecycle: Json, id: string): boolean {
  const phases = Array.isArray(lifecycle.phases) ? lifecycle.phases : [];
  return phases.some(
    (phase) => record(phase).id === id && record(phase).status === "completed",
  );
}

export function managedLifecycleReceipt(input: ManagedLifecycleReceiptInput) {
  const result = record(input.job?.result);
  const proof = record(result.e2e_proof);
  const lifecycle = record(result.runtime_lifecycle);
  const restore = record(record(result.runtime_proof).restore);
  const drillVerified = restore.status === "verified";
  const checks: Array<[string, boolean]> = [
    ["install", proof.target_bootstrap === "ready"],
    ["init", lifecyclePhaseCompleted(lifecycle, "prepare_apply")],
    ["generate", lifecyclePhaseCompleted(lifecycle, "generate")],
    ["apply", proof.rollout_result === "applied"],
    ["verify", proof.verification_result === "verified"],
    [
      "backup",
      drillVerified && typeof restore.backup_operation_id === "string",
    ],
    [
      "restore",
      drillVerified && typeof restore.restore_operation_id === "string",
    ],
  ];
  const phases = checks.map(([name, passed]) => ({
    name,
    status: passed ? "passed" : "failed",
  }));

  const readback = record(input.cleanupReadback);
  const lease = record(readback.lease);
  const operation = record(readback.provider_operation);
  const providerAbsent =
    lease.observed_terminal === true &&
    operation.terminal === true &&
    typeof operation.absence_evidence_ref === "string";
  const capacityReleased = operation.capacity_released === true;
  const cleanupPassed = providerAbsent && capacityReleased;

  return {
    schemaVersion: MANAGED_RECEIPT_SCHEMA,
    attemptId: input.attemptId,
    evidenceScope: MANAGED_EVIDENCE_SCOPE,
    publicSupportGrade: "unverified",
    cell: `cloud-kit-managed-${input.providerId}`,
    kit: String(result.stack_kit ?? ""),
    release: input.release,
    status: phases.every((phase) => phase.status === "passed")
      ? "passed"
      : "failed",
    phases,
    startedAt: input.startedAt,
    finishedAt: input.finishedAt,
    cleanup: {
      status: cleanupPassed ? "passed" : "failed",
      providerAbsent,
      capacityReleased,
    },
    producerCommit: input.producerCommit,
    substrate: "managed-vps",
    network: "public",
    provider: input.providerId,
    addressMode: String(result.address_mode ?? ""),
  };
}
