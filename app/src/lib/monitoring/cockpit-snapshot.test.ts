import { describe, expect, it } from "vitest";
import type { MonitoringCockpitPayload } from "$lib/api/monitoring";
import type { StackOperationServer } from "$lib/api/stacks";
import {
  applyCockpitRefreshError,
  beginCockpitRefresh,
  mergeCockpitSnapshot,
  type CockpitRefreshState,
} from "./cockpit-snapshot";

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

function cockpit(
  item: StackOperationServer | StackOperationServer[],
  overrides: Partial<MonitoringCockpitPayload> = {},
): MonitoringCockpitPayload {
  return {
    stacks: [],
    techstack_id: "stack-1",
    nextSteps: [],
    kpis: {
      registered_servers: 1,
      healthy_servers: 1,
      running_services: 2,
      active_alerts: 1,
    },
    servers: Array.isArray(item) ? item : [item],
    services: [],
    monitoring: {
      status: "ok",
      queryBackend: "prometheus",
      ingestBackend: "otlp",
      collectorMode: "gateway",
      compatibilityMode: "native",
    },
    alerts: [],
    jobs: [],
    ...overrides,
  };
}

describe("cockpit snapshot state", () => {
  it("retains same-stack capacity on a retryable refresh and ignores an older generation", () => {
    const snapshot = cockpit(server("healthy", "ok", 38));
    const initial: CockpitRefreshState = {
      snapshot,
      stackId: "stack-1",
      stale: false,
      error: null,
      generation: 0,
    };
    const first = beginCockpitRefresh(initial, "stack-1");
    const current = beginCockpitRefresh(first.state, "stack-1");

    const ignored = applyCockpitRefreshError(current.state, first.request, {
      message: "rate limited",
      status: 429,
      retryable: true,
    });
    expect(ignored.error).toBeNull();
    expect(ignored.snapshot).toBe(snapshot);

    const failed = applyCockpitRefreshError(current.state, current.request, {
      message: "rate limited",
      status: 429,
      retryable: true,
    });
    expect(failed.snapshot).toBe(snapshot);
    expect(failed.stale).toBe(true);
    expect(failed.error).toMatchObject({ status: 429, retryable: true });

    const switched = beginCockpitRefresh(failed, "stack-2");
    expect(switched.state.snapshot).toBeNull();
    expect(switched.state.stale).toBe(false);
    expect(switched.state.error).toBeNull();
  });

  it("retains verified telemetry when a refresh is only partially known", () => {
    const result = mergeCockpitSnapshot(
      cockpit(server("healthy", "ok", 38)),
      cockpit(server("unknown", "unknown")),
    );
    expect(result.retainedTelemetry).toBe(true);
    expect(result.snapshot.servers[0].health.memory_percent.value).toBe(38);
    expect(result.snapshot.servers[0].capabilities.ram_mb).toBe(8192);
  });

  it("retains cockpit telemetry aggregates across a partial refresh", () => {
    const previous = cockpit(server("healthy", "ok", 38), {
      kpis: {
        registered_servers: 1,
        healthy_servers: 1,
        running_services: 4,
        active_alerts: 2,
      },
      monitoring: {
        status: "ok",
        queryBackend: "prometheus",
        ingestBackend: "otlp",
        collectorMode: "gateway",
        compatibilityMode: "native",
      },
      alerts: [
        {
          name: "disk",
          severity: "warning",
          message: "Disk usage is high",
          value: 82,
          status: "firing",
        },
      ],
    });
    const result = mergeCockpitSnapshot(
      previous,
      cockpit(server("unknown", "unknown"), {
        kpis: {
          registered_servers: 1,
          healthy_servers: 0,
          running_services: 0,
          active_alerts: 0,
        },
        monitoring: {
          status: "unavailable",
          queryBackend: "unknown",
          ingestBackend: "unknown",
          collectorMode: "unknown",
          compatibilityMode: "unknown",
        },
        alerts: [],
      }),
    );

    expect(result.retainedTelemetry).toBe(true);
    expect(result.snapshot.kpis.healthy_servers).toBe(1);
    expect(result.snapshot.kpis.running_services).toBe(4);
    expect(result.snapshot.kpis.active_alerts).toBe(2);
    expect(result.snapshot.monitoring.status).toBe("ok");
    expect(result.snapshot.alerts).toHaveLength(1);
  });

  it("accepts replacement telemetry once the next refresh is known", () => {
    const result = mergeCockpitSnapshot(
      cockpit(server("healthy", "ok", 38)),
      cockpit(server("healthy", "ok", 44), {
        kpis: {
          registered_servers: 1,
          healthy_servers: 1,
          running_services: 3,
          active_alerts: 0,
        },
      }),
    );

    expect(result.retainedTelemetry).toBe(false);
    expect(result.snapshot.servers[0].health.memory_percent.value).toBe(44);
    expect(result.snapshot.kpis.running_services).toBe(3);
    expect(result.snapshot.kpis.active_alerts).toBe(0);
  });

  it("does not retain telemetry over explicit offline evidence", () => {
    const result = mergeCockpitSnapshot(
      cockpit(server("healthy", "ok", 38), {
        alerts: [
          {
            name: "disk",
            severity: "warning",
            message: "Disk usage is high",
            value: 82,
            status: "firing",
          },
        ],
      }),
      cockpit(server("offline", "unknown"), {
        kpis: {
          registered_servers: 1,
          healthy_servers: 0,
          running_services: 0,
          active_alerts: 0,
        },
        monitoring: {
          status: "unavailable",
          queryBackend: "unknown",
          ingestBackend: "unknown",
          collectorMode: "unknown",
          compatibilityMode: "unknown",
        },
        alerts: [],
      }),
    );
    expect(result.retainedTelemetry).toBe(false);
    expect(
      result.snapshot.servers[0].health.memory_percent.value,
    ).toBeUndefined();
    expect(result.snapshot.kpis.healthy_servers).toBe(0);
    expect(result.snapshot.monitoring.status).toBe("unavailable");
    expect(result.snapshot.alerts).toHaveLength(0);
  });

  it("honors offline status when legacy health is only unknown", () => {
    const offline = server("offline", "unknown");
    offline.health.state = "unknown";
    const result = mergeCockpitSnapshot(
      cockpit(server("healthy", "ok", 38)),
      cockpit(offline),
    );
    expect(result.retainedTelemetry).toBe(false);
    expect(
      result.snapshot.servers[0].health.memory_percent.value,
    ).toBeUndefined();
  });

  it("retains a server omitted by an incomplete successful refresh", () => {
    const result = mergeCockpitSnapshot(
      cockpit(server("healthy", "ok", 38)),
      cockpit([]),
      new Set(["server-1"]),
    );
    expect(result.retainedTelemetry).toBe(true);
    expect(result.snapshot.servers).toHaveLength(1);
    expect(result.snapshot.servers[0].health.memory_percent.value).toBe(38);
  });

  it("drops an omitted server after canonical inventory confirms removal", () => {
    const result = mergeCockpitSnapshot(
      cockpit(server("healthy", "ok", 38)),
      cockpit([]),
      new Set(["server-2"]),
    );
    expect(result.retainedTelemetry).toBe(false);
    expect(result.snapshot.servers).toHaveLength(0);
  });

  it("does not transfer telemetry to a replacement with a new canonical id", () => {
    const replacement = server("unknown", "unknown");
    replacement.id = "server-2";
    const result = mergeCockpitSnapshot(
      cockpit(server("healthy", "ok", 38)),
      cockpit(replacement),
      new Set(["server-2"]),
    );
    expect(result.retainedTelemetry).toBe(false);
    expect(result.snapshot.servers.map((item) => item.id)).toEqual([
      "server-2",
    ]);
    expect(
      result.snapshot.servers.find((item) => item.id === "server-2")?.health
        .memory_percent.value,
    ).toBeUndefined();
  });
});
