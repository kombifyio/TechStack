import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { recommendWizard } from "./unifier";

describe("unifier api", () => {
  const originalFetch = globalThis.fetch;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    globalThis.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("requests a server-owned recommendation without identity or kit overrides", async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            status: "ready",
            generated_at: "2026-08-26T00:00:00Z",
            catalog_source: "stackkits-catalog",
            missing_inputs: [],
            stale_inputs: [],
            recommendations: [],
          },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await recommendWizard({
      goals: ["vault"],
      services: [],
      deployment_lane: "self-hosted",
      surface: "easy",
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/api/v1/unifier/recommendations");
    expect(JSON.parse(String(init.body))).toEqual({
      goals: ["vault"],
      services: [],
      deployment_lane: "self-hosted",
      surface: "easy",
    });
  });
});
