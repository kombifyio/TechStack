import { afterEach, describe, expect, it, vi } from "vitest";

import { getServerPortInventory } from "./port-inventory";

describe("port inventory API", () => {
  afterEach(() => vi.restoreAllMocks());

  it("maps allocation ownership to the canonical deployment scope", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            server_id: "server-1",
            server_generation: 2,
            inventory_revision: 3,
            listeners_complete: true,
            exposures_complete: true,
            allocations: [
              {
                id: "port-1",
                kit_deployment_id: "deployment-1",
                transport: "tcp",
                bind_address: "0.0.0.0",
                port: 443,
                exposure: "public",
                observed_state: "present",
                exposed_state: "present",
                drift_state: "consistent",
                desired: true,
              },
            ],
          },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    const inventory = await getServerPortInventory("server-1");

    expect(inventory.allocations[0]?.kit_deployment_id).toBe("deployment-1");
  });
});
