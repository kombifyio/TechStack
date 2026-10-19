/**
 * One server, one detail route.
 *
 * The server page of a StackKit deployment (`/stacks/{deployment}/servers/{id}`)
 * is the canonical detail: overview, services, ports, checks, logs and
 * settings. The monitoring detail (`/monitoring/{id}`) is the same server seen
 * as history and metrics; it stays reachable from the detail page and is the
 * landing only for a server that belongs to no deployment yet, so every card
 * that names a server links the same way whether it sits on the dashboard, the
 * server list or Monitoring.
 */

/** `lease:` ids and agent ids resolve only inside a deployment. */
function canonicalOnly(serverId: string): boolean {
  return !serverId.startsWith("lease:");
}

export interface ServerLinkTarget {
  /** Canonical server id (or the agent / `lease:` id a deployment resolves). */
  serverId?: string | null;
  /** The deployment the server belongs to, when known. */
  deploymentId?: string | null;
}

/** The canonical detail route for a server, or undefined when none exists. */
export function serverDetailHref(target: ServerLinkTarget): string | undefined {
  const serverId = target.serverId?.trim();
  if (!serverId) return undefined;
  const deploymentId = target.deploymentId?.trim();
  if (deploymentId) {
    return `/stacks/${encodeURIComponent(deploymentId)}/servers/${encodeURIComponent(serverId)}`;
  }
  return canonicalOnly(serverId)
    ? `/monitoring/${encodeURIComponent(serverId)}`
    : undefined;
}

/** The monitoring view (history and metrics) of a server. */
export function serverHistoryHref(serverId: string): string {
  return `/monitoring/${encodeURIComponent(serverId)}`;
}

/** Adding a server to the homelab: its deployment, or the creation flow. */
export function addServerHref(deploymentId?: string | null): string {
  const id = deploymentId?.trim();
  return id ? `/stacks/${encodeURIComponent(id)}/servers/new` : "/stacks/new";
}
