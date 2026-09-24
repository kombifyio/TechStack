import { afterEach, describe, expect, it, vi } from "vitest";

import { listWorkers } from "./workers";

describe("workers API", () => {
  afterEach(() => vi.restoreAllMocks());

  it("reads the canonical deployment scope from worker inventory", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: [
            {
              id: "worker-1",
              hostname: "node-1",
              ip: "192.0.2.1",
              os: "linux",
              arch: "amd64",
              status: "connected",
              kit_deployment_id: "deployment-1",
            },
          ],
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    const [worker] = await listWorkers();

    expect(worker.kit_deployment_id).toBe("deployment-1");
  });
});
