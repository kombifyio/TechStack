<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
  import { tr } from "#lib/i18n.svelte.js";
  import MorphingText from "#lib/components/ui/MorphingText.svelte";
  import { TriangleAlert } from "@lucide/svelte";
</script>

<!-- Header with morphing text -->
<div class="min-w-0 text-center lg:text-left">
  <div
    class="inline-flex items-center gap-2 px-4 py-2 rounded-full bg-primary/10 text-primary mb-4"
  >
    {#if creation.isComplete && creation.degradedCompletion}
      <TriangleAlert class="w-4 h-4 text-warning" aria-hidden="true" />
    {:else if creation.isComplete}
      <svg
        class="w-4 h-4"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
        stroke-width="2"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          d="M5 13l4 4L19 7"
        />
      </svg>
    {:else if creation.hasFailed}
      <svg
        class="w-4 h-4"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
        stroke-width="2"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          d="M6 18L18 6M6 6l12 12"
        />
      </svg>
    {:else if creation.waitingForManagedRuntime || creation.agentPairingWaiting}
      <svg
        class="w-4 h-4"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
        stroke-width="2"
        aria-hidden="true"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          d="M12 8v4l3 2m6-2a9 9 0 11-18 0 9 9 0 0118 0z"
        />
      </svg>
    {:else}
      <svg
        class="w-4 h-4 animate-spin"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
      >
        <circle cx="12" cy="12" r="10" stroke-opacity="0.25" />
        <path d="M12 2a10 10 0 0 1 10 10" stroke-linecap="round" />
      </svg>
    {/if}
    <span class="text-sm font-medium">{creation.creationHeaderLabel}</span>
  </div>
  <h3
    class="text-2xl font-semibold text-foreground mb-3 min-h-[3.5rem] leading-tight"
  >
    {#if creation.isComplete}
      {creation.completionTitle}
    {:else if creation.handoffMissingFailure}
      {tr("ui.stacksCreatingCreationProgress.rolloutIncomplete")}
    {:else if creation.hasFailed}
      {tr("ui.stacksCreatingCreationProgress.creationFailed")}
    {:else if creation.waitingForManagedRuntime}
      {tr("ui.stacksCreatingCreationProgress.nodeProvisioningIsStillIn")}
    {:else if creation.agentPairingWaiting}
      {tr("ui.stacksCreatingCreationProgress.runThePairingCommand")}
    {:else}
      {tr("ui.stacksCreatingCreationProgress.pleaseWaitWhileWeRe")}
      <span class="inline-flex whitespace-nowrap">
        <MorphingText
          texts={[
            tr("ui.stacksCreatingCreationProgress.morphCompleting"),
            tr("ui.stacksCreatingCreationProgress.morphValidating"),
            tr("ui.stacksCreatingCreationProgress.morphConfiguring"),
            tr("ui.stacksCreatingCreationProgress.morphDeploying"),
          ]}
          interval={3000}
          typingSpeed={60}
          class="text-primary font-semibold"
        />
      </span>
    {/if}
  </h3>
  <p class="text-sm text-muted-foreground">
    {#if creation.waitingForManagedRuntime}
      {tr("ui.stacksCreatingCreationProgress.theNextManagedRuntimeCheck")}
    {:else if creation.agentPairingWaiting}
      {tr("ui.stacksCreatingCreationProgress.theRegistrationRequestIsReady")}
    {:else}
      {tr("ui.stacksCreatingCreationProgress.thisMayTakeAFew")}
    {/if}
  </p>
</div>
