import { describe, expect, it } from "vitest";
import {
  authoritativeConnectRemoteReady,
  resolvePlannedServerTarget,
  selectGuardConnectedServer,
  serverGuardConnectionReady,
  serverHasFreshGuardHeartbeat,
} from "./guard-connection";

const NOW = Date.parse("2026-09-14T18:00:00.000Z");
const HEARTBEAT = "2026-09-14T17:59:30.000Z";
const PAIRING_STARTED = "2026-09-14T17:58:00.000Z";

function server(
  id: string,
  overrides: Record<string, unknown> = {},
) {
  return {
    id,
    node_id: id,
    kit_deployment_id: "stack-1",
    name: id,
    worker_id: `guard-${id}`,
    lifecycle: { state: "active", desired_state: "running" },
    connection: {
      state: "connected",
      changed_at: HEARTBEAT,
      last_heartbeat_at: HEARTBEAT,
      staleness_seconds: 0,
    },
    health: { state: "healthy", observed_at: HEARTBEAT },
    channels: [],
    inventory_revision: 1,
    provider: {},
    mutations_allowed: true,
    created_at: HEARTBEAT,
    updated_at: HEARTBEAT,
    ...overrides,
  };
}

describe("guard connection selection", () => {
  it("accepts enrolling servers with a fresh connected heartbeat", () => {
    expect(
      serverGuardConnectionReady(
        server("node-new", {
          lifecycle: { state: "enrolling", desired_state: "running" },
        }) as never,
      ),
    ).toBe(true);
  });

  it("selects the newly created node when older stack servers also heartbeat", () => {
    const selected = selectGuardConnectedServer({
      servers: [
        server("server-old-1", {
          name: "srv1161760-a",
          created_at: "2026-08-01T10:00:00.000Z",
        }),
        server("server-old-2", {
          name: "srv1161760-b",
          created_at: "2026-08-02T10:00:00.000Z",
        }),
        server("server-new", {
          name: "remote-worker",
          created_at: "2026-09-14T17:59:00.000Z",
          provider: { ref: "hostinger-vps:srv9999999" },
        }),
      ] as never,
      stackId: "stack-1",
      creationOperation: "add-server",
      existingServerBaselineKnown: false,
      existingServerIds: new Set(),
      expectedDeviceName: "82.165.251.178",
      remoteServerHost: "82.165.251.178",
      pairingStartedAt: PAIRING_STARTED,
      connectionClock: NOW,
      freshMs: 90_000,
      futureSkewMs: 30_000,
    });

    expect(selected?.id).toBe("server-new");
  });

  it("honors the persisted join baseline before heartbeat disambiguation", () => {
    const selected = selectGuardConnectedServer({
      servers: [
        server("server-old", { created_at: "2026-08-01T10:00:00.000Z" }),
        server("server-new", { created_at: "2026-09-14T17:59:00.000Z" }),
      ] as never,
      stackId: "stack-1",
      creationOperation: "add-server",
      existingServerBaselineKnown: true,
      existingServerIds: new Set(["server-old"]),
      pairingStartedAt: PAIRING_STARTED,
      connectionClock: NOW,
      freshMs: 90_000,
      futureSkewMs: 30_000,
    });

    expect(selected?.id).toBe("server-new");
  });

  it("does not claim an existing Node when join evidence stays ambiguous", () => {
    const selected = selectGuardConnectedServer({
      servers: [
        server("server-old-1", {
          name: "srv1161760-a",
          created_at: "2026-08-01T10:00:00.000Z",
        }),
        server("server-old-2", {
          name: "srv1161760-b",
          created_at: "2026-08-02T10:00:00.000Z",
        }),
      ] as never,
      stackId: "stack-1",
      creationOperation: "add-server",
      existingServerBaselineKnown: false,
      existingServerIds: new Set(),
      remoteServerHost: "82.165.251.178",
      pairingStartedAt: PAIRING_STARTED,
      connectionClock: NOW,
      freshMs: 90_000,
      futureSkewMs: 30_000,
    });

    expect(selected).toBeNull();
  });

  it("requires a fresh heartbeat timestamp", () => {
    expect(
      serverHasFreshGuardHeartbeat(
        server("server-new", {
          connection: {
            state: "connected",
            changed_at: "2026-09-14T16:00:00.000Z",
            last_heartbeat_at: "2026-09-14T16:00:00.000Z",
          },
        }) as never,
        "stack-1",
        {
          connectionClock: NOW,
          freshMs: 90_000,
          futureSkewMs: 30_000,
          pairingStartedAt: PAIRING_STARTED,
        },
      ),
    ).toBe(false);
  });
});

describe("authoritative connect-remote target", () => {
  it("resolves planned_server_id before generic server_id", () => {
    expect(
      resolvePlannedServerTarget({
        planned_server_id: "server-planned",
        server_id: "server-other",
      }),
    ).toBe("server-planned");
  });

  it("accepts only the reserved server id", () => {
    const target = server("server-planned") as never;
    expect(
      authoritativeConnectRemoteReady(target, "server-planned", "stack-1", {
        connectionClock: NOW,
        freshMs: 90_000,
        futureSkewMs: 30_000,
        pairingStartedAt: PAIRING_STARTED,
      }),
    ).toBe(true);
    expect(
      authoritativeConnectRemoteReady(target, "server-other", "stack-1", {
        connectionClock: NOW,
        freshMs: 90_000,
        futureSkewMs: 30_000,
        pairingStartedAt: PAIRING_STARTED,
      }),
    ).toBe(false);
  });
});
