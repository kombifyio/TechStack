/**
 * kombify Cloud Stack Identity, read and written through the Gateway's
 * `/v1/cloud` route (Cloud `/api/v1/stack-identity`, StackIdentityV1) with the
 * signed-in user's own Gateway token. Techstack never calls Cloud directly and
 * never holds a service credential for it.
 *
 * Without a Gateway token (self-hosted Techstack without kombify Cloud, or a
 * signed-out session) Cloud is "unavailable" and Techstack stays the only
 * authority (STACK-IDENTITY-CUSTOMIZATION-STANDARD §8).
 */
import { getGatewayToken } from "#lib/auth/gateway-auth.js";
import { connectGatewayOrigin } from "./connectDevices";

/** Cloud's StackIdentityV1 wire form (the fields Techstack syncs). */
export interface CloudStackIdentityV1 {
  revision: number;
  name: string;
  character_id: string;
  animation_style: string;
  animation_enabled: boolean;
  icon_style: string;
  glow_color_override: string | null;
  updated_at: string;
}

export type CloudStackIdentityPatch = Partial<
  Omit<CloudStackIdentityV1, "revision" | "updated_at">
> & { name: string };

export type CloudStackIdentityRead =
  | { state: "found"; identity: CloudStackIdentityV1 }
  | { state: "missing" }
  | { state: "unavailable"; reason: string };

export type CloudStackIdentityWrite =
  | { state: "saved"; identity: CloudStackIdentityV1 }
  | { state: "conflict" }
  | { state: "unavailable"; reason: string };

interface CloudStackIdentityDeps {
  fetchImpl?: typeof fetch;
  getToken?: () => Promise<string>;
}

function endpoint(): string {
  return `${connectGatewayOrigin()}/v1/cloud/stack-identity`;
}

async function bearer(getToken: () => Promise<string>): Promise<string | null> {
  try {
    return await getToken();
  } catch {
    return null;
  }
}

function parseIdentity(value: unknown): CloudStackIdentityV1 | null {
  if (!value || typeof value !== "object") return null;
  const raw = value as Record<string, unknown>;
  if (
    typeof raw.revision !== "number" ||
    typeof raw.name !== "string" ||
    typeof raw.updated_at !== "string"
  ) {
    return null;
  }
  return {
    revision: raw.revision,
    name: raw.name,
    character_id: typeof raw.character_id === "string" ? raw.character_id : "",
    animation_style:
      typeof raw.animation_style === "string" ? raw.animation_style : "",
    animation_enabled: raw.animation_enabled !== false,
    icon_style: typeof raw.icon_style === "string" ? raw.icon_style : "",
    glow_color_override:
      typeof raw.glow_color_override === "string"
        ? raw.glow_color_override
        : null,
    updated_at: raw.updated_at,
  };
}

export async function readCloudStackIdentity(
  deps: CloudStackIdentityDeps = {},
): Promise<CloudStackIdentityRead> {
  const token = await bearer(deps.getToken ?? getGatewayToken);
  if (!token)
    return { state: "unavailable", reason: "gateway_auth_unavailable" };
  let response: Response;
  try {
    response = await (deps.fetchImpl ?? fetch)(endpoint(), {
      method: "GET",
      headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
    });
  } catch {
    return { state: "unavailable", reason: "network" };
  }
  if (response.status === 404) {
    // Cloud answers identity_not_found with a 404; a Gateway 404 for an
    // unknown route carries no such code and is not "no identity".
    const body = (await response.json().catch(() => null)) as {
      error_code?: string;
    } | null;
    return body?.error_code === "identity_not_found"
      ? { state: "missing" }
      : { state: "unavailable", reason: "route_not_found" };
  }
  if (!response.ok) {
    return { state: "unavailable", reason: `http_${response.status}` };
  }
  const identity = parseIdentity(await response.json().catch(() => null));
  return identity
    ? { state: "found", identity }
    : { state: "unavailable", reason: "invalid_payload" };
}

export async function writeCloudStackIdentity(
  patch: CloudStackIdentityPatch,
  ifMatch: number,
  deps: CloudStackIdentityDeps = {},
): Promise<CloudStackIdentityWrite> {
  const token = await bearer(deps.getToken ?? getGatewayToken);
  if (!token)
    return { state: "unavailable", reason: "gateway_auth_unavailable" };
  let response: Response;
  try {
    response = await (deps.fetchImpl ?? fetch)(endpoint(), {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${token}`,
        Accept: "application/json",
        "Content-Type": "application/json",
        "If-Match": `"${ifMatch}"`,
      },
      body: JSON.stringify(patch),
    });
  } catch {
    return { state: "unavailable", reason: "network" };
  }
  if (response.status === 412) return { state: "conflict" };
  if (!response.ok) {
    return { state: "unavailable", reason: `http_${response.status}` };
  }
  const identity = parseIdentity(await response.json().catch(() => null));
  return identity
    ? { state: "saved", identity }
    : { state: "unavailable", reason: "invalid_payload" };
}
