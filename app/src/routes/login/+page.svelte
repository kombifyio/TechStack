<script lang="ts">
  import { goto } from "$app/navigation";
  import { onMount } from "svelte";
  import { parseApiError } from "$lib/api/errors";
  import { loginWithLocalSession } from "$lib/api/auth";
  import { refreshEmbeddedCloudSession } from "$lib/auth/embedded-session";
  import { createLocalOwnerAccount } from "$lib/auth/local-owner";
  import {
    buildCloudAuthRedirectURL,
    formatLoginError,
    resolveLoginExperience,
    shouldAutoStartCloudLogin,
  } from "$lib/auth/login-experience";
  import { authStore } from "$lib/stores/auth.svelte";

  // ----- Backend payload shapes (GET /api/v1/auth/methods) -----
  interface ProviderInfo {
    id: string;
    kind: string; // "oidc" | "auth0" | "pocketid" | "generic" | "breakglass"
    label: string;
    auth_url?: string;
  }
  interface BreakGlassStatus {
    initialized: boolean;
    claimed: boolean;
    email: string;
    has_pending_reveal: boolean;
    reveal_expires_at?: string | null;
    locked: boolean;
  }
  interface MethodsResponse {
    providers: ProviderInfo[];
    breakglass: BreakGlassStatus;
  }
  interface RevealResponse {
    email: string;
    password: string;
    expires_at: string;
    claimed: boolean;
  }

  // ----- UI state -----
  let methods = $state<MethodsResponse | null>(null);
  let loading = $state(true);
  let error = $state("");
  let signedOut = $state(false);
  let embedded = $state(false);
  let manualLogin = $state(false);
  let autoStartingCloudLogin = $state(false);
  let windowsClient = $state(false);
  let mode = $state<"signin" | "signup">("signin");

  // Local owner sign-in form
  let ownerLoginEmail = $state("");
  let ownerLoginPassword = $state("");
  let ownerLoginBusy = $state(false);

  // Local owner sign-up form
  let ownerSignupEmail = $state("");
  let ownerSignupPassword = $state("");
  let ownerSignupPasswordConfirm = $state("");
  let ownerSignupBusy = $state(false);

  // Emergency admin form
  let emergencyPassword = $state("");
  let emergencyBusy = $state(false);
  let revealedPwd = $state<RevealResponse | null>(null);
  let revealError = $state("");
  let revealBusy = $state(false);

  // ----- Derived -----
  const cloudProviders = $derived(
    (methods?.providers ?? []).filter(
      (p) => p.id !== "breakglass" && p.kind !== "local" && !!p.auth_url,
    ),
  );
  const primaryCloudProvider = $derived<ProviderInfo | null>(
    cloudProviders[0] ??
      (authStore.cloudAuthUrl
        ? {
            id: "primary",
            kind: "auth0",
            label: "kombify Cloud",
            auth_url: authStore.cloudAuthUrl,
          }
        : null),
  );
  const loginExperience = $derived(
    resolveLoginExperience({
      deploymentMode: authStore.deploymentMode,
      embedded,
    }),
  );
  const isSaasCloudLogin = $derived(loginExperience === "saas-auth0");
  const bg = $derived(methods?.breakglass);
  const localOwnerEnabled = $derived(
    authStore.deploymentMode !== "saas" || authStore.allowLocalLogin,
  );
  const emergencyEmail = $derived(
    revealedPwd?.email || bg?.email || "breakglass@techstack.local",
  );
  const windowsBrowserCloudLoginUrl = $derived(
    primaryCloudProvider?.auth_url
      ? buildWindowsBrowserCloudLoginUrl(primaryCloudProvider.auth_url)
      : "",
  );

  // ----- Lifecycle -----
  onMount(async () => {
    embedded = window.parent !== window;
    const params = new URLSearchParams(window.location.search);
    const requestedMode = params.get("mode") ?? params.get("tab");
    signedOut = params.get("logged_out") === "1";
    manualLogin = params.get("manual") === "1" || signedOut;
    windowsClient = params.get("client") === "windows";
    const errParam = params.get("error");
    if (errParam) {
      error = formatLoginError(errParam);
      params.delete("error");
      const next = params.toString();
      window.history.replaceState({}, "", next ? `/login?${next}` : "/login");
    }

    if (embedded) {
      await recoverEmbeddedSession();
      return;
    }

    try {
      await authStore.init({ embedded: false });
      if (authStore.isAuthenticated || authStore.v2SessionActive) {
        await goto("/stacks");
        return;
      }
    } catch {
      /* fall through */
    }

    await loadMethods();

    mode = requestedMode === "signup" ? "signup" : "signin";

    loading = false;

    if (
      shouldAutoStartCloudLogin({
        deploymentMode: authStore.deploymentMode,
        embedded,
        cloudAuthUrl: primaryCloudProvider?.auth_url ?? null,
        hasError: Boolean(error),
        manualLogin,
      })
    ) {
      startCloudLogin();
    }
  });

  async function recoverEmbeddedSession(): Promise<void> {
    loading = true;
    error = "";
    const refreshed = await refreshEmbeddedCloudSession();
    if (refreshed) {
      await goto("/stacks");
      return;
    }

    error =
      "Your kombify Cloud session could not be restored. Please try again.";
    loading = false;
  }

  async function loadMethods() {
    try {
      const r = await fetch("/api/v1/auth/methods", {
        credentials: "include",
      });
      if (!r.ok) {
        throw new Error(`HTTP ${r.status}`);
      }
      methods = (await r.json()) as MethodsResponse;
    } catch (e) {
      error =
        e instanceof Error
          ? `Could not contact backend: ${e.message}`
          : "Could not contact backend.";
      methods = {
        providers: [],
        breakglass: {
          initialized: false,
          claimed: false,
          email: "",
          has_pending_reveal: false,
          reveal_expires_at: null,
          locked: false,
        },
      };
    }
  }

  async function safeJson(
    response: Response,
  ): Promise<{ error?: { message?: string } } | null> {
    try {
      return (await response.json()) as { error?: { message?: string } };
    } catch {
      return null;
    }
  }

  // ----- Cloud / OIDC -----
  function startCloudLogin() {
    if (embedded) {
      void recoverEmbeddedSession();
      return;
    }

    const provider = primaryCloudProvider;
    if (!provider?.auth_url) {
      error = "kombify Cloud sign-in is not available right now.";
      return;
    }
    autoStartingCloudLogin = true;
    window.location.href = buildCloudAuthRedirectURL(provider.auth_url, {
      returnTo: "/stacks",
    });
  }

  function buildWindowsBrowserCloudLoginUrl(target: string): string {
    const url = new URL(
      buildCloudAuthRedirectURL(target, {
        returnTo: "/stacks",
      }),
    );
    url.searchParams.set("client", "windows");
    url.searchParams.set("open_browser", "1");
    return url.toString();
  }

  function onCloudProviderClick(provider: ProviderInfo) {
    if (!provider.auth_url) {
      error = `${provider.label} is not configured for redirect login.`;
      return;
    }
    window.location.href = buildCloudAuthRedirectURL(provider.auth_url, {
      returnTo: "/stacks",
    });
  }

  // ----- Sign in (local owner / PocketBase) -----
  async function onOwnerLogin(e: Event) {
    e.preventDefault();
    error = "";
    ownerLoginBusy = true;
    try {
      const ok = await authStore.loginWithPassword(
        ownerLoginEmail.trim(),
        ownerLoginPassword,
      );
      if (!ok) {
        error = authStore.error || "Invalid email or password.";
        return;
      }
      await goto("/stacks");
    } catch (err) {
      error = err instanceof Error ? err.message : "Login failed.";
    } finally {
      ownerLoginBusy = false;
    }
  }

  // ----- Sign up (local owner / PocketBase) -----
  async function onOwnerSignup(e: Event) {
    e.preventDefault();
    error = "";

    if (ownerSignupPassword.length < 8) {
      error = "Password must be at least 8 characters.";
      return;
    }
    if (ownerSignupPassword !== ownerSignupPasswordConfirm) {
      error = "Passwords do not match.";
      return;
    }

    ownerSignupBusy = true;
    try {
      const email = await createLocalOwnerAccount({
        email: ownerSignupEmail,
        password: ownerSignupPassword,
        passwordConfirm: ownerSignupPasswordConfirm,
        isFirstRun: authStore.isFirstRun,
      });

      const ok = await authStore.loginWithPassword(email, ownerSignupPassword);
      if (!ok) {
        error = authStore.error || "Account created, but sign-in failed.";
        return;
      }

      await goto("/stacks");
    } catch (err) {
      const parsed = parseApiError(err);
      error =
        parsed.fieldErrors.email?.message ||
        parsed.fieldErrors.password?.message ||
        parsed.message ||
        "Local account sign-up failed.";
    } finally {
      ownerSignupBusy = false;
    }
  }

  // ----- Emergency admin recovery -----
  async function onRevealEmergency() {
    revealError = "";
    revealBusy = true;
    try {
      const r = await fetch("/api/v1/auth/breakglass/reveal", {
        credentials: "include",
      });
      if (r.status === 410) {
        revealError =
          "The bootstrap password has expired. Restart the server to generate a new one.";
        return;
      }
      if (r.status === 404) {
        revealError = "Break-glass admin is not initialized yet.";
        return;
      }
      if (!r.ok) {
        const data = await safeJson(r);
        revealError = data?.error?.message || `HTTP ${r.status}`;
        return;
      }
      revealedPwd = (await r.json()) as RevealResponse;
      emergencyPassword = revealedPwd.password;
    } catch (err) {
      revealError = err instanceof Error ? err.message : "Reveal failed.";
    } finally {
      revealBusy = false;
    }
  }

  async function onEmergencyLogin(e: Event) {
    e.preventDefault();
    error = "";
    emergencyBusy = true;
    try {
      await loginWithLocalSession(emergencyEmail, emergencyPassword);
      await authStore.init();
      await goto("/stacks");
    } catch (err) {
      const status =
        err && typeof err === "object" && "status" in err
          ? Number((err as { status?: number }).status)
          : 0;
      error =
        status === 429
          ? "Too many login attempts. Please wait and try again."
          : err instanceof Error
            ? err.message
            : "Emergency login failed.";
    } finally {
      emergencyBusy = false;
    }
  }
</script>

<svelte:head>
  <title>Sign in | kombify TechStack</title>
</svelte:head>

<div class="min-h-screen flex items-center justify-center p-4 bg-background">
  <div
    class={isSaasCloudLogin
      ? "w-full max-w-md space-y-6"
      : "w-full max-w-3xl space-y-6"}
  >
    <div class="text-center space-y-2">
      <img
        src="/kombify-techstack-kombi3d.png"
        alt="kombify TechStack"
        class="mx-auto h-16 w-auto max-w-full object-contain"
      />
      <h1 class="text-3xl font-semibold">kombify TechStack</h1>
      {#if isSaasCloudLogin}
        <p class="text-sm text-muted-foreground">Sign in with kombify Cloud.</p>
      {:else}
        <p class="text-sm text-muted-foreground">
          Sign in or create your owner account. Emergency admin access stays
          separate for recovery only.
        </p>
      {/if}
    </div>

    {#if signedOut}
      <div
        class="rounded-md border border-border bg-muted/40 px-3 py-2 text-sm text-muted-foreground"
      >
        You have been signed out.
      </div>
    {/if}

    {#if error}
      <div
        role="alert"
        class="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        {error}
      </div>
    {/if}

    {#if loading}
      <div class="text-center text-sm text-muted-foreground">Loading...</div>
    {:else if embedded}
      <div class="space-y-4">
        <button
          type="button"
          class="w-full rounded-md bg-primary px-4 py-3 text-sm font-medium text-primary-foreground transition hover:bg-primary/90"
          onclick={recoverEmbeddedSession}
        >
          Try again
        </button>
        <p class="text-center text-xs leading-5 text-muted-foreground">
          Techstack reconnects through the kombify Cloud page that contains this
          view. It will not open a second sign-in page inside this frame.
        </p>
      </div>
    {:else if isSaasCloudLogin}
      <div class="space-y-4">
        {#if autoStartingCloudLogin}
          <div class="text-center text-sm text-muted-foreground">
            Redirecting to kombify Cloud...
          </div>
        {:else}
          <button
            type="button"
            class="w-full rounded-md bg-primary px-4 py-3 text-sm font-medium text-primary-foreground transition hover:bg-primary/90 disabled:opacity-50"
            disabled={!primaryCloudProvider?.auth_url}
            onclick={startCloudLogin}
          >
            Continue with kombify Cloud
          </button>
          {#if windowsClient && windowsBrowserCloudLoginUrl}
            <a
              data-testid="windows-browser-cloud-login"
              class="flex w-full items-center justify-center rounded-md border border-border bg-background px-4 py-3 text-sm font-medium transition hover:bg-muted/50"
              href={windowsBrowserCloudLoginUrl}
            >
              Open kombify Cloud in browser
            </a>
            <p class="text-center text-xs leading-5 text-muted-foreground">
              Use this when your password manager or passkeys work better in
              your default browser.
            </p>
          {/if}
        {/if}
      </div>
    {:else}
      <div
        class="rounded-xl border border-border bg-card p-6 shadow-lg space-y-6"
      >
        <div
          role="tablist"
          aria-label="Authentication entry mode"
          class="grid grid-cols-2 rounded-md border border-border bg-muted/30 p-0.5 text-sm"
        >
          <button
            type="button"
            role="tab"
            aria-selected={mode === "signin"}
            class={mode === "signin"
              ? "rounded-[5px] bg-background px-3 py-2 font-medium shadow-sm"
              : "rounded-[5px] px-3 py-2 font-medium text-muted-foreground transition hover:text-foreground"}
            onclick={() => (mode = "signin")}
          >
            Sign In
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={mode === "signup"}
            class={mode === "signup"
              ? "rounded-[5px] bg-background px-3 py-2 font-medium shadow-sm"
              : "rounded-[5px] px-3 py-2 font-medium text-muted-foreground transition hover:text-foreground"}
            onclick={() => (mode = "signup")}
          >
            Sign Up
          </button>
        </div>

        {#if mode === "signin"}
          <div class="grid gap-6 lg:grid-cols-[1.2fr,0.8fr]">
            <section class="space-y-4">
              <div class="space-y-1">
                <h2 class="text-base font-semibold">Owner sign-in</h2>
                <p class="text-sm text-muted-foreground">
                  Use kombify Cloud or a managed local owner account.
                </p>
              </div>

              <div
                class="rounded-lg border border-border bg-muted/20 p-4 space-y-3"
              >
                <div class="flex items-center justify-between gap-3">
                  <h3 class="text-sm font-medium">kombify Cloud</h3>
                  <span
                    class="text-[10px] uppercase tracking-wider text-muted-foreground"
                  >
                    Preferred
                  </span>
                </div>

                {#if cloudProviders.length > 0}
                  <div class="space-y-2">
                    {#each cloudProviders as provider (provider.id)}
                      <button
                        type="button"
                        class="w-full rounded-md border border-border bg-background px-4 py-2.5 text-sm font-medium transition hover:bg-muted/50"
                        onclick={() => onCloudProviderClick(provider)}
                      >
                        Sign in with {provider.label}
                      </button>
                    {/each}
                  </div>
                {:else}
                  <div
                    class="rounded-md border border-dashed border-border bg-background px-3 py-2.5 text-sm text-muted-foreground"
                  >
                    Cloud sign-in is not configured yet.
                  </div>
                {/if}
              </div>

              <div
                class="rounded-lg border border-border bg-muted/20 p-4 space-y-3"
              >
                <div class="flex items-center justify-between gap-3">
                  <h3 class="text-sm font-medium">Local owner account</h3>
                  <span
                    class="text-[10px] uppercase tracking-wider text-muted-foreground"
                  >
                    PocketBase
                  </span>
                </div>

                {#if localOwnerEnabled}
                  <form class="space-y-3" onsubmit={onOwnerLogin}>
                    <div class="space-y-1">
                      <label
                        class="text-xs font-medium"
                        for="owner-login-email"
                      >
                        Email
                      </label>
                      <input
                        id="owner-login-email"
                        type="email"
                        required
                        autocomplete="email"
                        bind:value={ownerLoginEmail}
                        class="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                        placeholder="owner@example.com"
                      />
                    </div>
                    <div class="space-y-1">
                      <label
                        class="text-xs font-medium"
                        for="owner-login-password"
                      >
                        Password
                      </label>
                      <input
                        id="owner-login-password"
                        type="password"
                        required
                        autocomplete="current-password"
                        bind:value={ownerLoginPassword}
                        class="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                      />
                    </div>
                    <button
                      type="submit"
                      disabled={ownerLoginBusy}
                      class="w-full rounded-md bg-primary px-4 py-2.5 text-sm font-medium text-primary-foreground disabled:opacity-50"
                    >
                      {ownerLoginBusy ? "Signing in..." : "Sign in locally"}
                    </button>
                  </form>
                {:else}
                  <p class="text-sm text-muted-foreground">
                    Local owner sign-in is disabled for this deployment. Use
                    kombify Cloud instead.
                  </p>
                {/if}
              </div>
            </section>

            <section
              class="rounded-lg border border-amber-500/30 bg-amber-500/5 p-4 space-y-4"
            >
              <div class="flex items-start justify-between gap-3">
                <div class="space-y-1">
                  <h2 class="text-base font-semibold">Emergency admin</h2>
                  <p class="text-sm text-muted-foreground">
                    Recovery only. This bypasses owner and user management.
                  </p>
                </div>
                <span
                  class="text-[10px] uppercase tracking-wider text-amber-700"
                >
                  Recovery
                </span>
              </div>

              {#if bg?.initialized}
                {#if revealError}
                  <div
                    class="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
                  >
                    {revealError}
                  </div>
                {/if}

                <div
                  class="rounded-md border border-border bg-background px-3 py-3 text-xs space-y-2"
                >
                  <div>
                    <span class="text-muted-foreground">Emergency email:</span>
                    <code class="ml-2 font-mono">{emergencyEmail}</code>
                  </div>
                  {#if revealedPwd}
                    <div>
                      <span class="text-muted-foreground"
                        >Bootstrap password:</span
                      >
                      <code class="ml-2 font-mono break-all"
                        >{revealedPwd.password}</code
                      >
                    </div>
                    <div class="text-muted-foreground">
                      Visible until {new Date(
                        revealedPwd.expires_at,
                      ).toLocaleString()}
                    </div>
                  {/if}
                </div>

                <button
                  type="button"
                  disabled={revealBusy || !bg.has_pending_reveal}
                  class="w-full rounded-md border border-amber-500/30 bg-background px-4 py-2.5 text-sm font-medium disabled:opacity-50"
                  onclick={onRevealEmergency}
                >
                  {revealBusy
                    ? "Revealing..."
                    : bg.has_pending_reveal
                      ? "Reveal bootstrap password"
                      : "Use the stored emergency password"}
                </button>

                {#if !bg.has_pending_reveal && !revealedPwd}
                  <p class="text-xs text-muted-foreground">
                    No temporary bootstrap password can be revealed right now.
                    Use the emergency password you already stored for recovery.
                  </p>
                {/if}

                <form class="space-y-3" onsubmit={onEmergencyLogin}>
                  <div class="space-y-1">
                    <label class="text-xs font-medium" for="emergency-password">
                      Emergency password
                    </label>
                    <input
                      id="emergency-password"
                      type="password"
                      required
                      autocomplete="current-password"
                      bind:value={emergencyPassword}
                      class="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                    />
                  </div>
                  <button
                    type="submit"
                    disabled={emergencyBusy}
                    class="w-full rounded-md bg-foreground px-4 py-2.5 text-sm font-medium text-background disabled:opacity-50"
                  >
                    {emergencyBusy
                      ? "Signing in..."
                      : "Sign in as emergency admin"}
                  </button>
                </form>
              {:else}
                <p class="text-sm text-muted-foreground">
                  Emergency admin is not initialized yet.
                </p>
              {/if}
            </section>
          </div>
        {:else}
          <div class="grid gap-6 lg:grid-cols-[1.2fr,0.8fr]">
            <section class="space-y-4">
              <div class="space-y-1">
                <h2 class="text-base font-semibold">Create owner account</h2>
                <p class="text-sm text-muted-foreground">
                  Choose whether the owner should live in kombify Cloud or as a
                  local managed user.
                </p>
              </div>

              <div
                class="rounded-lg border border-border bg-muted/20 p-4 space-y-3"
              >
                <div class="flex items-center justify-between gap-3">
                  <h3 class="text-sm font-medium">kombify Cloud</h3>
                  <span
                    class="text-[10px] uppercase tracking-wider text-muted-foreground"
                  >
                    Managed identity
                  </span>
                </div>
                <p class="text-sm text-muted-foreground">
                  Use kombify Cloud when the owner and follow-up users should be
                  managed through the hosted identity system.
                </p>

                {#if cloudProviders.length > 0}
                  <div class="space-y-2">
                    {#each cloudProviders as provider (provider.id)}
                      <button
                        type="button"
                        class="w-full rounded-md border border-border bg-background px-4 py-2.5 text-sm font-medium transition hover:bg-muted/50"
                        onclick={() => onCloudProviderClick(provider)}
                      >
                        Create owner with {provider.label}
                      </button>
                    {/each}
                  </div>
                {:else}
                  <div
                    class="rounded-md border border-dashed border-border bg-background px-3 py-2.5 text-sm text-muted-foreground"
                  >
                    Cloud sign-up is not configured yet.
                  </div>
                {/if}
              </div>

              <div
                class="rounded-lg border border-border bg-muted/20 p-4 space-y-3"
              >
                <div class="flex items-center justify-between gap-3">
                  <h3 class="text-sm font-medium">Local owner account</h3>
                  <span
                    class="text-[10px] uppercase tracking-wider text-muted-foreground"
                  >
                    PocketBase
                  </span>
                </div>
                <p class="text-sm text-muted-foreground">
                  Use this when the owner and additional users should be managed
                  locally by PocketBase or Pocket ID.
                </p>

                {#if localOwnerEnabled}
                  <form class="space-y-3" onsubmit={onOwnerSignup}>
                    <div class="space-y-1">
                      <label
                        class="text-xs font-medium"
                        for="owner-signup-email"
                      >
                        Owner email
                      </label>
                      <input
                        id="owner-signup-email"
                        type="email"
                        required
                        autocomplete="email"
                        bind:value={ownerSignupEmail}
                        class="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                        placeholder="owner@example.com"
                      />
                    </div>
                    <div class="space-y-1">
                      <label
                        class="text-xs font-medium"
                        for="owner-signup-password"
                      >
                        Password
                      </label>
                      <input
                        id="owner-signup-password"
                        type="password"
                        required
                        minlength={8}
                        autocomplete="new-password"
                        bind:value={ownerSignupPassword}
                        class="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                      />
                    </div>
                    <div class="space-y-1">
                      <label
                        class="text-xs font-medium"
                        for="owner-signup-password-confirm"
                      >
                        Confirm password
                      </label>
                      <input
                        id="owner-signup-password-confirm"
                        type="password"
                        required
                        minlength={8}
                        autocomplete="new-password"
                        bind:value={ownerSignupPasswordConfirm}
                        class="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                      />
                    </div>
                    <button
                      type="submit"
                      disabled={ownerSignupBusy}
                      class="w-full rounded-md bg-primary px-4 py-2.5 text-sm font-medium text-primary-foreground disabled:opacity-50"
                    >
                      {ownerSignupBusy
                        ? "Creating account..."
                        : "Create local owner account"}
                    </button>
                  </form>
                {:else}
                  <p class="text-sm text-muted-foreground">
                    Local owner sign-up is disabled for this deployment. Use
                    kombify Cloud instead.
                  </p>
                {/if}
              </div>
            </section>

            <section
              class="rounded-lg border border-border bg-muted/20 p-4 space-y-3"
            >
              <div class="space-y-1">
                <h2 class="text-base font-semibold">Separate recovery path</h2>
                <p class="text-sm text-muted-foreground">
                  Emergency admin access is intentionally not the owner sign-up
                  flow. Use the Sign In tab when you need recovery access
                  without user management.
                </p>
              </div>

              {#if bg?.initialized}
                <div
                  class="rounded-md border border-border bg-background px-3 py-3 text-xs space-y-1"
                >
                  <p class="font-medium text-foreground">Recovery account</p>
                  <p class="text-muted-foreground">
                    Emergency email:
                    <code class="ml-2 font-mono"
                      >{bg.email || "breakglass@techstack.local"}</code
                    >
                  </p>
                </div>
              {/if}

              <p class="text-sm text-muted-foreground">
                Owner accounts are for day-to-day access. Emergency admin stays
                a last-resort credential outside that user-management path.
              </p>
            </section>
          </div>
        {/if}
      </div>

      <p class="text-center text-xs text-muted-foreground">
        Recovery and bootstrap details live in
        <a class="underline" href="/docs/SELFHOSTED-AUTH">SELFHOSTED-AUTH.md</a
        >.
      </p>
    {/if}
  </div>
</div>
