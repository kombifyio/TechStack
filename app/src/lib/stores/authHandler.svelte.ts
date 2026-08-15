/**
 * Global Auth Handler for kombify-TechStack
 *
 * Provides centralized handling for authentication errors (401 Unauthorized),
 * with automatic token refresh attempts and user-friendly relogin prompts.
 */

import * as Sentry from "@sentry/sveltekit";
import {
  getPocketBaseCompatStoredAuthToken,
  isPocketBaseAuthCompatEnabled,
} from "$lib/auth/pocketbase-compat";
import { authStore } from "$lib/stores/auth.svelte";
import {
  isEmbeddedWindow,
  refreshEmbeddedCloudSession,
} from "$lib/auth/embedded-session";
import { currentAuthReturnTo } from "$lib/auth/login-experience";
import {
  classifyAuthFailure,
  clearAutoReloginMarker,
  markAutoReloginAttempt,
  shouldAttemptAutoRelogin,
  type AuthRecoveryOutcome,
} from "$lib/auth/session-recovery";

export type { AuthRecoveryOutcome } from "$lib/auth/session-recovery";

export interface HandleUnauthorizedOptions {
  /**
   * Allow the one-shot automatic re-login round-trip for gateway-token
   * failures. Pages holding unsaved in-memory state (the create wizard) pass
   * false so a full-page redirect never destroys their form.
   */
  allowAutoRedirect?: boolean;
}

// Auth handler state (using Svelte 5 runes)
class AuthHandlerStore {
  // Show re-login modal
  showReloginModal = $state(false);

  // Pending action to retry after successful re-login
  private _pendingRetry: (() => Promise<void>) | null = null;

  // Track if we're currently refreshing
  private _isRefreshing = $state(false);

  // Error message to display
  errorMessage = $state<string>("");

  // Track last auth error time to prevent spam
  private _lastAuthErrorTime = 0;
  private _authErrorCooldown = 15000; // 15 seconds

  // Single-flight recovery so parallel loaders (stacks/workers/inventory)
  // trigger exactly one refresh/redirect per incident.
  private _recoveryInFlight: Promise<AuthRecoveryOutcome> | null = null;

  // Set once the automatic re-login navigation started; swallows concurrent
  // 401s while the page is unloading so no modal/panel flashes mid-redirect.
  private _autoRedirectStarted = false;

  // Which recovery rung reported success last (for the retry-failure reason).
  private _lastRecoveryRung: "session_refresh" | "embedded_refresh" =
    "session_refresh";

  get isRefreshing(): boolean {
    return this._isRefreshing;
  }

  /**
   * Attempt to refresh the authentication token.
   * PocketBase SDK handles token refresh automatically if the token is valid.
   * This method forces a refresh attempt.
   */
  async tryRefreshToken(): Promise<boolean> {
    if (this._isRefreshing) return false;

    this._isRefreshing = true;

    try {
      if (!isPocketBaseAuthCompatEnabled()) {
        await authStore.init({ embedded: isEmbeddedWindow() });
        return authStore.v2SessionActive;
      }

      // Check if we have a stored compatibility token.
      if (!getPocketBaseCompatStoredAuthToken()) {
        console.log("[AuthHandler] No valid auth token to refresh");
        return false;
      }

      await authStore.init({ embedded: isEmbeddedWindow() });
      return authStore.isAuthenticated;
    } catch (err) {
      console.warn("[AuthHandler] Token refresh failed:", err);

      // If the refresh itself is unauthorized, the stored token is no longer
      // usable. Clear it so we don't loop on subsequent requests.
      if (
        typeof err === "object" &&
        err !== null &&
        "status" in err &&
        (err as { status?: unknown }).status === 401
      ) {
        await authStore.clearSession();
      }
      return false;
    } finally {
      this._isRefreshing = false;
    }
  }

  /**
   * Handle a 401 Unauthorized error via the classified recovery ladder.
   *
   * Gateway-token failures with an alive browser session recover without the
   * modal: silent refresh -> ONE guarded automatic re-login round-trip ->
   * inline re-auth panel at the call site ("reauth_required"). Genuine
   * session death keeps the full-screen re-login modal ("modal_shown").
   *
   * @param retryFn Optional function to retry after successful recovery
   * @param customMessage Optional custom error message
   * @returns the recovery outcome so call sites can render inline UI
   */
  async handleUnauthorized(
    retryFn?: () => Promise<void>,
    customMessage?: string,
    cause?: unknown,
    options?: HandleUnauthorizedOptions,
  ): Promise<AuthRecoveryOutcome> {
    // If we're already prompting the user, don't keep retrying refresh.
    if (this.showReloginModal) {
      this._pendingRetry = retryFn || this._pendingRetry;
      return "modal_shown";
    }
    if (this._autoRedirectStarted) {
      return "redirecting";
    }

    let outcome: AuthRecoveryOutcome;
    if (this._recoveryInFlight) {
      outcome = await this._recoveryInFlight;
    } else {
      this._recoveryInFlight = this.recoverSession(
        customMessage,
        cause,
        options,
      );
      try {
        outcome = await this._recoveryInFlight;
      } finally {
        this._recoveryInFlight = null;
      }
    }

    if (outcome === "recovered" && retryFn) {
      try {
        await retryFn();
      } catch (err) {
        if (this.isAuthError(err)) {
          return this.escalateAfterRetry(retryFn, customMessage, err, options);
        }
        console.error("[AuthHandler] Retry after refresh failed:", err);
      }
    } else if (outcome === "modal_shown") {
      this._pendingRetry = retryFn || this._pendingRetry;
    }
    return outcome;
  }

  /**
   * The shared, single-flight part of the ladder: refresh, classify, and pick
   * the terminal outcome. Caller-specific retries stay outside.
   */
  private async recoverSession(
    customMessage: string | undefined,
    cause: unknown,
    options?: HandleUnauthorizedOptions,
  ): Promise<AuthRecoveryOutcome> {
    const now = Date.now();
    const withinCooldown =
      now - this._lastAuthErrorTime < this._authErrorCooldown;
    this._lastAuthErrorTime = now;

    console.log("[AuthHandler] Handling 401 Unauthorized");

    const refreshed = await this.tryRefreshToken();
    const failureClass = classifyAuthFailure(cause, {
      v2SessionActive: authStore.v2SessionActive,
    });

    if (failureClass === "gateway_token" && this.isStandaloneSaaS()) {
      // The cookie session is alive; only the Auth0 gateway token is broken.
      // Never the modal for this class — auto-redirect or inline panel.
      return this.recoverGatewayToken(cause, options);
    }

    if (withinCooldown) {
      // Still cool down repeated refresh attempts, but never leave the user
      // with a silently dead action: surface the re-login prompt instead of
      // returning quietly.
      console.log("[AuthHandler] Auth error cooldown active");
      if (!this._isRefreshing) {
        this.showReloginPrompt(undefined, customMessage, "cooldown", cause);
      }
      return "modal_shown";
    }

    if (refreshed) {
      this._lastRecoveryRung = "session_refresh";
      return "recovered";
    }

    const embeddedRefreshed = await refreshEmbeddedCloudSession();
    if (embeddedRefreshed) {
      this._lastRecoveryRung = "embedded_refresh";
      return "recovered";
    }

    // Token refresh failed, show re-login modal
    this.showReloginPrompt(undefined, customMessage, "refresh_failed", cause);
    return "modal_shown";
  }

  /**
   * Terminal handling for gateway-token failures in standalone SaaS: one
   * automatic re-login round-trip per tab per marker TTL (seamless when the
   * Auth0 SSO session at the custom domain is alive), then the inline panel.
   */
  private recoverGatewayToken(
    cause: unknown,
    options?: HandleUnauthorizedOptions,
  ): AuthRecoveryOutcome {
    if (options?.allowAutoRedirect === false) {
      this.captureReloginPrompt("gateway_inline_prompt", cause, {
        failure_class: "gateway_token",
        recovery_rung: "inline_prompt",
      });
      return "reauth_required";
    }
    if (authStore.v2SessionActive && shouldAttemptAutoRelogin()) {
      markAutoReloginAttempt();
      this.captureReloginPrompt("gateway_auto_relogin", cause, {
        failure_class: "gateway_token",
        recovery_rung: "auto_redirect",
      });
      const redirected = authStore.initiateCloudLogin({
        returnTo: currentAuthReturnTo(),
      });
      if (redirected) {
        this._autoRedirectStarted = true;
        return "redirecting";
      }
      // Login URL unavailable — fall through to the inline panel.
    }
    this.captureReloginPrompt("gateway_auto_relogin_exhausted", cause, {
      failure_class: "gateway_token",
      recovery_rung: "inline_prompt",
    });
    return "reauth_required";
  }

  /**
   * A retry right after a reported-successful refresh 401'd again: reclassify
   * and route to the correct terminal UI.
   */
  private async escalateAfterRetry(
    retryFn: () => Promise<void>,
    customMessage: string | undefined,
    err: unknown,
    options?: HandleUnauthorizedOptions,
  ): Promise<AuthRecoveryOutcome> {
    const failureClass = classifyAuthFailure(err, {
      v2SessionActive: authStore.v2SessionActive,
    });
    if (failureClass === "gateway_token" && this.isStandaloneSaaS()) {
      return this.recoverGatewayToken(err, options);
    }
    this.showReloginPrompt(
      retryFn,
      customMessage,
      this._lastRecoveryRung === "embedded_refresh"
        ? "retry_after_embedded_refresh_unauthorized"
        : "retry_after_refresh_unauthorized",
      err,
    );
    return "modal_shown";
  }

  private isStandaloneSaaS(): boolean {
    return authStore.deploymentMode === "saas" && !isEmbeddedWindow();
  }

  /**
   * Called by the central session-reprojection interceptor right before its
   * silent SSO redirect so concurrent 401s are swallowed while the page
   * unloads (mirrors the internal gateway-token auto-redirect behavior).
   */
  noteAutoRedirectStarted(): void {
    this._autoRedirectStarted = true;
  }

  /**
   * Terminal rung of the session-reprojection recovery: the loop guard
   * blocked another automatic attempt (or none was possible). Raises the
   * single-click renew-session UI - in the SaaS login experience this is the
   * one-click "Continue with Auth0" modal, never a credential form while the
   * upstream identity is alive.
   */
  promptSessionRenewal(cause?: unknown): void {
    this.showReloginPrompt(
      undefined,
      undefined,
      "session_reprojection_exhausted",
      cause,
    );
  }

  /**
   * Called when user successfully re-logs in via the modal.
   * Retries the pending action if available.
   */
  async onReloginSuccess(): Promise<void> {
    this.showReloginModal = false;
    this.errorMessage = "";
    this._lastAuthErrorTime = 0;

    const pendingRetry = this._pendingRetry;
    this._pendingRetry = null;

    if (pendingRetry) {
      try {
        await pendingRetry();
      } catch (err) {
        if (this.isAuthError(err)) {
          this.showReloginPrompt(
            pendingRetry,
            "Your session has expired. Please sign in again.",
            "retry_after_relogin_unauthorized",
            err,
          );
          return;
        }
        console.error("[AuthHandler] Retry after re-login failed:", err);
      }
    }
  }

  /**
   * Called when user cancels the re-login modal.
   * Logs out and redirects to login page.
   */
  async onReloginCancel(): Promise<void> {
    this.showReloginModal = false;
    this.errorMessage = "";
    this._pendingRetry = null;

    await authStore.logout();
  }

  private showReloginPrompt(
    retryFn: (() => Promise<void>) | undefined,
    customMessage: string | undefined,
    reason: string,
    cause?: unknown,
  ): void {
    this._pendingRetry = retryFn || this._pendingRetry;
    this.errorMessage = customMessage || "auth.session.expired.default";
    this.showReloginModal = true;
    this.captureReloginPrompt(reason, cause, {
      failure_class: authStore.v2SessionActive ? "generic_401" : "session_dead",
      recovery_rung: "modal",
    });
  }

  private captureReloginPrompt(
    reason: string,
    cause?: unknown,
    extraTags?: Record<string, string>,
  ): void {
    const errorContext = this.extractAuthErrorContext(cause);
    Sentry.withScope((scope) => {
      scope.setTag("component", "auth_handler");
      scope.setTag("flow", "browser_reauth");
      scope.setTag("reason", reason);
      scope.setTag("deployment_mode", authStore.deploymentMode);
      scope.setTag("v2_session_active", String(authStore.v2SessionActive));
      scope.setTag("embedded", String(isEmbeddedWindow()));
      scope.setTag(
        "pocketbase_compat",
        String(isPocketBaseAuthCompatEnabled()),
      );
      if (typeof window !== "undefined") {
        scope.setTag("route", window.location.pathname);
      }
      for (const [key, value] of Object.entries(extraTags ?? {})) {
        scope.setTag(key, value);
      }
      for (const [key, value] of Object.entries(errorContext)) {
        scope.setTag(key, value);
      }
      scope.setLevel("warning");
      Sentry.captureMessage("techstack_session_reauth_required");
    });
  }

  private extractAuthErrorContext(cause: unknown): Record<string, string> {
    if (typeof cause !== "object" || cause === null) {
      return {};
    }

    const err = cause as {
      status?: unknown;
      code?: unknown;
      method?: unknown;
      url?: unknown;
      requestId?: unknown;
    };
    const context: Record<string, string> = {};

    if (typeof err.status === "number" || typeof err.status === "string") {
      context.api_status = String(err.status);
    }
    if (typeof err.code === "number" || typeof err.code === "string") {
      context.api_code = String(err.code);
    }
    if (typeof err.method === "string" && err.method.trim() !== "") {
      context.api_method = err.method.toUpperCase();
    }
    if (typeof err.url === "string" && err.url.trim() !== "") {
      context.api_path = this.sanitizeApiPath(err.url);
    }
    if (typeof err.requestId === "string" && err.requestId.trim() !== "") {
      context.request_id = err.requestId;
    }

    return context;
  }

  private sanitizeApiPath(rawUrl: string): string {
    try {
      const base =
        typeof window !== "undefined"
          ? window.location.origin
          : "http://techstack.local";
      return new URL(rawUrl, base).pathname;
    } catch {
      return rawUrl.split("?")[0].slice(0, 160);
    }
  }

  /**
   * Check if an error is a 401 Unauthorized error.
   */
  isAuthError(err: unknown): boolean {
    // PocketBase ClientResponseError
    if (typeof err === "object" && err !== null && "status" in err) {
      return (err as { status: number }).status === 401;
    }

    // Standard Error with message
    if (err instanceof Error) {
      const msg = err.message.toLowerCase();
      return (
        msg.includes("401") ||
        msg.includes("unauthorized") ||
        msg.includes("authentication required")
      );
    }

    return false;
  }

  /**
   * Wrap an async function with automatic auth error handling.
   * If the function throws a 401, it will attempt token refresh
   * and optionally retry the function.
   */
  withAuthHandler<T extends unknown[], R>(
    fn: (...args: T) => Promise<R>,
    options?: { retry?: boolean; customMessage?: string },
  ): (...args: T) => Promise<R | undefined> {
    return async (...args: T): Promise<R | undefined> => {
      try {
        return await fn(...args);
      } catch (err) {
        if (this.isAuthError(err)) {
          const retryFn = options?.retry
            ? () => fn(...args).then(() => undefined)
            : undefined;
          await this.handleUnauthorized(retryFn, options?.customMessage, err);
          return undefined;
        }
        throw err;
      }
    };
  }

  /**
   * Reset the auth handler state.
   */
  reset(): void {
    this.showReloginModal = false;
    this.errorMessage = "";
    this._pendingRetry = null;
    this._isRefreshing = false;
    this._lastAuthErrorTime = 0;
    this._recoveryInFlight = null;
    this._autoRedirectStarted = false;
    clearAutoReloginMarker();
  }
}

// Export singleton instance
export const authHandler = new AuthHandlerStore();

/**
 * Helper function to wrap API calls with auth error handling.
 *
 * Usage:
 * ```ts
 * const data = await withAuth(() => fetchSomeData());
 * ```
 */
export async function withAuth<T>(
  fn: () => Promise<T>,
  options?: { retry?: boolean; customMessage?: string },
): Promise<T | undefined> {
  try {
    return await fn();
  } catch (err) {
    if (authHandler.isAuthError(err)) {
      const retryFn = options?.retry
        ? () => fn().then(() => undefined)
        : undefined;
      await authHandler.handleUnauthorized(
        retryFn,
        options?.customMessage,
        err,
      );
      return undefined;
    }
    throw err;
  }
}
