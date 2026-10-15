import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const gatewayBase = "https://api.kombify.io/v1/techstack";

vi.setConfig({ testTimeout: 30_000 });

beforeEach(() => {
  vi.resetModules();
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.doUnmock("#lib/auth/gateway-auth.js");
  vi.doUnmock("#lib/stores/postMessageBridge.js");
});

async function channelFor(mode: "self-hosted" | "saas-embedded") {
  const { deploymentMode } = await import("#lib/stores/deploymentMode.js");
  deploymentMode.setMode(mode);
  const { ownerChannelOptions } = await import("./owner-channel");
  return ownerChannelOptions();
}

describe("ownerChannelOptions", () => {
  it("gives a self-hosted instance no token source, so it never opens a Gateway channel", async () => {
    vi.stubEnv("VITE_GATEWAY_API_BASE", gatewayBase);
    expect(await channelFor("self-hosted")).toEqual({});
  });

  it("resolves no token on a SaaS instance when neither Auth0 nor the portal can supply one", async () => {
    vi.stubEnv("VITE_GATEWAY_API_BASE", gatewayBase);
    vi.doMock("#lib/auth/gateway-auth.js", () => ({
      getGatewayToken: vi.fn().mockRejectedValue(new Error("no session")),
    }));
    vi.doMock("#lib/stores/postMessageBridge.js", () => ({
      initBridge: vi.fn(),
      requestGatewayToken: vi.fn().mockRejectedValue(new Error("no portal")),
    }));
    const options = await channelFor("saas-embedded");
    expect(options.decisions).toBeDefined();
    await expect(options.getToken?.()).resolves.toBeNull();
  });
});
