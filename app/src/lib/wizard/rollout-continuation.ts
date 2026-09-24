export type CreationOperation = "stack" | "add-server";

/** The durable wizard ledger is the receipt authority after reload. */
export function readApplianceReceipt(
  value: unknown,
): import("#lib/api/wizardRuns.js").WizardApplianceReceipt | undefined {
  if (!value || typeof value !== "object") return undefined;
  const record = value as Record<string, unknown>;
  const { lease_id, server_id, operation_id } = record;
  if (
    typeof lease_id !== "string" ||
    !lease_id.trim() ||
    typeof server_id !== "string" ||
    !server_id.trim() ||
    typeof operation_id !== "string" ||
    !operation_id.trim()
  )
    return undefined;
  return { lease_id, server_id, operation_id };
}

export function requiresGuardConnection(
  operation: CreationOperation,
  provisioningMode: string,
): boolean {
  return (
    (operation === "stack" || operation === "add-server") &&
    (provisioningMode === "install-command" ||
      provisioningMode === "hypervisor" ||
      provisioningMode === "connect-remote")
  );
}

export function belongsToCurrentConnectionAttempt(input: {
  serverId: string;
  serverStackId: string;
  stackId: string;
  existingServerBaselineKnown: boolean;
  existingServerIds: ReadonlySet<string>;
}): boolean {
  if (input.existingServerBaselineKnown) {
    return !input.existingServerIds.has(input.serverId);
  }
  return Boolean(input.stackId) && input.serverStackId === input.stackId;
}
