import { describe, expect, it } from "vitest";
import {
  buildCloudAuthRedirectURL,
  buildV2ProviderLogoutPath,
  formatLoginError,
  getPostLogoutRedirectPath,
  resolveLoginExperience,
  sanitizeAuthReturnTo,
  shouldAutoStartCloudLogin,
} from "./login-experience";

describe("resolveLoginExperience", () => {
  it("uses Auth0 login for SaaS deployments", () => {
    expect(
      resolveLoginExperience({
        deploymentMode: "saas",
        embedded: false,
      }),
    ).toBe("saas-auth0");
  });

  it("uses Auth0 login when embedded in the SaaS portal", () => {
    expect(
      resolveLoginExperience({
        deploymentMode: "self-hosted",
        embedded: true,
      }),
    ).toBe("saas-auth0");
  });

  it("keeps the self-hosted login flow for standalone instances", () => {
    expect(
      resolveLoginExperience({
        deploymentMode: "self-hosted",
        embedded: false,
      }),
    ).toBe("self-hosted");
  });

  it("auto-starts Auth0 login for SaaS when no manual fallback is requested", () => {
    expect(
      shouldAutoStartCloudLogin({
        deploymentMode: "saas",
        embedded: false,
        cloudAuthUrl: "https://login.kombify.io/authorize",
        hasError: false,
        manualLogin: false,
      }),
    ).toBe(true);
  });

  it("never starts Auth0 inside an embedded portal frame", () => {
    expect(
      shouldAutoStartCloudLogin({
        deploymentMode: "saas",
        embedded: true,
        cloudAuthUrl: "https://login.kombify.io/authorize",
        hasError: false,
        manualLogin: false,
      }),
    ).toBe(false);
  });

  it("does not auto-start Auth0 when the user explicitly wants the fallback screen", () => {
    expect(
      shouldAutoStartCloudLogin({
        deploymentMode: "saas",
        embedded: false,
        cloudAuthUrl: "https://login.kombify.io/authorize",
        hasError: false,
        manualLogin: true,
      }),
    ).toBe(false);
  });

  it("does not auto-start Auth0 when the SaaS login URL is missing", () => {
    expect(
      shouldAutoStartCloudLogin({
        deploymentMode: "saas",
        embedded: false,
        cloudAuthUrl: null,
        hasError: false,
        manualLogin: false,
      }),
    ).toBe(false);
  });

  it("routes SaaS logout flows back to the server-side Auth0 redirect", () => {
    expect(
      getPostLogoutRedirectPath({
        deploymentMode: "saas",
        embedded: false,
      }),
    ).toBe("/login?manual=1&logged_out=1");
  });

  it("chains SaaS V2 logout through the upstream cloud logout", () => {
    expect(
      buildV2ProviderLogoutPath({
        deploymentMode: "saas",
        nextPath: "/login?manual=1&logged_out=1",
      }),
    ).toBe("/api/v2/auth/logout?next=%2Fauth%2Fcloud-logout");
  });

  it("keeps self-hosted logout flows on the normal login screen", () => {
    expect(
      getPostLogoutRedirectPath({
        deploymentMode: "self-hosted",
        embedded: false,
      }),
    ).toBe("/login");
  });

  it("keeps the current workflow path for SaaS re-auth redirects", () => {
    expect(
      buildCloudAuthRedirectURL("/api/v2/auth/login", {
        origin: "https://techstack.kombify.io",
        returnTo: "/stacks/creating?job_id=job-1&techstack_id=techstack-1",
      }),
    ).toBe(
      "https://techstack.kombify.io/api/v2/auth/login?return_to=%2Fstacks%2Fcreating%3Fjob_id%3Djob-1%26techstack_id%3Dtechstack-1",
    );
  });

  it("preserves an explicit login-page return target", () => {
    expect(
      buildCloudAuthRedirectURL("/api/v2/auth/login?return_to=%2Fstacks", {
        origin: "https://techstack.kombify.io",
        returnTo: "/stacks/creating?job_id=job-1",
      }),
    ).toBe(
      "https://techstack.kombify.io/api/v2/auth/login?return_to=%2Fstacks",
    );
  });

  it("rejects unsafe re-auth return targets", () => {
    expect(sanitizeAuthReturnTo("https://evil.example/path")).toBe("/stacks");
    expect(sanitizeAuthReturnTo("//evil.example/path")).toBe("/stacks");
    expect(sanitizeAuthReturnTo("/login?manual=1")).toBe("/stacks");
  });

  it("explains Auth0 callback session minting failures without leaking internals", () => {
    expect(formatLoginError("callback_session_failed")).toBe(
      "kombify Cloud sign-in completed, but TechStack could not create a browser session. Try again or contact support.",
    );
    expect(formatLoginError("session_mint_failed")).toBe(
      "kombify Cloud sign-in completed, but TechStack could not create a browser session. Try again or contact support.",
    );
  });

  it("keeps unrelated login errors available for debugging", () => {
    expect(formatLoginError("mocked_v2_login")).toBe("mocked_v2_login");
  });
});
