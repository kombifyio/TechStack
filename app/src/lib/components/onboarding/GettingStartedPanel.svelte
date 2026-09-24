<script lang="ts">
  /**
   * Getting started — onboarding phase two, in the navigation.
   *
   * Tasks the user actually completes, tracked server-side. It lives in the
   * rail and NOWHERE else: this is the surface. It was also mounted in the
   * dashboard content once, which put two copies of the same checklist on
   * screen at the same time.
   *
   * Nothing about ordering, progress or blocking is decided here — the core
   * already did that, and a second opinion would be a second answer.
   */
  import { goto } from "$app/navigation";
  import type { ResolvedStep } from "@kombiverselabs/onboarding-core";
  import { GettingStarted } from "@kombiverselabs/ui/onboarding";

  import {
    readGettingStartedMinimized,
    requestIntroductionStart,
    writeGettingStartedMinimized,
  } from "#lib/onboarding/introduction.js";
  import { resolveOnboardingMessage } from "#lib/onboarding/journey.js";
  import { onboardingStore } from "#lib/onboarding/store.svelte.js";

  interface Props {
    collapsed?: boolean;
    class?: string;
  }

  let { collapsed = false, class: className = "" }: Props = $props();

  const journey = $derived(onboardingStore.resolved);
  let minimized = $state(readGettingStartedMinimized());
  const nextStep = $derived(
    journey?.steps.find(
      (step) =>
        !step.done &&
        !step.blocked &&
        step.availability.status === "available" &&
        step.route,
    ),
  );

  /**
   * A cost-bearing step can only be ticked as a manual skip — the server
   * refuses a reported completion, so offering "mark done" for it would be an
   * offer we cannot honour.
   */
  function markDone(step: ResolvedStep): void {
    void (step.costBearing
      ? onboardingStore.skip(step.id)
      : onboardingStore.complete(step.id));
  }

  function navigate(step: ResolvedStep): void {
    if (step.route) void goto(step.route);
  }
</script>

{#if journey}
  <!-- The introduction points at this; see introduction.ts. -->
  <div data-onboarding-anchor="getting-started">
    {#if !collapsed && !minimized && journey.status === "active" && nextStep?.route}
      <a
        class="mb-2 block rounded-lg border border-border p-3 text-sm"
        href={nextStep.route}
      >
        <span class="block text-xs text-muted-foreground"
          >{resolveOnboardingMessage("onboarding.ui.continue")}</span
        >
        {resolveOnboardingMessage(nextStep.ctaMessageId)}
      </a>
    {/if}
    <GettingStarted
      {journey}
      {collapsed}
      resolveMessage={resolveOnboardingMessage}
      onMarkDone={markDone}
      onNavigate={navigate}
      onDismiss={() => void onboardingStore.dismiss()}
      onResume={() => void onboardingStore.resume()}
      onReplayIntroduction={requestIntroductionStart}
      {minimized}
      onMinimizedChange={(value) => {
        minimized = value;
        writeGettingStartedMinimized(value);
      }}
      class={className}
    />
  </div>
{/if}
