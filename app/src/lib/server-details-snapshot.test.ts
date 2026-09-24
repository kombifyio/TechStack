import { describe, expect, it } from "vitest";
import type {
  KitDeployment,
  StackOperationMonitoring,
  StackOperationServer,
  StackServerDetailsPayload,
} from "./api/stacks.js";
import {
  mergeServerDetailsPayload,
  retainServerDetailsSnapshot,
  serverDetailsIdentity,
} from "./server-details-snapshot.js";

function server(
  state: string,
  metricStatus: string,
  value?: number,
): StackOperationServer {
  const metric = { status: metricStatus, value, unit: "%" };
  return {
    id: "server-1",
    hostname: "server-1",
    role: "foundation",
    status: state,
    assignment: "stack",
    agent_id: "agent-1",
    approved: true,
    precheck_state: "passed",
    capabilities: {
      disk_gb: value === undefined ? undefined : 100,
      ram_mb: value === undefined ? undefined : 8192,
    },
    health: {
      state,
      source: "promql",
      cpu_percent: metric,
      memory_percent: metric,
      disk_percent: metric,
      uptime_seconds: { ...metric, unit: "s" },
    },
  };
}

function details(item: StackOperationServer): StackServerDetailsPayload {
  const stack: KitDeployment & { status?: string; state?: string } = {
    id: "stack-1",
    kit_deployment_id: "stack-1",
    name: "stack",
    provider: "centron",
    state: "running",
    services: [],
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
  };
  const monitoring: StackOperationMonitoring = {
    status: "ok",
    queryBackend: "prometheus",
    ingestBackend: "otlp",
    collectorMode: "gateway",
    compatibilityMode: "native",
  };
  return {
    stack,
    server: item,
    services: [],
    checks: [],
    logs: [],
    health: item.health,
    monitoring,
  };
}

describe("server details snapshot", () => {
  it("keeps the same-server snapshot eligible for an in-place refresh", () => {
    const current = details(server("healthy", "ok", 38));
    expect(
      retainServerDetailsSnapshot(
        current,
        serverDetailsIdentity("stack-1", "server-1"),
        "stack-1",
        "server-1",
      ),
    ).toBe(true);
    expect(
      retainServerDetailsSnapshot(
        current,
        serverDetailsIdentity("stack-1", "server-1"),
        "stack-1",
        "server-2",
      ),
    ).toBe(false);
  });

  it("updates known health in place and keeps last verified KPIs when the refresh is only partially known", () => {
    const previous = details(server("healthy", "ok", 38));
    const partial = mergeServerDetailsPayload(
      previous,
      details(server("unknown", "unknown")),
    );
    expect(partial.health.cpu_percent.value).toBe(38);
    expect(partial.server.health.memory_percent.value).toBe(38);

    const replaced = mergeServerDetailsPayload(
      partial,
      details(server("healthy", "ok", 44)),
    );
    expect(replaced.health.cpu_percent.value).toBe(44);
    expect(replaced.server.health.memory_percent.value).toBe(44);
  });
});
