/**
 * Gateway token acquisition for the embedded TechStack SPA.
 *
 * In embedded SaaS mode the data plane is routed through
 * `api.kombify.io/v1/techstack/*`, which requires the user's own Auth0 access
 * token (audience = the kombify API) so the Cloudflare edge can validate it and
 * sign the entitlement-flag envelope. The token is acquired silently — the user
 * already holds the Auth0 tenant session from the Cloud login, and everything is
 * same-site `*.kombify.io`, so `getTokenSilently` resolves without an
 * interactive prompt.
 *
 * Fail-closed: this never returns an empty/anonymous token. On failure it throws
 * so callers surface a loud error (and fall back to the parent-seed path) rather
 * than silently downgrading the data plane.
 */
import { createAuth0Client, type Auth0Client } from "@auth0/auth0-spa-js";

const DOMAIN =
  (import.meta.env.VITE_AUTH0_DOMAIN as string | undefined)?.trim() ?? "";
const CLIENT_ID =
  (import.meta.env.VITE_AUTH0_SPA_CLIENT_ID as string | undefined)?.trim() ??
  "";
const AUDIENCE =
  (import.meta.env.VITE_AUTH0_AUDIENCE as string | undefined)?.trim() ?? "";

let client: Auth0Client | null = null;
let testClient: Auth0Client | null = null;

/** Test seam: inject a fake Auth0Client. Pass null to reset. */
export function __setAuth0ClientForTest(c: Auth0Client | null): void {
  testClient = c;
  client = null;
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
      authorizationParams: { audience: AUDIENCE },
      useRefreshTokens: true,
      useRefreshTokensFallback: true,
      cacheLocation: "memory",
    });
  }
  return client;
}

/**
 * Return the user's Auth0 access token for the kombify API audience.
 * Throws on failure (fail-closed) — callers must not treat a rejection as
 * "anonymous"; they should surface an error or use the parent-seed fallback.
 *
 * Tenant scoping needs no `organization` parameter: the Auth0 post-login
 * action stamps the kombify org/tenant claims from app_metadata into every
 * token, and the Cloudflare edge derives x-org-id from those claims. Users
 * without an org claim resolve their owner tenant server-side — identically
 * for embedded and standalone (one-truth rule).
 */
export async function getGatewayToken(): Promise<string> {
  const c = await getClient();
  const token = await c.getTokenSilently({
    authorizationParams: { audience: AUDIENCE },
  });
  if (!token) {
    throw new Error("gateway_token_empty");
  }
  return token;
}
