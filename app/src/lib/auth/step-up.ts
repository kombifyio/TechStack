import { browser } from "$app/env";
import type { ParsedApiError } from "#lib/api/errors.js";
import { authStore } from "#lib/stores/auth.svelte.js";

/**
 * Step-up sign-in for actions that need a fresh authentication (server
 * terminal, SSH key authorization, credential reveal, Guard changes).
 *
 * Standalone, the page navigates to the sign-in and comes back to itself.
 * Embedded in kombify Cloud the frame must never navigate: Auth0 is not
 * frameable, so the sign-in opens in a new top-level window instead, and the
 * frame learns about its completion through {@link watchStepUpCompletion}.
 */

export const SERVER_TERMINAL_REAUTH_PURPOSE =
  "kombify.cloud.server-terminal.v1";
export const WALLET_REAUTH_PURPOSE = "kombify.cloud.wallet.reauth.v1";
/** Reasons the backend names on a refusal that a fresh sign-in resolves. */
export const SERVER_ACCESS_STEP_UP_REASON = "server_access_step_up_required";
export const WALLET_REVEAL_STEP_UP_REASON = "wallet_reveal_step_up_required";
/** Same-origin page the sign-in returns to inside the step-up window. */
export const STEP_UP_COMPLETE_PATH = "/auth/reauth-complete";

const STEP_UP_CHANNEL = "kombify-techstack-step-up";
const STEP_UP_MESSAGE = "kombify:step-up-complete";
const POPUP_POLL_MS = 500;

/** What the fresh sign-in is for. */
export interface StepUpRequest {
  /** Binds the sign-in to one purpose and resource the backend checks. */
  binding?: { purpose: string; resource: string };
  /** Makes the identity provider ask for credentials again. */
  forceLogin?: boolean;
}

/** A fresh sign-in that no single resource depends on (Guard changes). */
export const FRESH_SIGN_IN: StepUpRequest = { forceLogin: true };

export function serverStepUp(serverId: string): StepUpRequest {
  return {
    binding: { purpose: SERVER_TERMINAL_REAUTH_PURPOSE, resource: serverId },
  };
}

export function walletStepUp(itemId: string): StepUpRequest {
  return {
    binding: { purpose: WALLET_REAUTH_PURPOSE, resource: itemId },
    forceLogin: true,
  };
}

/**
 * Whether a refused request asks for a fresh sign-in. A 403 that names its
 * reason decides by that reason. A structured denial (demo restriction,
 * entitlement) names an error code of its own that no new sign-in can change.
 * A bare 403 carries nothing but the envelope: an older server answered the
 * re-authentication refusal that way, so it still counts as one.
 */
export function isStepUpRefusal(
  parsed: ParsedApiError,
  reason: string,
): boolean {
  if (!parsed.isForbidden) return false;
  const details =
    typeof parsed.details === "object" && parsed.details !== null
      ? (parsed.details as Record<string, unknown>)
      : {};
  if (details.reason === reason) return true;
  return !["reason", "error_code", "reason_code"].some((key) => key in details);
}

/** Login URL for a fresh sign-in that returns to `returnTo`. */
export function buildStepUpLoginUrl(
  request: StepUpRequest,
  returnTo: string,
): string {
  const target = new URL(
    authStore.v2LoginUrl || authStore.cloudAuthUrl || "/api/v2/auth/login",
    window.location.origin,
  );
  target.searchParams.set("return_to", returnTo);
  if (request.binding) {
    target.searchParams.set("reauth_purpose", request.binding.purpose);
    target.searchParams.set("reauth_resource", request.binding.resource);
  }
  if (request.forceLogin) {
    target.searchParams.set("prompt", "login");
    target.searchParams.set("max_age", "0");
  }
  return target.toString();
}

/** Standalone step-up: full-page sign-in, then back to the current page. */
export function redirectToStepUp(request: StepUpRequest): void {
  if (!browser) return;
  const returnTo = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  window.location.href = buildStepUpLoginUrl(request, returnTo);
}

export interface StepUpWindow {
  url: string;
  /** Null when the browser blocked the window. */
  popup: Window | null;
}

/**
 * Embedded step-up: open the sign-in in a new top-level window. Call it from
 * a click handler so the browser counts it as a user gesture. The opener link
 * is kept on purpose: the frame watches the window to learn when it closes.
 */
export function openStepUpWindow(request: StepUpRequest): StepUpWindow {
  const url = buildStepUpLoginUrl(request, STEP_UP_COMPLETE_PATH);
  let popup: Window | null = null;
  try {
    popup = window.open(url, "_blank");
  } catch {
    popup = null;
  }
  return { url, popup };
}

/** Called by the completion page: tell the waiting frame the sign-in is done. */
export function announceStepUpComplete(): void {
  if (!browser) return;
  try {
    const channel = new BroadcastChannel(STEP_UP_CHANNEL);
    channel.postMessage({ type: STEP_UP_MESSAGE });
    window.setTimeout(() => channel.close(), 2_000);
  } catch {
    // The opener message below still reaches a frame that kept its link.
  }
  try {
    window.opener?.postMessage(
      { type: STEP_UP_MESSAGE },
      window.location.origin,
    );
  } catch {
    // An opener severed by a cross-origin policy cannot be messaged.
  }
}

export type StepUpSignal = "complete" | "window-closed" | "returned";

function isCompletionMessage(data: unknown): boolean {
  return (
    typeof data === "object" &&
    data !== null &&
    (data as { type?: unknown }).type === STEP_UP_MESSAGE
  );
}

/**
 * Report that the step-up window finished. "complete" is the page's own
 * announcement and may repeat. "window-closed" and "returned" (the frame
 * regained focus or became visible) are weaker fallbacks for a lost message:
 * together they fire at most once, and never while the window is still open.
 * Returns a function that stops watching.
 */
export function watchStepUpCompletion(
  popup: Window | null,
  onSignal: (signal: StepUpSignal) => void,
): () => void {
  let weakSignalSent = false;
  const weakSignal = (signal: "window-closed" | "returned") => {
    if (weakSignalSent) return;
    if (popup && !popup.closed) return;
    weakSignalSent = true;
    onSignal(signal);
  };

  let channel: BroadcastChannel | undefined;
  try {
    channel = new BroadcastChannel(STEP_UP_CHANNEL);
    channel.onmessage = (event) => {
      if (isCompletionMessage(event.data)) onSignal("complete");
    };
  } catch {
    channel = undefined;
  }

  const onMessage = (event: MessageEvent) => {
    if (
      event.origin === window.location.origin &&
      isCompletionMessage(event.data)
    ) {
      onSignal("complete");
    }
  };
  const onFocus = () => weakSignal("returned");
  const onVisibility = () => {
    if (document.visibilityState === "visible") weakSignal("returned");
  };
  window.addEventListener("message", onMessage);
  window.addEventListener("focus", onFocus);
  document.addEventListener("visibilitychange", onVisibility);

  const poll = popup
    ? window.setInterval(() => {
        if (!popup.closed) return;
        window.clearInterval(poll);
        weakSignal("window-closed");
      }, POPUP_POLL_MS)
    : undefined;

  return () => {
    channel?.close();
    window.removeEventListener("message", onMessage);
    window.removeEventListener("focus", onFocus);
    document.removeEventListener("visibilitychange", onVisibility);
    if (poll !== undefined) window.clearInterval(poll);
  };
}
