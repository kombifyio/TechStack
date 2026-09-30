<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  /**
   * Cloud Link Complete Page
   *
   * Landing page for the cloud-link PKCE callback redirect. The backend
   * finishes the code exchange and redirects here with the outcome in the
   * URL fragment (#status=ok | #status=error&reason=...). This page reports
   * the outcome to the wizard tab (window.opener) and closes itself; when
   * opened as a full tab (popup-blocked fallback) it shows the outcome and
   * points back to the wizard, whose polling picks the link up anyway.
   */

  import { onMount } from "svelte";
  import { browser } from '$app/env';
  import { cloudLinkReasonMessage } from "#lib/wizard/owner-state.svelte.js";

  let status = $state<"ok" | "error" | "unknown">("unknown");
  let reason = $state<string | null>(null);
  let canAutoClose = $state(false);

  onMount(() => {
    if (!browser) return;
    const params = new URLSearchParams(window.location.hash.substring(1));
    status = params.get("status") === "ok" ? "ok" : "error";
    reason = params.get("reason");
    window.history.replaceState({}, "", "/auth/cloud-link-complete");

    if (window.opener) {
      try {
        window.opener.postMessage(
          { type: "kombify:cloud-link", status, reason },
          window.location.origin,
        );
        canAutoClose = true;
        setTimeout(() => window.close(), 800);
      } catch {
        // COOP may sever the opener; the wizard's status polling covers this.
      }
    }
  });
</script>

<svelte:head>
  <title>{tr("ui.authCloud-link-complete.kombifyCloudLinkKombifyTechstack")}</title>
</svelte:head>

<div class="min-h-screen bg-background flex items-center justify-center">
  <div class="max-w-md w-full px-6 text-center">
    {#if status === "ok"}
      <div class="mb-6">
        <div
          class="h-12 w-12 mx-auto rounded-full bg-success/20 flex items-center justify-center"
        >
          <svg
            class="w-6 h-6 text-success"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M5 13l4 4L19 7"
            ></path>
          </svg>
        </div>
      </div>
      <h1 class="text-2xl font-bold text-foreground mb-2">
        {tr("ui.authCloud-link-complete.kombifyCloudConnected")}
      </h1>
      <p class="text-muted-foreground">
        {canAutoClose
          ? tr("ui.authCloud-link-complete.thisWindowClosesAutomatically")
          : tr("ui.authCloud-link-complete.youCanCloseThisTab")}
      </p>
    {:else}
      <div class="mb-6">
        <div
          class="h-12 w-12 mx-auto rounded-full bg-destructive/20 flex items-center justify-center"
        >
          <svg
            class="w-6 h-6 text-destructive"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M6 18L18 6M6 6l12 12"
            ></path>
          </svg>
        </div>
      </div>
      <h1 class="text-2xl font-bold text-foreground mb-2">
        {tr("ui.cloudLink.incomplete")}
      </h1>
      <p class="text-muted-foreground" data-testid="cloud-link-complete-reason">
        {cloudLinkReasonMessage(reason ?? undefined)}
      </p>
      <p class="text-muted-foreground mt-4">
        {canAutoClose
          ? tr("ui.authCloud-link-complete.thisWindowClosesAutomatically")
          : tr("ui.authCloud-link-complete.closeThisTabAndTry")}
      </p>
    {/if}
  </div>
</div>
