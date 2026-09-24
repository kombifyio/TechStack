<script lang="ts">
  import { goto } from "$app/navigation";
  import { onMount } from "svelte";
  import { loginWithLocalSession } from "#lib/api/auth.js";
  import { refreshEmbeddedCloudSession } from "#lib/auth/embedded-session.js";
  import {
    buildCloudAuthRedirectURL,
    formatLoginError,
    resolveLoginExperience,
    shouldAutoStartCloudLogin,
  } from "#lib/auth/login-experience.js";
  import {
    hostNavigationRequested,
    withHostNavigation,
  } from "#lib/embedded-navigation.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { selfHostedLoginHref } from "#lib/client/windows-onboarding.js";
  import Button from "#lib/components/ui/Button.svelte";
  import ThemeToggle from "#lib/components/ui/ThemeToggle.svelte";
  import TechstackBrandLogo from "#lib/components/TechstackBrandLogo.svelte";

  interface ProviderInfo {
    id: string;
    kind: string;
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

  let methods = $state<MethodsResponse | null>(null);
  let loading = $state(true);
  let error = $state("");
  let signedOut = $state(false);
  let embedded = $state(false);
  let hostNavigation = $state(false);
  let manualLogin = $state(false);
  let autoStartingCloudLogin = $state(false);
  let windowsClient = $state(false);
  let recovery = $state(false);
  let backendUnavailable = $state(false);

  let emergencyPassword = $state("");
  let emergencyBusy = $state(false);
  let revealedPwd = $state<RevealResponse | null>(null);
  let revealError = $state("");
  let revealBusy = $state(false);

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
  const localOwnerHref = $derived(
    selfHostedLoginHref({
      windowsClient,
      storage: typeof window === "undefined" ? null : window.localStorage,
    }),
  );

  onMount(async () => {
    const params = new URLSearchParams(window.location.search);
    embedded = window.parent !== window;
    hostNavigation = embedded && hostNavigationRequested(params);
    signedOut = params.get("logged_out") === "1";
    manualLogin = params.get("manual") === "1" || signedOut;
    windowsClient = params.get("client") === "windows";
    recovery = params.get("recovery") === "1";
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
      if (
        signedOut &&
        (authStore.isAuthenticated || authStore.v2SessionActive)
      ) {
        await authStore.clearSession();
      } else if (authStore.isAuthenticated || authStore.v2SessionActive) {
        await goto(
          hostNavigation ? withHostNavigation("/dashboard") : "/dashboard",
        );
        return;
      }
    } catch {
      /* fall through */
    }

    await loadMethods();
    loading = false;

    if (!recovery && windowsClient && authStore.deploymentMode !== "saas") {
      await goto(localOwnerHref);
      return;
    }

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
      await goto(
        hostNavigation ? withHostNavigation("/dashboard") : "/dashboard",
      );
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
      backendUnavailable = false;
    } catch (e) {
      backendUnavailable = true;
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
      returnTo: "/dashboard",
    });
  }

  function buildWindowsBrowserCloudLoginUrl(target: string): string {
    const url = new URL(
      buildCloudAuthRedirectURL(target, {
        returnTo: "/dashboard",
      }),
    );
    url.searchParams.set("client", "windows");
    url.searchParams.set("open_browser", "1");
    return url.toString();
  }

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
      await goto(
        hostNavigation ? withHostNavigation("/dashboard") : "/dashboard",
      );
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

  async function retryBackend() {
    error = "";
    loading = true;
    await loadMethods();
    try {
      await authStore.init({ embedded: false });
    } catch {
      /* keep current error */
    }
    loading = false;
  }
</script>

<svelte:head>
  <title>Sign in | kombify Techstack</title>
</svelte:head>

<div
  data-kx="app"
  class="relative flex min-h-screen items-center justify-center p-4"
>
  <div class="absolute top-4 right-4">
    <ThemeToggle />
  </div>

  <div class="w-full max-w-md space-y-6">
    <div class="space-y-2 text-center">
      <div class="flex h-14 w-full items-center justify-center px-4">
        <TechstackBrandLogo
          sizes="256px"
          class="h-12 w-auto max-w-full object-contain"
        />
      </div>
      <h1 class="text-3xl font-semibold">kombify Techstack</h1>
      <p class="text-sm text-muted-foreground">
        {#if recovery}
          Emergency recovery only. Day-to-day sign-in stays on kombify Cloud or
          the local owner path.
        {:else if loginExperience === "self-hosted"}
          Sign in with the local owner account for this Techstack.
        {:else}
          Sign in with kombify Cloud.
        {/if}
      </p>
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
        <Button
          variant="primary"
          class="w-full"
          onclick={recoverEmbeddedSession}
        >
          Try again
        </Button>
        <p class="text-center text-xs leading-5 text-muted-foreground">
          Techstack reconnects through the kombify Cloud page that contains this
          view. It will not open a second sign-in page inside this frame.
        </p>
      </div>
    {:else if recovery}
      <div class="space-y-4 rounded-xl border border-border bg-card p-6">
        <h2 class="text-base font-semibold">Emergency admin</h2>
        {#if bg?.initialized}
          {#if revealError}
            <div
              class="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              {revealError}
            </div>
          {/if}
          <div class="space-y-2 text-xs">
            <div>
              <span class="text-muted-foreground">Emergency email:</span>
              <code class="ml-2 font-mono">{emergencyEmail}</code>
            </div>
            {#if revealedPwd}
              <div>
                <span class="text-muted-foreground">Bootstrap password:</span>
                <code class="ml-2 font-mono break-all"
                  >{revealedPwd.password}</code
                >
              </div>
            {/if}
          </div>
          <Button
            variant="secondary"
            class="w-full"
            disabled={revealBusy || !bg.has_pending_reveal}
            onclick={onRevealEmergency}
          >
            {revealBusy
              ? "Revealing..."
              : bg.has_pending_reveal
                ? "Reveal bootstrap password"
                : "Use the stored emergency password"}
          </Button>
          <form class="space-y-3" onsubmit={onEmergencyLogin}>
            <label
              class="block space-y-1 text-xs font-medium"
              for="emergency-password"
            >
              Emergency password
              <input
                id="emergency-password"
                type="password"
                required
                autocomplete="current-password"
                bind:value={emergencyPassword}
                class="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
              />
            </label>
            <Button
              type="submit"
              variant="primary"
              class="w-full"
              disabled={emergencyBusy}
            >
              {emergencyBusy ? "Signing in..." : "Sign in as emergency admin"}
            </Button>
          </form>
        {:else}
          <p class="text-sm text-muted-foreground">
            Emergency admin is not initialized yet.
          </p>
        {/if}
      </div>
    {:else if backendUnavailable}
      <Button variant="secondary" class="w-full" onclick={retryBackend}>
        Retry connection
      </Button>
    {:else}
      <div class="space-y-3">
        {#if autoStartingCloudLogin}
          <div class="text-center text-sm text-muted-foreground">
            Redirecting to kombify Cloud...
          </div>
        {:else}
          {#if localOwnerEnabled}
            <Button
              variant="primary"
              class="w-full"
              onclick={() => goto(localOwnerHref)}
            >
              Continue as local owner
            </Button>
          {/if}

          {#if primaryCloudProvider?.auth_url}
            <Button
              variant={localOwnerEnabled ? "secondary" : "primary"}
              class="w-full"
              onclick={startCloudLogin}
            >
              Continue with kombify Cloud
            </Button>
            {#if windowsClient && windowsBrowserCloudLoginUrl}
              <a
                data-testid="windows-browser-cloud-login"
                class="flex w-full items-center justify-center rounded-md border border-border bg-background px-4 py-3 text-sm font-medium transition hover:bg-muted/50"
                href={windowsBrowserCloudLoginUrl}
              >
                Open kombify Cloud in browser
              </a>
            {/if}
          {/if}

          {#if !primaryCloudProvider?.auth_url && (isSaasCloudLogin || !localOwnerEnabled)}
            <p class="text-center text-sm text-muted-foreground">
              Cloud sign-in is not configured yet.
            </p>
          {/if}
        {/if}
      </div>
    {/if}
  </div>
</div>
