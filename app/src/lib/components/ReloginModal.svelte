<script lang="ts">
  import { authHandler } from "$lib/stores/authHandler.svelte";
  import { authStore } from "$lib/stores/auth.svelte";
  import { isSaaSEmbedded } from "$lib/stores/deploymentMode";
  import { resolveLoginExperience } from "$lib/auth/login-experience";
  import { refreshEmbeddedCloudSession } from "$lib/auth/embedded-session";
  import { getPortalOrigin } from "$lib/stores/postMessageBridge";
  import { tr } from "$lib/i18n.svelte";

  // Form state
  let email = $state("");
  let password = $state("");
  let error = $state<string | null>(null);
  let isLoading = $state(false);
  let embeddedRefreshFailed = $state(false);
  const loginExperience = $derived(
    resolveLoginExperience({
      deploymentMode: authStore.deploymentMode,
      embedded: $isSaaSEmbedded,
    }),
  );

  // Pre-fill email from current user if available
  $effect(() => {
    if (authHandler.showReloginModal && authStore.userEmail) {
      email = authStore.userEmail;
    }
  });

  async function handleSubmit(e: Event) {
    e.preventDefault();
    error = null;
    isLoading = true;

    try {
      const ok = await authStore.loginWithPassword(email, password);
      if (!ok) throw new Error(authStore.error || "Login failed");
      await authHandler.onReloginSuccess();
    } catch (err) {
      error = err instanceof Error ? err.message : "Login failed";
    } finally {
      isLoading = false;
    }
  }

  function handleCancel() {
    authHandler.onReloginCancel();
  }

  async function handleCloudRelogin() {
    error = null;
    embeddedRefreshFailed = false;
    if ($isSaaSEmbedded) {
      isLoading = true;
      try {
        const refreshed = await refreshEmbeddedCloudSession();
        if (!refreshed) {
          embeddedRefreshFailed = true;
          error = tr("auth.session.expired.embeddedRefreshFailed");
          return;
        }
        await authHandler.onReloginSuccess();
      } finally {
        isLoading = false;
      }
      return;
    }
    authStore.initiateCloudLogin();
  }

  function reopenPortal() {
    // The tool iframe cannot navigate the top frame, but its sandbox grants
    // allow-popups-to-escape-sandbox. Open the parent portal top-level so the
    // user can revive the dead kombify Cloud session, then retry the in-frame
    // refresh via "Retry".
    const origin = getPortalOrigin() ?? "https://kombify.io";
    window.open(origin, "_blank", "noopener,noreferrer");
  }
</script>

{#if authHandler.showReloginModal}
  <section
    class="mx-auto w-full max-w-7xl px-4 pt-4 sm:px-6"
    role="status"
    aria-live="polite"
    aria-labelledby="relogin-title"
    data-testid="session-renewal-banner"
  >
    <div class="card border-warning/40 bg-warning/5 shadow-lg">
      <!-- Header -->
      <div class="p-4 border-b border-warning/20 sm:p-5">
        <div class="flex items-center gap-3">
          <div
            class="w-10 h-10 rounded-lg bg-warning/10 flex items-center justify-center"
          >
            <svg
              class="w-5 h-5 text-warning"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
              />
            </svg>
          </div>
          <div>
            <h2
              id="relogin-title"
              class="text-lg font-semibold text-foreground"
            >
              {tr("auth.session.expired.title")}
            </h2>
            <p class="text-sm text-muted-foreground">
              {tr("auth.session.expired.subtitle")}
            </p>
          </div>
        </div>
      </div>

      <!-- Body -->
      <div class="p-4 sm:p-5">
        {#if authHandler.errorMessage}
          <div
            class="mb-4 p-3 rounded-lg bg-warning/10 border border-warning/30 text-foreground text-sm"
          >
            {tr(authHandler.errorMessage)}
          </div>
        {/if}

        {#if error}
          <div
            class="mb-4 p-3 rounded-lg bg-destructive/10 border border-destructive/30 text-foreground text-sm"
          >
            {error}
          </div>
        {/if}

        {#if loginExperience === "saas-auth0"}
          <div class="space-y-4">
            <p class="text-sm text-muted-foreground">
              {tr("auth.session.expired.saasBody")}
            </p>
            {#if embeddedRefreshFailed}
              <p class="text-sm text-muted-foreground">
                {tr("auth.session.expired.embeddedHint")}
              </p>
            {/if}

            <div class="flex gap-3 pt-2">
              <button
                type="button"
                onclick={handleCloudRelogin}
                disabled={isLoading}
                class="btn btn-primary flex-1"
              >
                {#if isLoading}
                  {tr("auth.session.expired.reconnecting")}
                {:else if embeddedRefreshFailed}
                  {tr("auth.session.renewal.retry")}
                {:else}
                  {tr("auth.session.expired.continueAuth0")}
                {/if}
              </button>
              <button
                type="button"
                onclick={handleCancel}
                disabled={isLoading}
                class="btn btn-secondary"
              >
                {tr("auth.session.expired.signOut")}
              </button>
            </div>

            {#if embeddedRefreshFailed}
              <button
                type="button"
                onclick={reopenPortal}
                class="btn btn-secondary w-full"
              >
                {tr("auth.session.expired.reopenPortal")}
              </button>
            {/if}
          </div>
        {:else}
          <form onsubmit={handleSubmit} class="space-y-4">
            <div>
              <label
                for="relogin-email"
                class="block text-sm font-medium text-foreground mb-1"
              >
                E-Mail
              </label>
              <input
                id="relogin-email"
                type="email"
                bind:value={email}
                required
                class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent"
                placeholder="admin@example.com"
              />
            </div>

            <div>
              <label
                for="relogin-password"
                class="block text-sm font-medium text-foreground mb-1"
              >
                Password
              </label>
              <input
                id="relogin-password"
                type="password"
                bind:value={password}
                required
                class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent"
                placeholder="••••••••"
              />
            </div>

            <div class="flex gap-3 pt-2">
              <button
                type="submit"
                disabled={isLoading}
                class="btn btn-primary flex-1"
              >
                {#if isLoading}
                  <span class="flex items-center justify-center gap-2">
                    <svg class="animate-spin h-4 w-4" viewBox="0 0 24 24">
                      <circle
                        class="opacity-25"
                        cx="12"
                        cy="12"
                        r="10"
                        stroke="currentColor"
                        stroke-width="4"
                        fill="none"
                      />
                      <path
                        class="opacity-75"
                        fill="currentColor"
                        d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                      />
                    </svg>
                    Logging in...
                  </span>
                {:else}
                  Log In Again
                {/if}
              </button>
              <button
                type="button"
                onclick={handleCancel}
                disabled={isLoading}
                class="btn btn-secondary"
              >
                Cancel
              </button>
            </div>
          </form>
        {/if}
      </div>

      <!-- Footer hint -->
      <div class="px-4 pb-4 sm:px-5">
        <p class="text-xs text-muted-foreground">
          {tr("auth.session.expired.dataPreserved")}
        </p>
      </div>
    </div>
  </section>
{/if}
