<!--
  OwnerSourceSelector - who becomes the owner of the new stack.

  Three bootstrap shapes:
  - none:   server expansion, owner untouched (summary card only)
  - auto:   SaaS lane, owner seeded from the kombify Cloud profile
  - custom: local owner form, plus (self-hosted lane) the kombify Cloud
            profile link as an alternative owner source
-->
<script lang="ts">
  import { Accordion } from "../index";
  import type { StackConfig, WizardDeploymentLane } from "$lib/wizard";
  import type { OwnerStepState } from "$lib/wizard/owner-state.svelte";
  import { tr } from "$lib/i18n.svelte";
  import CloudLinkCard from "./CloudLinkCard.svelte";
  import LocalOwnerForm from "./LocalOwnerForm.svelte";

  interface Props {
    config: StackConfig;
    owner: OwnerStepState;
    lane: WizardDeploymentLane;
  }

  let { config, owner, lane }: Props = $props();

  const localOwnerSelected = $derived(config.owner.source === "local");
</script>

<div class="space-y-4">
  <div>
    <h3 class="text-lg font-semibold text-foreground">
      {tr("wizard.login.owner.title")}
    </h3>
    <p class="text-sm text-muted-foreground mt-1">
      {tr("wizard.login.owner.subtitle")}
    </p>
  </div>

  {#if owner.bootstrapSkipped}
    <div
      class="card p-4 border-primary/30 bg-primary/5"
      data-testid="owner-bootstrap-skipped-summary"
    >
      <p class="text-foreground font-semibold">
        Existing TechStack owner stays unchanged
      </p>
      <p class="text-sm text-muted-foreground mt-1">
        This wizard only adds or registers another server. Owner,
        authentication, and recovery settings stay on the existing TechStack.
      </p>
    </div>
  {:else if owner.bootstrapAuto}
    <div
      class="card p-4 border-primary/30 bg-primary/5"
      data-testid="owner-bootstrap-summary"
    >
      <p class="text-foreground font-semibold">
        Owner will be prepared from your kombify Cloud profile
      </p>
      <p class="text-sm text-muted-foreground mt-1">
        TechStack creates the Homelab Owner as a Pocket ID user after rollout.
        Passkey setup follows in the handoff, and recovery material is
        generated server-side.
      </p>
    </div>

    <Accordion title={tr("wizard.advanced.title")}>
      <div class="space-y-3">
        <p class="text-sm text-muted-foreground">
          Use a custom Owner only when the Homelab should not use your kombify
          Cloud profile as the seed identity.
        </p>
        <button
          type="button"
          class="btn btn-secondary"
          onclick={() => owner.useCustomOwnerBootstrap()}
          data-testid="owner-custom-override"
        >
          Configure custom Owner
        </button>
      </div>
    </Accordion>
  {:else}
    {#if lane === "saas"}
      <button
        type="button"
        class="btn btn-secondary"
        onclick={() => owner.useAutoOwnerBootstrap()}
        data-testid="owner-auto-default"
      >
        Use kombify Cloud profile
      </button>
    {/if}

    <div class="grid gap-4 lg:grid-cols-2">
      <div
        class="card p-4 text-left transition-all {localOwnerSelected
          ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
          : ''}"
      >
        <button
          type="button"
          class="w-full text-left"
          aria-pressed={localOwnerSelected}
          onclick={() => owner.selectOwnerSource("local")}
          data-testid="owner-source-local"
        >
          <p class="text-foreground font-semibold">
            {tr("wizard.login.owner.local.title")}
          </p>
          <p class="text-sm text-muted-foreground mt-1">
            {tr("wizard.login.owner.local.description")}
          </p>
        </button>

        {#if localOwnerSelected}
          <LocalOwnerForm {config} />
        {/if}
      </div>

      {#if lane !== "saas"}
        <CloudLinkCard {config} {owner} />
      {/if}
    </div>
  {/if}
</div>
