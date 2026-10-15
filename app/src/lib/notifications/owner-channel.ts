/**
 * Where the notification bell and center get their live channel and where
 * Interaction Card answers go.
 *
 * Only a SaaS data plane has a Gateway and an Auth0 session: there the shared
 * components open the Gateway's live channel (wss .../v1/notifications/live)
 * and answer cards at POST /v1/decisions/{id}/answer, both with the user's
 * access token. A self-hosted or preview instance has neither Auth0 nor a
 * Gateway, so it gets no options and the components keep polling the
 * same-origin feed. The token getter never invents a token: it resolves null
 * when none can be acquired and the components then stay on the poll.
 */
import { get as readStore } from "svelte/store";
import { resolveGatewayUrl } from "#lib/api/client.js";
import { getGatewayToken } from "#lib/auth/gateway-auth.js";
import { getClientBootstrap } from "#lib/client/bootstrap.js";
import { deploymentMode } from "#lib/stores/deploymentMode.js";
import type { DecisionTransport } from "@kombiverselabs/ui/interaction";

export interface OwnerChannelOptions {
  getToken: () => Promise<string | null>;
  decisions: DecisionTransport;
}

async function gatewayAccessToken(): Promise<string | null> {
  try {
    const token = await getGatewayToken();
    if (token) return token;
  } catch {
    // Fall through to the embedding portal, which holds the token in a frame.
  }
  try {
    const { initBridge, requestGatewayToken } =
      await import("#lib/stores/postMessageBridge.js");
    initBridge();
    return (await requestGatewayToken()) || null;
  } catch {
    return null;
  }
}

/** Whether this instance's feed is served by the Gateway data plane. */
function usesGatewayDataPlane(): boolean {
  const gatewayBase =
    (import.meta.env.VITE_GATEWAY_API_BASE as string | undefined)?.replace(
      /\/+$/,
      "",
    ) || "";
  const config = readStore(deploymentMode);
  return (
    resolveGatewayUrl("/api/v1/notifications/feed", {
      mode: config.mode,
      dataPlane: config.dataPlane,
      gatewayBase,
      kombifyEdition: getClientBootstrap().kombifyEdition,
    }) !== null
  );
}

/** Live and decision options for a SaaS instance, nothing for any other. */
export function ownerChannelOptions(): Partial<OwnerChannelOptions> {
  if (!usesGatewayDataPlane()) return {};
  return {
    getToken: gatewayAccessToken,
    decisions: {
      // Loaded on the first answer: the card components already hold the module.
      async answerDecision(decisionId, request) {
        const { decisionClient } =
          await import("@kombiverselabs/ui/interaction");
        await decisionClient({ getToken: gatewayAccessToken }).answerDecision(
          decisionId,
          request,
        );
      },
    },
  };
}
