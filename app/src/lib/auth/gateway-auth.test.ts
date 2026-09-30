// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import type { Auth0Client } from "@auth0/auth0-spa-js";

import {
  __setAuth0ClientForTest,
  clearGatewayAuth,
  completeGatewayRedirectIfPresent,
  getGatewayToken,
  renewGatewaySession,
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

  // platform-jx5m6: "Sign in again" forced prompt=login/max_age=0, so a live
  // Auth0 session still demanded credentials (one login per device rule).
  it("renews silently first and never forces a credential login", async () => {
    const getTokenSilently = vi
      .fn()
      .mockRejectedValue(new Error("login_required"));
    const loginWithRedirect = vi.fn().mockResolvedValue(undefined);
    __setAuth0ClientForTest(
      fakeClient({ getTokenSilently, loginWithRedirect }),
    );

    await expect(renewGatewaySession({ returnTo: "/dashboard" })).resolves.toBe(
      "redirecting",
    );

    expect(getTokenSilently).toHaveBeenCalledTimes(1);
    expect(loginWithRedirect).toHaveBeenCalledTimes(1);
    const { authorizationParams, appState } =
      loginWithRedirect.mock.calls[0][0];
    expect(authorizationParams.prompt).toBeUndefined();
    expect(authorizationParams.max_age).toBeUndefined();
    expect(appState).toEqual({ returnTo: "/dashboard" });
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

  // platform-jx5m6: a staff MFA refusal made every API call replay the same
  // rejected refresh token (hundreds of Auth0 mfa_required events per hour).
  it("stops silent refresh on an MFA refusal and steps up exactly once", async () => {
    const refusal = Object.assign(
      new Error("Multifactor authentication required"),
      {
        error: "mfa_required",
      },
    );
    const getTokenSilently = vi.fn().mockRejectedValue(refusal);
    const loginWithRedirect = vi.fn().mockResolvedValue(undefined);
    __setAuth0ClientForTest(
      fakeClient({ getTokenSilently, loginWithRedirect }),
    );

    const concurrent = await Promise.allSettled([
      getGatewayToken(),
      getGatewayToken(),
      getGatewayToken(),
    ]);
    // Auth init, the recovery ladder and the renewal panel may all ask.
    await startGatewayLogin({ returnTo: "/dashboard" });
    await renewGatewaySession({ returnTo: "/dashboard" });
    await expect(getGatewayToken()).rejects.toBe(refusal);

    expect(concurrent.every((r) => r.status === "rejected")).toBe(true);
    expect(getTokenSilently).toHaveBeenCalledTimes(1);
    expect(loginWithRedirect).toHaveBeenCalledTimes(1);
    const params = loginWithRedirect.mock.calls[0][0].authorizationParams;
    expect(params.acr_values).toBe(
      "http://schemas.openid.net/pape/policies/2007/06/multi-factor",
    );
    expect(params.prompt).toBeUndefined();
    expect(loginWithRedirect.mock.calls[0][0].appState).toEqual({
      returnTo: "/dashboard",
    });
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
