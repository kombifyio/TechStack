/**
 * The account design default (KOMBIFY-DESIGN-SYSTEM-STANDARD §4): the
 * `companionV2.panelAppearance.{finish,theme}` fields of the Gateway's
 * `/v1/ai/settings` document, written from the Workbench /design account band
 * (and later from kombify Cloud settings).
 *
 * Fail-soft by design: every unavailable precondition — no gateway base
 * configured (self-hosted), no token (signed out), a non-OK response, a
 * foreign payload — resolves to `null`, and the caller keeps the tier below
 * (device choice, then the kombify baseline). Nothing here throws.
 */
import {
  parseCentralDesign,
  type AppearancePreference,
} from "@kombiverselabs/design/preferences";

const GATEWAY_API_BASE =
  (import.meta.env.VITE_GATEWAY_API_BASE as string | undefined)?.replace(
    /\/+$/,
    "",
  ) || "";

export interface AccountDesign {
  appearance: AppearancePreference;
  finish: string;
}

export async function fetchAccountDesign(): Promise<AccountDesign | null> {
  if (!GATEWAY_API_BASE || typeof window === "undefined") return null;
  let origin: string;
  try {
    origin = new URL(GATEWAY_API_BASE).origin;
  } catch {
    return null;
  }

  // Same acquisition order as the data plane (client.ts): the user's own
  // Auth0 token first, the embedded portal's bridge token as fallback.
  let token = "";
  try {
    const { getGatewayToken } = await import("#lib/auth/gateway-auth.js");
    token = await getGatewayToken();
  } catch {
    try {
      if (window.parent === window) return null;
      const { initBridge, requestGatewayToken } =
        await import("#lib/stores/postMessageBridge.js");
      initBridge();
      token = (await requestGatewayToken()) ?? "";
    } catch {
      return null;
    }
  }
  if (!token) return null;

  try {
    const response = await fetch(`${origin}/v1/ai/settings`, {
      headers: {
        accept: "application/json",
        authorization: `Bearer ${token}`,
      },
    });
    if (!response.ok) return null;
    const central = parseCentralDesign(await response.json());
    return { appearance: central.appearance, finish: central.finish };
  } catch {
    return null;
  }
}
