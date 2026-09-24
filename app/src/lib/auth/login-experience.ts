import type { DeploymentMode } from "#lib/api/auth.js";
import {
  selfHostedOnboardingUrl,
  windowsLocalClientReturnUrl,
} from "#lib/client/windows-onboarding.js";
import { withHostNavigation } from "#lib/embedded-navigation.js";

export type LoginExperience = "saas-auth0" | "self-hosted";
export const SAAS_MANUAL_LOGIN_QUERY = "logged_out=1";
export const SAAS_MANUAL_LOGOUT_QUERY = "manual=1&logged_out=1";
export const DEFAULT_AUTH_RETURN_TO = "/dashboard";
export const V2_CLOUD_LOGIN_PATH = "/api/v2/auth/login";
export const AUTH0_SESSION_CREATION_ERROR =
  "kombify Cloud sign-in completed, but Techstack could not create a browser session. Try again or contact support.";

export function resolveLoginExperience(options: {
  deploymentMode: DeploymentMode;
  embedded: boolean;
}): LoginExperience {
  if (options.embedded || options.deploymentMode === "saas") {
    return "saas-auth0";
  }

  return "self-hosted";
}

export function shouldAutoStartCloudLogin(options: {
  deploymentMode: DeploymentMode;
  embedded: boolean;
  cloudAuthUrl: string | null;
  hasError: boolean;
  manualLogin: boolean;
}): boolean {
  // Auth0 Universal Login is intentionally not frameable. Embedded callers
  // authenticate through the portal postMessage bridge and must never turn a
  // missing Techstack cookie into an Auth0 navigation inside the iframe.
  if (options.embedded) {
    return false;
  }

  if (resolveLoginExperience(options) !== "saas-auth0") {
    return false;
  }

  if (!options.cloudAuthUrl || options.hasError || options.manualLogin) {
    return false;
  }

  return true;
}

export function unauthenticatedEntryPath(options: {
  deploymentMode: DeploymentMode;
  embedded: boolean;
  hostNavigation?: boolean;
  windowsClient?: boolean;
  windowsLocal?: boolean;
}): string {
  if (options.embedded) {
    const path = "/login?embedded=true";
    return options.hostNavigation ? withHostNavigation(path) : path;
  }
  if (resolveLoginExperience(options) === "saas-auth0") {
    return "/login";
  }
  // Windows desktop client only. The browser webapp never uses this chooser.
  if (options.windowsLocal) {
    return windowsLocalClientReturnUrl;
  }
  if (options.windowsClient) {
    return selfHostedOnboardingUrl;
  }
  return "/login";
}

export function getPostLogoutRedirectPath(options: {
  deploymentMode: DeploymentMode;
  embedded: boolean;
  windowsLocal?: boolean;
}): string {
  if (resolveLoginExperience(options) === "saas-auth0") {
    return `/login?${SAAS_MANUAL_LOGOUT_QUERY}`;
  }

  return unauthenticatedEntryPath(options);
}

export function buildV2ProviderLogoutPath(options: {
  deploymentMode: DeploymentMode;
  nextPath: string;
}): string {
  // The shared V2 handler clears only the Techstack browser cookie. SaaS must
  // then traverse Auth0 Universal Login logout so the next visit cannot silently
  // reuse the upstream SSO session.
  const next =
    options.deploymentMode === "saas" ? "/auth/cloud-logout" : options.nextPath;
  return `/api/v2/auth/logout?next=${encodeURIComponent(next)}`;
}

export function currentAuthReturnTo(): string {
  if (typeof window === "undefined") {
    return DEFAULT_AUTH_RETURN_TO;
  }

  return sanitizeAuthReturnTo(
    `${window.location.pathname}${window.location.search}`,
  );
}

export function sanitizeAuthReturnTo(raw: string | null | undefined): string {
  const fallback = DEFAULT_AUTH_RETURN_TO;
  const value = `${raw ?? ""}`.trim();
  if (!value || !value.startsWith("/") || value.startsWith("//")) {
    return fallback;
  }
  if (value.includes("\\") || value.includes("://")) {
    return fallback;
  }

  let parsed: URL;
  try {
    parsed = new URL(value, "https://techstack.local");
  } catch {
    return fallback;
  }

  if (parsed.origin !== "https://techstack.local") {
    return fallback;
  }

  const path = `${parsed.pathname}${parsed.search}`;
  if (!path || isAuthEntryPath(parsed.pathname)) {
    return fallback;
  }

  return path;
}

/**
 * Keep an embedded SSO continuation available while the auth page is still
 * mounted. The root layout can switch its navigation branch after the portal
 * session is established, which may remount this page before the final goto.
 */
export function buildSsoContinuationPath(options: {
  returnTo: string | null | undefined;
  embedded: boolean;
  hostNavigation: boolean;
}): string {
  const params = new URLSearchParams();
  if (options.embedded) params.set("embedded", "true");
  if (options.hostNavigation) params.set("host_navigation", "true");
  params.set("return_url", sanitizeAuthReturnTo(options.returnTo));
  return `/auth/sso?${params.toString()}`;
}

export function buildCloudAuthRedirectURL(
  target: string,
  options: {
    origin?: string;
    returnTo?: string | null;
    interactive?: boolean;
  } = {},
): string {
  const origin =
    options.origin ??
    (typeof window !== "undefined" ? window.location.origin : "");
  const url = origin
    ? new URL(target, origin)
    : new URL(target, "https://kombify.invalid");

  if (
    (!origin || url.origin === new URL(origin).origin) &&
    url.pathname === V2_CLOUD_LOGIN_PATH
  ) {
    if (!url.searchParams.has("return_to")) {
      url.searchParams.set(
        "return_to",
        sanitizeAuthReturnTo(options.returnTo ?? currentAuthReturnTo()),
      );
    }
    if (options.interactive) {
      url.searchParams.set("prompt", "login");
    }
  }

  return origin ? url.toString() : `${url.pathname}${url.search}`;
}

export function formatLoginError(raw: string | null | undefined): string {
  const value = `${raw ?? ""}`.trim();
  if (!value) return "";

  if (
    value === "callback_session_failed" ||
    value === "session_mint_failed" ||
    value === "session_create_failed" ||
    value === "session_cookie_failed" ||
    /callback.*session|session.*mint/i.test(value)
  ) {
    return AUTH0_SESSION_CREATION_ERROR;
  }

  return value;
}

function isAuthEntryPath(pathname: string): boolean {
  return (
    pathname === "/" ||
    pathname === "/login" ||
    pathname === "/auth/logout" ||
    pathname.startsWith("/api/v1/auth/") ||
    pathname.startsWith("/api/v2/auth/")
  );
}
