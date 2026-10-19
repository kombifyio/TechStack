import { afterEach, describe, expect, it, vi } from "vitest";
import { loadUseCaseCatalog } from "./useCaseCatalog.js";

describe("loadUseCaseCatalog", () => {
  afterEach(() => vi.restoreAllMocks());

  // A failed read must never look like "no use case is released yet": every
  // card would read "Coming soon" and a homelab without services could be
  // created. The wizard models loading, error and ready on top of this.
  it("rejects when the catalog cannot be read", async () => {
    vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("offline"));
    await expect(loadUseCaseCatalog()).rejects.toBeDefined();
  });

  it("rejects on a server error instead of returning an empty catalog", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: { message: "unavailable" } }), {
        status: 503,
        headers: { "content-type": "application/json" },
      }),
    );
    await expect(loadUseCaseCatalog()).rejects.toBeDefined();
  });

  it("rejects a catalog that lists no use cases", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ data: { use_cases: [] } }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    await expect(loadUseCaseCatalog()).rejects.toThrow(/no use cases/);
  });

  it("reads a deployment that ships no catalog as a state, not a failure", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({ data: { configured: false, use_cases: [] } }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );
    await expect(loadUseCaseCatalog()).resolves.toEqual(new Map());
  });
});
