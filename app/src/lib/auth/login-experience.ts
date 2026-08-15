import type { DeploymentMode } from "$lib/api/auth";

export type LoginExperience = "saas-auth0" | "self-hosted";
export const SAAS_MANUAL_LOGIN_QUERY = "logged_out=1";
export const SAAS_MANUAL_LOGOUT_QUERY = "manual=1&logged_out=1";
export const DEFAULT_AUTH_RETURN_TO = "/stacks";
export const V2_CLOUD_LOGIN_PATH = "/api/v2/auth/login";
export const AUTH0_SESSION_CREATION_ERROR =
  "kombify Cloud sign-in completed, but TechStack could not create a browser session. Try again or contact support.";

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

export function getPostLogoutRedirectPath(options: {
  deploymentMode: DeploymentMode;
  embedded: boolean;
}): string {
  if (resolveLoginExperience(options) === "saas-auth0") {
    return `/login?${SAAS_MANUAL_LOGOUT_QUERY}`;
  }

  return "/login";
}

export function buildV2ProviderLogoutPath(options: {
  deploymentMode: DeploymentMode;
  nextPath: string;
}): string {
  // The shared V2 handler clears only the Techstack browser cookie. SaaS must
  // then traverse the Cloud/IdP logout route so the next login cannot silently
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

export function buildCloudAuthRedirectURL(
  target: string,
  options: { origin?: string; returnTo?: string | null } = {},
): string {
  const origin =
    options.origin ??
    (typeof window !== "undefined"
      ? window.location.origin
      : "http://localhost");
  const base = new URL(origin);
  const url = new URL(target, base.origin);

  if (
    url.origin === base.origin &&
    url.pathname === V2_CLOUD_LOGIN_PATH &&
    !url.searchParams.has("return_to")
  ) {
    url.searchParams.set(
      "return_to",
      sanitizeAuthReturnTo(options.returnTo ?? currentAuthReturnTo()),
    );
  }

  return url.toString();
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
    pathname === "/register" ||
    pathname === "/auth/logout" ||
    pathname.startsWith("/api/v1/auth/") ||
    pathname.startsWith("/api/v2/auth/")
  );
}
