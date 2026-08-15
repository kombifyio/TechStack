/**
 * Pure helpers for the standalone SaaS session-recovery ladder.
 *
 * Failure classification separates "the gateway token could not be minted
 * while the browser session is alive" (recoverable without user interaction)
 * from a genuinely dead session (full re-login). The loop guard persists one
 * automatic re-login round-trip per tab across the full-page redirect so the
 * app can silently repair the Auth0 SSO session without ever bouncing in a
 * loop.
 */
import * as Sentry from "@sentry/sveltekit";

export type AuthFailureClass = "gateway_token" | "session_dead" | "generic_401";

export type AuthRecoveryOutcome =
  | "recovered"
  | "redirecting"
  | "reauth_required"
  | "modal_shown";

export const AUTO_RELOGIN_MARKER_KEY = "techstack:auth:auto_relogin_at";
export const AUTO_RELOGIN_TTL_MS = 10 * 60_000;

const GATEWAY_FAILURE_CODES = new Set(["gateway_auth_unavailable"]);

const AUTH0_SILENT_FAILURE_CODES = new Set([
  "login_required",
  "consent_required",
  "missing_refresh_token",
  "invalid_grant",
]);

/**
 * True when the error is a gateway-token acquisition failure (our typed
 * ApiRequestError codes, or an Auth0 SPA error shape from a direct caller).
 */
export function isGatewayAuthFailure(err: unknown): boolean {
  if (typeof err !== "object" || err === null) return false;
  const candidate = err as {
    code?: unknown;
    error?: unknown;
    message?: unknown;
  };
  if (
    typeof candidate.code === "string" &&
    GATEWAY_FAILURE_CODES.has(candidate.code)
  ) {
    return true;
  }
  if (
    typeof candidate.error === "string" &&
    AUTH0_SILENT_FAILURE_CODES.has(candidate.error)
  ) {
    return true;
  }
  if (err instanceof Error && GATEWAY_FAILURE_CODES.has(err.message.trim())) {
    return true;
  }
  return false;
}

export function classifyAuthFailure(
  err: unknown,
  opts: { v2SessionActive: boolean },
): AuthFailureClass {
  if (!opts.v2SessionActive) return "session_dead";
  if (isGatewayAuthFailure(err)) return "gateway_token";
  return "generic_401";
}

function storageOrNull(storage?: Storage): Storage | null {
  if (storage) return storage;
  if (typeof window === "undefined") return null;
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

/** True when no automatic re-login round-trip happened within the TTL. */
export function shouldAttemptAutoRelogin(
  now: number = Date.now(),
  storage?: Storage,
): boolean {
  const store = storageOrNull(storage);
  if (!store) return false;
  const raw = store.getItem(AUTO_RELOGIN_MARKER_KEY);
  if (!raw) return true;
  const markedAt = Number.parseInt(raw, 10);
  if (!Number.isFinite(markedAt)) return true;
  return now - markedAt > AUTO_RELOGIN_TTL_MS;
}

/** Record the automatic re-login attempt BEFORE navigating away. */
export function markAutoReloginAttempt(
  now: number = Date.now(),
  storage?: Storage,
): void {
  storageOrNull(storage)?.setItem(AUTO_RELOGIN_MARKER_KEY, String(now));
}

export function clearAutoReloginMarker(storage?: Storage): void {
  storageOrNull(storage)?.removeItem(AUTO_RELOGIN_MARKER_KEY);
}

/**
 * Called on every successful gateway-token acquisition. When a marker is
 * pending, the preceding automatic round-trip recovered the session: emit the
 * telemetry counterpart of `techstack_session_auto_relogin` and clear the
 * marker so a later incident gets a fresh automatic attempt.
 */
export function noteGatewayTokenSuccess(storage?: Storage): void {
  const store = storageOrNull(storage);
  if (!store) return;
  if (!store.getItem(AUTO_RELOGIN_MARKER_KEY)) return;
  store.removeItem(AUTO_RELOGIN_MARKER_KEY);
  Sentry.withScope((scope) => {
    scope.setTag("component", "auth_handler");
    scope.setTag("flow", "browser_reauth");
    scope.setTag("failure_class", "gateway_token");
    scope.setTag("recovery_rung", "auto_redirect");
    scope.setLevel("info");
    Sentry.captureMessage("techstack_session_auto_relogin_recovered");
  });
}
