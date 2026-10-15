import { describe, expect, it } from "vitest";
import type { CanonicalServer } from "#lib/api/registry.js";
import type { Worker } from "#lib/api/workers.js";
import { buildServerNodes } from "./model";

function server(over: Partial<CanonicalServer>): CanonicalServer {
  return {
    id: "srv-1",
    node_id: "node-1",
    name: "nas",
    lifecycle: { state: "active", desired_state: "active" },
    connection: { state: "connected", changed_at: "2026-10-10T10:00:00Z" },
    health: { state: "healthy" },
    channels: [],
    inventory_revision: 1,
    provider: {},
    offering: "self_owned_device",
    mutations_allowed: true,
    created_at: "2026-10-01T10:00:00Z",
    updated_at: "2026-10-10T10:00:00Z",
    ...over,
  } as CanonicalServer;
}

function worker(over: Partial<Worker>): Worker {
  return {
    id: "w-1",
    hostname: "nas.lan",
    ip: "192.168.1.10",
    os: "linux",
    arch: "amd64",
    status: "online",
    created: "2026-10-02T10:00:00Z",
    ram_mb: 16384,
    ...over,
  };
}

describe("buildServerNodes", () => {
  it("lists a registered server once with its worker hardware and an unclaimed worker as pending", () => {
    const nodes = buildServerNodes(
      [server({ worker_id: "w-1" })],
      [
        worker({}),
        worker({
          id: "w-2",
          hostname: "new-box",
          created: "2026-10-09T10:00:00Z",
        }),
      ],
    );

    expect(
      nodes.map((node) => [
        node.name,
        node.placement,
        node.pending,
        node.ramMb,
      ]),
    ).toEqual([
      ["nas", "local", false, 16384],
      ["new-box", "unknown", true, 16384],
    ]);
  });
});
