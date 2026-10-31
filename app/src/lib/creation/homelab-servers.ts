/**
 * The servers a person already has, as choices on the Server step. There is
 * one homelab per person: everything they run is part of it. New use cases go
 * onto a single server they pick here, or onto a new one. A server that runs a
 * StackKit takes them through that StackKit; a server without one gets its
 * first StackKit.
 */
import {
  isCurrentCanonicalServer,
  type CanonicalServer,
} from "#lib/api/registry.js";

export type HomelabServerState =
  /** Can take new use cases now. */
  | "ready"
  /** A server without a StackKit that must reconnect first. */
  | "disconnected"
  /** A managed server that must keep its current provider binding. */
  | "leased"
  /** Changes on this server are not allowed. */
  | "restricted";

export interface HomelabServerChoice {
  id: string;
  name: string;
  /** The StackKit deployment the server belongs to, if any. */
  deploymentId?: string;
  /** The StackKit it runs, as the catalog names it. */
  stackkit?: string;
  state: HomelabServerState;
}

/** What the Server step needs to offer the person's existing servers. */
export interface HomelabServerSelection {
  readonly servers: readonly HomelabServerChoice[];
  readonly loading: boolean;
  readonly failed: boolean;
  /** Why the server the person started from is not the one chosen. */
  readonly notice?: string;
  /** The chosen server's id; empty while a new server is chosen. */
  readonly selected: string;
  select(id: string): void;
  retry(): void;
}

function stateOf(server: CanonicalServer): HomelabServerState {
  if (!server.mutations_allowed) return "restricted";
  if (server.kit_deployment_id) return "ready";
  if (server.provider.lease_id) return "leased";
  return server.connection.state === "connected" ? "ready" : "disconnected";
}

/**
 * The servers that can be offered: current, enrolled, never the hypervisor
 * host that only carries guests. Ready servers come first.
 */
export function homelabServerChoices(
  servers: readonly CanonicalServer[],
  stackkitOf: (deploymentId: string) => string | undefined = () => undefined,
): HomelabServerChoice[] {
  return servers
    .filter(
      (server) =>
        isCurrentCanonicalServer(server) &&
        server.node_role !== "substrate" &&
        (Boolean(server.kit_deployment_id) || Boolean(server.worker_id)),
    )
    .map((server) => ({
      id: server.id,
      name: server.name,
      deploymentId: server.kit_deployment_id || undefined,
      stackkit: server.kit_deployment_id
        ? stackkitOf(server.kit_deployment_id)
        : undefined,
      state: stateOf(server),
    }))
    .sort(
      (a, b) =>
        Number(b.state === "ready") - Number(a.state === "ready") ||
        a.name.localeCompare(b.name),
    );
}
