<script lang="ts">
  import CreationGoals from "../CreationGoals.svelte";
  import CreationTemplates from "../CreationTemplates.svelte";
  import SmartHomeAdvice from "../SmartHomeAdvice.svelte";
  import WizardHintZone from "../WizardHintZone.svelte";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import { onMount } from "svelte";
  import type { WizardRecommendation } from "#lib/api/unifier.js";
  import { listWorkers } from "#lib/api/workers.js";
  import {
    acceleratorFromGpu,
    type SetupHardwareHints,
  } from "@kombiverselabs/ui/usecase";
  import type { UseCaseCatalogView } from "#lib/api/useCaseCatalog.js";
  import type { WizardPreviewState } from "#lib/wizard/WizardPreviewController.js";
  import type {
    BundleQuestionDefinition,
    GoalConfigKey,
    StackConfig,
  } from "#lib/wizard/index.js";

  let {
    config = $bindable(),
    primaryGoalQuestions,
    advancedGoalQuestions,
    onGoalChange,
    onSettingChange,
    recommendationState = { phase: "idle" },
    useCaseCatalog = new Map(),
    catalogState = "ready",
    onCatalogReload,
    onRecommendationOpen,
    ondisclose,
  }: {
    config: StackConfig;
    primaryGoalQuestions: BundleQuestionDefinition<GoalConfigKey>[];
    advancedGoalQuestions: BundleQuestionDefinition<GoalConfigKey>[];
    onGoalChange: (goal: GoalConfigKey, value: boolean) => void;
    onSettingChange: (
      goal: GoalConfigKey,
      id: string,
      value: string | boolean,
    ) => void;
    recommendationState?: WizardPreviewState;
    useCaseCatalog?: Map<string, UseCaseCatalogView>;
    /**
     * Where the release catalog stands. Cards need it: without it every use
     * case would read "Coming soon", which is a lie when the catalog merely
     * failed to load.
     */
    catalogState?: "loading" | "ready" | "error";
    onCatalogReload?: () => void;
    onRecommendationOpen?: (recommendation: WizardRecommendation) => void;
    ondisclose?: (event: {
      kind: "detail-level" | "drawer" | "group";
      id?: string;
      open: boolean;
    }) => void;
  } = $props();

  const questions = $derived([
    ...primaryGoalQuestions,
    ...advancedGoalQuestions,
  ]);
  const catalog = $derived(useCaseCatalog);

  // The strongest approved server (a GPU first, then memory) drives the
  // guided setup's hardware recommendations. Without servers or access the
  // catalog defaults stay.
  let hardware = $state<SetupHardwareHints>({});
  onMount(() => {
    listWorkers()
      .then((workers) => {
        const best = workers
          .filter((worker) => worker.approved)
          .sort(
            (a, b) =>
              Number(Boolean(acceleratorFromGpu(b.gpu))) -
                Number(Boolean(acceleratorFromGpu(a.gpu))) ||
              (b.ram_mb ?? 0) - (a.ram_mb ?? 0),
          )[0];
        if (best)
          hardware = {
            gpu: acceleratorFromGpu(best.gpu) ? best.gpu : undefined,
            ramMb: best.ram_mb || undefined,
            server: best.hostname || undefined,
          };
      })
      .catch(() => {});
  });
</script>

<div class="space-y-8" data-testid="easy-step-1">
  {#if catalogState !== "ready"}
    <header class="space-y-2">
      <p class="text-xs font-semibold uppercase tracking-wider text-primary">
        {tr("wizard.preview.eyebrow")}
      </p>
      <h2 class="text-3xl font-semibold tracking-tight text-foreground">
        {tr("wizard.preview.title")}
      </h2>
      <p class="max-w-prose text-sm text-muted-foreground">
        {tr("wizard.preview.subtitle")}
      </p>
    </header>
  {/if}
  {#if catalogState === "loading"}
    <div class="space-y-4" aria-busy="true" data-testid="use-case-catalog-loading">
      <LoadingNote label={tr("wizard.catalog.loading")} />
      <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3" aria-hidden="true">
        {#each Array(6) as _, index (index)}
          <div
            class="h-40 animate-pulse rounded-2xl border border-border bg-muted/40 motion-reduce:animate-none"
          ></div>
        {/each}
      </div>
    </div>
  {:else if catalogState === "error"}
    <ErrorCallout
      title={tr("wizard.catalog.errorTitle")}
      message={tr("wizard.catalog.errorBody")}
      onRetry={onCatalogReload}
      retryLabel={tr("wizard.catalog.reload")}
      testId="use-case-catalog-error"
    />
  {:else}
    <CreationGoals
      bind:config
      {questions}
      {catalog}
      runtimeCatalog={useCaseCatalog}
      {onGoalChange}
      {onSettingChange}
      {ondisclose}
      {hardware}
    >
      {#snippet afterCards()}
        <CreationTemplates {config} {questions} {catalog} {onGoalChange} />
      {/snippet}
    </CreationGoals>
  {/if}
  <WizardHintZone state={recommendationState} onopen={onRecommendationOpen} />
  {#if config.goals?.["smart-home"] && recommendationState.phase === "resolved"}
    <SmartHomeAdvice
      recommendation={recommendationState.result.smart_home_recommendation}
    />
  {/if}
</div>
