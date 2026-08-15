export interface CustodyLeaseActionable {
  lease_id: string;
  allowed_actions?: readonly string[];
}

export type CustodyCleanupAction = "decommission" | "resolve_custody";

export function cleanupActionForFailure<T extends CustodyLeaseActionable>(
  leases: readonly T[],
  failedLeaseId: string | undefined,
): { lease: T; action: CustodyCleanupAction } | null {
  const exactLeaseId = failedLeaseId?.trim();
  if (!exactLeaseId) return null;
  const lease = leases.find((candidate) => candidate.lease_id === exactLeaseId);
  if (!lease) return null;
  if (lease.allowed_actions?.includes("decommission")) {
    return { lease, action: "decommission" };
  }
  if (lease.allowed_actions?.includes("resolve_custody")) {
    return { lease, action: "resolve_custody" };
  }
  return null;
}
