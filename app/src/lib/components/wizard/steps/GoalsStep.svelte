<script lang="ts">
  import CreationGoals from "../CreationGoals.svelte";
  import CreationTemplates from "../CreationTemplates.svelte";
  import SmartHomeAdvice from "../SmartHomeAdvice.svelte";
  import WizardHintZone from "../WizardHintZone.svelte";
  import { onMount } from "svelte";
  import type { WizardRecommendation } from "#lib/api/unifier.js";
  import { listWorkers } from "#lib/api/workers.js";
  import {
    acceleratorFromGpu,
    type SetupHardwareHints,
  } from "#lib/wizard/setupFlows.js";
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
  <WizardHintZone state={recommendationState} onopen={onRecommendationOpen} />
  {#if config.goals?.["smart-home"] && recommendationState.phase === "resolved"}
    <SmartHomeAdvice
      recommendation={recommendationState.result.smart_home_recommendation}
    />
  {/if}
</div>
