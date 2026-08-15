import { describe, expect, it } from "vitest";

import type { StackOperationServer } from "$lib/api/stacks";
import {
  formatCapacity,
  formatMetric,
  serverCardKit,
  serverCardMeta,
  serverCardMetrics,
  serverCardStatus,
  serverDomains,
  serverOSLabel,
  serverPrimaryAddress,
  serverStackKitName,
  serverStackKitVariant,
} from "./server-card-adapter";

function makeServer(
  overrides: Partial<StackOperationServer> = {},
): StackOperationServer {
  return {
    id: "server-1",
    hostname: "web-01",
    role: "worker",
    status: "connected",
    assignment: "stack",
    agent_id: "agent-1",
    approved: true,
    precheck_state: "passed",
    capabilities: {},
    health: {
      state: "healthy",
      source: "canonical-server",
      cpu_percent: { status: "ok", value: 12.345, unit: "%" },
      memory_percent: { status: "ok", value: 41, unit: "%" },
      disk_percent: { status: "unknown" },
      uptime_seconds: { status: "ok", value: 3600, unit: "s" },
    },
    ...overrides,
  };
}

describe("server card adapter", () => {
  it("formats health metrics to one decimal and reports unverified readings as unknown", () => {
    expect(formatMetric({ status: "ok", value: 1.2345678, unit: "%" })).toBe(
      "1.2%",
    );
    expect(formatMetric({ status: "ok", value: 41, unit: "%" })).toBe("41%");
    expect(formatMetric({ status: "ok", value: 7 })).toBe("7");
    expect(formatMetric({ status: "unknown" })).toBe("unknown");
    expect(formatMetric({ status: "error", value: 3 })).toBe("unknown");
    expect(formatMetric(undefined)).toBe("unknown");
  });

  it("formats capacity values and hides non-positive readings", () => {
    expect(formatCapacity(4, "CPU")).toBe("4 CPU");
    expect(formatCapacity(8192.4, "MB RAM")).toBe("8192 MB RAM");
    expect(formatCapacity(0, "CPU")).toBe("unknown");
    expect(formatCapacity(undefined, "CPU")).toBe("unknown");
  });

  it("builds an OS label without repeating a version already embedded in the OS name", () => {
    expect(
      serverOSLabel(
        makeServer({ os: "Ubuntu", os_version: "24.04", arch: "amd64" }),
      ),
    ).toBe("Ubuntu 24.04/amd64");
    expect(
      serverOSLabel(
        makeServer({ os: "Ubuntu 24.04.2 LTS", os_version: "24.04" }),
      ),
    ).toBe("Ubuntu 24.04.2 LTS/arch unknown");
    expect(serverOSLabel(makeServer({}))).toBe("os unknown/arch unknown");
  });

  it("prefers the reported IP and falls back through address scopes", () => {
    expect(serverPrimaryAddress(makeServer({ ip: "203.0.113.10" }))).toBe(
      "203.0.113.10",
    );
    expect(
      serverPrimaryAddress(
        makeServer({
          host_addresses: [
            { address: "10.0.0.5", scope: "private", provenance: "guard" },
            { address: "198.51.100.7", scope: "public", provenance: "guard" },
          ],
        }),
      ),
    ).toBe("198.51.100.7");
    expect(
      serverPrimaryAddress(
        makeServer({
          host_addresses: [
            { address: "10.0.0.5", scope: "private", provenance: "guard" },
          ],
        }),
      ),
    ).toBe("10.0.0.5");
    expect(serverPrimaryAddress(makeServer({}))).toBe("not reported");
  });

  it("dedupes explicit domains and service endpoint domains", () => {
    expect(
      serverDomains(
        makeServer({
          domains: ["base.kombified.com", " auth.kombified.com "],
          service_endpoints: [
            {
              url: "https://base.kombified.com",
              source: "guard inventory",
              domain: "base.kombified.com",
            },
            { url: "", source: "guard inventory", domain: "" },
            { url: "", source: "guard inventory" },
          ],
        }),
      ),
    ).toEqual(["base.kombified.com", "auth.kombified.com"]);
    expect(serverDomains(makeServer({}))).toEqual([]);
  });

  it("summarises the StackKit name and observed variant", () => {
    const server = makeServer({
      stackkit: {
        name: "Cloud Kit",
        catalog_ref: "cloud-kit",
        version: "2.1.0",
        mode: "standard",
        context: "cloud",
        paas: "coolify",
        compute_tier: "managed-vps",
        state: "observed",
        sources: ["guard inventory"],
      },
    });
    expect(serverStackKitName(server)).toBe("Cloud Kit");
    expect(
      serverStackKitName(
        makeServer({
          stackkit: { catalog_ref: "x", state: "configured", sources: [] },
        }),
      ),
    ).toBe("x");
    expect(serverStackKitName(makeServer({}))).toBe("not reported");
    expect(serverStackKitVariant(server)).toBe(
      "2.1.0 · standard · cloud · coolify · managed-vps · observed",
    );
    expect(
      serverStackKitVariant(
        makeServer({ stackkit: { state: "", sources: [] } }),
      ),
    ).toBe("state unknown");
    expect(serverStackKitVariant(makeServer({}))).toBe("deployment unknown");
  });

  it("maps health states onto the shared card status vocabulary", () => {
    const cases: Array<{
      state: string;
      expected: ReturnType<typeof serverCardStatus>;
    }> = [
      { state: "healthy", expected: "healthy" },
      { state: "degraded", expected: "degraded" },
      { state: "error", expected: "degraded" },
      { state: "failed", expected: "degraded" },
      { state: "offline", expected: "offline" },
      { state: "stale", expected: "offline" },
      { state: "pending", expected: "pending" },
      { state: "unknown", expected: "unknown" },
      { state: "", expected: "unknown" },
    ];
    for (const { state, expected } of cases) {
      const server = makeServer();
      server.health.state = state;
      expect(serverCardStatus(server)).toBe(expected);
    }
  });

  it("joins provider, role, OS, and the managed-runtime marker into the meta line", () => {
    expect(
      serverCardMeta(
        makeServer({
          capabilities: { provider: "ionos" },
          role: "worker",
          os: "Ubuntu",
          os_version: "24.04",
          arch: "amd64",
          lease_id: "lease-1",
        }),
      ),
    ).toBe("ionos · worker · Ubuntu 24.04/amd64 · managed runtime");
    expect(serverCardMeta(makeServer({ os: "Debian", arch: "arm64" }))).toBe(
      "worker · Debian/arm64",
    );
    expect(
      serverCardMeta(makeServer({ role: "", source: "managed-runtime" })),
    ).toBe("os unknown/arch unknown · managed runtime");
  });

  it("exposes CPU, RAM, and Disk chips through the shared metric shape", () => {
    expect(serverCardMetrics(makeServer())).toEqual([
      { label: "CPU", value: "12.3%" },
      { label: "RAM", value: "41%" },
      { label: "Disk", value: "unknown" },
    ]);
  });

  it("builds the kit facts from StackKit evidence with explicit fallbacks", () => {
    expect(
      serverCardKit(
        makeServer({
          stackkit: {
            name: "Basement Kit",
            version: "1.4.0",
            state: "observed",
            sources: ["guard inventory"],
          },
        }),
      ),
    ).toEqual({
      name: "Basement Kit",
      detail: "1.4.0 · observed",
    });
    expect(serverCardKit(makeServer({}))).toEqual({
      name: "not reported",
      detail: "deployment unknown",
    });
  });
});
