// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

const authState = vi.hoisted(() => ({
  deploymentMode: "saas" as "self-hosted" | "saas",
  v2SessionActive: false,
  init: vi.fn(),
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

vi.mock("@sentry/sveltekit", () => sentryState);

vi.mock("$lib/auth/embedded-session", () => embeddedState);

vi.mock("$lib/auth/pocketbase-compat", () => ({
  getPocketBaseCompatStoredAuthToken: vi.fn(() => null),
  isPocketBaseAuthCompatEnabled: vi.fn(() => false),
}));

vi.mock("$lib/stores/auth.svelte", () => ({
  authStore: authState,
}));

import { authHandler } from "./authHandler.svelte";

describe("authHandler reauth handling", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authHandler.reset();
    authState.deploymentMode = "saas";
    authState.v2SessionActive = false;
    authState.init.mockImplementation(async () => {
      authState.v2SessionActive = true;
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

    expect(authState.init).toHaveBeenCalled();
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
    authState.init.mockImplementation(async () => {
      authState.v2SessionActive = true;
    });
    embeddedState.isEmbeddedWindow.mockReturnValue(false);
    embeddedState.refreshEmbeddedCloudSession.mockResolvedValue(false);
  });

  it("auto-redirects exactly once through the cloud login, no modal", async () => {
    const outcome = await authHandler.handleUnauthorized(
      undefined,
      undefined,
      gatewayError(),
    );

    expect(outcome).toBe("redirecting");
    expect(authState.initiateCloudLogin).toHaveBeenCalledTimes(1);
    expect(authHandler.showReloginModal).toBe(false);
    expect(
      window.sessionStorage.getItem("techstack:auth:auto_relogin_at"),
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
    expect(authState.initiateCloudLogin).toHaveBeenCalledTimes(1);
  });

  it("falls to the inline panel when the marker is fresh (no redirect loop)", async () => {
    window.sessionStorage.setItem(
      "techstack:auth:auto_relogin_at",
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
    authState.init.mockImplementation(async () => {
      authState.v2SessionActive = false;
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
    authState.init.mockImplementation(async () => {
      authState.v2SessionActive = false;
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
    expect(authState.init).toHaveBeenCalledTimes(1);
    expect(authState.initiateCloudLogin).toHaveBeenCalledTimes(1);
  });
});
