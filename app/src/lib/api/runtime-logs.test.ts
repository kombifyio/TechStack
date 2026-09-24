import { afterEach, describe, expect, it, vi } from "vitest";

import { getRuntimeLogs } from "./runtime-logs";

describe("runtime logs API", () => {
  afterEach(() => vi.restoreAllMocks());

  it("maps log ownership to the canonical deployment scope", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: [
            {
              timestamp: "2026-08-28T08:00:00Z",
              level: "info",
              message: "ready",
              kit_deployment_id: "deployment-1",
            },
          ],
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    const [entry] = await getRuntimeLogs({ serverId: "server-1" });

    expect(entry?.kit_deployment_id).toBe("deployment-1");
  });
});
