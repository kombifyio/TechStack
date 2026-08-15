export type CreationOperation = "stack" | "add-server";

export function requiresGuardConnection(
  operation: CreationOperation,
  provisioningMode: string,
): boolean {
  return (
    (operation === "stack" || operation === "add-server") &&
    (provisioningMode === "install-command" ||
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
