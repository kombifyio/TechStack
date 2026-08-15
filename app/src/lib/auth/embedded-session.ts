import { browser } from "$app/environment";
import { resolveLoginExperience } from "$lib/auth/login-experience";
import { authStore } from "$lib/stores/auth.svelte";
import { initBridge, requestAuthToken } from "$lib/stores/postMessageBridge";
import {
  getGatewayToken,
  isGatewayAuthConfigured,
} from "$lib/auth/gateway-auth";

let refreshInFlight: Promise<boolean> | null = null;
let lastExchangedPortalToken: string | null = null;
let forcePortalVerifyRequested = false;
let activeRefreshForcesPortalVerify = false;

export interface EmbeddedSessionRefreshOptions {
  /**
   * Re-run portal-verify even when the parent returns its still-valid cached
   * SSO token. A session_reprojection_required response means the browser's
   * V2 cookie is the part that must be minted again, not the portal token.
   */
  forcePortalVerify?: boolean;
}

export function isEmbeddedWindow(): boolean {
  return browser && window.parent !== window;
}

export async function refreshEmbeddedCloudSession(
  options: EmbeddedSessionRefreshOptions = {},
): Promise<boolean> {
  if (options.forcePortalVerify && !activeRefreshForcesPortalVerify) {
    forcePortalVerifyRequested = true;
  }
  if (refreshInFlight) return refreshInFlight;

  refreshInFlight = refreshEmbeddedCloudSessionUntilSettled().finally(() => {
    refreshInFlight = null;
    activeRefreshForcesPortalVerify = false;
    forcePortalVerifyRequested = false;
  });
  return refreshInFlight;
}

async function refreshEmbeddedCloudSessionUntilSettled(): Promise<boolean> {
  let forcePortalVerify = forcePortalVerifyRequested;
  forcePortalVerifyRequested = false;

  while (true) {
    activeRefreshForcesPortalVerify = forcePortalVerify;
    const refreshed = await refreshEmbeddedCloudSessionOnce(forcePortalVerify);
    if (!refreshed) return false;

    // If a reprojection recovery joined an ordinary in-flight refresh, run one
    // additional exchange before resolving the shared single-flight promise.
    if (!forcePortalVerifyRequested) return true;
    forcePortalVerify = true;
    forcePortalVerifyRequested = false;
  }
}

async function refreshEmbeddedCloudSessionOnce(
  forcePortalVerify: boolean,
): Promise<boolean> {
  if (!isEmbeddedWindow()) return false;

  await authStore.init({ embedded: true });
  if (
    resolveLoginExperience({
      deploymentMode: authStore.deploymentMode,
      embedded: true,
    }) !== "saas-auth0"
  ) {
    return false;
  }

  try {
    initBridge();
    const token = await requestAuthToken();
    if (
      forcePortalVerify ||
      token !== lastExchangedPortalToken ||
      !authStore.cloudUser ||
      !authStore.v2SessionActive
    ) {
      await authStore.completePortalLogin(token);
      lastExchangedPortalToken = token;
    }
  } catch (err) {
    console.warn("[embedded-session] Parent SSO refresh failed:", err);
    return false;
  }

  // Prime the user's Auth0 token used by the data plane through the Cloudflare
  // gateway (api.kombify.io/v1/techstack/*), so the edge can sign entitlement
  // flags. Best-effort: silent acquisition is expected to succeed (same-site
  // *.kombify.io). On failure the data plane fails closed per request rather
  // than downgrading to anonymous here.
  if (isGatewayAuthConfigured()) {
    try {
      await getGatewayToken();
    } catch (err) {
      console.warn(
        "[embedded-session] gateway token prime failed (data plane will retry):",
        err,
      );
    }
  }

  // completePortalLogin only resolves after /api/v2/whoami has accepted the
  // freshly issued HttpOnly cookie. Never treat the compatibility payload as
  // a successful embedded recovery by itself.
  return authStore.v2SessionActive;
}
