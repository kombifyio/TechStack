import { describe, expect, it } from "vitest";

import type { CanonicalServer } from "#lib/api/registry.js";
import { serverLifecycleActions } from "./server-lifecycle-actions";

function server(stackActions?: string[]): CanonicalServer {
  return {
    id: "server-1",
    node_id: "server-1",
    name: "node-01",
    lifecycle: { state: "active", desired_state: "running" },
    connection: { state: "connected", changed_at: "2026-08-27T12:00:00Z" },
    health: { state: "healthy" },
    channels: [],
    inventory_revision: 7,
    provider: {},
    mutations_allowed: true,
    stack_actions: stackActions,
    created_at: "2026-08-27T12:00:00Z",
    updated_at: "2026-08-27T12:00:00Z",
  } as CanonicalServer;
}

describe("serverLifecycleActions", () => {
  it("renders exactly the operations the backend granted, in its order", () => {
    const actions = serverLifecycleActions(server(["plan", "verify", "apply"]));
    expect(actions.map((action) => action.operation)).toEqual([
      "plan",
      "verify",
      "apply",
    ]);
    expect(actions.find((a) => a.operation === "apply")?.mutates).toBe(true);
  });

  it("offers nothing when the read model carries no grant", () => {
    // Absent is not "everything allowed": a server whose backend predates the
    // capability field must not get a mutation button by default.
    expect(serverLifecycleActions(server())).toEqual([]);
    expect(serverLifecycleActions(undefined)).toEqual([]);
  });

  it("drops a granted operation this build cannot present", () => {
    const actions = serverLifecycleActions(server(["plan", "teleport"]));
    expect(actions.map((action) => action.operation)).toEqual(["plan"]);
  });
});
