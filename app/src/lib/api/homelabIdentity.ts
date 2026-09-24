import { fetchApi } from "./client";

export interface HomelabOwnerActivation {
  status: "pending" | "active" | "expired" | "failed" | "unavailable";
  origin?: string;
  expires_at?: string;
  reason_code?: string;
}
export interface HouseholdPlannedPerson {
  client_ref: string;
  name?: string;
  email?: string;
}
export interface HomelabIdentityStatus {
  owner: HomelabOwnerActivation;
  household: {
    planned_people?: HouseholdPlannedPerson[];
    members: {
      username: string;
      email?: string;
      display_name?: string;
      status?: string;
    }[];
  };
}
export interface HomelabIdentityActivation extends HomelabOwnerActivation {
  activation_url?: string;
}
const identityPath = (deploymentId: string) =>
  `/api/v1/stacks/${encodeURIComponent(deploymentId)}/identity`;

async function identityRequest<T>(path: string, body?: unknown): Promise<T> {
  const response = await fetchApi<T>(path, {
    method: body === undefined ? "GET" : "POST",
    body: body === undefined ? undefined : JSON.stringify(body),
    timeoutMs: 45_000,
  });
  return response.data;
}

export function getHomelabIdentity(deploymentId: string) {
  return identityRequest<HomelabIdentityStatus>(identityPath(deploymentId));
}
export async function activateHomelabOwner(deploymentId: string) {
  const result = await identityRequest<{ owner: HomelabIdentityActivation }>(
    `${identityPath(deploymentId)}/owner-activation`,
    { owner_approved: true },
  );
  return result.owner;
}
export async function inviteHomelabMember(
  deploymentId: string,
  person: HouseholdPlannedPerson & { username: string },
) {
  const result = await identityRequest<{
    invitation: HomelabIdentityActivation;
  }>(`${identityPath(deploymentId)}/household-invitations`, {
    client_ref: person.client_ref,
    username: person.username,
    email: person.email,
    display_name: person.name,
    owner_approved: true,
  });
  return result.invitation;
}

/** Enrollment material is transient and must stay on the verified identity origin. */
export function trustedIdentityActivationUrl(
  value: HomelabIdentityActivation,
): string | null {
  if (!value.activation_url || !value.origin || value.status !== "pending")
    return null;
  try {
    const target = new URL(value.activation_url);
    const origin = new URL(value.origin);
    if (
      target.protocol !== "https:" ||
      origin.protocol !== "https:" ||
      target.origin !== origin.origin ||
      target.username ||
      target.password
    )
      return null;
    return target.href;
  } catch {
    return null;
  }
}
