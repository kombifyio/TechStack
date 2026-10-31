<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { creation } from "./creation-controller.svelte.js";
  import { onMount, onDestroy } from "svelte";
  import { useViewContextOverlay } from "#lib/stores/viewContext.svelte.js";
  import CreationProgress from "./CreationProgress.svelte";
  import CreationCompletion from "./CreationCompletion.svelte";
  import CreationNextStep from "./CreationNextStep.svelte";
  import CreationRequirements from "./CreationRequirements.svelte";
  import CreationLease from "./CreationLease.svelte";
  import CreationFailure from "./CreationFailure.svelte";
  import CreationRunStatus from "./CreationRunStatus.svelte";
  import CreationLeanBack from "./CreationLeanBack.svelte";
  import CreationHeader from "./CreationHeader.svelte";
  import CreationGoodToKnow from "./CreationGoodToKnow.svelte";
  import { page } from "$app/state";
  import { emitCreationEvent } from "#lib/creation/host.js";

  let running = $derived(!creation.isComplete && !creation.hasFailed);

  // The host's Companion learns which deployment and run this page shows.
  useViewContextOverlay(() => creation.viewContext);

  // A host that opened this run (the embedded module) hears how it ends. The
  // controller outlives the page, so this waits until it holds this page's run.
  let reset = $state(false);
  $effect(() => {
    if (!reset) return;
    const runId = page.url.searchParams.get("run_id") ?? "";
    if (creation.isComplete || creation.hasFailed) {
      emitCreationEvent({
        event: "completed",
        runId,
        stackId: creation.stackId,
        outcome: creation.isComplete ? "succeeded" : "failed",
      });
    } else if (creation.jobState) {
      emitCreationEvent({ event: "progress", runId, phase: creation.jobState });
    }
  });

  onMount(() => {
    void creation.mount();
    reset = true;
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
      class="mb-8 flex flex-col-reverse gap-6 lg:flex-row lg:items-start lg:justify-between"
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
          <div class="flex flex-col gap-6">
            <CreationNextStep />
            <div>
              <CreationCompletion />
              <CreationLease part="handoff" />
            </div>
            <!-- The cards below are for people who want the evidence: what the
                 release requires and what was provisioned. They stay one
                 click away instead of stacking up between the person and
                 their next step. -->
            <details
              class="rounded-2xl border border-border bg-card/50"
              data-testid="creation-technical-details"
            >
              <summary
                class="cursor-pointer select-none px-5 py-4 text-sm font-medium text-foreground"
              >
                {tr("creation.technical.summary")}
              </summary>
              <div class="border-t border-border/60 p-5">
                <CreationRequirements />
                <CreationLease part="technical" />
              </div>
            </details>
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
