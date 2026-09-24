<script lang="ts">
  import CreationGoals from "../CreationGoals.svelte";
  import SmartHomeAdvice from "../SmartHomeAdvice.svelte";
  import WizardHintZone from "../WizardHintZone.svelte";
  import { withCreationTargets } from "#lib/wizard/creationGoals.js";
  import type { WizardRecommendation } from "#lib/api/unifier.js";
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
  const catalog = $derived(withCreationTargets(useCaseCatalog));
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
  />
  <WizardHintZone state={recommendationState} onopen={onRecommendationOpen} />
  {#if config.goals?.["smart-home"] && recommendationState.phase === "resolved"}
    <SmartHomeAdvice
      recommendation={recommendationState.result.smart_home_recommendation}
    />
  {/if}
</div>
