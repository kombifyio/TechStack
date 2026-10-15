/**
 * The detail level the person chose in the kombify Cloud account setup, read
 * through the Gateway's `/v1/cloud` route (Cloud `/api/v1/profile`) with the
 * signed-in user's own Gateway token. Without a token (self-hosted Techstack,
 * signed-out session) or a reachable Cloud it reads as unknown.
 */
import { getGatewayToken } from "#lib/auth/gateway-auth.js";
import type { SelfDisclosureExperience } from "./operatorProfile.js";
import { connectGatewayOrigin } from "./connectDevices";

const LEVELS: readonly SelfDisclosureExperience[] = [
  "hands-off",
  "guided",
  "curious",
  "techie",
  "expert",
];

interface CloudProfileDeps {
  fetchImpl?: typeof fetch;
  getToken?: () => Promise<string>;
}

export async function readCloudExperienceLevel(
  deps: CloudProfileDeps = {},
): Promise<SelfDisclosureExperience | null> {
  let token: string;
  try {
    token = await (deps.getToken ?? getGatewayToken)();
  } catch {
    return null;
  }
  if (!token) return null;
  try {
    const response = await (deps.fetchImpl ?? fetch)(
      `${connectGatewayOrigin()}/v1/cloud/profile`,
      {
        method: "GET",
        headers: {
          Authorization: `Bearer ${token}`,
          Accept: "application/json",
        },
      },
    );
    if (!response.ok) return null;
    const body = (await response.json().catch(() => null)) as {
      experienceLevel?: unknown;
    } | null;
    return LEVELS.find((level) => level === body?.experienceLevel) ?? null;
  } catch {
    return null;
  }
}
