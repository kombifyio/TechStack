import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ManagementStateContractError } from "./registry";
import {
  listInventoryServers,
  listInventoryServicePage,
  listInventoryServices,
  type InventoryServer,
  type InventoryServerList,
} from "./inventory";

function servicePage(services: unknown[]): unknown {
  return {
    observed_at: "2026-07-21T10:00:00Z",
    freshness: { state: "fresh", stale_after_seconds: 60 },
    inventory_revision: 3,
    collection_cursor: "snapshot-1",
    page_size: services.length,
    services,
  };
}

function inventoryService(overrides: Record<string, unknown> = {}) {
  return {
    id: "svc-1",
    stack_id: "stack-1",
    server_id: "server-1",
    key: "vaultwarden",
    name: "Vaultwarden",
    management_state: "managed",
    desired_state: "running",
    observed_state: "running",
    freshness: { state: "fresh", stale_after_seconds: 60 },
    inventory_revision: 3,
    health: { state: "healthy" },
    stackkit: {},
    links: [],
    allowed_actions: ["start", "stop"],
    source: "stackkits-inventory",
    ...overrides,
  };
}

function server(id: string, revision: number): InventoryServer {
  return {
    id,
    name: id,
    freshness: { state: "fresh", stale_after_seconds: 60 },
    inventory_revision: revision,
    addresses: {
      public_ips: [],
      private_ips: [],
      local_ips: [],
      domains: [],
    },
    platform: {},
    stackkit: {},
    lifecycle: { state: "active" },
    desired: { state: "running" },
    lease: { state: "active" },
    cleanup: {
      state: "not_requested",
      provider_absence_verified: false,
    },
    connection: { state: "connected" },
    health: { state: "healthy" },
  };
}

function serverPage(
  servers: InventoryServer[],
  collectionCursor: string,
  nextCursor?: string,
): InventoryServerList {
  return {
    observed_at: "2026-07-21T10:00:00Z",
    freshness: { state: "fresh", stale_after_seconds: 60 },
    inventory_revision: Math.max(
      0,
      ...servers.map((item) => item.inventory_revision),
    ),
    collection_cursor: collectionCursor,
    next_cursor: nextCursor,
    page_size: servers.length,
    servers,
  };
}

describe("inventory api", () => {
  const originalFetch = globalThis.fetch;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    globalThis.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("traverses a stable server collection without truncating existing consumers", async () => {
    fetchMock
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            data: serverPage([server("server-1", 4)], "snapshot-1", "page-2"),
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            data: serverPage([server("server-2", 7)], "snapshot-1"),
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      );

    const result = await listInventoryServers();

    expect(result.servers.map((item) => item.id)).toEqual([
      "server-1",
      "server-2",
    ]);
    expect(result.inventory_revision).toBe(7);
    expect(result.collection_cursor).toBe("snapshot-1");
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      expect.stringContaining(
        "/api/v1/inventory/servers?limit=200&cursor=page-2",
      ),
      expect.objectContaining({ credentials: "include" }),
    );
  });

  it("binds service paging requests to the requested server filter", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: {
            observed_at: "2026-07-21T10:00:00Z",
            freshness: { state: "fresh", stale_after_seconds: 60 },
            inventory_revision: 0,
            collection_cursor: "snapshot-1",
            page_size: 0,
            services: [],
          },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await listInventoryServicePage({
      cursor: "page-2",
      limit: 25,
      serverId: "server-1",
    });

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining(
        "/api/v1/inventory/services?limit=25&cursor=page-2&server_id=server-1",
      ),
      expect.objectContaining({ credentials: "include" }),
    );
  });

  it("rejects a cursor cycle instead of polling the same page forever", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: serverPage([server("server-1", 1)], "snapshot-1", "loop"),
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    await expect(listInventoryServers()).rejects.toThrow(
      "Inventory server cursor did not advance",
    );
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("carries the canonical management_state through the service pages", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: servicePage([
            inventoryService(),
            inventoryService({ id: "svc-2", management_state: "observed" }),
          ]),
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    const result = await listInventoryServices();

    expect(result.services.map((item) => item.management_state)).toEqual([
      "managed",
      "observed",
    ]);
  });

  it("fails the inventory read when a service arrives without management_state", async () => {
    // A dropped ownership field used to degrade every service to "not
    // managed" in the Applications view without any visible error.
    const { management_state: _dropped, ...withoutOwnership } =
      inventoryService();
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ data: servicePage([withoutOwnership]) }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );

    await expect(listInventoryServices()).rejects.toBeInstanceOf(
      ManagementStateContractError,
    );
  });
});
