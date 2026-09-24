<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
  import { tr } from "#lib/i18n.svelte.js";
  import MorphingText from "#lib/components/ui/MorphingText.svelte";
  import GroupedTaskList from "#lib/components/ui/GroupedTaskList.svelte";
</script>

    <!-- LEFT SIDE: Progress & Task List -->
    <div class="lg:sticky lg:top-8 lg:self-start min-w-0">
      <!-- Header with morphing text -->
      <div class="text-center lg:text-left mb-4 min-h-[8rem]">
        <div
          class="inline-flex items-center gap-2 px-4 py-2 rounded-full bg-primary/10 text-primary mb-4"
        >
          {#if creation.isComplete}
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
            Rollout incomplete
          {:else if creation.hasFailed}
            Creation failed
          {:else if creation.waitingForManagedRuntime}
            Node provisioning is still in progress
          {:else if creation.agentPairingWaiting}
            Run the pairing command
          {:else}
            Please wait while we're
            <span class="inline-flex whitespace-nowrap">
              <MorphingText
                texts={["Completing", "Validating", "Configuring", "Deploying"]}
                interval={3000}
                typingSpeed={60}
                class="text-primary font-semibold"
              />
            </span>
          {/if}
        </h3>
        <p class="text-sm text-muted-foreground">
          {#if creation.waitingForManagedRuntime}
            The next managed-runtime check is scheduled for this operation. If
            it remains overdue after a replica handover or restart, Techstack
            offers a resume action for that exact Node.
          {:else if creation.agentPairingWaiting}
            The registration request is ready, but the Node is not connected
            until a fresh Guard heartbeat appears in the Node projection.
          {:else}
            This may take a few minutes. You can track the progress below.
          {/if}
        </p>
      </div>

      <!-- Progress card with task list -->
      <div data-kx="plate" class="p-6">
        {#if creation.initError}
          <div
            class="mb-6 p-4 rounded-xl border border-destructive/30 bg-destructive/10"
            data-testid="init-error"
          >
            <p class="text-destructive font-medium">Setup cannot continue</p>
            <p class="text-sm text-destructive/80 mt-1">
              {creation.initError}
            </p>
            <a
              href="/stacks/new"
              class="inline-block mt-3 text-sm text-primary hover:text-primary/80 underline"
            >
              Back to setup wizard →
            </a>
          </div>
        {/if}

        <!-- Progress bar -->
        <div class="mb-8">
          <div class="flex justify-between text-sm mb-2">
            <span class="text-muted-foreground">Progress</span>
            <span class="text-primary font-mono" aria-live="polite"
              >{creation.overallProgress}%</span
            >
          </div>
          <div class="h-2 bg-muted rounded-full overflow-hidden">
            <div
              class="h-full bg-primary rounded-full transition-all duration-500 ease-out"
              style="width: {creation.overallProgress}%"
            ></div>
          </div>
        </div>

        <!-- Phase-grouped task list -->
        <GroupedTaskList tasks={creation.tasks} />
        {#if creation.applianceReceipt}
          <p class="mt-4 text-sm text-muted-foreground" role="status">
            {tr("wizard.smartHome.applianceRetained")}
          </p>
        {/if}

        <!-- Footer hint for in-progress -->
        {#if !creation.isComplete && !creation.hasFailed}
          <p class="text-center text-muted-foreground text-xs mt-6">
            {#if creation.waitingForManagedRuntime}
              The next check is scheduled. Dashboard, services, and Node access
              remain locked until enrollment is confirmed; an overdue check can
              be resumed safely here.
            {:else if creation.agentPairingWaiting}
              This page checks the real Node projection every few seconds.
            {:else}
              This can take a few minutes. Please do not close this page.
            {/if}
          </p>
        {/if}
      </div>
    </div>
