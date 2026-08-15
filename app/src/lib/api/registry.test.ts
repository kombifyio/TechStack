import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  attachCatalogService,
  getCanonicalServer,
  importObservedService,
  isCurrentCanonicalServer,
  legacyServerState,
  listCanonicalServers,
  listServiceRegistry,
  ManagementStateContractError,
  migrateRegistryService,
  serverRolloutReady,
  verifyRegistryService,
  deleteRegistryService,
  type CanonicalServer,
} from "./registry";

function canonicalServer(
  overrides: Partial<CanonicalServer> = {},
): CanonicalServer {
  return {
    id: "server-1",
    techstack_id: "techstack-1",
    name: "node-a",
    worker_id: "agent-1",
    lifecycle: { state: "active", desired_state: "running" },
    connection: {
      state: "connected",
      changed_at: "2026-08-12T10:00:00Z",
      last_heartbeat_at: "2026-08-12T10:00:00Z",
      staleness_seconds: 3,
    },
    health: { state: "healthy", observed_at: "2026-08-12T10:00:00Z" },
    channels: [],
    inventory_revision: 7,
    provider: { lease_id: "lease-1" },
    mutations_allowed: true,
    created_at: "2026-08-01T10:00:00Z",
    updated_at: "2026-08-12T10:00:00Z",
    ...overrides,
  };
}

describe("registry api", () => {
  const originalFetch = globalThis.fetch;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    globalThis.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("loads service catalog and observed inventory from the registry BFF", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              catalog: [{ id: "pocket_id", display_name: "Pocket ID" }],
              stacks: [{ id: "stack-1", name: "Demo" }],
              servers: [{ id: "node-1", stack_id: "stack-1" }],
              services: [
                {
                  id: "svc-1",
                  name: "custom_dashboard",
                  display_name: "Custom Dashboard",
                  application_key: "custom_dashboard",
                  application_name: "Custom Dashboard",
                  status: "observed",
                  management_state: "observed",
                  placement_scope: "stack",
                  move_allowed: false,
                  move_blocked_reason:
                    "Observed unmanaged applications must be adopted before they can be moved.",
                },
              ],
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    const result = await listServiceRegistry();

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/registry/services"),
      expect.objectContaining({ credentials: "include" }),
    );
    expect(result.catalog[0].id).toBe("pocket_id");
    expect(result.services[0].management_state).toBe("observed");
    expect(result.services[0].application_name).toBe("Custom Dashboard");
    expect(result.services[0].move_allowed).toBe(false);
  });

  it("posts catalog attach requests to the registry BFF", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              service: {
                id: "svc-1",
                name: "vaultwarden",
                display_name: "Vaultwarden",
                status: "pending",
                management_state: "managed",
              },
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    const result = await attachCatalogService({
      stack_id: "stack-1",
      server_id: "node-1",
      service_id: "vaultwarden",
    });

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/registry/services/attach"),
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          stack_id: "stack-1",
          server_id: "node-1",
          service_id: "vaultwarden",
        }),
      }),
    );
    expect(result.management_state).toBe("managed");
  });

  it("posts unmanaged imports as observed service registration", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              service: {
                id: "svc-2",
                name: "custom_dashboard",
                display_name: "Custom Dashboard",
                status: "observed",
                management_state: "observed",
              },
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    const result = await importObservedService({
      stack_id: "stack-1",
      server_id: "node-1",
      name: "custom-dashboard",
      port: 8088,
    });

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/registry/services/import"),
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          stack_id: "stack-1",
          server_id: "node-1",
          name: "custom-dashboard",
          port: 8088,
        }),
      }),
    );
    expect(result.status).toBe("observed");
  });

  it("posts migrate requests to the registry BFF", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              job_id: "job-migrate-1",
              source_service: {
                id: "svc-1",
                status: "migrating",
                management_state: "managed",
              },
              target_service: {
                id: "svc-new",
                status: "pending_verification",
                management_state: "managed",
              },
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    const result = await migrateRegistryService("svc-1", "node-2");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/registry/services/migrate"),
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          service_id: "svc-1",
          target_server_id: "node-2",
        }),
      }),
    );
    expect(result.job_id).toBe("job-migrate-1");
    expect(result.source_service.status).toBe("migrating");
    expect(result.target_service.status).toBe("pending_verification");
  });

  it("posts verify requests to the registry BFF", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              service: {
                id: "svc-new",
                status: "running",
                management_state: "managed",
              },
              archived_service: {
                id: "svc-1",
                status: "archived",
                management_state: "managed",
              },
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    const result = await verifyRegistryService("svc-new");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/registry/services/verify"),
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ service_id: "svc-new" }),
      }),
    );
    expect(result.service.status).toBe("running");
    expect(result.archived_service?.status).toBe("archived");
  });

  it("sends delete request to the registry BFF", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: { message: "Service successfully deleted", id: "svc-1" },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    const result = await deleteRegistryService("svc-1");

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/registry/services/svc-1"),
      expect.objectContaining({
        method: "DELETE",
      }),
    );
    expect(result.id).toBe("svc-1");
  });

  it("reads servers from the canonical route, not the registry projection", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ data: [canonicalServer()] }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      ),
    );

    const servers = await listCanonicalServers();

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/servers"),
      expect.objectContaining({ credentials: "include" }),
    );
    expect(fetchMock.mock.calls[0][0]).not.toContain("/api/v1/registry/");
    expect(servers[0].connection.last_heartbeat_at).toBe(
      "2026-08-12T10:00:00Z",
    );
    expect(servers[0].worker_id).toBe("agent-1");
  });

  it("scopes the canonical server list by Techstack and reads one server by id", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ data: canonicalServer() }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      ),
    );

    await getCanonicalServer("server 1/a");
    expect(fetchMock.mock.calls[0][0]).toContain(
      "/api/v1/servers/server%201%2Fa",
    );

    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ data: [] }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      ),
    );
    await listCanonicalServers("techstack-1");
    expect(fetchMock.mock.calls[1][0]).toContain(
      "/api/v1/servers?techstack_id=techstack-1",
    );
  });

  it("keeps decommissioned aggregates out of current dashboard inventory", () => {
    expect(isCurrentCanonicalServer(canonicalServer())).toBe(true);
    expect(
      isCurrentCanonicalServer(
        canonicalServer({
          lifecycle: {
            state: "decommissioned",
            desired_state: "absent",
            ended_at: "2026-08-14T04:00:00Z",
          },
        }),
      ),
    ).toBe(false);
  });

  // Pins the client mirror of pkg/serverregistry/legacy_projection.go against
  // the mapping table in that file's doc comment. If the Go side changes, this
  // table has to change with it.
  it("collapses the canonical connection/health pair exactly like the Go projection", () => {
    const cases: Array<[string, string, string]> = [
      ["connected", "healthy", "healthy"],
      ["connected", "unknown", "healthy"],
      ["connected", "degraded", "degraded"],
      ["connected", "unhealthy", "degraded"],
      ["degraded", "healthy", "degraded"],
      ["stale", "healthy", "stale"],
      ["offline", "healthy", "offline"],
      ["revoked", "healthy", "offline"],
      ["pending", "healthy", "provisioned"],
      ["connecting", "healthy", "provisioned"],
      ["", "", "provisioned"],
    ];
    for (const [connection, health, expected] of cases) {
      expect(legacyServerState(connection, health)).toBe(expected);
    }
  });

  it("keeps rollout readiness stricter than mutations_allowed", () => {
    expect(serverRolloutReady(canonicalServer())).toBe(true);
    // Degraded connection still allows mutations, but never a rollout.
    expect(
      serverRolloutReady(
        canonicalServer({
          connection: { state: "degraded", changed_at: "2026-08-12T10:00:00Z" },
          mutations_allowed: true,
        }),
      ),
    ).toBe(false);
    expect(
      serverRolloutReady(
        canonicalServer({
          lifecycle: { state: "decommissioning", desired_state: "absent" },
        }),
      ),
    ).toBe(false);
  });

  it("rejects a service payload whose management_state is missing", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              catalog: [],
              stacks: [],
              servers: [],
              // Field dropped by the API. Silently rendering this as
              // "not managed" would strip every managed-only control.
              services: [
                { id: "svc-1", name: "vaultwarden", status: "running" },
              ],
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    await expect(listServiceRegistry()).rejects.toBeInstanceOf(
      ManagementStateContractError,
    );
  });

  it("rejects an unrecognized management_state instead of downgrading it", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              catalog: [],
              stacks: [],
              servers: [],
              services: [
                {
                  id: "svc-1",
                  name: "vaultwarden",
                  status: "running",
                  management_state: "supervised",
                },
              ],
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    await expect(listServiceRegistry()).rejects.toThrow(/supervised/);
  });

  it("accepts the canonical management_state values case-insensitively", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              catalog: [],
              stacks: [],
              servers: [],
              services: [
                { id: "svc-1", name: "a", management_state: " Managed " },
                { id: "svc-2", name: "b", management_state: "OBSERVED" },
              ],
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    const result = await listServiceRegistry();
    expect(result.services.map((s) => s.management_state)).toEqual([
      "managed",
      "observed",
    ]);
  });
});
