// Builds the StackKits compatibility receipt (techstack.depot-vm-run/v1) for
// one managed Cloud Kit lane from job outcome flags and owner-scoped cleanup
// readback. The release is advertised by the control-plane catalog; the
// producer commit identifies the test source. Neither proves the installed
// StackKits release or served Techstack revision. StackKits imports it with
// scripts/compat/import-managed-evidence.mjs and grades its public-vps row.
//
// Each lifecycle phase maps to the product proof that the managed job records:
//   install   Guard reported target bootstrap readiness (no version readback)
//   init      Guard's StackKit prepare (stackkit init) completed on the node
//   generate  the pinned rollout artifacts were generated
//   apply     the StackKit rollout was applied
//   verify    the StackKit verification passed
//   backup    the native restore drill took its backup snapshot
//   restore   the native restore drill restored and verified it
// A phase without its proof is "failed", never "passed".
// This compatibility receipt does not qualify the complete paid-beta B4/B5
// journey (which additionally requires retry, upgrade and port admission).

import type { ManagedDay2Evidence } from "./managed-day2-evidence";

export const MANAGED_RECEIPT_SCHEMA = "techstack.depot-vm-run/v1";
export const MANAGED_EVIDENCE_SCOPE = "managed-provider-lifecycle";

type Json = Record<string, unknown>;

export interface ManagedLifecycleReceiptInput {
  attemptId: string;
  stackId: string;
  leaseId: string;
  providerId: string;
  advertisedStackkitsRelease: string;
  testSourceCommit: string;
  startedAt: string;
  finishedAt: string;
  job?: Json;
  destroyJobId?: string;
  day2?: ManagedDay2Evidence;
  cleanupReadback?: Json;
  cleanupReadbackVerified: boolean;
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
    ["backup", drillVerified && nonblank(restore.backup_operation_id)],
    ["restore", drillVerified && nonblank(restore.restore_operation_id)],
  ];
  const phases = checks.map(([name, passed]) => ({
    name,
    status: passed ? "passed" : "failed",
  }));

  const readback = record(input.cleanupReadback);
  const lease = record(readback.lease);
  const server = record(readback.server);
  const operation = record(readback.provider_operation);
  const leaseBound = nonblank(input.leaseId) && readback.lease_id === input.leaseId;
  const providerAbsent =
    leaseBound &&
    lease.desired_terminal === true &&
    lease.observed_terminal === true &&
    operation.found === true &&
    operation.terminal === true &&
    nonblank(operation.absence_evidence_ref);
  const inventoryReleased =
    leaseBound && server.bound === true && server.terminal === true;
  const capacityReleased = leaseBound && operation.capacity_released === true;
  const cleanupPassed =
    input.cleanupReadbackVerified &&
    providerAbsent && inventoryReleased && capacityReleased;
  const metadataPresent =
    nonblank(input.stackId) &&
    nonblank(input.job?.id) &&
    result.stack_kit === "cloud-kit" &&
    /^v\d+\.\d+\.\d+$/.test(input.advertisedStackkitsRelease) &&
    /^[0-9a-f]{40}$/.test(input.testSourceCommit);

  return {
    schemaVersion: MANAGED_RECEIPT_SCHEMA,
    attemptId: input.attemptId,
    evidenceScope: MANAGED_EVIDENCE_SCOPE,
    publicSupportGrade: "unverified",
    cell: `cloud-kit-managed-${input.providerId}`,
    kit: String(result.stack_kit ?? ""),
    // Keep the importer contract, with its evidence source explicit.
    release: input.advertisedStackkitsRelease,
    releaseSource: "control-plane-catalog",
    installedStackkitsRelease: null,
    status:
      input.job?.state === "completed" &&
      metadataPresent &&
      cleanupPassed &&
      phases.every((phase) => phase.status === "passed")
      ? "passed"
      : "failed",
    phases,
    startedAt: input.startedAt,
    finishedAt: input.finishedAt,
    cleanup: {
      status: cleanupPassed ? "passed" : "failed",
      readbackVerified: input.cleanupReadbackVerified,
      providerAbsent,
      inventoryReleased,
      capacityReleased,
      leaseId: input.leaseId,
      absenceEvidenceRef:
        typeof operation.absence_evidence_ref === "string"
          ? operation.absence_evidence_ref
          : undefined,
      lastReadback: input.cleanupReadback
        ? {
            lease_id: readback.lease_id,
            lease: {
              desired_terminal: lease.desired_terminal,
              observed_terminal: lease.observed_terminal,
            },
            server: { bound: server.bound, terminal: server.terminal },
            provider_operation: {
              found: operation.found,
              terminal: operation.terminal,
              absence_evidence_ref: operation.absence_evidence_ref,
              capacity_released: operation.capacity_released,
            },
          }
        : undefined,
    },
    stackId: input.stackId,
    leaseId: input.leaseId,
    provisionJobId: input.job?.id,
    backupOperationId: restore.backup_operation_id,
    restoreOperationId: restore.restore_operation_id,
    destroyJobId: input.destroyJobId,
    day2: input.day2,
    producerCommit: input.testSourceCommit,
    producerCommitRole: "test-source",
    servedTechstackRevision: null,
    substrate: "managed-vps",
    network: "public",
    provider: input.providerId,
    addressMode: String(result.address_mode ?? ""),
  };
}

function nonblank(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}
