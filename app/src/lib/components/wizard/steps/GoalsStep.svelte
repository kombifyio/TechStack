<!--
  GoalsStep - wizard step 1 (What do you want to do?).

  Embeddable module (ADR-0036 / D4c): state stays with the host; this
  component renders the goal tiles plus the advanced accordion and reports
  every change through onGoalChange. The host owns the everything-toggle and
  availability rules.
-->
<script lang="ts">
  import { Accordion, FeatureCard, TipBox } from "../index";
  import type {
    BundleQuestionDefinition,
    GoalConfigKey,
    StackConfig,
  } from "$lib/wizard";
  import { tr } from "$lib/i18n.svelte";

  interface Props {
    config: StackConfig;
    primaryGoalQuestions: BundleQuestionDefinition<GoalConfigKey>[];
    advancedGoalQuestions: BundleQuestionDefinition<GoalConfigKey>[];
    advancedOpen?: boolean;
    onGoalChange: (goal: GoalConfigKey, value: boolean) => void;
  }

  let {
    config,
    primaryGoalQuestions,
    advancedGoalQuestions,
    advancedOpen = false,
    onGoalChange,
  }: Props = $props();
</script>

<div class="space-y-8" data-testid="easy-step-1">
  <div class="text-center mb-4">
    <h2 class="text-2xl font-semibold text-foreground mb-3">
      {tr("wizard.goals.title")}
    </h2>
    <p class="text-muted-foreground max-w-lg mx-auto">
      {tr("wizard.goals.subtitle")}
    </p>
  </div>

  <div class="grid gap-5 md:grid-cols-2 xl:grid-cols-3">
    {#each primaryGoalQuestions as goal (goal.key)}
      <FeatureCard
        checked={config.goals?.[goal.configKey]}
        title={tr(goal.titleKey)}
        description={tr(goal.descriptionKey)}
        helpText={goal.helpKey ? tr(goal.helpKey) : undefined}
        helpTip={goal.tipKey ? tr(goal.tipKey) : undefined}
        testId={goal.testId}
        onchange={(v) => onGoalChange(goal.configKey, v)}
      />
    {/each}
  </div>

  <!-- Tip below cards -->
  <TipBox>
    <strong>{tr("common.tip")}:</strong>
    {tr("wizard.goals.tipText")}
  </TipBox>

  <!-- Advanced Settings -->
  <Accordion title={tr("wizard.advanced.title")} open={advancedOpen}>
    <div class="space-y-5">
      {#if advancedGoalQuestions.length > 0}
        <div class="space-y-3" data-testid="advanced-use-cases">
          <p class="text-sm font-medium text-foreground">
            {tr("wizard.goals.advancedTitle")}
          </p>
          <div class="grid gap-4 md:grid-cols-2">
            {#each advancedGoalQuestions as goal (goal.key)}
              <FeatureCard
                checked={config.goals?.[goal.configKey]}
                title={tr(goal.titleKey)}
                description={tr(goal.descriptionKey)}
                helpText={goal.helpKey ? tr(goal.helpKey) : undefined}
                helpTip={goal.tipKey ? tr(goal.tipKey) : undefined}
                testId={goal.testId}
                onchange={(v) => onGoalChange(goal.configKey, v)}
              />
            {/each}
          </div>
        </div>
      {/if}

      <div>
        <label
          for="easy-advanced-isolation"
          class="block text-sm text-muted-foreground mb-2"
          >{tr("wizard.advanced.isolation")}</label
        >
        <select
          id="easy-advanced-isolation"
          bind:value={config.advanced.isolation}
          class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground"
        >
          <option value="isolated"
            >{tr("wizard.advanced.isolation.isolated")}</option
          >
          <option value="shared"
            >{tr("wizard.advanced.isolation.shared")}</option
          >
        </select>
      </div>
      <div>
        <label
          for="easy-advanced-autostart"
          class="block text-sm text-muted-foreground mb-2"
          >{tr("wizard.advanced.autostart")}</label
        >
        <select
          id="easy-advanced-autostart"
          bind:value={config.advanced.autostart}
          class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground"
        >
          <option value="auto">{tr("wizard.advanced.autostart.auto")}</option>
          <option value="manual"
            >{tr("wizard.advanced.autostart.manual")}</option
          >
        </select>
      </div>
      <label class="flex items-center gap-2 text-foreground cursor-pointer">
        <input
          type="checkbox"
          bind:checked={config.advanced.backupsEnabled}
          class="rounded border-border text-primary"
        />
        {tr("wizard.advanced.backups")}
      </label>
    </div>
  </Accordion>
</div>
