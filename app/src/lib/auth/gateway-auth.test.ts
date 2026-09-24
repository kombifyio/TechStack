// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import type { Auth0Client } from "@auth0/auth0-spa-js";

import {
  __setAuth0ClientForTest,
  clearGatewayAuth,
  completeGatewayRedirectIfPresent,
  getGatewayToken,
  startGatewayLogin,
} from "./gateway-auth";

afterEach(() => {
  __setAuth0ClientForTest(null);
});

function fakeClient(over: Partial<Auth0Client>): Auth0Client {
  return over as Auth0Client;
}

describe("getGatewayToken", () => {
  it("returns the silently-acquired audience token", async () => {
    const getTokenSilently = vi.fn().mockResolvedValue("tok_abc");
    __setAuth0ClientForTest(fakeClient({ getTokenSilently }));

    await expect(getGatewayToken()).resolves.toBe("tok_abc");
    expect(getTokenSilently).toHaveBeenCalledTimes(1);
  });

  it("does not navigate away on a silent-auth miss", async () => {
    const loginWithRedirect = vi.fn();
    __setAuth0ClientForTest(
      fakeClient({
        getTokenSilently: vi
          .fn()
          .mockRejectedValue(new Error("login_required")),
        loginWithRedirect,
      }),
    );

    await expect(getGatewayToken()).rejects.toThrow("login_required");
    expect(loginWithRedirect).not.toHaveBeenCalled();
  });

  it("starts an interactive SPA login when asked", async () => {
    const loginWithRedirect = vi.fn().mockResolvedValue(undefined);
    __setAuth0ClientForTest(fakeClient({ loginWithRedirect }));

    await expect(
      startGatewayLogin({ interactive: true, returnTo: "/dashboard" }),
    ).resolves.toBe(true);
    expect(loginWithRedirect).toHaveBeenCalledWith(
      expect.objectContaining({
        authorizationParams: expect.objectContaining({
          prompt: "login",
        }),
        appState: { returnTo: "/dashboard" },
      }),
    );
  });

  it("claims an SPA callback on the app origin", async () => {
    window.history.pushState({}, "", "/?code=abc&state=xyz");
    const handleRedirectCallback = vi.fn().mockResolvedValue({
      appState: { returnTo: "/dashboard" },
    });
    __setAuth0ClientForTest(fakeClient({ handleRedirectCallback }));

    await expect(completeGatewayRedirectIfPresent()).resolves.toBe(
      "/dashboard",
    );
    expect(handleRedirectCallback).toHaveBeenCalledTimes(1);
    expect(window.location.search).not.toContain("code=");
  });

  it("leaves a foreign OIDC callback for the v2 handler", async () => {
    window.history.pushState({}, "", "/?code=abc&state=xyz");
    __setAuth0ClientForTest(
      fakeClient({
        handleRedirectCallback: vi
          .fn()
          .mockRejectedValue(new Error("missing_transaction")),
      }),
    );

    await expect(completeGatewayRedirectIfPresent()).resolves.toBeNull();
    expect(window.location.search).toContain("code=abc");
  });

  it("does not start a second SPA login while one is already navigating", async () => {
    const loginWithRedirect = vi.fn().mockResolvedValue(undefined);
    __setAuth0ClientForTest(fakeClient({ loginWithRedirect }));

    await expect(startGatewayLogin({ returnTo: "/dashboard" })).resolves.toBe(
      true,
    );
    await expect(startGatewayLogin({ returnTo: "/wallet" })).resolves.toBe(
      true,
    );
    expect(loginWithRedirect).toHaveBeenCalledTimes(1);
  });

  it("throws when the token comes back empty", async () => {
    __setAuth0ClientForTest(
      fakeClient({ getTokenSilently: vi.fn().mockResolvedValue("") }),
    );

    await expect(getGatewayToken()).rejects.toThrow("gateway_token_empty");
  });

  it("clears the cached Auth0 SPA session without navigating", async () => {
    const logout = vi.fn().mockResolvedValue(undefined);
    __setAuth0ClientForTest(fakeClient({ logout }));

    await clearGatewayAuth();

    expect(logout).toHaveBeenCalledWith({ openUrl: false });
  });
});
