<script lang="ts">
  import { tick } from "svelte";
  import {
    Fingerprint,
    KeyRound,
    ShieldCheck,
    LifeBuoy,
    Settings2,
    ArrowRight,
    House,
    Laptop,
    CircleCheck,
  } from "@lucide/svelte";
  import { UseCaseAdvanced } from "@kombiverselabs/ui/usecase";
  import type { StackConfig, WizardDeploymentLane } from "#lib/wizard/index.js";
  import type { OwnerStepState } from "#lib/wizard/owner-state.svelte.js";
  import { ownerUsernameFromEmail } from "#lib/wizard/session-owner.js";
  import { revealWizardDetails } from "#lib/wizard/reveal-details.js";
  import { tr } from "#lib/i18n.svelte.js";
  import BrandLogoScope from "#lib/components/BrandLogoScope.svelte";
  import BrandLogoIcon from "#lib/components/BrandLogoIcon.svelte";
  import OwnerSourceSelector from "../owner/OwnerSourceSelector.svelte";
  import RecoveryPassphraseCard from "../owner/RecoveryPassphraseCard.svelte";
  let {
    config,
    owner,
    lane,
  }: {
    config: StackConfig;
    owner: OwnerStepState;
    lane: WizardDeploymentLane;
  } = $props();
  let advancedElement = $state<HTMLDivElement>();
  const groups = $derived([
    {
      id: "signin",
      label: tr("wizard.login.details.signin"),
      description: tr("wizard.login.details.signinDescription"),
      icon: KeyRound,
    },
    {
      id: "recovery",
      label: tr("wizard.login.details.recovery"),
      description: tr("wizard.login.details.recoveryDescription"),
      icon: LifeBuoy,
    },
    {
      id: "tools",
      label: tr("wizard.login.details.tools"),
      description: tr("wizard.login.details.toolsDescription"),
      icon: Settings2,
    },
  ]);
</script>

{#snippet tool(
  domain: string,
  name: string,
  label: string,
  description: string,
)}
  <div class="tool-row">
    <BrandLogoScope {domain}
      ><BrandLogoIcon class="size-9" fallbackLabel={name} /></BrandLogoScope
    >
    <div>
      <span class="tool-purpose">{tr(label)}</span>
      <h4>{name}</h4>
      <p>{tr(description)}</p>
    </div>
  </div>
{/snippet}
{#snippet body(group: string)}
  {#if group === "signin"}
    <div class="detail-copy">
      <h3>{tr("wizard.login.signin.title")}</h3>
      <p>{tr("wizard.login.signin.description")}</p>
      <p>{tr("wizard.login.signin.noTransfer")}</p>
    </div>
    {#if config.owner.source === "local" && !owner.bootstrapSkipped}
      <label class="username-field" for="owner-username"
        ><span>{tr("wizard.login.profile.username")}</span>
        <input
          id="owner-username"
          type="text"
          bind:value={config.owner.username}
          placeholder={ownerUsernameFromEmail(config.owner.email)}
          aria-describedby="owner-username-hint"
        />
        <span id="owner-username-hint" class="field-hint"
          >{tr("wizard.login.profile.usernameHint")}</span
        >
      </label>
    {/if}
  {:else if group === "recovery"}
    {#if owner.bootstrapAuto}
      <div class="detail-copy">
        <h3>{tr("wizard.login.recovery.auto")}</h3>
        <p>{tr("wizard.login.recovery.autoDescription")}</p>
      </div>
    {:else}<RecoveryPassphraseCard {owner} />{/if}
  {:else}
    <div class="tool-list">
      {@render tool(
        "pocket-id.org",
        "Pocket ID",
        "wizard.login.tools.identity",
        "wizard.login.tools.identityDescription",
      )}
      {@render tool(
        "tinyauth.app",
        "TinyAuth",
        "wizard.login.tools.gateway",
        "wizard.login.tools.gatewayDescription",
      )}
      <div class="tool-row">
        <ShieldCheck size={30} aria-hidden="true" />
        <div>
          <h4>{tr("wizard.login.tools.cloud")}</h4>
          <p>{tr("wizard.login.tools.cloudDescription")}</p>
        </div>
      </div>
    </div>
  {/if}
{/snippet}

<div class="login-step" data-testid="easy-step-5">
  <header class="login-heading">
    <p class="eyebrow">{tr("wizard.login.setup.eyebrow")}</p>
    <h2>{tr("wizard.login.setup.title")}</h2>
    <p class="intro">{tr("wizard.login.setup.subtitle")}</p>
  </header>
  <OwnerSourceSelector {config} {owner} {lane} />
  {#if !owner.bootstrapSkipped}
    <section class="passkey-section" aria-labelledby="passkey-title">
      <div class="passkey-copy">
        <div class="passkey-label">
          <Fingerprint size={18} aria-hidden="true" /><span
            >{tr("wizard.login.passkey.label")}</span
          ><span class="recommended"
            >{tr("wizard.login.passkey.recommended")}</span
          >
        </div>
        <h3 id="passkey-title">{tr("wizard.login.passkey.title")}</h3>
        <p>{tr("wizard.login.passkey.description")}</p>
        <div class="next-step">
          <CircleCheck size={16} aria-hidden="true" /><strong
            >{tr("wizard.login.passkey.afterSetup")}</strong
          >
        </div>
        <p class="next-hint">{tr("wizard.login.passkey.next")}</p>
      </div>
      <div class="passkey-visual" aria-hidden="true">
        <span class="device-label"
          ><Laptop size={22} />{tr("wizard.login.passkey.device")}</span
        >
        <div class="passkey-path">
          <span></span><Fingerprint size={65} strokeWidth={1.1} /><span
          ></span><ArrowRight size={18} />
        </div>
        <span class="device-label"
          ><House size={24} />{tr("wizard.login.passkey.home")}</span
        >
      </div>
    </section>
    <div bind:this={advancedElement} class="login-details">
      <UseCaseAdvanced
        {groups}
        label={tr("wizard.login.details.title")}
        subtitle={tr("wizard.login.details.subtitle")}
        presentation="tabs"
        tabsLabel={tr("wizard.login.details.tabs")}
        {body}
        ondisclose={async (event) => {
          if (event.open) {
            await tick();
            revealWizardDetails(
              advancedElement?.querySelector('[role="tablist"]'),
            );
          }
        }}
      />
    </div>
  {/if}
</div>

<style>
  .login-step {
    display: grid;
    gap: 28px;
  }
  .login-heading {
    padding-block: 14px 10px;
  }
  .eyebrow {
    color: var(--primary);
    font-size: 10px;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.12em;
    margin-bottom: 9px;
  }
  h2 {
    font-size: clamp(27px, 3vw, 36px);
    font-weight: 600;
    line-height: 1.15;
    letter-spacing: -0.035em;
    margin-bottom: 13px;
  }
  .intro {
    max-width: 760px;
    color: var(--muted-foreground);
    font-size: 15px;
    line-height: 1.8;
  }
  .passkey-section {
    display: grid;
    grid-template-columns: minmax(0, 1.3fr) minmax(240px, 1fr);
    align-items: center;
    gap: 35px;
    border-block: 1px solid var(--border);
    padding-block: 30px;
  }
  .passkey-label {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--primary);
    margin-bottom: 11px;
  }
  .recommended {
    font-size: 10px;
    font-weight: 600;
    margin-inline-start: 4px;
    padding: 3px 8px;
    background: color-mix(in oklch, var(--primary) 9%, transparent);
    border-radius: var(--radius);
  }
  .passkey-copy h3 {
    font-size: 22px;
    font-weight: 550;
    letter-spacing: -0.02em;
    margin-bottom: 10px;
  }
  .passkey-copy p,
  .detail-copy p,
  .tool-row p {
    color: var(--muted-foreground);
    font-size: 13px;
    line-height: 1.8;
    max-width: 670px;
  }
  .next-step {
    display: flex;
    gap: 8px;
    align-items: center;
    margin-top: 21px;
    font-size: 12px;
  }
  .next-step :global(svg) {
    color: var(--primary);
  }
  .next-hint {
    margin-top: 5px;
  }
  .passkey-visual {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 14px;
    color: var(--primary);
    min-height: 160px;
  }
  .device-label {
    display: grid;
    justify-items: center;
    text-align: center;
    gap: 11px;
    font-size: 10px;
    color: var(--muted-foreground);
    max-width: 78px;
  }
  .passkey-path {
    display: flex;
    align-items: center;
    gap: 9px;
  }
  .passkey-path span {
    width: 23px;
    border-top: 1px dashed color-mix(in oklch, var(--primary) 50%, transparent);
  }
  .detail-copy {
    display: grid;
    gap: 10px;
    padding-top: 4px;
  }
  .detail-copy h3 {
    font-size: 16px;
    font-weight: 600;
  }
  .username-field {
    display: grid;
    gap: 8px;
    max-width: 450px;
    margin-top: 25px;
    font-size: 13px;
  }
  .username-field input {
    padding: 11px 13px;
    background: var(--input);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--foreground);
  }
  .username-field input:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 2px;
  }
  .field-hint {
    color: var(--muted-foreground);
    font-size: 12px;
    line-height: 1.7;
  }
  .tool-list {
    display: grid;
    gap: 22px;
  }
  .tool-row {
    display: flex;
    align-items: flex-start;
    gap: 17px;
  }
  .tool-row :global(svg) {
    flex-shrink: 0;
    color: var(--primary);
  }
  .tool-row h4 {
    font-size: 14px;
    font-weight: 600;
    margin-bottom: 4px;
  }
  .tool-purpose {
    display: block;
    font-size: 10px;
    color: var(--muted-foreground);
    margin-bottom: 3px;
  }
  @media (max-width: 780px) {
    .passkey-section {
      grid-template-columns: 1fr;
    }
    .passkey-visual {
      min-height: 120px;
    }
  }
</style>
