import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { getInfo } from "./health";

describe("health api", () => {
  const originalFetch = globalThis.fetch;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    globalThis.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("unwraps the PocketBase API envelope for info version", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            service: "techstack",
            version: "0.1.0",
            revision: "02fa3578e0e6f74362d4208023a241cf4d2434ac",
          },
          meta: { request_id: "req-1" },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await expect(getInfo()).resolves.toMatchObject({
      service: "techstack",
      version: "0.1.0",
      revision: "02fa3578e0e6f74362d4208023a241cf4d2434ac",
    });
  });

  it("keeps compatibility with a flat info response", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          service: "techstack",
          version: "dev",
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await expect(getInfo()).resolves.toMatchObject({
      service: "techstack",
      version: "dev",
    });
  });

  it("rejects failed or identity-less responses so the UI can use compile fallback", async () => {
    fetchMock.mockResolvedValueOnce(new Response("", { status: 503 }));
    await expect(getInfo()).rejects.toThrow(/HTTP 503/);

    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { service: "techstack" } }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    await expect(getInfo()).rejects.toThrow(/missing product version/);
  });
});
