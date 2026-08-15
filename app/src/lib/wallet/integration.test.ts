import { beforeEach, describe, expect, it, vi } from "vitest";

import { findServicesWithoutCredentials } from "./integration";
import { listServiceRegistry } from "$lib/api/registry";
import { getWalletItems } from "$lib/api/wallet";

vi.mock("$lib/api/registry", () => ({
  listServiceRegistry: vi.fn(),
}));

vi.mock("$lib/api/wallet", () => ({
  createWalletItem: vi.fn(),
  getWalletItems: vi.fn(),
  getWalletItemsByStack: vi.fn(),
}));

const mockedListServiceRegistry = vi.mocked(listServiceRegistry);
const mockedGetWalletItems = vi.mocked(getWalletItems);

describe("wallet integration service discovery", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("uses BFF registry and wallet clients instead of collection access", async () => {
    mockedListServiceRegistry.mockResolvedValue({
      catalog: [],
      stacks: [],
      servers: [],
      services: [
        {
          id: "svc-traefik",
          name: "traefik",
          display_name: "Traefik",
          type: "traefik",
          status: "running",
          management_state: "managed",
          stack_id: "stack-1",
          stack_name: "Stack",
          server_id: "node-1",
          server_name: "Node",
          url: "https://traefik.example.test",
        },
        {
          id: "svc-headscale",
          name: "headscale",
          display_name: "Headscale",
          type: "headscale",
          status: "running",
          management_state: "managed",
          stack_id: "stack-1",
          stack_name: "Stack",
          server_id: "node-1",
          server_name: "Node",
        },
      ],
    });
    mockedGetWalletItems.mockResolvedValue([
      {
        id: "wallet-1",
        collectionId: "wallet",
        collectionName: "wallet",
        name: "Existing Headscale key",
        kind: "api_key",
        service_id: "svc-headscale",
      },
    ]);

    const result = await findServicesWithoutCredentials();

    expect(mockedListServiceRegistry).toHaveBeenCalledTimes(1);
    expect(mockedGetWalletItems).toHaveBeenCalledTimes(1);
    expect(result).toHaveLength(1);
    expect(result[0]?.service.id).toBe("svc-traefik");
    expect(result[0]?.missingCredentials[0]).toMatchObject({
      name: "Traefik Dashboard",
      kind: "password",
      service_id: "svc-traefik",
      url: "https://traefik.example.test/dashboard/",
    });
  });
});
