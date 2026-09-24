<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
  import { onMount, onDestroy } from "svelte";
  import CreationProgress from "./CreationProgress.svelte";
  import CreationCompletion from "./CreationCompletion.svelte";
  import CreationRequirements from "./CreationRequirements.svelte";
  import CreationLease from "./CreationLease.svelte";
  import CreationInstallCommand from "./CreationInstallCommand.svelte";
  import CreationFailure from "./CreationFailure.svelte";
  import CreationRunStatus from "./CreationRunStatus.svelte";

  onMount(() => {
    void creation.mount();
  });

  onDestroy(() => {
    creation.unmount();
  });
</script>


<svelte:head>
  <title>Creating StackKit deployment | kombify-Techstack</title>
</svelte:head>

<div class="min-h-full w-full bg-background p-6 lg:p-8 overflow-x-hidden">
  <!-- Side-by-side layout: Task list on left, content on right -->
  <div
    class="mx-auto w-full max-w-6xl min-w-0 grid gap-8 lg:grid-cols-[420px_minmax(0,1fr)]"
  >
    <CreationProgress />
    <!-- RIGHT SIDE: Success/Error Content -->
    <div class="flex flex-col min-w-0 lg:pt-[9.75rem]">
      <!-- Completion state with worker registration -->
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
              ? "Open operations"
              : "Review and start StackKit rollout"}
          </button>
        </div>
      {:else if creation.hasFailed}
        <CreationFailure />
      {:else if !creation.isComplete}
        <CreationRunStatus />
      {/if}
    </div>
  </div>
</div>
