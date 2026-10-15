<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { creation } from "./creation-controller.svelte.js";
  import { onMount, onDestroy } from "svelte";
  import { useViewContextOverlay } from "#lib/stores/viewContext.svelte.js";
  import CreationProgress from "./CreationProgress.svelte";
  import CreationCompletion from "./CreationCompletion.svelte";
  import CreationRequirements from "./CreationRequirements.svelte";
  import CreationLease from "./CreationLease.svelte";
  import CreationInstallCommand from "./CreationInstallCommand.svelte";
  import CreationFailure from "./CreationFailure.svelte";
  import CreationRunStatus from "./CreationRunStatus.svelte";
  import CreationLeanBack from "./CreationLeanBack.svelte";
  import CreationHeader from "./CreationHeader.svelte";
  import CreationGoodToKnow from "./CreationGoodToKnow.svelte";

  let running = $derived(!creation.isComplete && !creation.hasFailed);

  // The host's Companion learns which deployment and run this page shows.
  useViewContextOverlay(() => creation.viewContext);

  onMount(() => {
    void creation.mount();
  });

  onDestroy(() => {
    creation.unmount();
  });
</script>


<svelte:head>
  <title>{tr("ui.stacksCreating.creatingStackkitDeploymentKombifyTechstack")}</title>
</svelte:head>

<div class="min-h-full w-full bg-background p-6 lg:p-8 overflow-x-hidden">
  <div class="mx-auto w-full max-w-7xl min-w-0">
    <div
      class="mt-12 mb-8 flex flex-col-reverse gap-6 lg:mt-0 lg:flex-row lg:items-start lg:justify-between"
    >
      <CreationHeader />
      {#if running}
        <CreationLeanBack />
      {/if}
    </div>

    <!-- While the run works the progress reel is the main column and the
         side column passes the wait with tips; once it ends the phase list
         moves to the side and the outcome takes the main column. -->
    <div
      class="grid gap-8 lg:grid-cols-[minmax(0,22rem)_minmax(0,1fr)] xl:grid-cols-[24rem_minmax(0,1fr)]"
    >
      <aside class="order-2 min-w-0 lg:order-1 lg:sticky lg:top-8 lg:self-start">
        {#if running}
          <CreationGoodToKnow />
        {:else}
          <CreationProgress />
        {/if}
      </aside>

      <div class="order-1 flex min-w-0 flex-col gap-4 lg:order-2">
        {#if creation.isComplete}
          <!-- Boxless section (operator direction 2026-08-19): the completion
               content sits directly on the page ground; only genuine content
               cards inside keep a surface. -->
          <div>
            <CreationCompletion />
            <CreationRequirements />
            <CreationLease />
            <CreationInstallCommand />
            <button
              onclick={creation.goToStack}
              data-kx="control"
              data-variant="primary"
              class="inline-flex w-full items-center justify-center gap-2 whitespace-nowrap rounded-xl px-4 py-3 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
              data-testid="continue-to-stackkit-rollout"
            >
              {creation.serverProvisioningMode === "kombify-cloud"
                ? tr("ui.stacksCreating.openOperations")
                : tr("ui.stacksCreating.reviewAndStartStackkitRollout")}
            </button>
          </div>
        {:else if creation.hasFailed}
          <CreationFailure />
        {:else}
          <CreationRunStatus />
          <CreationProgress focused />
        {/if}
      </div>
    </div>
  </div>
</div>
