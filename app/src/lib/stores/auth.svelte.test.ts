// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("$app/environment", () => ({
  browser: true,
}));

vi.mock("$app/navigation", () => ({
  goto: vi.fn(),
}));

vi.mock("$lib/api/auth", () => ({
  getAuthMode: vi.fn(),
  getV2AuthProviders: vi.fn(),
  getV2WhoAmI: vi.fn(),
  getStackIdentitySettings: vi.fn(),
  loginWithLocalSession: vi.fn(),
  logoutLocalSession: vi.fn(),
  updateStackIdentitySettings: vi.fn(),
  verifyPortalToken: vi.fn(),
}));

vi.mock("$lib/auth/pocketbase-compat", () => ({
  clearPocketBaseCompatStoredSession: vi.fn(),
  getPocketBaseCompatStoredUser: vi.fn(() => null),
  isPocketBaseAuthCompatEnabled: vi.fn(() => false),
  savePocketBaseCompatStoredSession: vi.fn(),
}));

vi.mock("$lib/stores/stackIdentity", () => ({
  clearStackIdentity: vi.fn(),
  setStackIdentity: vi.fn(),
}));

import { goto } from "$app/navigation";
import {
  getAuthMode,
  getV2AuthProviders,
  getV2WhoAmI,
  logoutLocalSession,
  verifyPortalToken,
} from "$lib/api/auth";
import { rememberWindowsLocalClientContext } from "$lib/client/windows-onboarding";
import { authStore } from "./auth.svelte";

describe("authStore cloud login redirects", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authStore.reset();
    window.history.pushState({}, "", "/");
  });

  it("keeps the current stack creation workflow as the re-auth return target", () => {
    const redirect = vi.fn();
    authStore.v2LoginUrl = "/api/v2/auth/login";
    window.history.pushState(
      {},
      "",
      "/stacks/creating?job_id=job-1&stack_id=stack-1",
    );

    const redirectURL = authStore.initiateCloudLogin({ redirect });

    expect(redirectURL).toBe(
      `${window.location.origin}/api/v2/auth/login?return_to=%2Fstacks%2Fcreating%3Fjob_id%3Djob-1%26stack_id%3Dstack-1`,
    );
    expect(redirect).toHaveBeenCalledWith(redirectURL);
  });

  it("uses an explicit safe return target when one is provided", () => {
    const redirect = vi.fn();
    authStore.v2LoginUrl = "/api/v2/auth/login";
    window.history.pushState({}, "", "/stacks/creating?job_id=job-1");

    const redirectURL = authStore.initiateCloudLogin({
      returnTo: "/stacks",
      redirect,
    });

    expect(new URL(redirectURL ?? "").searchParams.get("return_to")).toBe(
      "/stacks",
    );
    expect(redirect).toHaveBeenCalledWith(redirectURL);
  });

  it("sanitizes unsafe return targets before redirecting", () => {
    const redirect = vi.fn();
    authStore.v2LoginUrl = "/api/v2/auth/login";

    const redirectURL = authStore.initiateCloudLogin({
      returnTo: "https://evil.example/path",
      redirect,
    });

    expect(new URL(redirectURL ?? "").searchParams.get("return_to")).toBe(
      "/stacks",
    );
    expect(redirect).toHaveBeenCalledWith(redirectURL);
  });

  it("returns null and records an error when cloud auth is not configured", () => {
    const redirect = vi.fn();

    const redirectURL = authStore.initiateCloudLogin({ redirect });

    expect(redirectURL).toBeNull();
    expect(redirect).not.toHaveBeenCalled();
    expect(authStore.error).toBe("Cloud authentication not configured");
  });

  it("initializes backend auth state only once until explicitly reset", async () => {
    vi.mocked(getAuthMode).mockResolvedValue({
      mode: "cloud",
      deployment_mode: "saas",
      is_first_run: false,
      cloud_auth_url: "/api/v2/auth/login",
      portal_url: "https://app.kombify.io",
      allow_local_login: false,
    });
    vi.mocked(getV2AuthProviders).mockResolvedValue([]);
    vi.mocked(getV2WhoAmI).mockRejectedValue(new Error("no cookie session"));

    await Promise.all([authStore.init(), authStore.init()]);
    await authStore.init();

    expect(getAuthMode).toHaveBeenCalledTimes(1);
    expect(getV2AuthProviders).toHaveBeenCalledTimes(1);
    expect(getV2WhoAmI).toHaveBeenCalledTimes(1);
    expect(authStore.deploymentMode).toBe("saas");
  });

  it("defers same-origin whoami until the embedded portal exchange", async () => {
    vi.mocked(getAuthMode).mockResolvedValue({
      mode: "cloud",
      deployment_mode: "saas",
      is_first_run: false,
      cloud_auth_url: "/api/v2/auth/login",
      portal_url: "https://app.kombify.io",
      allow_local_login: false,
    });
    vi.mocked(getV2AuthProviders).mockResolvedValue([]);

    await authStore.init({ embedded: true });

    expect(getV2WhoAmI).not.toHaveBeenCalled();
    expect(authStore.v2SessionActive).toBe(false);
  });

  it("globally signs out an embedded portal session without requiring whoami", async () => {
    const redirect = vi.fn();
    authStore.deploymentMode = "saas";
    authStore.allowLocalLogin = true;
    vi.mocked(verifyPortalToken).mockResolvedValue({
      pb_token: "portal-session",
      user: { id: "user-1", email: "owner@example.test", name: "Owner" },
      cloud_user: {
        sub: "auth0|user-1",
        email: "owner@example.test",
        name: "Owner",
        is_admin: false,
      },
    });
    vi.mocked(getV2WhoAmI).mockResolvedValue({
      subject: "auth0|user-1",
      tenantId: "default",
      email: "owner@example.test",
      provider: "cloud",
    });

    await authStore.completePortalLogin("signed-parent-token");
    expect(authStore.v2SessionActive).toBe(true);
    window.sessionStorage.setItem(
      "techstack:auth:session_reprojection_at",
      "1000000",
    );

    await authStore.logout({ manualLogin: true, redirect });

    expect(redirect).toHaveBeenCalledWith(
      "/api/v2/auth/logout?next=%2Fauth%2Fcloud-logout",
    );
    expect(
      window.sessionStorage.getItem("techstack:auth:session_reprojection_at"),
    ).toBeNull();
  });

  it("keeps the verified Cloud portal session on the central hosted-provider path", async () => {
    vi.mocked(getAuthMode).mockResolvedValue({
      mode: "cloud",
      deployment_mode: "saas",
      is_first_run: false,
      cloud_auth_url: "/api/v2/auth/login",
      portal_url: "https://app.kombify.io",
      allow_local_login: false,
    });
    vi.mocked(getV2AuthProviders).mockResolvedValue([
      {
        id: "primary",
        kind: "auth0",
        issuer: "https://login.kombify.io/",
      },
    ]);
    window.sessionStorage.setItem(
      "techstack:auth:session_reprojection_at",
      "1000000",
    );
    vi.mocked(verifyPortalToken).mockResolvedValue({
      pb_token: "portal-session",
      user: { id: "user-1", email: "owner@example.test", name: "Owner" },
      cloud_user: {
        sub: "auth0|user-1",
        email: "owner@example.test",
        name: "Owner",
        is_admin: false,
      },
    });
    vi.mocked(getV2WhoAmI).mockResolvedValue({
      subject: "auth0|user-1",
      tenantId: "default",
      email: "owner@example.test",
      provider: "cloud",
    });

    await authStore.init({ embedded: true });
    await expect(
      authStore.completePortalLogin("signed-parent-token"),
    ).resolves.toBeUndefined();

    expect(getV2WhoAmI).toHaveBeenCalledTimes(1);
    expect(authStore.v2SessionActive).toBe(true);
    expect(authStore.cloudUser?.sub).toBe("auth0|user-1");
    expect(logoutLocalSession).not.toHaveBeenCalled();
    expect(
      window.sessionStorage.getItem("techstack:auth:session_reprojection_at"),
    ).toBeNull();
  });

  it("rejects portal login when portal-verify did not establish a V2 cookie session", async () => {
    authStore.deploymentMode = "saas";
    authStore.allowLocalLogin = true;
    vi.mocked(verifyPortalToken).mockResolvedValue({
      pb_token: "portal-session",
      user: { id: "user-1", email: "owner@example.test", name: "Owner" },
      cloud_user: {
        sub: "auth0|user-1",
        email: "owner@example.test",
        name: "Owner",
        is_admin: false,
      },
    });
    vi.mocked(getV2WhoAmI).mockRejectedValue(new Error("no cookie session"));

    await expect(
      authStore.completePortalLogin("signed-parent-token"),
    ).rejects.toThrow("did not establish a verified browser session");

    expect(authStore.v2SessionActive).toBe(false);
  });

  it("accepts the exact hosted provider advertised by the backend", async () => {
    vi.mocked(getAuthMode).mockResolvedValue({
      mode: "cloud",
      deployment_mode: "saas",
      is_first_run: false,
      cloud_auth_url: "/api/v2/auth/login",
      portal_url: "https://app.kombify.io",
      allow_local_login: false,
    });
    vi.mocked(getV2AuthProviders).mockResolvedValue([
      {
        id: "primary",
        kind: "oidc",
        issuer: "https://login.kombify.io",
      },
    ]);
    vi.mocked(getV2WhoAmI).mockResolvedValue({
      subject: "auth0|cloud-user",
      tenantId: "default",
      email: "cloud@example.test",
      provider: "primary",
    });

    await authStore.init();

    expect(authStore.isAuthenticated).toBe(true);
    expect(authStore.v2SessionActive).toBe(true);
    expect(authStore.cloudUser?.provider).toBe("primary");
    expect(logoutLocalSession).not.toHaveBeenCalled();
  });

  it("rejects a SaaS session from a provider not advertised by the backend", async () => {
    vi.mocked(getAuthMode).mockResolvedValue({
      mode: "cloud",
      deployment_mode: "saas",
      is_first_run: false,
      cloud_auth_url: "/api/v2/auth/login",
      portal_url: "https://app.kombify.io",
      allow_local_login: false,
    });
    vi.mocked(getV2AuthProviders).mockResolvedValue([
      {
        id: "primary",
        kind: "oidc",
        issuer: "https://login.kombify.io",
      },
    ]);
    vi.mocked(getV2WhoAmI).mockResolvedValue({
      subject: "local|stale-user",
      tenantId: "default",
      email: "stale@example.test",
      provider: "local",
    });

    await authStore.init();

    expect(logoutLocalSession).toHaveBeenCalledTimes(1);
    expect(authStore.isAuthenticated).toBe(false);
    expect(authStore.v2SessionActive).toBe(false);
  });

  it("logs out self-hosted Windows sessions back to the local client sign-in", async () => {
    rememberWindowsLocalClientContext(window.localStorage);
    authStore.deploymentMode = "self-hosted";
    authStore.v2SessionActive = true;

    await authStore.logout({ manualLogin: true });

    expect(logoutLocalSession).toHaveBeenCalled();
    expect(goto).toHaveBeenCalledWith("/client/local?client=windows");
  });
});
