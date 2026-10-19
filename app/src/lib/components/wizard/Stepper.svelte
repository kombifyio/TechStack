<!--
  Stepper Component

  Displays wizard progress as an ordered list of steps and the navigation
  buttons (Back/Next/Create) on the same line.

  A blocked "Next" or "Create" stays focusable and announces itself with
  aria-disabled; the visible reason sits right under the button, so nobody has
  to guess why it does nothing. Pressing it still reports to the wizard, which
  then highlights what is missing.
-->
<script lang="ts">
  import type { WizardStep } from "#lib/wizard/types.js";
  import { tr } from "#lib/i18n.svelte.js";

  interface Props {
    steps: WizardStep[];
    disabledStepReasons?: Record<number, string>;
    currentStep: number;
    canGoNext?: boolean;
    canDeploy?: boolean;
    showDeploy?: boolean;
    isDeploying?: boolean;
    deployLabel?: string;
    deployingLabel?: string;
    /** Why the forward button is blocked right now; shown while it is. */
    blockedReason?: string;
    onprev?: () => void;
    onnext?: () => void;
    ondeploy?: () => void;
    navigationOnly?: boolean;
  }

  let {
    steps,
    disabledStepReasons = {},
    currentStep,
    canGoNext = true,
    canDeploy = false,
    showDeploy = true,
    isDeploying = false,
    deployLabel,
    deployingLabel,
    blockedReason = "",
    onprev,
    onnext,
    ondeploy,
    navigationOnly = false,
  }: Props = $props();

  const activeSteps = $derived(
    steps.filter((step) => !disabledStepReasons[step.id]),
  );
  const isFirstStep = $derived(currentStep === activeSteps[0]?.id);
  const isLastStep = $derived(currentStep === activeSteps.at(-1)?.id);
  const currentPosition = $derived(
    Math.max(
      1,
      activeSteps.findIndex((step) => step.id === currentStep) + 1,
    ),
  );
  const currentLabel = $derived(
    steps.find((step) => step.id === currentStep)?.label ?? "",
  );
  // The footer repeats the navigation controls; its test ids stay distinct so
  // wizard-back/-next/-create each name exactly one control.
  const controlTestIdPrefix = $derived(
    navigationOnly ? "wizard-footer" : "wizard",
  );
  const forwardBlocked = $derived(
    isLastStep ? !canDeploy : !canGoNext,
  );
  const reasonId = $derived(`${controlTestIdPrefix}-blocked-reason`);
  const forwardClasses =
    "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium disabled:pointer-events-none disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50";
</script>

<div
  class="flex w-full min-w-0 gap-3 border-border sm:items-center sm:justify-between sm:gap-4 {navigationOnly
    ? 'mt-10 border-t pt-6 flex-row'
    : 'mb-6 flex-col border-b pb-4 sm:mb-8 sm:flex-row sm:pb-6'}"
  data-testid={navigationOnly ? "wizard-footer" : "wizard-stepper"}
>
  <!-- Back Button -->
  <div class="flex min-h-8 w-full items-center sm:w-auto sm:min-w-24 sm:shrink-0">
    {#if !isFirstStep}
      <button
        type="button"
        onclick={onprev}
        data-kx="control"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
        data-testid={`${controlTestIdPrefix}-back`}
      >
        ← {tr("wizard.navigation.back")}
      </button>
    {/if}
  </div>

  <!-- Step Indicators -->
  {#if !navigationOnly}
    <nav
      class="min-w-0 flex-1 overflow-x-auto overscroll-contain px-0.5 py-1 sm:px-0"
      aria-label={tr("wizard.stepper.label")}
    >
      <ol class="flex w-max min-w-full items-center justify-start gap-1 sm:justify-center sm:gap-2">
        {#each steps as step, idx (step.id)}
          {@const disabledReason = disabledStepReasons[step.id]}
          {@const state = disabledReason
            ? "inactive"
            : currentStep === step.id
              ? "current"
              : currentStep > step.id
                ? "complete"
                : "upcoming"}
          <li
            class="flex shrink-0 items-center gap-1 sm:gap-2 {disabledReason
              ? 'opacity-45'
              : ''}"
            data-testid={`wizard-step-${step.id}`}
            data-state={state}
            aria-current={state === "current" ? "step" : undefined}
            title={disabledReason || undefined}
          >
            <span
              class="flex h-7 w-7 items-center justify-center rounded-full text-sm font-bold transition-colors sm:h-8 sm:w-8 {!disabledReason &&
              currentStep >= step.id
                ? 'bg-primary text-primary-foreground'
                : 'bg-muted text-muted-foreground'}"
              aria-hidden="true"
            >
              {step.id}
            </span>
            <!-- Phones show the name of the current step and keep the others
                 available to assistive technology; wider screens show all. -->
            <span
              class="text-sm {state === 'current'
                ? 'max-w-[8rem] truncate font-medium sm:max-w-none'
                : 'sr-only sm:not-sr-only'} {!disabledReason &&
              currentStep >= step.id
                ? 'text-foreground'
                : 'text-muted-foreground'}"
            >
              {step.label}
              {#if state === "complete"}
                <span class="sr-only">, {tr("wizard.stepper.completed")}</span>
              {:else if disabledReason}
                <span class="sr-only">: {disabledReason}</span>
              {/if}
            </span>
            {#if idx < steps.length - 1}
              <span
                class="h-0.5 w-4 shrink-0 sm:w-8 {currentStep > step.id
                  ? 'bg-primary'
                  : 'bg-muted'}"
                aria-hidden="true"
              ></span>
            {/if}
          </li>
        {/each}
      </ol>
      <p class="mt-1 text-xs text-muted-foreground sm:hidden" aria-hidden="true">
        {tr("wizard.stepper.position", {
          current: currentPosition,
          total: activeSteps.length,
          step: currentLabel,
        })}
      </p>
    </nav>
  {/if}

  <!-- Next/Deploy Button -->
  <div
    class="flex min-h-8 w-full flex-col items-end gap-1 sm:w-auto sm:min-w-40 sm:shrink-0"
  >
    {#if !isLastStep}
      <button
        type="button"
        onclick={onnext}
        aria-disabled={forwardBlocked ? "true" : undefined}
        aria-describedby={forwardBlocked && blockedReason ? reasonId : undefined}
        data-kx="control"
        data-variant="primary"
        class={forwardClasses}
        data-testid={`${controlTestIdPrefix}-next`}
      >
        {tr("wizard.navigation.next")} →
      </button>
    {:else if showDeploy}
      <button
        type="button"
        onclick={ondeploy}
        disabled={isDeploying}
        aria-disabled={forwardBlocked ? "true" : undefined}
        aria-busy={isDeploying ? "true" : undefined}
        aria-describedby={forwardBlocked && blockedReason ? reasonId : undefined}
        data-kx="control"
        data-variant="primary"
        class={forwardClasses}
        data-testid={`${controlTestIdPrefix}-create`}
      >
        {#if isDeploying}
          <svg class="animate-spin h-4 w-4" viewBox="0 0 24 24" aria-hidden="true">
            <circle
              class="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              stroke-width="4"
              fill="none"
            />
            <path
              class="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
            />
          </svg>
          {deployingLabel ?? tr("wizard.create.submitting")}
        {:else}
          {deployLabel ?? tr("wizard.create.submit")}
        {/if}
      </button>
    {/if}
    {#if forwardBlocked && blockedReason && (!isLastStep || showDeploy)}
      <p
        id={reasonId}
        class="max-w-64 text-end text-xs text-muted-foreground"
        data-testid={`${controlTestIdPrefix}-blocked-reason`}
      >
        {blockedReason}
      </p>
    {/if}
  </div>
</div>
