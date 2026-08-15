<!--
  AccessStep - wizard step 3 (Where do you need access?).

  Embeddable module (ADR-0036 / D4c): renders the access-mode cards and the
  conditional access profile accordion; the host owns the config state and
  receives mode selections through onAccessModeSelect.
-->
<script lang="ts">
  import { Accordion, RadioCard, TipBox } from "../index";
  import type {
    AccessModeValue,
    BundleChoiceDefinition,
    StackConfig,
  } from "$lib/wizard";
  import { tr } from "$lib/i18n.svelte";

  interface Props {
    config: StackConfig;
    accessQuestions: BundleChoiceDefinition[];
    vpnQuestions: BundleChoiceDefinition[];
    onAccessModeSelect: (value: AccessModeValue) => void;
  }

  let { config, accessQuestions, vpnQuestions, onAccessModeSelect }: Props =
    $props();
</script>

<div class="space-y-8" data-testid="easy-step-3">
  <div class="text-center mb-4">
    <h2 class="text-2xl font-semibold text-foreground mb-3">
      {tr("wizard.access.title")}
    </h2>
    <p class="text-muted-foreground max-w-lg mx-auto">
      {tr("wizard.access.subtitle")}
    </p>
  </div>

  <div class="grid gap-5 md:grid-cols-2">
    {#each accessQuestions as access (access.value)}
      <RadioCard
        name="access"
        value={access.value}
        selected={config.network.accessMode === access.value}
        title={tr(access.titleKey)}
        description={tr(access.descriptionKey)}
        helpText={access.helpKey ? tr(access.helpKey) : undefined}
        helpTip={access.tipKey ? tr(access.tipKey) : undefined}
        testId={access.testId}
        onselect={(value) => onAccessModeSelect(value as AccessModeValue)}
      />
    {/each}
  </div>

  <TipBox>
    <strong>{tr("common.tip")}:</strong>
    {tr("wizard.access.tipText")}
  </TipBox>

  {#if config.network.accessMode === "anywhere"}
    <Accordion title={tr("wizard.access.vpn.title")}>
      <div class="space-y-4">
        <div>
          <label
            for="easy-access-profile"
            class="block text-sm text-muted-foreground mb-2"
            >{tr("wizard.access.vpn.type")}</label
          >
          <select
            id="easy-access-profile"
            bind:value={config.network.vpn}
            class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground"
          >
            {#each vpnQuestions as vpn (vpn.value)}
              <option value={vpn.value}>{tr(vpn.titleKey)}</option>
            {/each}
          </select>
        </div>
        <label class="flex items-center gap-2 text-foreground cursor-pointer">
          <input
            type="checkbox"
            bind:checked={config.network.enableCloudflare}
            class="rounded border-border text-primary"
          />
          {tr("wizard.access.vpn.cloudflare")}
        </label>
      </div>
    </Accordion>
  {/if}
</div>
