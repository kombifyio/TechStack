/**
 * Central recovery for 401 session_reprojection_required responses
 * (kombify-Techstack-nzy1.14).
 *
 * The server emits this reason code when a signature-valid session's
 * identity/tenant projection could not be resolved or recovered server-side
 * (e.g. after a deploy/migration invalidated projections). The fetch core
 * routes EVERY such response through this module - pages never interpret
 * this class themselves:
 *
 * - Embedded surfaces signal the host through the existing embed/session
 *   contract (postMessage token request + portal-verify) instead of
 *   redirecting inside the iframe, then the failed request is replayed once.
 * - Standalone surfaces run ONE silent SSO round-trip (full-page redirect to
 *   the hosted login, which re-mints the session from the still-valid
 *   Cloud/Auth0 upstream), guarded to one automatic attempt per 5 minutes
 *   via a sessionStorage stamp.
 * - When the guard blocks or the attempt fails, the single-click
 *   "Renew session" UI is raised - never a credential form while the
 *   upstream identity is alive.
 */
import * as Sentry from "@sentry/sveltekit";

export const SESSION_REPROJECTION_REASON_CODE = "session_reprojection_required";

export const REPROJECTION_REAUTH_MARKER_KEY =
  "techstack:auth:session_reprojection_at";
export const REPROJECTION_REAUTH_TTL_MS = 5 * 60_000;

export type SessionReprojectionOutcome =
  | "retry"
  | "redirecting"
  | "reauth_required";

export interface SessionReprojectionDeps {
  isEmbedded: () => boolean;
  /** Existing embed contract: ask the host to re-run the SSO handoff. */
  refreshEmbeddedSession: (options?: {
    forcePortalVerify?: boolean;
  }) => Promise<boolean>;
  /** Standalone: start the hosted SSO round-trip; true when navigating. */
  redirectToSSO: () => Promise<boolean>;
  /** Terminal rung: raise the single-click "Renew session" action. */
  promptRenewSession: (cause: unknown) => Promise<void> | void;
  now?: () => number;
  storage?: Storage;
}

/**
 * True when the error is the server's session-recovery signal: a 401 whose
 * envelope details carry reason_code=session_reprojection_required.
 */
export function isSessionReprojectionError(err: unknown): boolean {
  if (typeof err !== "object" || err === null) return false;
  const candidate = err as {
    status?: unknown;
    code?: unknown;
    details?: unknown;
  };
  if (candidate.status !== 401) return false;
  if (candidate.code === SESSION_REPROJECTION_REASON_CODE) return true;
  const details = candidate.details;
  if (typeof details !== "object" || details === null) return false;
  const coded = details as { reason_code?: unknown; error_code?: unknown };
  return (
    coded.reason_code === SESSION_REPROJECTION_REASON_CODE ||
    coded.error_code === SESSION_REPROJECTION_REASON_CODE
  );
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

/** True when no automatic reprojection re-auth happened within the TTL. */
export function shouldAttemptReprojectionReauth(
  now: number = Date.now(),
  storage?: Storage,
): boolean {
  const store = storageOrNull(storage);
  if (!store) return false;
  const raw = store.getItem(REPROJECTION_REAUTH_MARKER_KEY);
  if (!raw) return true;
  const markedAt = Number.parseInt(raw, 10);
  if (!Number.isFinite(markedAt)) return true;
  return now - markedAt > REPROJECTION_REAUTH_TTL_MS;
}

/** Record the automatic attempt BEFORE any navigation/host signal. */
export function markReprojectionReauthAttempt(
  now: number = Date.now(),
  storage?: Storage,
): void {
  storageOrNull(storage)?.setItem(REPROJECTION_REAUTH_MARKER_KEY, String(now));
}

export function clearReprojectionReauthMarker(storage?: Storage): void {
  storageOrNull(storage)?.removeItem(REPROJECTION_REAUTH_MARKER_KEY);
}

function defaultDeps(): SessionReprojectionDeps {
  return {
    isEmbedded: () => typeof window !== "undefined" && window.parent !== window,
    refreshEmbeddedSession: async (options) => {
      const { refreshEmbeddedCloudSession } =
        await import("$lib/auth/embedded-session");
      return refreshEmbeddedCloudSession(options);
    },
    redirectToSSO: async () => {
      const [{ authStore }, { currentAuthReturnTo }, { authHandler }] =
        await Promise.all([
          import("$lib/stores/auth.svelte"),
          import("$lib/auth/login-experience"),
          import("$lib/stores/authHandler.svelte"),
        ]);
      await authStore.init({ embedded: false });
      const redirected =
        authStore.initiateCloudLogin({ returnTo: currentAuthReturnTo() }) !==
        null;
      if (redirected) {
        // Swallow concurrent 401 handling while the page unloads so no
        // modal/panel flashes mid-redirect.
        authHandler.noteAutoRedirectStarted();
      }
      return redirected;
    },
    promptRenewSession: async (cause: unknown) => {
      const { authHandler } = await import("$lib/stores/authHandler.svelte");
      authHandler.promptSessionRenewal(cause);
    },
  };
}

function captureOutcome(
  outcome: SessionReprojectionOutcome,
  rung: string,
  embedded: boolean,
): void {
  Sentry.withScope((scope) => {
    scope.setTag("component", "session_reprojection");
    scope.setTag("flow", "silent_session_recovery");
    scope.setTag("reason_code", SESSION_REPROJECTION_REASON_CODE);
    scope.setTag("recovery_rung", rung);
    scope.setTag("outcome", outcome);
    scope.setTag("embedded", String(embedded));
    if (typeof window !== "undefined") {
      scope.setTag("route", window.location.pathname);
    }
    scope.setLevel("warning");
    Sentry.captureMessage("techstack_session_reprojection_signal");
  });
}

let recoveryInFlight: Promise<SessionReprojectionOutcome> | null = null;

/**
 * Single-flight recovery: parallel loaders that hit the signal at the same
 * time share one attempt. Returns how the caller should proceed:
 * "retry" (session re-minted in place - replay the request once),
 * "redirecting" (full-page SSO round-trip started), or
 * "reauth_required" (loop guard/terminal - renew-session UI raised).
 */
export async function recoverFromSessionReprojection(
  cause: unknown,
  deps: SessionReprojectionDeps = defaultDeps(),
): Promise<SessionReprojectionOutcome> {
  if (recoveryInFlight) return recoveryInFlight;
  recoveryInFlight = recoverOnce(cause, deps).finally(() => {
    recoveryInFlight = null;
  });
  return recoveryInFlight;
}

async function recoverOnce(
  cause: unknown,
  deps: SessionReprojectionDeps,
): Promise<SessionReprojectionOutcome> {
  const now = deps.now ?? Date.now;
  const embedded = deps.isEmbedded();

  if (!shouldAttemptReprojectionReauth(now(), deps.storage)) {
    captureOutcome("reauth_required", "loop_guard", embedded);
    await deps.promptRenewSession(cause);
    return "reauth_required";
  }
  markReprojectionReauthAttempt(now(), deps.storage);

  if (embedded) {
    // Embedded surfaces never redirect inside the iframe: the host re-runs
    // the SSO handoff via the postMessage bridge and portal-verify re-mints
    // the session cookie in place.
    let refreshed = false;
    try {
      refreshed = await deps.refreshEmbeddedSession({
        forcePortalVerify: true,
      });
    } catch {
      refreshed = false;
    }
    if (refreshed) {
      captureOutcome("retry", "embedded_host_refresh", embedded);
      return "retry";
    }
    captureOutcome("reauth_required", "embedded_host_refresh", embedded);
    await deps.promptRenewSession(cause);
    return "reauth_required";
  }

  let redirected = false;
  try {
    redirected = await deps.redirectToSSO();
  } catch {
    redirected = false;
  }
  if (redirected) {
    captureOutcome("redirecting", "sso_redirect", embedded);
    return "redirecting";
  }
  captureOutcome("reauth_required", "sso_redirect", embedded);
  await deps.promptRenewSession(cause);
  return "reauth_required";
}
