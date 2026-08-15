import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { chosenHomelabName, getHomelab } from "./homelab";
import type { HomelabSummary } from "./homelab";

describe("homelab api", () => {
  const originalFetch = globalThis.fetch;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    globalThis.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it("returns the homelab umbrella with its kit deployments", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            homelab: {
              id: "hl-1",
              name: "My Homelab",
              intent: { goals: ["photos"] },
              created: "2026-07-29T12:00:00Z",
              updated: "2026-07-29T12:00:00Z",
            },
            kit_deployments: [{ id: "stack-1", name: "basement" }],
          },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    const view = await getHomelab();
    expect(view?.homelab?.id).toBe("hl-1");
    expect(view?.kit_deployments).toHaveLength(1);
    expect(view?.kit_deployments[0]?.id).toBe("stack-1");
    const requestedUrl = String(fetchMock.mock.calls[0]?.[0]);
    expect(requestedUrl).toContain("/api/v1/homelab");
  });

  it("keeps a null homelab when only legacy deployments exist", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            homelab: null,
            kit_deployments: [{ id: "stack-legacy", name: "old" }],
          },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    const view = await getHomelab();
    expect(view?.homelab).toBeNull();
    expect(view?.kit_deployments).toHaveLength(1);
  });

  it("maps the guided 404 to null", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          error: {
            code: "NOT_FOUND",
            message: "No homelab provisioned yet",
            details: { reason_code: "homelab_not_found" },
          },
        }),
        { status: 404, headers: { "content-type": "application/json" } },
      ),
    );

    await expect(getHomelab()).resolves.toBeNull();
  });

  it("propagates non-404 failures", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: "INTERNAL_ERROR", message: "boom" },
        }),
        { status: 500, headers: { "content-type": "application/json" } },
      ),
    );

    await expect(getHomelab()).rejects.toMatchObject({ status: 500 });
  });
});

function summary(overrides: Partial<HomelabSummary>): HomelabSummary {
  return {
    id: "hl-1",
    name: "homelab",
    intent: {},
    created: "2026-07-30T09:00:00Z",
    updated: "2026-07-30T09:00:00Z",
    ...overrides,
  };
}

describe("chosenHomelabName", () => {
  it("reports no chosen name while the row still carries the generated one", () => {
    expect(chosenHomelabName(summary({ named: false }))).toBeNull();
    expect(chosenHomelabName(null)).toBeNull();
    expect(chosenHomelabName(undefined)).toBeNull();
  });

  it("accepts a rename to exactly the generated name", () => {
    // The server flag is the authority; a string compare would silently
    // discard this operator's choice.
    expect(chosenHomelabName(summary({ name: "homelab", named: true }))).toBe(
      "homelab",
    );
  });

  it("falls back to the string compare when the flag is absent", () => {
    expect(chosenHomelabName(summary({ name: "Homelab" }))).toBeNull();
    expect(chosenHomelabName(summary({ name: "Basement Lab" }))).toBe(
      "Basement Lab",
    );
  });

  it("ignores whitespace-only names", () => {
    expect(chosenHomelabName(summary({ name: "   ", named: true }))).toBeNull();
  });
});
