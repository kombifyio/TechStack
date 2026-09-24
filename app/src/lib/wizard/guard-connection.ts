import type { CanonicalServer } from "#lib/api/registry.js";
import { legacyServerState } from "#lib/api/registry.js";
import {
  belongsToCurrentConnectionAttempt,
  type CreationOperation,
} from "./rollout-continuation.js";

function normalizedServerIdentity(value?: string): string {
  return (value || "").trim().toLowerCase();
}

export type GuardHeartbeatFreshness = {
  connectionClock: number;
  freshMs: number;
  futureSkewMs: number;
  pairingStartedAt?: string;
};

export type GuardConnectionSelectionInput = GuardHeartbeatFreshness & {
  servers: CanonicalServer[];
  stackId: string;
  creationOperation: CreationOperation;
  existingServerBaselineKnown: boolean;
  existingServerIds: ReadonlySet<string>;
  expectedDeviceName?: string;
  remoteServerHost?: string;
};

const ENROLLING_LIFECYCLE = new Set([
  "enrolling",
  "provisioning",
  "active",
]);

export function serverGuardConnectionReady(server: CanonicalServer): boolean {
  const health = legacyServerState(
    server.connection.state,
    server.health.state,
  );
  const lifecycle = server.lifecycle.state.trim().toLowerCase();
  return (
    Boolean(server.worker_id) &&
    health === "healthy" &&
    ENROLLING_LIFECYCLE.has(lifecycle)
  );
}

export function serverHasFreshGuardHeartbeat(
  server: CanonicalServer,
  stackId: string,
  freshness: GuardHeartbeatFreshness,
): boolean {
  const lastSeenAt = Date.parse(server.connection.last_heartbeat_at || "");
  const pairingStartedAtMs = Date.parse(freshness.pairingStartedAt || "");
  const heartbeatAgeMs = freshness.connectionClock - lastSeenAt;
  return (
    server.kit_deployment_id === stackId &&
    serverGuardConnectionReady(server) &&
    Number.isFinite(lastSeenAt) &&
    heartbeatAgeMs >= -freshness.futureSkewMs &&
    heartbeatAgeMs <= freshness.freshMs &&
    (!Number.isFinite(pairingStartedAtMs) ||
      lastSeenAt >= pairingStartedAtMs - 30_000)
  );
}

function serverMatchesRemoteTarget(
  server: CanonicalServer,
  remoteNeedle: string,
): boolean {
  if (!remoteNeedle) return false;
  const values = [
    server.name,
    server.provider?.ref,
    server.provider?.id,
    server.provider?.target_ref,
    server.provider_id,
    server.provider_target_ref,
    server.target_evidence?.ref,
  ];
  return values.some(
    (value) => normalizedServerIdentity(value) === remoteNeedle,
  );
}

function serverCreatedDuringPairing(
  server: CanonicalServer,
  pairingStartedAtMs: number,
): boolean {
  const createdAtMs = Date.parse(server.created_at || "");
  return (
    Number.isFinite(createdAtMs) &&
    createdAtMs >= pairingStartedAtMs - 60_000
  );
}

function heartbeatTimestamp(server: CanonicalServer): number {
  const lastSeenAt = Date.parse(server.connection.last_heartbeat_at || "");
  return Number.isFinite(lastSeenAt) ? lastSeenAt : 0;
}

function disambiguateGuardCandidates(
  candidates: CanonicalServer[],
  input: GuardConnectionSelectionInput,
): CanonicalServer | null {
  const remoteNeedle = normalizedServerIdentity(input.remoteServerHost);
  const expectedNeedle = normalizedServerIdentity(input.expectedDeviceName);

  if (remoteNeedle) {
    const remoteMatches = candidates.filter((server) =>
      serverMatchesRemoteTarget(server, remoteNeedle),
    );
    if (remoteMatches.length === 1) return remoteMatches[0];
    if (remoteMatches.length > 1) {
      return [...remoteMatches].sort(
        (left, right) => heartbeatTimestamp(right) - heartbeatTimestamp(left),
      )[0];
    }
  }

  if (expectedNeedle) {
    const expectedMatches = candidates.filter(
      (server) => normalizedServerIdentity(server.name) === expectedNeedle,
    );
    if (expectedMatches.length === 1) return expectedMatches[0];
  }

  const pairingStartedAtMs = Date.parse(input.pairingStartedAt || "");
  if (
    input.creationOperation === "add-server" &&
    Number.isFinite(pairingStartedAtMs)
  ) {
    const createdDuringPairing = candidates.filter((server) =>
      serverCreatedDuringPairing(server, pairingStartedAtMs),
    );
    if (createdDuringPairing.length === 1) return createdDuringPairing[0];
    if (createdDuringPairing.length > 1) {
      return [...createdDuringPairing].sort(
        (left, right) => heartbeatTimestamp(right) - heartbeatTimestamp(left),
      )[0];
    }
  }

  if (candidates.length === 1) return candidates[0];

  if (input.creationOperation === "add-server") {
    return null;
  }

  return [...candidates].sort(
    (left, right) => heartbeatTimestamp(right) - heartbeatTimestamp(left),
  )[0];
}

export function selectGuardConnectedServer(
  input: GuardConnectionSelectionInput,
): CanonicalServer | null {
  let candidates = input.servers.filter((server) => {
    if (
      !serverHasFreshGuardHeartbeat(server, input.stackId, {
        connectionClock: input.connectionClock,
        freshMs: input.freshMs,
        futureSkewMs: input.futureSkewMs,
        pairingStartedAt: input.pairingStartedAt,
      })
    ) {
      return false;
    }
    return belongsToCurrentConnectionAttempt({
      serverId: server.id,
      serverStackId: server.kit_deployment_id ?? "",
      stackId: input.stackId,
      existingServerBaselineKnown: input.existingServerBaselineKnown,
      existingServerIds: input.existingServerIds,
    });
  });

  if (candidates.length === 0) return null;

  if (
    input.creationOperation === "add-server" &&
    !input.existingServerBaselineKnown
  ) {
    const pairingStartedAtMs = Date.parse(input.pairingStartedAt || "");
    if (Number.isFinite(pairingStartedAtMs)) {
      const createdDuringPairing = candidates.filter((server) =>
        serverCreatedDuringPairing(server, pairingStartedAtMs),
      );
      if (createdDuringPairing.length > 0) {
        candidates = createdDuringPairing;
      }
    }
  }

  return disambiguateGuardCandidates(candidates, input);
}

/** Connect-remote uses one reserved server id instead of heartbeat guessing. */
export function authoritativeConnectRemoteReady(
  server: CanonicalServer,
  plannedServerId: string,
  stackId: string,
  freshness: GuardHeartbeatFreshness,
): boolean {
  const targetId = plannedServerId.trim();
  if (!targetId || server.id !== targetId) {
    return false;
  }
  return serverHasFreshGuardHeartbeat(server, stackId, freshness);
}

export function resolvePlannedServerTarget(source: {
  planned_server_id?: unknown;
  server_id?: unknown;
}): string {
  const planned =
    typeof source.planned_server_id === "string"
      ? source.planned_server_id.trim()
      : "";
  if (planned) return planned;
  const serverId =
    typeof source.server_id === "string" ? source.server_id.trim() : "";
  return serverId;
}

export function restoreJoinServerBaseline(
  existingServerIds: unknown,
): { known: true; ids: Set<string> } | { known: false } {
  if (!Array.isArray(existingServerIds)) return { known: false };
  const ids = new Set(
    existingServerIds.filter(
      (value): value is string =>
        typeof value === "string" && value.trim() !== "",
    ),
  );
  return { known: true, ids };
}
