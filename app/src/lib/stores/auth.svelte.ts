/**
 * Unified Auth Store for kombify-TechStack
 *
 * Manages authentication state across both self-hosted and
 * cloud-hosted (kombify Cloud SSO) deployment modes. This store is the single
 * source of truth for authentication state in the frontend.
 *
 * Features:
 * - Automatic auth mode detection on init
 * - Local Go session integration (self-hosted)
 * - Hosted cloud SSO integration (cloud mode)
 * - User role detection (admin vs homelab user)
 * - Session persistence and refresh
 */

import { goto } from "$app/navigation";
import { browser } from "$app/environment";
import {
  getAuthMode,
  getV2AuthProviders,
  getV2WhoAmI,
  loginWithLocalSession,
  logoutLocalSession,
  verifyPortalToken,
  type AuthMode,
  type V2WhoAmIResponse,
  type DeploymentMode,
  type CloudUser,
} from "$lib/api/auth";
import {
  getPocketBaseCompatStoredUser,
  clearPocketBaseCompatStoredSession,
  isPocketBaseAuthCompatEnabled,
  savePocketBaseCompatStoredSession,
  type AuthModel,
} from "$lib/auth/pocketbase-compat";
import {
  buildCloudAuthRedirectURL,
  buildV2ProviderLogoutPath,
  currentAuthReturnTo,
  getPostLogoutRedirectPath,
  resolveLoginExperience,
} from "$lib/auth/login-experience";
import { clearReprojectionReauthMarker } from "$lib/api/session-reprojection";
import {
  hasWindowsLocalClientContext,
  windowsLocalClientReturnUrl,
} from "$lib/client/windows-onboarding";
import { setStackIdentity } from "$lib/stores/stackIdentity";
import { clearTechstackSecuritySessionState } from "$lib/logout-cleanup";

const CLOUD_PORTAL_SESSION_PROVIDER_ID = "cloud";

// ============================================================================
// Types
// ============================================================================

export interface AuthState {
  /** Current authentication mode */
  mode: AuthMode;
  /** Whether auth mode has been detected */
  modeDetected: boolean;
  /** Cloud auth URL for hosted cloud login */
  cloudAuthUrl: string | null;
  /** Portal URL for cloud mode */
  portalUrl: string | null;
  /** Whether local login is allowed in cloud mode */
  allowLocalLogin: boolean;
  /** Current legacy compatibility user (if any) */
  pocketbaseUser: AuthModel | null;
  /** Current cloud user claims (if any) */
  cloudUser: CloudUser | null;
  /** Whether user is authenticated */
  isAuthenticated: boolean;
  /** Whether user is an admin */
  isAdmin: boolean;
  /** Loading state */
  loading: boolean;
  /** Error message */
  error: string | null;
}

// ============================================================================
// Auth Store (Svelte 5 Runes)
// ============================================================================

class AuthStore {
  private initInFlight: Promise<void> | null = null;
  private initialized = false;
  private subscriptionsBound = false;
  private hostedProviderIDs = new Set<string>();

  // Core state using Svelte 5 runes
  mode = $state<AuthMode>("local");
  deploymentMode = $state<DeploymentMode>("self-hosted");
  isFirstRun = $state(false);
  modeDetected = $state(false);
  cloudAuthUrl = $state<string | null>(null);
  v2LoginUrl = $state<string | null>(null);
  portalUrl = $state<string | null>(null);
  allowLocalLogin = $state(true);
  pocketbaseUser = $state<AuthModel | null>(null);
  cloudUser = $state<CloudUser | null>(null);
  v2SessionActive = $state(false);
  loading = $state(false);
  error = $state<string | null>(null);

  // Derived state
  get isAuthenticated(): boolean {
    return this.pocketbaseUser !== null || this.cloudUser !== null;
  }

  get isAdmin(): boolean {
    // Cloud users with admin role are admins
    if (this.cloudUser?.is_admin) return true;
    // In self-hosted mode, local PocketBase users are the owner/admin path.
    if (this.deploymentMode === "self-hosted" && this.pocketbaseUser) {
      return true;
    }
    return false;
  }

  get currentUser(): AuthModel | CloudUser | null {
    return this.cloudUser || this.pocketbaseUser;
  }

  get userEmail(): string | null {
    if (this.cloudUser) return this.cloudUser.email;
    if (this.pocketbaseUser) return this.pocketbaseUser.email ?? null;
    return null;
  }

  get userName(): string | null {
    if (this.cloudUser) return this.cloudUser.name;
    if (this.pocketbaseUser) {
      return (
        (this.pocketbaseUser as { name?: string }).name ||
        this.pocketbaseUser.email ||
        null
      );
    }
    return null;
  }

  // ============================================================================
  // Initialization
  // ============================================================================

  /**
   * Initialize auth state - call this on app mount
   */
  async init(options: { embedded?: boolean } = {}): Promise<void> {
    if (!browser) return;

    // Derive iframe context at the authority boundary so alternate boot paths
    // (including initAuth()) cannot accidentally issue a pre-handoff whoami.
    const embedded = options.embedded ?? window.parent !== window;

    if (this.initialized) return;

    if (this.initInFlight) {
      await this.initInFlight;
      return;
    }

    this.initInFlight = (async () => {
      this.loading = true;
      this.error = null;

      try {
        // 1. Detect auth mode from backend
        await this.detectAuthMode();

        // 1.5 Detect whether the V2 auth endpoints are available.
        await this.detectV2Auth();

        // 2. Check for an existing legacy compatibility token.
        this.syncCompatAuth();

        // 2.5 Check for an existing V2 cookie-backed session.
        // Embedded identity is established by the parent portal exchange. A
        // speculative same-origin whoami before that handoff only produces a
        // misleading 401 and cannot authenticate the iframe.
        if (!embedded) {
          await this.syncV2Session();
        }

        // 3. Check for hosted OIDC callback params.
        await this.checkPortalRedirect();

        if (!this.subscriptionsBound) {
          this.subscriptionsBound = true;
        }
      } catch (err) {
        console.error("[AuthStore] Init error:", err);
        this.error = err instanceof Error ? err.message : "Auth init failed";
      } finally {
        this.loading = false;
        this.initialized = true;
      }
    })();

    try {
      await this.initInFlight;
    } finally {
      this.initInFlight = null;
    }
  }

  /**
   * Detect auth mode from backend
   */
  private async detectAuthMode(): Promise<void> {
    try {
      const authModeResponse = await getAuthMode();
      this.mode = authModeResponse.mode;
      this.deploymentMode = authModeResponse.deployment_mode || "self-hosted";
      this.isFirstRun = authModeResponse.is_first_run || false;
      this.cloudAuthUrl = authModeResponse.cloud_auth_url;
      this.portalUrl = authModeResponse.portal_url;
      this.allowLocalLogin = authModeResponse.allow_local_login;
      this.modeDetected = true;
    } catch {
      // Default to local mode if backend is unreachable
      console.warn(
        "[AuthStore] Could not detect auth mode, defaulting to local",
      );
      this.mode = "local";
      this.deploymentMode = "self-hosted";
      this.isFirstRun = false;
      this.modeDetected = true;
    }
  }

  private async detectV2Auth(): Promise<void> {
    try {
      const providers = await getV2AuthProviders();
      const hostedProviderIDs = providers
        .map((provider) => provider.id.trim())
        .filter((providerID) => providerID.length > 0);
      if (this.deploymentMode === "saas") {
        // The embedded portal exchange issues the same authoritative V2
        // browser session with provider=cloud. Keep it on the existing hosted
        // session allowlist so syncV2Session does not immediately clear the
        // freshly verified cookie as if it were a local compatibility session.
        hostedProviderIDs.push(CLOUD_PORTAL_SESSION_PROVIDER_ID);
      }
      this.hostedProviderIDs = new Set(hostedProviderIDs);
      if (providers.length > 0) {
        this.v2LoginUrl = "/api/v2/auth/login";
        if (!this.cloudAuthUrl) {
          this.cloudAuthUrl = this.v2LoginUrl;
        }
      } else {
        this.v2LoginUrl = null;
      }
    } catch {
      this.hostedProviderIDs.clear();
      this.v2LoginUrl = null;
    }
  }

  /**
   * Sync with the legacy compatibility auth payload.
   */
  private syncCompatAuth(): void {
    // SaaS never accepts a stale local/PocketBase compatibility session. Such
    // a cookie makes the UI look authenticated while the gateway cannot mint
    // the user's Auth0 entitlement envelope, leaving managed VPS disabled.
    if (this.deploymentMode === "saas" && !this.allowLocalLogin) {
      clearPocketBaseCompatStoredSession();
      this.pocketbaseUser = null;
      return;
    }
    this.pocketbaseUser = getPocketBaseCompatStoredUser();
  }

  private async syncV2Session(): Promise<boolean> {
    try {
      const whoami = await getV2WhoAmI();
      if (
        this.deploymentMode === "saas" &&
        !this.allowLocalLogin &&
        whoami.provider &&
        !this.hostedProviderIDs.has(whoami.provider)
      ) {
        await logoutLocalSession();
        this.cloudUser = null;
        this.v2SessionActive = false;
        return false;
      }
      this.cloudUser = v2WhoAmIToCloudUser(whoami);
      this.v2SessionActive = true;
      clearReprojectionReauthMarker();
      return true;
    } catch {
      this.cloudUser = null;
      this.v2SessionActive = false;
      return false;
    }
  }

  /** Check for hosted OIDC callback params. */
  private async checkPortalRedirect(): Promise<void> {
    if (!browser) return;

    const params = new URLSearchParams(window.location.search);
    const code = params.get("code");
    const state = params.get("state");

    // Handle OIDC callback (code + state)
    if (code && state) {
      await this.handleOIDCCallback(code, state);
      // Clean URL
      const url = new URL(window.location.href);
      url.searchParams.delete("code");
      url.searchParams.delete("state");
      window.history.replaceState({}, "", url.toString());
    }
  }

  // ============================================================================
  // Login Methods
  // ============================================================================

  /**
   * Login with email and password via the Go local session endpoint.
   */
  async loginWithPassword(email: string, password: string): Promise<boolean> {
    this.loading = true;
    this.error = null;

    try {
      await loginWithLocalSession(email, password);
      await this.syncV2Session();
      this.pocketbaseUser = null;
      return true;
    } catch (err) {
      if (isPocketBaseAuthCompatEnabled()) {
        console.warn(
          "[AuthStore] Legacy PocketBase auth compatibility is enabled, but SDK login fallback has been removed; use the Go local session endpoint.",
        );
      }
      console.error("[AuthStore] Login error:", err);
      this.error = "Invalid email or password";
      return false;
    } finally {
      this.loading = false;
    }
  }

  /**
   * Initiate cloud login (redirect to the hosted identity provider)
   */
  initiateCloudLogin(options?: {
    returnTo?: string | null;
    redirect?: (url: string) => void;
  }): string | null {
    const target = this.v2LoginUrl || this.cloudAuthUrl;
    if (target) {
      const redirectURL = buildCloudAuthRedirectURL(target, {
        returnTo: options?.returnTo ?? currentAuthReturnTo(),
      });
      (options?.redirect ?? window.location.assign.bind(window.location))(
        redirectURL,
      );
      return redirectURL;
    } else {
      this.error = "Cloud authentication not configured";
      return null;
    }
  }

  /**
   * Complete portal SSO login and sync the shared auth state.
   */
  async completePortalLogin(token: string): Promise<void> {
    const data = await verifyPortalToken(token);
    const v2SessionConfirmed = await this.syncV2Session();
    if (!v2SessionConfirmed || !this.v2SessionActive || !this.cloudUser) {
      throw new Error(
        "Portal sign-in did not establish a verified browser session",
      );
    }
    if (!data.pb_token) {
      throw new Error("Portal sign-in did not return a compatibility session");
    }

    savePocketBaseCompatStoredSession(data.pb_token, data.user);
    this.syncCompatAuth();
    this.cloudUser = data.cloud_user
      ? portalCloudUserToCloudUser(data.cloud_user)
      : this.cloudUser;
    setStackIdentity(data.stack_identity ?? null);
    clearReprojectionReauthMarker();
  }

  /**
   * Handle OIDC callback (after hosted cloud redirect)
   */
  private async handleOIDCCallback(code: string, state: string): Promise<void> {
    const callbackUrl = new URL(
      "/api/v2/auth/callback",
      window.location.origin,
    );
    callbackUrl.searchParams.set("code", code);
    callbackUrl.searchParams.set("state", state);
    window.location.replace(callbackUrl.toString());
  }

  // ============================================================================
  // Logout
  // ============================================================================

  /**
   * Clear the local auth/session state without navigation.
   */
  async clearSession(): Promise<void> {
    await logoutLocalSession();
    clearTechstackSecuritySessionState();
    clearReprojectionReauthMarker();
    this.pocketbaseUser = null;
    this.cloudUser = null;
    this.v2SessionActive = false;
  }

  /**
   * Logout from all auth providers
   */
  async logout(options?: {
    manualLogin?: boolean;
    redirect?: (url: string) => void;
  }): Promise<void> {
    this.loading = true;

    try {
      const shouldUseV2ProviderLogout = this.deploymentMode !== "self-hosted";

      await this.clearSession();

      const nextPath = options?.manualLogin
        ? getManualLogoutRedirectPath({
            deploymentMode: this.deploymentMode,
            embedded: false,
          })
        : "/login";

      if (browser && shouldUseV2ProviderLogout) {
        (options?.redirect ?? window.location.assign.bind(window.location))(
          buildV2ProviderLogoutPath({
            deploymentMode: this.deploymentMode,
            nextPath,
          }),
        );
        return;
      }

      if (
        browser &&
        resolveLoginExperience({
          deploymentMode: this.deploymentMode,
          embedded: false,
        }) === "saas-auth0"
      ) {
        window.location.assign("/api/v1/auth/logout");
        return;
      }

      // Future: implement back-channel logout with the hosted identity provider.
      await goto(nextPath);
    } finally {
      this.loading = false;
    }
  }

  // ============================================================================
  // Helpers
  // ============================================================================

  /**
   * Reset auth store state
   */
  reset(): void {
    this.initialized = false;
    this.mode = "local";
    this.modeDetected = false;
    this.cloudAuthUrl = null;
    this.v2LoginUrl = null;
    this.hostedProviderIDs.clear();
    this.portalUrl = null;
    this.allowLocalLogin = true;
    this.pocketbaseUser = null;
    this.cloudUser = null;
    this.v2SessionActive = false;
    clearTechstackSecuritySessionState();
    clearReprojectionReauthMarker();
    this.loading = false;
    this.error = null;
  }

  /**
   * Get full auth state (for debugging)
   */
  getState(): AuthState {
    return {
      mode: this.mode,
      modeDetected: this.modeDetected,
      cloudAuthUrl: this.cloudAuthUrl,
      portalUrl: this.portalUrl,
      allowLocalLogin: this.allowLocalLogin,
      pocketbaseUser: this.pocketbaseUser,
      cloudUser: this.cloudUser,
      isAuthenticated: this.isAuthenticated,
      isAdmin: this.isAdmin,
      loading: this.loading,
      error: this.error,
    };
  }
}

// Export singleton instance
export const authStore = new AuthStore();

// ============================================================================
// Convenience Exports
// ============================================================================

/**
 * Initialize auth - call on app mount
 */
export async function initAuth(): Promise<void> {
  return authStore.init();
}

/**
 * Check if user is authenticated
 */
export function isAuthenticated(): boolean {
  return authStore.isAuthenticated;
}

/**
 * Check if user is admin
 */
export function isAdmin(): boolean {
  return authStore.isAdmin;
}

function v2WhoAmIToCloudUser(user: V2WhoAmIResponse): CloudUser {
  const roles = user.role ? [user.role] : [];
  return {
    sub: user.subject,
    orgId: user.orgId,
    tenantId: user.tenantId,
    email: user.email ?? "",
    email_verified: true,
    name: user.email || user.subject,
    provider: user.provider,
    role: user.role,
    roles,
    is_admin: roles.includes("admin") || roles.includes("owner"),
  };
}

function portalCloudUserToCloudUser(user: {
  sub: string;
  email: string;
  name: string;
  is_admin: boolean;
}): CloudUser {
  const roles = user.is_admin ? ["admin"] : [];
  return {
    sub: user.sub,
    email: user.email,
    email_verified: true,
    name: user.name || user.email || user.sub,
    roles,
    is_admin: user.is_admin,
  };
}

function getManualLogoutRedirectPath(options: {
  deploymentMode: DeploymentMode;
  embedded: boolean;
}): string {
  if (browser) {
    try {
      if (hasWindowsLocalClientContext(window.localStorage)) {
        return windowsLocalClientReturnUrl;
      }
    } catch {
      // Storage can be unavailable in restricted browser contexts.
    }
  }

  return getPostLogoutRedirectPath(options);
}
