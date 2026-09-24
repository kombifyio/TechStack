<!--
  Stepper Component
  
  Displays wizard progress with step numbers and navigation buttons inline.
  Navigation buttons (Back/Next/Create) are positioned on the same line as the step indicators.
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
    deployLabel = "Create",
    deployingLabel = "Creating...",
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
  // The footer repeats the navigation controls; its test ids stay distinct so
  // wizard-back/-next/-create each name exactly one control.
  const controlTestIdPrefix = $derived(navigationOnly ? "wizard-footer" : "wizard");
</script>

<div
  class="flex w-full min-w-0 gap-3 border-border sm:items-center sm:justify-between sm:gap-4 {navigationOnly
    ? 'mt-10 border-t pt-6 flex-row'
    : 'mb-6 flex-col border-b pb-4 sm:mb-8 sm:flex-row sm:pb-6'}"
  data-testid={navigationOnly ? "wizard-footer" : "wizard-stepper"}
>
  <!-- Back Button -->
  <div class="flex min-h-8 w-full items-center sm:w-24">
    {#if !isFirstStep}
      <button
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
  {#if !navigationOnly}<div
      class="flex w-full min-w-0 flex-1 items-center justify-start gap-1 overflow-x-auto overscroll-contain px-0.5 py-1 sm:justify-center sm:gap-2 sm:overflow-visible sm:px-0 sm:py-0"
    >
      {#each steps as step, idx (step.id)}
        {@const disabledReason = disabledStepReasons[step.id]}
        <div
          class="flex shrink-0 items-center gap-1 sm:gap-2 {disabledReason
            ? 'opacity-45'
            : ''}"
          data-testid={`wizard-step-${step.id}`}
          role="group"
          data-state={disabledReason
            ? "inactive"
            : currentStep === step.id
              ? "current"
              : currentStep > step.id
                ? "complete"
                : "upcoming"}
          title={disabledReason || undefined}
          aria-label={disabledReason
            ? `${step.label}: ${disabledReason}`
            : undefined}
        >
          <div
            class="flex h-7 w-7 items-center justify-center rounded-full text-sm font-bold transition-colors sm:h-8 sm:w-8 {!disabledReason &&
            currentStep >= step.id
              ? 'bg-primary text-primary-foreground'
              : 'bg-muted text-muted-foreground'}"
          >
            {step.id}
          </div>
          <span
            class="text-sm hidden sm:inline {!disabledReason &&
            currentStep >= step.id
              ? 'text-foreground'
              : 'text-muted-foreground'}"
          >
            {step.label}
          </span>
        </div>
        {#if idx < steps.length - 1}
          <div
            class="h-0.5 w-5 shrink-0 sm:w-8 {currentStep > step.id
              ? 'bg-primary'
              : 'bg-muted'}"
          ></div>
        {/if}
      {/each}
    </div>
  {/if}

  <!-- Next/Deploy Button -->
  <div class="flex min-h-8 w-full justify-end sm:w-40">
    {#if !isLastStep}
      <button
        onclick={onnext}
        disabled={!canGoNext}
        data-kx="control"
        data-variant="primary"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
        data-testid={`${controlTestIdPrefix}-next`}
      >
        {tr("wizard.navigation.next")} →
      </button>
    {:else if showDeploy}
      <button
        onclick={ondeploy}
        disabled={!canDeploy || isDeploying}
        data-kx="control"
        data-variant="primary"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
        data-testid={`${controlTestIdPrefix}-create`}
      >
        {#if isDeploying}
          <svg class="animate-spin h-4 w-4" viewBox="0 0 24 24">
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
          {deployingLabel}
        {:else}
          {deployLabel}
        {/if}
      </button>
    {/if}
  </div>
</div>
