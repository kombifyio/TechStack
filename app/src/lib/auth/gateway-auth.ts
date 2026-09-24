/**
 * Gateway token acquisition for the SaaS TechStack SPA.
 *
 * The Cloudflare edge at `api.kombify.io/v1/techstack/*` needs the user's
 * Auth0 access token (audience = the kombify API). Embedded Cloud frames get
 * that token from the parent portal. Standalone Universal Login users (including
 * the demo account) only mint a Techstack session cookie, so this module must
 * acquire the API token itself. Silent refresh first; auth init and the
 * recovery ladder start one SPA SSO round-trip so the operator is not stuck
 * on a dead error banner.
 *
 * Fail-closed: this never returns an empty/anonymous token.
 */
import { createAuth0Client, type Auth0Client } from "@auth0/auth0-spa-js";
import {
  currentAuthReturnTo,
  sanitizeAuthReturnTo,
} from "#lib/auth/login-experience.js";

const DOMAIN =
  (import.meta.env.VITE_AUTH0_DOMAIN as string | undefined)?.trim() ?? "";
const CLIENT_ID =
  (import.meta.env.VITE_AUTH0_SPA_CLIENT_ID as string | undefined)?.trim() ??
  "";
const AUDIENCE =
  (import.meta.env.VITE_AUTH0_AUDIENCE as string | undefined)?.trim() ?? "";

let client: Auth0Client | null = null;
let testClient: Auth0Client | null = null;
let loginRedirectStarted = false;

/** Test seam: inject a fake Auth0Client. Pass null to reset. */
export function __setAuth0ClientForTest(c: Auth0Client | null): void {
  testClient = c;
  client = null;
  loginRedirectStarted = false;
}

/** True when the SPA build carries the Auth0 + audience config for the gateway path. */
export function isGatewayAuthConfigured(): boolean {
  return Boolean(DOMAIN && CLIENT_ID && AUDIENCE);
}

async function getClient(): Promise<Auth0Client> {
  if (testClient) return testClient;
  if (!isGatewayAuthConfigured()) {
    throw new Error("gateway_auth_not_configured");
  }
  if (!client) {
    client = await createAuth0Client({
      domain: DOMAIN,
      clientId: CLIENT_ID,
      authorizationParams: {
        audience: AUDIENCE,
        scope: "openid profile email offline_access",
        redirect_uri:
          typeof window !== "undefined" ? window.location.origin : undefined,
      },
      useRefreshTokens: true,
      useRefreshTokensFallback: true,
      // Memory cache dropped the refresh token on every reload, so a later
      // silent renew had nothing to use once the Auth0 SSO cookie aged out.
      cacheLocation: "localstorage",
    });
  }
  return client;
}

/** Drop cached gateway tokens without navigating to Auth0 logout. */
export async function clearGatewayAuth(): Promise<void> {
  const active = testClient ?? client;
  client = null;
  loginRedirectStarted = false;
  if (!active) return;
  try {
    await active.logout({ openUrl: false });
  } catch {
    // Cache may already be empty or the SDK may not have a session.
  }
}

/**
 * Consume an Auth0 SPA redirect on the app origin. Returns the post-login
 * path when this tab's SPA transaction claimed the query, otherwise null so
 * the v2 server callback can still run.
 */
export async function completeGatewayRedirectIfPresent(): Promise<string | null> {
  if (typeof window === "undefined") return null;
  if (!isGatewayAuthConfigured() && !testClient) return null;
  const params = new URLSearchParams(window.location.search);
  if (!params.get("code") || !params.get("state")) return null;
  if (window.location.pathname.startsWith("/api/")) return null;

  try {
    const c = await getClient();
    const result = await c.handleRedirectCallback();
    const returnTo = sanitizeAuthReturnTo(
      (result?.appState as { returnTo?: string } | undefined)?.returnTo,
    );
    const url = new URL(window.location.href);
    url.searchParams.delete("code");
    url.searchParams.delete("state");
    window.history.replaceState(
      {},
      "",
      `${url.pathname}${url.search}${url.hash}`,
    );
    loginRedirectStarted = false;
    return returnTo;
  } catch {
    // Not this SPA client's transaction. Leave code+state for the v2
    // OIDC handler instead of destroying a cookie-session callback.
    return null;
  }
}

/**
 * Start the Auth0 SPA login that mints the API-audience token. Returns true
 * when navigation started. Standalone demo users land here after Universal
 * Login because the v2 cookie is not that token.
 */
export async function startGatewayLogin(options?: {
  interactive?: boolean;
  returnTo?: string | null;
}): Promise<boolean> {
  if (typeof window === "undefined") return false;
  if (!isGatewayAuthConfigured() && !testClient) return false;
  if (loginRedirectStarted) return true;

  const c = await getClient();
  if (typeof c.loginWithRedirect !== "function") return false;

  loginRedirectStarted = true;
  try {
    await c.loginWithRedirect({
      authorizationParams: {
        audience: AUDIENCE,
        scope: "openid profile email offline_access",
        redirect_uri: window.location.origin,
        ...(options?.interactive ? { prompt: "login", max_age: "0" } : {}),
      },
      appState: {
        returnTo: sanitizeAuthReturnTo(
          options?.returnTo ?? currentAuthReturnTo(),
        ),
      },
    });
    return true;
  } catch (err) {
    loginRedirectStarted = false;
    throw err;
  }
}

/**
 * Return the user's Auth0 access token for the kombify API audience.
 * Throws on failure (fail-closed) — callers must not treat a rejection as
 * "anonymous"; they should surface an error or use the parent-seed fallback.
 *
 * Silent-only: the SPA login round-trip lives in auth init / the recovery
 * ladder so a wizard holding unsaved state can refuse the navigation.
 */
export async function getGatewayToken(): Promise<string> {
  const c = await getClient();
  const token = await c.getTokenSilently({
    authorizationParams: {
      audience: AUDIENCE,
      scope: "openid profile email offline_access",
    },
  });
  if (!token) {
    throw new Error("gateway_token_empty");
  }
  return token;
}
