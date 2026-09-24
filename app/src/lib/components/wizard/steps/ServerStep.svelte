<!--
  ServerStep - wizard step 2 (Where should the server come from?).

  Embeddable module (ADR-0036 / D4c): a thin heading shell around the
  ServerRegistryPanel so the services dashboard and expansion runs can reuse
  the exact provisioning surface.
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { SlidersHorizontal, ArrowRight, ShieldCheck } from "@lucide/svelte";
  import { ServerRegistryPanel } from "../index";
  import ServerProvisioningStep from "../ServerProvisioningStep.svelte";
  import type { ManagedProviderID, StackConfig } from "#lib/wizard/index.js";
  import { tr } from "#lib/i18n.svelte.js";

  interface Props {
    config: StackConfig;
    nodeOptions?: Snippet<[StackConfig]>;
    nodeAlternative?: Snippet;
    showRole?: boolean;
    showServices?: boolean;
    lockFoundation?: boolean;
    joinSurface?: boolean;
    onmanagedproviderselect?: (provider: ManagedProviderID) => void;
    recommendedStackKit?: string;
    reviewMode?: boolean;
    selectionReady?: boolean;
  }

  let {
    config = $bindable(),
    nodeOptions,
    nodeAlternative,
    showRole = false,
    showServices = false,
    lockFoundation = false,
    joinSurface = false,
    onmanagedproviderselect,
    recommendedStackKit,
    reviewMode = false,
    selectionReady = $bindable(true),
  }: Props = $props();
  let advancedOpen = $state(false);
</script>

<div class="space-y-8" data-testid="easy-step-2">
  <div class="node-heading">
    <p class="eyebrow">{tr("wizard.server.eyebrow")}</p>
    <h2>
      {tr("wizard.server.title")}
    </h2>
    <p class="intro">
      {tr("wizard.server.subtitle")}
    </p>
  </div>

  {#if joinSurface}
    <div
      class="inheritance-context"
      role="note"
      data-testid="node-inherits-homelab"
    >
      <ShieldCheck size={19} strokeWidth={1.7} aria-hidden="true" />
      <div>
        <strong>{tr("wizard.server.join.title")}</strong>
        <p>{tr("wizard.server.join.description")}</p>
      </div>
    </div>
  {/if}

  {#if !nodeAlternative}
    <ServerProvisioningStep
      bind:config
      bind:selectionReady
      {onmanagedproviderselect}
      {reviewMode}
      {joinSurface}
    />
  {/if}

  <section class="advanced-region">
    <button
      type="button"
      class="advanced-trigger"
      aria-expanded={advancedOpen}
      aria-controls="node-advanced-options"
      onclick={() => (advancedOpen = !advancedOpen)}
    >
      <SlidersHorizontal size={15} aria-hidden="true" />{tr(
        "wizard.advanced.title",
      )}<ArrowRight size={15} aria-hidden="true" />
    </button>
    {#if advancedOpen}
      <div id="node-advanced-options" class="advanced-content">
        {@render nodeOptions?.(config)}
        {#if nodeAlternative}
          {@render nodeAlternative()}
        {:else}
          <ServerRegistryPanel
            bind:config
            showProvisioning={false}
            flat
            {showRole}
            {showServices}
            {lockFoundation}
            {joinSurface}
            {onmanagedproviderselect}
            {recommendedStackKit}
          />
        {/if}
      </div>
    {/if}
  </section>
</div>

<style>
  .inheritance-context {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    border-left: 2px solid var(--primary);
    padding: 4px 0 4px 16px;
    color: var(--muted-foreground);
    font-size: 13px;
    line-height: 1.55;
  }
  .inheritance-context :global(svg) {
    flex: none;
    margin-top: 2px;
    color: var(--primary);
  }
  .inheritance-context strong {
    display: block;
    color: var(--foreground);
    font-weight: 600;
  }
  .advanced-region {
    border-top: 1px solid var(--border);
    padding-top: 18px;
  }
  .advanced-trigger {
    display: flex;
    gap: 9px;
    align-items: center;
    color: var(--muted-foreground);
    font-size: 12px;
    padding-block: 5px;
    cursor: pointer;
  }
  .advanced-trigger[aria-expanded="true"] {
    color: var(--primary);
  }
  .advanced-trigger[aria-expanded="true"] :global(svg:last-child) {
    transform: rotate(90deg);
  }
  .advanced-trigger:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 4px;
  }
  .advanced-content {
    padding-top: 24px;
  }
  .node-heading {
    padding-block: 14px 10px;
  }
  .eyebrow {
    font-size: 10px;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.12em;
    color: var(--primary);
    margin-bottom: 9px;
  }
  h2 {
    font-size: clamp(27px, 3vw, 36px);
    font-weight: 600;
    letter-spacing: -0.035em;
    line-height: 1.15;
    text-wrap: balance;
  }
  .intro {
    max-width: 65ch;
    font-size: 14px;
    line-height: 1.75;
    color: var(--muted-foreground);
    margin-top: 14px;
  }
</style>
