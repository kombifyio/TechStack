/**
 * kombify-TechStack API - Auth Module
 *
 * Authentication mode detection and configuration endpoints.
 */

import { fetchApi, post } from "./client";

// ============================================================================
// Types
// ============================================================================

export type AuthMode = "local" | "cloud";

export type DeploymentMode = "self-hosted" | "saas";

export type DeploymentEdition =
  | "selfhost-oss"
  | "preview"
  | "saas-standalone"
  | "saas-embedded";

export interface AuthModeResponse {
  mode: AuthMode;
  edition?: DeploymentEdition;
  deployment_mode: DeploymentMode;
  is_first_run: boolean;
  cloud_auth_url: string | null;
  portal_url: string | null;
  allow_local_login: boolean;
}

export interface CloudUser {
  sub: string;
  orgId?: string;
  tenantId?: string;
  email: string;
  email_verified: boolean;
  name: string;
  provider?: string;
  role?: string;
  given_name?: string;
  family_name?: string;
  roles: string[];
  is_admin: boolean;
}

export interface V2WhoAmIResponse {
  subject: string;
  tenantId: string;
  orgId?: string;
  email?: string;
  provider?: string;
  role?: string;
}

export interface V2AuthProviderInfo {
  id: string;
  kind: string;
  issuer: string;
}

export interface LocalSessionLoginResponse {
  ok: boolean;
  email: string;
  provider: string;
}

import type { StackIdentity } from "$lib/components/open-core";
export type { StackIdentity };

export interface PortalVerifyResponse {
  pb_token: string;
  user: {
    id: string;
    email: string;
    name: string;
  };
  cloud_user: {
    sub: string;
    email: string;
    name: string;
    is_admin: boolean;
  };
  stack_identity?: StackIdentity;
}

export interface StackIdentitySettingsResponse {
  stack_identity?: StackIdentity;
  editable: boolean;
}

// ============================================================================
// API Functions
// ============================================================================

/**
 * Get current authentication mode and configuration
 * This endpoint does not require authentication
 */
export async function getAuthMode(): Promise<AuthModeResponse> {
  const res = await fetchApi<AuthModeResponse>("/api/v1/auth/mode", {
    credentials: "include",
  });
  return res.data;
}

/**
 * Complete OIDC callback flow
 * Called after redirect from the hosted cloud identity provider
 */
export async function completeOIDCCallback(
  code: string,
  state: string,
): Promise<{ token: string; user: CloudUser }> {
  const res = await fetchApi<{ token: string; user: CloudUser }>(
    "/api/v2/auth/callback",
    {
      method: "POST",
      body: JSON.stringify({ code, state }),
    },
  );
  return res.data;
}

/**
 * Verify a portal session token for SSO
 */
export async function verifyPortalToken(
  portalToken: string,
): Promise<PortalVerifyResponse> {
  const res = await fetchApi<PortalVerifyResponse>(
    "/api/v1/auth/portal-verify",
    {
      method: "POST",
      body: JSON.stringify({ token: portalToken }),
    },
  );
  return res.data;
}

export async function getV2WhoAmI(): Promise<V2WhoAmIResponse> {
  const response = await fetch("/api/v2/whoami", {
    credentials: "include",
  });
  if (response.status !== 200) {
    throw new Error(`V2 whoami failed: ${response.status}`);
  }
  return (await response.json()) as V2WhoAmIResponse;
}

export async function getV2AuthProviders(): Promise<V2AuthProviderInfo[]> {
  const response = await fetch("/api/v2/auth/providers", {
    credentials: "include",
  });
  if (!response.ok) {
    throw new Error(`V2 auth providers failed: ${response.status}`);
  }
  const data = (await response.json()) as {
    providers?: V2AuthProviderInfo[];
  };
  return data.providers ?? [];
}

export async function loginWithLocalSession(
  email: string,
  password: string,
): Promise<LocalSessionLoginResponse> {
  return post<LocalSessionLoginResponse>("/api/v1/auth/login", {
    email,
    password,
  });
}

export async function logoutLocalSession(): Promise<void> {
  await post<{ ok: boolean }>("/api/v1/auth/logout").catch(() => undefined);
}

export async function getStackIdentitySettings(): Promise<StackIdentitySettingsResponse> {
  const res = await fetchApi<StackIdentitySettingsResponse>(
    "/api/v1/auth/stack-identity",
    {
      credentials: "include",
    },
  );
  return res.data;
}

export async function updateStackIdentitySettings(
  identity: StackIdentity,
): Promise<StackIdentitySettingsResponse> {
  const res = await fetchApi<StackIdentitySettingsResponse>(
    "/api/v1/auth/stack-identity",
    {
      method: "PUT",
      body: JSON.stringify(identity),
      credentials: "include",
    },
  );
  return res.data;
}
