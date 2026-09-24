// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

const authState = vi.hoisted(() => ({
  deploymentMode: "saas" as "self-hosted" | "saas",
  v2SessionActive: false,
  refreshSession: vi.fn(),
  clearSession: vi.fn(),
  logout: vi.fn(),
  initiateCloudLogin: vi.fn(
    () => "https://techstack.kombify.io/api/v2/auth/login",
  ),
}));

const sentryState = vi.hoisted(() => {
  const scope = {
    setLevel: vi.fn(),
    setTag: vi.fn(),
  };
  return {
    captureMessage: vi.fn(),
    scope,
    withScope: vi.fn((callback: (scope: unknown) => void) => {
      callback(scope);
    }),
  };
});

const embeddedState = vi.hoisted(() => ({
  refreshEmbeddedCloudSession: vi.fn(async () => false),
  isEmbeddedWindow: vi.fn(() => false),
}));

const gatewayAuthState = vi.hoisted(() => ({
  startGatewayLogin: vi.fn(async () => true),
}));

vi.mock("@sentry/sveltekit", () => sentryState);

vi.mock("#lib/auth/embedded-session.js", () => embeddedState);

vi.mock("#lib/auth/gateway-auth.js", () => gatewayAuthState);

vi.mock("#lib/stores/auth.svelte.js", () => ({
  authStore: authState,
}));

import { authHandler } from "./authHandler.svelte";

describe("authHandler reauth handling", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authHandler.reset();
    authState.deploymentMode = "saas";
    authState.v2SessionActive = false;
    authState.refreshSession.mockImplementation(async () => {
      authState.v2SessionActive = true;
      return true;
    });
  });

  it("keeps the retry and opens reauth when retry still 401s after refresh", async () => {
    const retry = vi.fn(async () => {
      throw Object.assign(new Error("Request failed: 401"), {
        status: 401,
        code: "authentication_required",
        method: "GET",
        url: "https://techstack.kombify.io/api/v1/features?token=secret",
        requestId: "req-123",
      });
    });

    await authHandler.handleUnauthorized(
      retry,
      "Your session has expired. Please log in again to continue.",
    );

    expect(authState.refreshSession).toHaveBeenCalled();
    expect(retry).toHaveBeenCalledOnce();
    expect(authHandler.showReloginModal).toBe(true);
    expect(authHandler.errorMessage).toBe(
      "Your session has expired. Please log in again to continue.",
    );
    expect(sentryState.captureMessage).toHaveBeenCalledWith(
      "techstack_session_reauth_required",
    );
    expect(sentryState.scope.setTag).toHaveBeenCalledWith("api_status", "401");
    expect(sentryState.scope.setTag).toHaveBeenCalledWith(
      "api_code",
      "authentication_required",
    );
    expect(sentryState.scope.setTag).toHaveBeenCalledWith("api_method", "GET");
    expect(sentryState.scope.setTag).toHaveBeenCalledWith(
      "api_path",
      "/api/v1/features",
    );
    expect(sentryState.scope.setTag).toHaveBeenCalledWith(
      "request_id",
      "req-123",
    );
  });
});

describe("gateway-token recovery ladder (standalone SaaS)", () => {
  const gatewayError = () =>
    Object.assign(new Error("Gateway authentication unavailable"), {
      status: 401,
      code: "gateway_auth_unavailable",
    });

  beforeEach(() => {
    vi.clearAllMocks();
    authHandler.reset();
    window.sessionStorage.clear();
    authState.deploymentMode = "saas";
    authState.v2SessionActive = false;
    authState.refreshSession.mockImplementation(async () => {
      authState.v2SessionActive = true;
      return true;
    });
    embeddedState.isEmbeddedWindow.mockReturnValue(false);
    embeddedState.refreshEmbeddedCloudSession.mockResolvedValue(false);
    gatewayAuthState.startGatewayLogin.mockResolvedValue(true);
  });

  it("auto-redirects exactly once through the SPA gateway login, no modal", async () => {
    const outcome = await authHandler.handleUnauthorized(
      undefined,
      undefined,
      gatewayError(),
    );

    expect(outcome).toBe("redirecting");
    expect(gatewayAuthState.startGatewayLogin).toHaveBeenCalledTimes(1);
    expect(authState.initiateCloudLogin).not.toHaveBeenCalled();
    expect(authHandler.showReloginModal).toBe(false);
    expect(
      window.sessionStorage.getItem("techstack:auth:spa_gateway_login_at"),
    ).not.toBeNull();
  });

  it("swallows concurrent 401s while the redirect is in flight", async () => {
    await authHandler.handleUnauthorized(undefined, undefined, gatewayError());
    const second = await authHandler.handleUnauthorized(
      undefined,
      undefined,
      gatewayError(),
    );

    expect(second).toBe("redirecting");
    expect(gatewayAuthState.startGatewayLogin).toHaveBeenCalledTimes(1);
    expect(authState.initiateCloudLogin).not.toHaveBeenCalled();
  });

  it("falls back to v2 cloud login when the SPA client cannot start", async () => {
    gatewayAuthState.startGatewayLogin.mockResolvedValueOnce(false);

    const outcome = await authHandler.handleUnauthorized(
      undefined,
      undefined,
      gatewayError(),
    );

    expect(outcome).toBe("redirecting");
    expect(authState.initiateCloudLogin).toHaveBeenCalledTimes(1);
  });

  it("falls to the inline panel when the marker is fresh (no redirect loop)", async () => {
    window.sessionStorage.setItem(
      "techstack:auth:spa_gateway_login_at",
      String(Date.now()),
    );

    const outcome = await authHandler.handleUnauthorized(
      undefined,
      undefined,
      gatewayError(),
    );

    expect(outcome).toBe("reauth_required");
    expect(authState.initiateCloudLogin).not.toHaveBeenCalled();
    expect(authHandler.showReloginModal).toBe(false);
    expect(sentryState.scope.setTag).toHaveBeenCalledWith(
      "failure_class",
      "gateway_token",
    );
    expect(sentryState.scope.setTag).toHaveBeenCalledWith(
      "recovery_rung",
      "inline_prompt",
    );
  });

  it("never auto-redirects when the caller disallows it (wizard)", async () => {
    const outcome = await authHandler.handleUnauthorized(
      undefined,
      undefined,
      gatewayError(),
      { allowAutoRedirect: false },
    );

    expect(outcome).toBe("reauth_required");
    expect(authState.initiateCloudLogin).not.toHaveBeenCalled();
    expect(authHandler.showReloginModal).toBe(false);
  });

  it("keeps the embedded bridge path untouched in an iframe", async () => {
    embeddedState.isEmbeddedWindow.mockReturnValue(true);
    embeddedState.refreshEmbeddedCloudSession.mockResolvedValue(true);
    // The cookie-session refresh reports inactive so the ladder reaches the
    // embedded bridge rung.
    authState.refreshSession.mockImplementation(async () => {
      authState.v2SessionActive = false;
      return false;
    });

    const retry = vi.fn(async () => {});
    const outcome = await authHandler.handleUnauthorized(
      retry,
      undefined,
      gatewayError(),
    );

    expect(outcome).toBe("recovered");
    expect(embeddedState.refreshEmbeddedCloudSession).toHaveBeenCalled();
    expect(authState.initiateCloudLogin).not.toHaveBeenCalled();
    expect(retry).toHaveBeenCalledOnce();
  });

  it("shows the modal for a genuinely dead session", async () => {
    authState.refreshSession.mockImplementation(async () => {
      authState.v2SessionActive = false;
      return false;
    });

    const outcome = await authHandler.handleUnauthorized(
      undefined,
      undefined,
      gatewayError(),
    );

    expect(outcome).toBe("modal_shown");
    expect(authHandler.showReloginModal).toBe(true);
    expect(authState.initiateCloudLogin).not.toHaveBeenCalled();
  });

  it("joins concurrent recoveries into a single refresh", async () => {
    const [first, second] = await Promise.all([
      authHandler.handleUnauthorized(undefined, undefined, gatewayError()),
      authHandler.handleUnauthorized(undefined, undefined, gatewayError()),
    ]);

    expect(first).toBe("redirecting");
    expect(second).toBe("redirecting");
    expect(authState.refreshSession).toHaveBeenCalledTimes(1);
    expect(gatewayAuthState.startGatewayLogin).toHaveBeenCalledTimes(1);
    expect(authState.initiateCloudLogin).not.toHaveBeenCalled();
  });
});

describe("origin trust failures", () => {
  it("does not offer re-login when the origin cannot verify the edge decision", async () => {
    // The 401 refuses the request but says nothing about the session. Running
    // the recovery ladder sent users through Universal Login against a cause
    // no sign-in repairs, and straight back into the same refusal.
    const retry = vi.fn(async () => {});
    // authHandler is a module singleton; clear what earlier cases recorded so
    // "the ladder never ran" is an assertion about this case only.
    authState.refreshSession.mockClear();

    const outcome = await authHandler.handleUnauthorized(retry, undefined, {
      status: 401,
      details: { reason_code: "edge_decision_unverifiable", retryable: true },
    });

    expect(outcome).toBe("origin_unverifiable");
    expect(authHandler.showReloginModal).toBe(false);
    expect(retry).not.toHaveBeenCalled();
    expect(authState.refreshSession).not.toHaveBeenCalled();
  });
});
