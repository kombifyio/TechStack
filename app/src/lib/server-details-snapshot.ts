import { mergeServerTelemetry } from "./monitoring/cockpit-snapshot.js";
import type { StackServerDetailsPayload } from "./api/stacks.js";

export function serverDetailsIdentity(
  stackId: string,
  serverId: string,
): string {
  return `${stackId}\0${serverId}`;
}

export function retainServerDetailsSnapshot(
  details: StackServerDetailsPayload | null,
  loadedIdentity: string,
  stackId: string,
  serverId: string,
): boolean {
  return (
    Boolean(details) &&
    loadedIdentity === serverDetailsIdentity(stackId, serverId)
  );
}

/**
 * Keep last verified health numbers while a same-server refresh is only
 * partially known. Explicit unreachability still replaces the snapshot.
 */
export function mergeServerDetailsPayload(
  previous: StackServerDetailsPayload | null,
  next: StackServerDetailsPayload,
): StackServerDetailsPayload {
  if (!previous || previous.server.id !== next.server.id) return next;
  const [server] = mergeServerTelemetry(previous.server, next.server);
  const [healthHost] = mergeServerTelemetry(
    { ...previous.server, health: previous.health },
    { ...next.server, health: next.health },
  );
  return {
    ...next,
    server,
    health: healthHost.health,
  };
}
