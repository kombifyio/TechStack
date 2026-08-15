// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

const authState = vi.hoisted(() => ({
  deploymentMode: "saas" as "self-hosted" | "saas",
  isAuthenticated: false,
  v2SessionActive: false,
  cloudUser: null as { sub: string } | null,
  init: vi.fn(),
  completePortalLogin: vi.fn(async () => {
    authState.isAuthenticated = true;
    authState.v2SessionActive = true;
    authState.cloudUser = { sub: "portal-user" };
  }),
}));

const bridgeState = vi.hoisted(() => ({
  initBridge: vi.fn(),
  requestAuthToken: vi.fn(async () => "portal-token"),
}));

vi.mock("$app/environment", () => ({
  browser: true,
}));

vi.mock("$lib/stores/auth.svelte", () => ({
  authStore: authState,
}));

vi.mock("$lib/stores/postMessageBridge", () => bridgeState);

function mockParent(parent: Window | object) {
  Object.defineProperty(window, "parent", {
    value: parent,
    configurable: true,
  });
}

describe("refreshEmbeddedCloudSession", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    authState.deploymentMode = "saas";
    authState.isAuthenticated = false;
    authState.v2SessionActive = false;
    authState.cloudUser = null;
    authState.completePortalLogin.mockImplementation(async () => {
      authState.isAuthenticated = true;
      authState.v2SessionActive = true;
      authState.cloudUser = { sub: "portal-user" };
    });
    bridgeState.requestAuthToken.mockResolvedValue("portal-token");
  });

  it("does nothing outside an iframe", async () => {
    mockParent(window);

    const { refreshEmbeddedCloudSession } = await import("./embedded-session");

    await expect(refreshEmbeddedCloudSession()).resolves.toBe(false);
    expect(bridgeState.requestAuthToken).not.toHaveBeenCalled();
    expect(authState.completePortalLogin).not.toHaveBeenCalled();
  });

  it("refreshes SaaS iframe sessions through the parent SSO bridge", async () => {
    mockParent({ postMessage: vi.fn() });

    const { refreshEmbeddedCloudSession } = await import("./embedded-session");

    await expect(refreshEmbeddedCloudSession()).resolves.toBe(true);
    expect(authState.init).toHaveBeenCalled();
    expect(bridgeState.initBridge).toHaveBeenCalled();
    expect(bridgeState.requestAuthToken).toHaveBeenCalled();
    expect(authState.completePortalLogin).toHaveBeenCalledWith("portal-token");
  });

  it("coalesces concurrent refreshes and exchanges a portal token only once", async () => {
    mockParent({ postMessage: vi.fn() });
    let resolveToken!: (token: string) => void;
    bridgeState.requestAuthToken.mockImplementation(
      () => new Promise((resolve) => (resolveToken = resolve)),
    );

    const { refreshEmbeddedCloudSession } = await import("./embedded-session");
    const first = refreshEmbeddedCloudSession();
    const second = refreshEmbeddedCloudSession();
    await vi.waitFor(() => expect(resolveToken).toBeTypeOf("function"));
    resolveToken("shared-token");

    await expect(Promise.all([first, second])).resolves.toEqual([true, true]);
    expect(bridgeState.requestAuthToken).toHaveBeenCalledTimes(1);
    expect(authState.completePortalLogin).toHaveBeenCalledTimes(1);
  });

  it("does not re-exchange the cached portal token for reactive refreshes", async () => {
    mockParent({ postMessage: vi.fn() });

    const { refreshEmbeddedCloudSession } = await import("./embedded-session");

    await expect(refreshEmbeddedCloudSession()).resolves.toBe(true);
    await expect(refreshEmbeddedCloudSession()).resolves.toBe(true);
    expect(bridgeState.requestAuthToken).toHaveBeenCalledTimes(2);
    expect(authState.completePortalLogin).toHaveBeenCalledTimes(1);
  });

  it("re-mints the V2 cookie once with the cached parent token after reprojection", async () => {
    mockParent({ postMessage: vi.fn() });

    const { refreshEmbeddedCloudSession } = await import("./embedded-session");

    await expect(refreshEmbeddedCloudSession()).resolves.toBe(true);
    await expect(
      refreshEmbeddedCloudSession({ forcePortalVerify: true }),
    ).resolves.toBe(true);

    expect(bridgeState.requestAuthToken).toHaveBeenCalledTimes(2);
    expect(authState.completePortalLogin).toHaveBeenCalledTimes(2);
    expect(authState.completePortalLogin).toHaveBeenLastCalledWith(
      "portal-token",
    );
  });

  it("coalesces concurrent forced reprojection refreshes into one portal exchange", async () => {
    mockParent({ postMessage: vi.fn() });
    let resolvePortalLogin!: () => void;
    authState.completePortalLogin.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolvePortalLogin = () => {
            authState.isAuthenticated = true;
            authState.v2SessionActive = true;
            authState.cloudUser = { sub: "portal-user" };
            resolve();
          };
        }),
    );

    const { refreshEmbeddedCloudSession } = await import("./embedded-session");
    const first = refreshEmbeddedCloudSession({ forcePortalVerify: true });
    const second = refreshEmbeddedCloudSession({ forcePortalVerify: true });
    await vi.waitFor(() => expect(resolvePortalLogin).toBeTypeOf("function"));
    resolvePortalLogin();

    await expect(Promise.all([first, second])).resolves.toEqual([true, true]);
    expect(authState.completePortalLogin).toHaveBeenCalledTimes(1);
  });

  it("restores a lost Cloud identity even when the compatibility session remains", async () => {
    mockParent({ postMessage: vi.fn() });

    const { refreshEmbeddedCloudSession } = await import("./embedded-session");

    await expect(refreshEmbeddedCloudSession()).resolves.toBe(true);
    authState.cloudUser = null;
    authState.isAuthenticated = true;
    await expect(refreshEmbeddedCloudSession()).resolves.toBe(true);

    expect(authState.completePortalLogin).toHaveBeenCalledTimes(2);
  });

  it("falls back when the parent cannot provide a token", async () => {
    mockParent({ postMessage: vi.fn() });
    bridgeState.requestAuthToken.mockRejectedValue(new Error("no token"));

    const { refreshEmbeddedCloudSession } = await import("./embedded-session");

    await expect(refreshEmbeddedCloudSession()).resolves.toBe(false);
    expect(authState.completePortalLogin).not.toHaveBeenCalled();
  });
});
