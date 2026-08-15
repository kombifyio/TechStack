import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  PipelinePreflightError,
  preflightPipeline,
  previewPipeline,
} from "./unifier";

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

  it("runs wizard preflight against the pipeline preview endpoint", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              valid: true,
              resolved_stackkit: "basement-kit",
              detected_addons: ["vpn-overlay"],
              stages: [],
              warnings: [],
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    const result = await preflightPipeline({ name: "stack" });

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/unifier/pipeline/preview"),
      expect.objectContaining({ method: "POST" }),
    );
    expect(result.resolved_stackkit).toBe("basement-kit");
    expect(result.detected_addons).toEqual(["vpn-overlay"]);
  });

  it("keeps the full pipeline preview open beyond the generic CRUD timeout but remains bounded", async () => {
    vi.useFakeTimers();
    let signal: AbortSignal | undefined;
    fetchMock.mockImplementation((_url, init: RequestInit) => {
      signal = init.signal as AbortSignal;
      return new Promise((_resolve, reject) => {
        signal?.addEventListener("abort", () => {
          const error = new Error("aborted");
          error.name = "AbortError";
          reject(error);
        });
      });
    });

    try {
      const preview = previewPipeline({ name: "stack" }).then(
        () => new Error("preview unexpectedly resolved"),
        (error) => error as Error,
      );
      await vi.advanceTimersByTimeAsync(10_001);
      expect(fetchMock).toHaveBeenCalledOnce();
      expect(signal?.aborted).toBe(false);

      await vi.advanceTimersByTimeAsync(50_000);
      expect((await preview).message).toContain("timed out after 60s");
      expect(signal?.aborted).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("fails when backend preview reports validation or StackKit resolution errors", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              valid: false,
              resolved_stackkit: "missing-kit",
              detected_addons: [],
              stages: [],
              errors: [
                {
                  path: "stackkit",
                  code: "stackkit_resolution",
                  message: "StackKit resolution failed",
                },
              ],
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    const result = preflightPipeline({ stackkit: "missing-kit" });

    await expect(result).rejects.toBeInstanceOf(PipelinePreflightError);
    await expect(result).rejects.toThrow("StackKit resolution failed");
  });

  it("sends preview decision context as a sidecar envelope", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              valid: true,
              resolved_stackkit: "basement-kit",
              detected_addons: [],
              stages: [],
              decision_context_hash: "sha256:test",
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    await previewPipeline(
      { name: "stack" },
      {
        channel: "wizard:techie",
        operator: { score: 7, band: "techie", source: "test" },
      },
    );

    const [, init] = fetchMock.mock.calls[0];
    const body = JSON.parse(String(init.body));
    expect((init.headers as Headers).get("Content-Type")).toBe(
      "application/json",
    );
    expect(body.spec).toEqual({ name: "stack" });
    expect(body.decision_context.operator.score).toBe(7);
    expect(body.decision_context.channel).toBe("wizard:techie");
  });
});
