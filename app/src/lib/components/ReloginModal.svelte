<script lang="ts">
  import { authHandler } from "#lib/stores/authHandler.svelte.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { isSaaSEmbedded } from "#lib/stores/deploymentMode.js";
  import { resolveLoginExperience } from "#lib/auth/login-experience.js";
  import { resolveUnauthenticatedEntry } from "#lib/auth/unauthenticated-redirect.js";
  import { refreshEmbeddedCloudSession } from "#lib/auth/embedded-session.js";
  import { getPortalOrigin } from "#lib/stores/postMessageBridge.js";
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";

  let error = $state<string | null>(null);
  let isLoading = $state(false);
  let embeddedRefreshFailed = $state(false);
  const loginExperience = $derived(
    resolveLoginExperience({
      deploymentMode: authStore.deploymentMode,
      embedded: $isSaaSEmbedded,
    }),
  );

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
    authStore.initiateCloudLogin({ interactive: true });
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
    <div class="rounded-xl border border-warning/30 bg-warning/5 shadow-lg">
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
              <Button
                type="button"
                onclick={handleCloudRelogin}
                disabled={isLoading}
                variant="primary"
                class="flex-1"
              >
                {#if isLoading}
                  {tr("auth.session.expired.reconnecting")}
                {:else if embeddedRefreshFailed}
                  {tr("auth.session.renewal.retry")}
                {:else}
                  {tr("auth.session.expired.continueAuth0")}
                {/if}
              </Button>
              <Button
                type="button"
                onclick={handleCancel}
                disabled={isLoading}
                variant="secondary"
              >
                {tr("auth.session.expired.signOut")}
              </Button>
            </div>

            {#if embeddedRefreshFailed}
              <Button
                type="button"
                onclick={reopenPortal}
                variant="secondary"
                class="w-full"
              >
                {tr("auth.session.expired.reopenPortal")}
              </Button>
            {/if}
          </div>
        {:else}
          <div class="space-y-4">
            <p class="text-sm text-muted-foreground">
              Continue on the local Techstack setup path. This screen does not
              collect a password.
            </p>
            <Button
              variant="primary"
              class="w-full"
              onclick={() => {
                authHandler.onReloginCancel();
                window.location.assign(resolveUnauthenticatedEntry());
              }}
            >
              Continue local setup
            </Button>
            <Button variant="secondary" class="w-full" onclick={handleCancel}>
              Cancel
            </Button>
          </div>
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
