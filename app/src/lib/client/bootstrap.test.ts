import { beforeEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";
import {
  BOOTSTRAP_ENDPOINT,
  clientBootstrap,
  emptyClientBootstrap,
  getClientBootstrap,
  loadClientBootstrap,
  parseClientBootstrap,
  resetClientBootstrapForTesting,
} from "./bootstrap";

const samplePayload = {
  data: {
    edition: "saas-standalone",
    deployment_mode: "saas",
    kombify_edition: "saas-standalone",
    version: "0.6.13",
    public_origin: "https://techstack.kombify.io",
    telemetry: {
      sentry: {
        dsn: "https://public@sentry.example/1",
        environment: "prod",
        release: "abc123",
      },
      posthog: {
        key: "phc_test",
        host: "https://e.kombify.io",
        environment: "prod",
      },
    },
  },
};

function okResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  resetClientBootstrapForTesting();
});

describe("parseClientBootstrap", () => {
  it("parses an enveloped payload", () => {
    const parsed = parseClientBootstrap(samplePayload);
    expect(parsed.edition).toBe("saas-standalone");
    expect(parsed.deploymentMode).toBe("saas");
    expect(parsed.kombifyEdition).toBe("saas-standalone");
    expect(parsed.telemetry.sentry.dsn).toBe("https://public@sentry.example/1");
    expect(parsed.telemetry.posthog.key).toBe("phc_test");
    expect(parsed.telemetry.posthog.host).toBe("https://e.kombify.io");
  });

  it("parses a bare payload without envelope", () => {
    const parsed = parseClientBootstrap(samplePayload.data);
    expect(parsed.edition).toBe("saas-standalone");
    expect(parsed.telemetry.sentry.release).toBe("abc123");
  });

  it("returns empty config for malformed bodies", () => {
    for (const body of [null, undefined, 42, "nope", [], { data: "x" }]) {
      expect(parseClientBootstrap(body)).toEqual(emptyClientBootstrap());
    }
  });

  it("never enables telemetry from partial responses", () => {
    const parsed = parseClientBootstrap({
      data: { telemetry: { sentry: { dsn: 123 }, posthog: null } },
    });
    expect(parsed.telemetry.sentry.dsn).toBe("");
    expect(parsed.telemetry.posthog.key).toBe("");
  });
});

describe("loadClientBootstrap", () => {
  it("fetches once, caches, and publishes to the store", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(okResponse(samplePayload));

    const first = await loadClientBootstrap(fetchImpl as typeof fetch);
    const second = await loadClientBootstrap(fetchImpl as typeof fetch);

    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(fetchImpl).toHaveBeenCalledWith(
      BOOTSTRAP_ENDPOINT,
      expect.objectContaining({
        headers: { Accept: "application/json" },
      }),
    );
    expect(first).toEqual(second);
    expect(get(clientBootstrap).kombifyEdition).toBe("saas-standalone");
    expect(getClientBootstrap().telemetry.posthog.key).toBe("phc_test");
  });

  it("resolves to a telemetry-free config on HTTP errors", async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(new Response("not found", { status: 404 }));

    const result = await loadClientBootstrap(fetchImpl as typeof fetch);
    expect(result).toEqual(emptyClientBootstrap());
    expect(getClientBootstrap()).toEqual(emptyClientBootstrap());
  });

  it("resolves to a telemetry-free config on network failures", async () => {
    const fetchImpl = vi.fn().mockRejectedValue(new Error("offline"));

    const result = await loadClientBootstrap(fetchImpl as typeof fetch);
    expect(result).toEqual(emptyClientBootstrap());
  });
});
