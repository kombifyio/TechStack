<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { creation } from "./creation-controller.svelte.js";
  import CreationInstallCommand from "./CreationInstallCommand.svelte";

  // One card, on top, says what to do now: run the command on your own server,
  // or go on to the homelab. Everything else on the finished page is detail.
  const connectServer = $derived(creation.installCommandVisible);
  const managed = $derived(creation.serverProvisioningMode === "kombify-cloud");
</script>

<section
  class="rounded-2xl border border-primary/30 bg-primary/5 p-5 sm:p-6"
  aria-labelledby="creation-next-step-title"
  data-testid="creation-next-step"
>
  <h2
    id="creation-next-step-title"
    class="text-xs font-semibold uppercase tracking-wide text-primary"
  >
    {tr("creation.next.title")}
  </h2>
  <p class="mt-2 text-lg font-semibold text-foreground">
    {#if connectServer}
      {tr("creation.next.connectHeadline")}
    {:else if managed}
      {tr("creation.next.openHeadline")}
    {:else}
      {tr("creation.next.reviewHeadline")}
    {/if}
  </p>
  <p class="mt-1 max-w-2xl text-sm text-muted-foreground">
    {#if connectServer}
      {tr("creation.next.connectBody")}
    {:else if managed}
      {tr("creation.next.openBody")}
    {:else}
      {tr("creation.next.reviewBody")}
    {/if}
  </p>

  {#if connectServer}
    <div class="mt-5">
      <CreationInstallCommand />
    </div>
  {/if}

  <button
    type="button"
    onclick={creation.goToStack}
    data-kx="control"
    data-variant={connectServer ? undefined : "primary"}
    class="mt-5 inline-flex w-full items-center justify-center gap-2 whitespace-nowrap rounded-xl px-4 py-3 text-sm font-medium disabled:pointer-events-none disabled:opacity-50 sm:w-auto"
    data-testid="continue-to-stackkit-rollout"
  >
    {managed
      ? tr("ui.stacksCreating.openOperations")
      : connectServer
        ? tr("creation.next.dashboard")
        : tr("creation.next.review")}
  </button>
</section>
