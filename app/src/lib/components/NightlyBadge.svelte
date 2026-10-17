<script lang="ts">
  /**
   * Persistent "Nightly" badge on the preview deployment
   * (CI-CD-PLATFORM-STANDARD §4.6.2), with the way back to Stable. Renders
   * nothing on the stable deployment.
   */
  import { tr } from "#lib/i18n.svelte.js";
  import { clientBootstrap } from "#lib/client/bootstrap.js";

  const stableHref = $derived($clientBootstrap.stableOrigin);
</script>

{#if $clientBootstrap.releaseChannel === "preview"}
  <aside
    class="bg-background/95 text-foreground fixed bottom-3 left-3 z-90 flex items-center gap-2 rounded-full border px-3 py-1 text-xs shadow-sm"
    aria-label={tr("ui.releaseChannel.hint")}
    data-testid="nightly-badge"
  >
    <span class="font-semibold" title={tr("ui.releaseChannel.hint")}
      >{tr("ui.releaseChannel.badge")}</span
    >
    {#if stableHref}
      <a
        class="underline underline-offset-2"
        href={stableHref}
        data-sveltekit-reload>{tr("ui.releaseChannel.backToStable")}</a
      >
    {/if}
  </aside>
{/if}
