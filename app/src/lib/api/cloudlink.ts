/**
 * kombify-TechStack API - Cloud Link Module
 *
 * Connects the authenticated operator's kombify Cloud profile to the local
 * account (PKCE authorization-code flow handled server-side). The link seeds
 * the "cloud-linked" owner source in the stack creation wizard.
 */

import { del, get, post } from "./client";

export interface CloudLinkStartResponse {
  authorization_url: string;
  expires_at: string;
}

export interface CloudLinkStatus {
  linked: boolean;
  external_email?: string;
  external_name?: string;
  email_verified?: boolean;
  linked_at?: string;
}

/** Mint a single-use PKCE state and get the hosted authorization URL. */
export async function startCloudLink(): Promise<CloudLinkStartResponse> {
  return post<CloudLinkStartResponse>("/api/v1/auth/cloud-link/start");
}

/** Current cloud link of the authenticated operator. */
export async function getCloudLinkStatus(): Promise<CloudLinkStatus> {
  return get<CloudLinkStatus>("/api/v1/auth/cloud-link/status");
}

/** Remove the cloud link ("use a different account"). */
export async function unlinkCloud(): Promise<{ removed: boolean }> {
  return del<{ removed: boolean }>("/api/v1/auth/cloud-link");
}
