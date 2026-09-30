<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
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
      tr("ui.authSso.yourKombifyCloudSessionCould");
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
          ? tr("ui.login.couldNotContactBackendWith", { message: e.message })
          : tr("ui.login.couldNotContactBackend");
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
      error = tr("ui.login.kombifyCloudSignInIs");
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
          tr("ui.login.theBootstrapPasswordHasExpired");
        return;
      }
      if (r.status === 404) {
        revealError = tr("ui.login.breakGlassAdminIsNot");
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
      revealError = err instanceof Error ? err.message : tr("ui.login.revealFailed");
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
          ? tr("ui.login.tooManyLoginAttemptsPlease")
          : err instanceof Error
            ? err.message
            : tr("ui.login.emergencyLoginFailed");
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
  <title>{tr("ui.login.signInKombifyTechstack")}</title>
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
          {tr("ui.login.emergencyRecoveryOnlyDayTo")}
        {:else if loginExperience === "self-hosted"}
          {tr("ui.login.signInWithTheLocal")}
        {:else}
          {tr("ui.login.signInWithKombifyCloud")}
        {/if}
      </p>
    </div>

    {#if signedOut}
      <div
        class="rounded-md border border-border bg-muted/40 px-3 py-2 text-sm text-muted-foreground"
      >
        {tr("ui.login.youHaveBeenSignedOut")}
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
      <div class="text-center text-sm text-muted-foreground">{tr("ui.login.loading")}</div>
    {:else if embedded}
      <div class="space-y-4">
        <Button
          variant="primary"
          class="w-full"
          onclick={recoverEmbeddedSession}
        >
          {tr("ui.login.tryAgain")}
        </Button>
        <p class="text-center text-xs leading-5 text-muted-foreground">
          {tr("ui.login.techstackReconnectsThroughTheKombify")}
        </p>
      </div>
    {:else if recovery}
      <div class="space-y-4 rounded-xl border border-border bg-card p-6">
        <h2 class="text-base font-semibold">{tr("ui.login.emergencyAdmin")}</h2>
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
              <span class="text-muted-foreground">{tr("ui.login.emergencyEmail")}</span>
              <code class="ml-2 font-mono">{emergencyEmail}</code>
            </div>
            {#if revealedPwd}
              <div>
                <span class="text-muted-foreground">{tr("ui.login.bootstrapPassword")}</span>
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
              ? tr("ui.login.revealing")
              : bg.has_pending_reveal
                ? tr("ui.login.revealBootstrapPassword")
                : tr("ui.login.useTheStoredEmergencyPassword")}
          </Button>
          <form class="space-y-3" onsubmit={onEmergencyLogin}>
            <label
              class="block space-y-1 text-xs font-medium"
              for="emergency-password"
            >
              {tr("ui.login.emergencyPassword")}
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
              {emergencyBusy ? tr("ui.clientLocal.signingIn") : tr("ui.login.signInAsEmergencyAdmin")}
            </Button>
          </form>
        {:else}
          <p class="text-sm text-muted-foreground">
            {tr("ui.login.emergencyAdminIsNotInitialized")}
          </p>
        {/if}
      </div>
    {:else if backendUnavailable}
      <Button variant="secondary" class="w-full" onclick={retryBackend}>
        {tr("ui.login.retryConnection")}
      </Button>
    {:else}
      <div class="space-y-3">
        {#if autoStartingCloudLogin}
          <div class="text-center text-sm text-muted-foreground">
            {tr("ui.login.redirectingToKombifyCloud")}
          </div>
        {:else}
          {#if localOwnerEnabled}
            <Button
              variant="primary"
              class="w-full"
              onclick={() => goto(localOwnerHref)}
            >
              {tr("ui.login.continueAsLocalOwner")}
            </Button>
          {/if}

          {#if primaryCloudProvider?.auth_url}
            <Button
              variant={localOwnerEnabled ? "secondary" : "primary"}
              class="w-full"
              onclick={startCloudLogin}
            >
              {tr("ui.login.continueWithKombifyCloud")}
            </Button>
            {#if windowsClient && windowsBrowserCloudLoginUrl}
              <a
                data-testid="windows-browser-cloud-login"
                class="flex w-full items-center justify-center rounded-md border border-border bg-background px-4 py-3 text-sm font-medium transition hover:bg-muted/50"
                href={windowsBrowserCloudLoginUrl}
              >
                {tr("ui.login.openKombifyCloudInBrowser")}
              </a>
            {/if}
          {/if}

          {#if !primaryCloudProvider?.auth_url && (isSaasCloudLogin || !localOwnerEnabled)}
            <p class="text-center text-sm text-muted-foreground">
              {tr("ui.login.cloudSignInIsNot")}
            </p>
          {/if}
        {/if}
      </div>
    {/if}
  </div>
</div>
