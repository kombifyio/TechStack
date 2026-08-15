<!--
  LoginStep - wizard step 5 "How will users authenticate?"

  Composes the owner-source selection, recovery passphrase, and login-method
  cards. All step state that is not part of StackConfig lives in
  OwnerStepState (passed in by the wizard orchestrator).
-->
<script lang="ts">
  import { Accordion, TipBox } from "../index";
  import type { StackConfig, WizardDeploymentLane } from "$lib/wizard";
  import type { OwnerStepState } from "$lib/wizard/owner-state.svelte";
  import { tr } from "$lib/i18n.svelte";
  import OwnerSourceSelector from "../owner/OwnerSourceSelector.svelte";
  import RecoveryPassphraseCard from "../owner/RecoveryPassphraseCard.svelte";
  import LoginMethodCards from "../owner/LoginMethodCards.svelte";

  interface Props {
    config: StackConfig;
    owner: OwnerStepState;
    lane: WizardDeploymentLane;
  }

  let { config, owner, lane }: Props = $props();

  const customBootstrap = $derived(
    !owner.bootstrapAuto && !owner.bootstrapSkipped,
  );
</script>

<div class="space-y-8" data-testid="easy-step-5">
  <div class="text-center mb-4">
    <h2 class="text-2xl font-semibold text-foreground mb-3">
      {tr("wizard.login.title")}
    </h2>
    <p class="text-muted-foreground max-w-lg mx-auto">
      {tr("wizard.login.subtitle")}
    </p>
  </div>

  <OwnerSourceSelector {config} {owner} {lane} />

  {#if customBootstrap}
    <RecoveryPassphraseCard {owner} />
    <LoginMethodCards {config} {owner} />
  {/if}

  <TipBox>
    <strong>{tr("common.tip")}:</strong>
    {#if owner.bootstrapSkipped}
      Server expansion reuses the existing TechStack owner and login
      configuration.
    {:else if owner.bootstrapAuto}
      Pocket ID is the Homelab identity source. The Cloud user controls
      rollout and recovery, while Homelab users are added later in
      Dashboard/Access.
    {:else}
      {tr("wizard.login.tipText")}
    {/if}
  </TipBox>

  {#if !owner.bootstrapSkipped}
    <Accordion title={tr("wizard.advanced.title")}>
      <div class="space-y-4">
        <label class="flex items-center gap-2 text-foreground cursor-pointer">
          <input
            type="checkbox"
            bind:checked={config.auth.centralIdentity}
            class="rounded border-border text-primary"
          />
          Use central identity provider (SSO)
        </label>
        <label class="flex items-center gap-2 text-foreground cursor-pointer">
          <input
            type="checkbox"
            bind:checked={config.auth.sessionTimeout}
            class="rounded border-border text-primary"
          />
          Enable automatic logout after inactivity
        </label>
      </div>
    </Accordion>
  {/if}
</div>
