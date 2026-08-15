import { get } from "svelte/store";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { listServiceRegistry } from "$lib/api/registry";
import { servicesStore } from "./services";

vi.mock("$lib/api/registry", () => ({
  listServiceRegistry: vi.fn(),
}));

const listServiceRegistryMock = vi.mocked(listServiceRegistry);

describe("servicesStore", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    servicesStore.reset();
  });

  it("loads services through the registry services API", async () => {
    listServiceRegistryMock.mockResolvedValue({
      catalog: [],
      stacks: [],
      servers: [],
      services: [
        {
          id: "service-1",
          name: "postgres",
          display_name: "Postgres",
          type: "database",
          status: "running",
          management_state: "managed",
          stack_id: "stack-1",
          stack_name: "Data",
          server_id: "server-1",
          server_name: "Foundation",
          port: 5432,
        },
        {
          id: "service-2",
          name: "redis",
          display_name: "Redis",
          type: "cache",
          status: "stopped",
          management_state: "managed",
          stack_id: "stack-2",
          stack_name: "Cache",
          server_id: "server-2",
          server_name: "Worker",
        },
      ],
    });

    const result = await servicesStore.load({
      stackId: "stack-1",
      nodeId: "server-1",
      status: "running",
      serviceType: "database",
      search: "postgres",
    });

    expect(result.success).toBe(true);
    expect(listServiceRegistryMock).toHaveBeenCalledTimes(1);
    expect(get(servicesStore).items).toEqual([
      expect.objectContaining({
        id: "service-1",
        collectionName: "services",
        name: "postgres",
        type: "database",
        node_id: "server-1",
        stack_id: "stack-1",
      }),
    ]);
  });

  it("does not write PocketBase service records for legacy mutations", async () => {
    const result = await servicesStore.create({
      name: "postgres",
      type: "database",
      node_id: "server-1",
      status: "pending",
    });

    expect(result.success).toBe(false);
    expect(result.error).toContain("backend registry APIs");
    expect(listServiceRegistryMock).not.toHaveBeenCalled();
  });

  it("keeps subscription compatibility without PocketBase realtime", async () => {
    listServiceRegistryMock.mockResolvedValue({
      catalog: [],
      stacks: [],
      servers: [],
      services: [],
    });

    const unsubscribe = servicesStore.startSubscription("server-1");

    expect(get(servicesStore).subscribed).toBe(true);
    await vi.waitFor(() => {
      expect(listServiceRegistryMock).toHaveBeenCalledTimes(1);
    });

    unsubscribe();
    expect(get(servicesStore).subscribed).toBe(false);
  });
});
