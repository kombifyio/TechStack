import { describe, expect, it } from "vitest";
import type { StackOperationServer } from "$lib/api/stacks";
import {
  canRunServerLifecycleActions,
  serverLifecycleActions,
} from "./server-lifecycle-actions";

function server(
  overrides: Partial<StackOperationServer> = {},
): StackOperationServer {
  return {
    id: "server-1",
    hostname: "foundation-1",
    role: "foundation",
    status: "connected",
    assignment: "stack",
    techstack_id: "techstack-1",
    agent_id: "agent-1",
    approved: true,
    precheck_state: "passed",
    capabilities: {},
    health: {
      state: "healthy",
      source: "guard",
      cpu_percent: { status: "ok", value: 10, unit: "%" },
      memory_percent: { status: "ok", value: 20, unit: "%" },
      disk_percent: { status: "ok", value: 30, unit: "%" },
      uptime_seconds: { status: "ok", value: 60, unit: "s" },
    },
    stackkit: { state: "observed", sources: ["guard"] },
    ...overrides,
  };
}

describe("server lifecycle actions", () => {
  it("offers operational actions on the concrete connected server", () => {
    expect(canRunServerLifecycleActions(server())).toBe(true);
    expect(
      serverLifecycleActions(server()).map((action) => action.operation),
    ).toEqual(["plan", "verify", "upgrade", "drift_detect"]);
  });

  it("offers mutations only for the matching lifecycle state", () => {
    expect(
      serverLifecycleActions(
        server({ stackkit: { state: "planned", sources: ["job"] } }),
      ).map((action) => action.operation),
    ).toEqual(["plan", "verify", "apply"]);
    expect(
      serverLifecycleActions(
        server({ stackkit: { state: "drifted", sources: ["drift"] } }),
      ).map((action) => action.operation),
    ).toEqual(["plan", "verify", "drift_detect", "drift_reconcile"]);
  });

  it("fails closed for an offline, unapproved, or unassigned target", () => {
    expect(serverLifecycleActions(server({ status: "offline" }))).toEqual([]);
    expect(serverLifecycleActions(server({ approved: false }))).toEqual([]);
    expect(
      serverLifecycleActions(
        server({ assignment: "unassigned", techstack_id: undefined }),
      ),
    ).toEqual([]);
  });
});
