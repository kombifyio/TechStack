import { describe, expect, it, vi } from "vitest";

import { listConnectDevices } from "./connectDevices";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

describe("listConnectDevices", () => {
  // The route is still rolling out. A missing route must read as "not
  // available yet", a broken one as an error, and neither as "no devices".
  it("keeps route absence, read failure and an empty list apart", async () => {
    const token = vi.fn().mockResolvedValue("user-token");
    const outcome = async (fetchImpl: typeof fetch) =>
      (await listConnectDevices({ fetchImpl, getToken: token })).state;

    for (const status of [403, 404, 503]) {
      expect(
        await outcome(async () => jsonResponse(status, { code: "x" })),
      ).toBe("unavailable");
    }
    expect(
      await outcome(async () =>
        jsonResponse(409, { reason_code: "connect_unavailable" }),
      ),
    ).toBe("unavailable");
    expect(await outcome(async () => jsonResponse(502, {}))).toBe("error");
    expect(
      await outcome(async () => {
        throw new TypeError("network");
      }),
    ).toBe("error");
    expect(
      await outcome(async () => jsonResponse(200, { unexpected: true })),
    ).toBe("error");
    expect(
      (
        await listConnectDevices({
          fetchImpl: async () => jsonResponse(200, { devices: [] }),
          getToken: vi.fn().mockRejectedValue(new Error("no gateway auth")),
        })
      ).state,
    ).toBe("unavailable");

    const ready = await listConnectDevices({
      getToken: token,
      fetchImpl: async () =>
        jsonResponse(200, {
          devices: [
            { device_id: "pc-1", display_name: "Desk", installations: [] },
            { display_name: "no identity" },
          ],
        }),
    });
    expect(
      ready.state === "ready" && ready.devices.map((d) => d.device_id),
    ).toEqual(["pc-1"]);
  });
});
