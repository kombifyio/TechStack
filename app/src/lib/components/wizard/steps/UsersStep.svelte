<!--
  UsersStep - wizard step 4 (Who will use it?).

  Embeddable module (ADR-0036 / D4c): renders the audience tiles; the host
  owns the config state and receives selections through onAudienceChange.
-->
<script lang="ts">
  import { FeatureCard, TipBox } from "../index";
  import type {
    AudienceConfigKey,
    BundleQuestionDefinition,
    StackConfig,
  } from "$lib/wizard";
  import { tr } from "$lib/i18n.svelte";

  interface Props {
    config: StackConfig;
    audienceQuestions: BundleQuestionDefinition<AudienceConfigKey>[];
    onAudienceChange: (audience: AudienceConfigKey, value: boolean) => void;
  }

  let { config, audienceQuestions, onAudienceChange }: Props = $props();
</script>

<div class="space-y-8" data-testid="easy-step-4">
  <div class="text-center mb-4">
    <h2 class="text-2xl font-semibold text-foreground mb-3">
      {tr("wizard.users.title")}
    </h2>
    <p class="text-muted-foreground max-w-lg mx-auto">
      {tr("wizard.users.subtitle")}
    </p>
  </div>

  <div class="grid gap-5">
    {#each audienceQuestions as audience (audience.key)}
      <FeatureCard
        checked={config.audience[audience.configKey]}
        title={tr(audience.titleKey)}
        description={tr(audience.descriptionKey)}
        helpText={audience.helpKey ? tr(audience.helpKey) : undefined}
        helpTip={audience.tipKey ? tr(audience.tipKey) : undefined}
        testId={audience.testId}
        onchange={(v) => onAudienceChange(audience.configKey, v)}
      />
    {/each}
  </div>

  <TipBox>
    <strong>{tr("common.tip")}:</strong>
    {tr("wizard.users.tipText")}
  </TipBox>
</div>
