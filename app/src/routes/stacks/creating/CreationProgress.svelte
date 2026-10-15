<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
  import { tr } from "#lib/i18n.svelte.js";
  import GroupedTaskList from "#lib/components/ui/GroupedTaskList.svelte";

  interface Props {
    /** While the run works the list is a reel centred on the active phase. */
    focused?: boolean;
  }

  let { focused = false }: Props = $props();

  // The waiting and pairing cards carry their own status; the reel only
  // details ordinary running steps.
  let showStepDetail = $derived(
    focused &&
      !creation.agentPairingWaiting &&
      !creation.waitingForManagedRuntime &&
      Boolean(creation.currentStepInfo || creation.currentStepMessage),
  );
</script>

{#snippet stepDetail()}
  {#if creation.currentStepInfo}
    <p class="text-sm font-medium text-foreground">
      {creation.currentStepInfo.title}
    </p>
    <p class="mt-1 text-sm leading-relaxed text-muted-foreground">
      {creation.currentStepInfo.description}
    </p>
  {/if}
  {#if creation.currentStepMessage}
    <div class="mt-3 rounded-lg border border-primary/20 bg-primary/10 px-3 py-2">
      <p class="text-xs font-medium text-primary">
        {tr("ui.stacksCreatingCreationRunStatus.currentStatus")}
      </p>
      <p class="mt-1 text-sm text-foreground">
        {creation.currentStepMessage}
      </p>
    </div>
  {/if}
  {#if creation.currentStepInfo?.detail}
    <p class="mt-3 text-xs leading-relaxed text-muted-foreground/70">
      {creation.currentStepInfo.detail}
    </p>
  {/if}
{/snippet}

<!-- Progress card with the phase list -->
<div data-kx="plate" class="p-6">
  {#if creation.initError}
    <div
      class="mb-6 p-4 rounded-xl border border-destructive/30 bg-destructive/10"
      data-testid="init-error"
    >
      <p class="text-destructive font-medium">{tr("ui.stacksCreatingCreationProgress.setupCannotContinue")}</p>
      <p class="text-sm text-destructive/80 mt-1">
        {creation.initError}
      </p>
      <a
        href="/stacks/new"
        class="inline-block mt-3 text-sm text-primary hover:text-primary/80 underline"
      >
        {tr("ui.stacksCreatingCreationProgress.backToSetupWizard")}
      </a>
    </div>
  {/if}

  {#if creation.connectionIssue}
    <!-- The browser lost the job, not the job itself: the run keeps
         going on the server and this page keeps checking. -->
    <div
      class="mb-6 rounded-xl border border-warning/30 bg-warning/5 p-4"
      data-testid="creation-connection-issue"
      role="status"
    >
      <p class="text-sm font-medium text-foreground">
        {creation.connectionIssue === "auth"
          ? tr("ui.stacksCreatingCreation-controller.sessionExpired")
          : tr("ui.stacksCreatingCreation-controller.connectionToServerLost")}
      </p>
      <p class="mt-1 text-xs text-muted-foreground">
        {creation.connectionIssue === "auth"
          ? tr("ui.stacksCreatingCreation-controller.yourSessionIsNoLonger")
          : tr(
              "ui.stacksCreatingCreation-controller.afterFailedAttemptsTheBackend",
              { MAX_POLL_ERRORS: creation.maxPollErrors },
            )}
      </p>
      <button
        type="button"
        data-kx="control"
        class="mt-3 inline-flex items-center rounded-lg px-3 py-1.5 text-sm font-medium"
        onclick={creation.reconnect}
        data-testid="creation-reconnect"
      >
        {tr("ui.stacksCreatingCreationProgress.checkAgain")}
      </button>
    </div>
  {/if}

  <!-- Progress bar -->
  <div class="mb-8">
    <div class="flex justify-between text-sm mb-2">
      <span class="flex items-center gap-2">
        <span class="text-muted-foreground" id="creation-progress-label"
          >{tr("ui.stacksCreatingCreationProgress.progress")}</span
        >
        {#if focused && creation.phaseCount > 0}
          <span class="text-muted-foreground/60" aria-hidden="true">·</span>
          <span class="text-muted-foreground"
            >{tr("ui.creationRun.stepOf", {
              step: creation.phaseStep,
              total: creation.phaseCount,
            })}</span
          >
        {/if}
      </span>
      <span class="text-primary font-mono" aria-hidden="true"
        >{creation.overallProgress}%</span
      >
    </div>
    <div
      class="h-2 bg-muted rounded-full overflow-hidden"
      role="progressbar"
      aria-labelledby="creation-progress-label"
      aria-valuemin="0"
      aria-valuemax="100"
      aria-valuenow={creation.overallProgress}
    >
      <div
        class="h-full bg-primary rounded-full transition-all duration-500 ease-out"
        style="width: {creation.overallProgress}%"
      ></div>
    </div>
  </div>

  <!-- Phase-grouped task list -->
  <GroupedTaskList
    tasks={creation.tasks}
    timeline={creation.taskTimeline}
    now={creation.progressClock}
    {focused}
    activeDetail={showStepDetail ? stepDetail : undefined}
  />
  {#if creation.applianceReceipt}
    <p class="mt-4 text-sm text-muted-foreground" role="status">
      {tr("wizard.smartHome.applianceRetained")}
    </p>
  {/if}

  <!-- Footer hint for in-progress -->
  {#if !creation.isComplete && !creation.hasFailed}
    <p class="text-center text-muted-foreground text-xs mt-4">
      {#if creation.waitingForManagedRuntime}
        {tr("ui.stacksCreatingCreationProgress.theNextCheckIsScheduled")}
      {:else if creation.agentPairingWaiting}
        {tr("ui.stacksCreatingCreationProgress.thisPageChecksTheReal")}
      {:else}
        {tr("ui.stacksCreatingCreationProgress.thisCanTakeAFew")}
      {/if}
    </p>
  {/if}
</div>
