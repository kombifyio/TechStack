<!--
  Stepper Component
  
  Displays wizard progress with step numbers and navigation buttons inline.
  Navigation buttons (Back/Next/Create) are positioned on the same line as the step indicators.
-->
<script lang="ts">
  import type { WizardStep } from "$lib/wizard/types";

  interface Props {
    steps: WizardStep[];
    currentStep: number;
    canGoNext?: boolean;
    canDeploy?: boolean;
    isDeploying?: boolean;
    deployLabel?: string;
    deployingLabel?: string;
    onprev?: () => void;
    onnext?: () => void;
    ondeploy?: () => void;
  }

  let {
    steps,
    currentStep,
    canGoNext = true,
    canDeploy = false,
    isDeploying = false,
    deployLabel = "Create",
    deployingLabel = "Creating...",
    onprev,
    onnext,
    ondeploy,
  }: Props = $props();

  const isFirstStep = $derived(currentStep === 1);
  const isLastStep = $derived(currentStep === steps.length);
</script>

<div
  class="mb-6 flex w-full min-w-0 flex-col gap-3 border-b border-border pb-4 sm:mb-8 sm:flex-row sm:items-center sm:justify-between sm:gap-4 sm:pb-6"
  data-testid="wizard-stepper"
>
  <!-- Back Button -->
  <div class="flex min-h-8 w-full items-center sm:w-24">
    {#if !isFirstStep}
      <button
        onclick={onprev}
        class="btn btn-ghost btn-sm whitespace-nowrap"
        data-testid="wizard-back"
      >
        ← Back
      </button>
    {/if}
  </div>

  <!-- Step Indicators -->
  <div
    class="flex w-full min-w-0 flex-1 items-center justify-start gap-1 overflow-x-auto overscroll-contain px-0.5 py-1 sm:justify-center sm:gap-2 sm:overflow-visible sm:px-0 sm:py-0"
  >
    {#each steps as step, idx}
      <div class="flex shrink-0 items-center gap-1 sm:gap-2">
        <div
          class="flex h-7 w-7 items-center justify-center rounded-full text-sm font-bold transition-colors sm:h-8 sm:w-8 {currentStep >=
          step.id
            ? 'bg-primary text-primary-foreground'
            : 'bg-muted text-muted-foreground'}"
        >
          {step.id}
        </div>
        <span
          class="text-sm hidden sm:inline {currentStep >= step.id
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

  <!-- Next/Deploy Button -->
  <div class="flex min-h-8 w-full justify-end sm:w-40">
    {#if !isLastStep}
      <button
        onclick={onnext}
        disabled={!canGoNext}
        class="btn btn-primary btn-sm whitespace-nowrap"
        data-testid="wizard-next"
      >
        Next →
      </button>
    {:else}
      <button
        onclick={ondeploy}
        disabled={!canDeploy || isDeploying}
        class="btn btn-primary btn-sm flex items-center gap-2 whitespace-nowrap"
        data-testid="wizard-create"
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
