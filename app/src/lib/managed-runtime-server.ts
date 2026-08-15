import type { StackOperationServer } from "$lib/api/stacks";

export function isManagedRuntimeServer(
  server: Pick<StackOperationServer, "source" | "lease_id">,
): boolean {
  return (
    server.source === "managed-runtime" || Boolean(server.lease_id?.trim())
  );
}
