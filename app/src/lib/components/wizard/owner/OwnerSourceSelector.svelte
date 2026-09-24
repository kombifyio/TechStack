<script lang="ts">
  import {
    Check,
    UserRound,
    UserRoundPlus,
    BadgeCheck,
    House,
  } from "@lucide/svelte";
  import type { StackConfig, WizardDeploymentLane } from "#lib/wizard/index.js";
  import type { OwnerStepState } from "#lib/wizard/owner-state.svelte.js";
  import { tr } from "#lib/i18n.svelte.js";
  import CloudLinkCard from "./CloudLinkCard.svelte";
  import LocalOwnerForm from "./LocalOwnerForm.svelte";
  let {
    config,
    owner,
    lane,
  }: {
    config: StackConfig;
    owner: OwnerStepState;
    lane: WizardDeploymentLane;
  } = $props();
  const profileSelected = $derived(
    owner.bootstrapAuto || config.owner.source === "cloud-linked",
  );
  const email = $derived(
    lane === "saas" ? owner.signedInEmail : owner.cloudLink.external_email,
  );
  const name = $derived(
    lane === "saas"
      ? owner.signedInAccount.displayName
      : owner.cloudLink.external_name,
  );
  const profileReady = $derived(
    lane === "saas" ? owner.signedInProfileReady : owner.cloudLinkReady,
  );
</script>

{#if owner.bootstrapSkipped}
  <div class="existing-owner">
    <House size={32} aria-hidden="true" />
    <div>
      <h3>{tr("wizard.login.profile.existing")}</h3>
      <p>{tr("wizard.login.profile.existingHint")}</p>
    </div>
  </div>
{:else}
  <fieldset class="owner-options">
    <legend class="sr-only">{tr("wizard.login.owner.title")}</legend>
    <label class="owner-choice" class:selected={profileSelected}>
      <input
        type="radio"
        name="homelab-owner-source"
        checked={profileSelected}
        onchange={() =>
          lane === "saas"
            ? owner.useAutoOwnerBootstrap()
            : owner.selectOwnerSource("cloud-linked")}
        data-testid={lane === "saas"
          ? "owner-auto-default"
          : "owner-source-cloud-linked-select"}
      />
      <span class="choice-art" aria-hidden="true"
        ><UserRound size={34} strokeWidth={1.3} /><span class="art-detail"
          ><BadgeCheck size={19} /></span
        ></span
      >
      <span class="choice-copy"
        ><strong>{tr("wizard.login.profile.title")}</strong><span
          >{tr("wizard.login.profile.description")}</span
        ></span
      >
      <span class="choice-check" aria-hidden="true"
        >{#if profileSelected}<Check size={14} />{/if}</span
      >
    </label>
    <label class="owner-choice" class:selected={!profileSelected}>
      <input
        type="radio"
        name="homelab-owner-source"
        checked={!profileSelected}
        onchange={() => owner.useCustomOwnerBootstrap()}
        data-testid="owner-source-local"
      />
      <span class="choice-art" aria-hidden="true"
        ><UserRoundPlus size={34} strokeWidth={1.3} /></span
      >
      <span class="choice-copy"
        ><strong>{tr("wizard.login.profile.local")}</strong><span
          >{tr("wizard.login.profile.localDescription")}</span
        ></span
      >
      <span class="choice-check" aria-hidden="true"
        >{#if !profileSelected}<Check size={14} />{/if}</span
      >
    </label>
  </fieldset>
  <div class="owner-detail">
    {#if profileSelected}
      {#if lane !== "saas"}<CloudLinkCard {config} {owner} flat />
      {:else}
        <div class="profile-summary" data-testid="owner-bootstrap-summary">
          <span class="avatar" aria-hidden="true"
            >{(name || email || "?").slice(0, 1).toUpperCase()}</span
          >
          <div class="profile-name">
            <strong>{name || tr("wizard.login.profile.current")}</strong><span
              >{email}</span
            >
          </div>
          {#if profileReady}<span class="verified"
              ><BadgeCheck size={14} aria-hidden="true" />{tr(
                "wizard.login.profile.verified",
              )}</span
            >{/if}
        </div>
      {/if}
      <p
        class="profile-note"
        class:needs-verification={lane === "saas" && !profileReady}
      >
        {tr(
          lane === "saas" && !profileReady
            ? "wizard.login.profile.unverified"
            : "wizard.login.profile.dataOnly",
        )}
      </p>
    {:else}<LocalOwnerForm {config} />{/if}
  </div>
{/if}

<style>
  .owner-options {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 18px;
    border: 0;
    padding: 0;
    margin: 0;
  }
  .owner-choice {
    position: relative;
    display: flex;
    align-items: center;
    gap: 22px;
    padding: 27px 26px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    cursor: pointer;
    transition:
      border-color 160ms,
      background 160ms;
  }
  .owner-choice:hover,
  .owner-choice.selected {
    border-color: var(--primary);
    background: color-mix(in oklch, var(--primary) 5%, var(--card));
  }
  .owner-choice:has(input:focus-visible) {
    outline: 2px solid var(--ring);
    outline-offset: 4px;
  }
  .owner-choice input {
    position: absolute;
    opacity: 0;
    inline-size: 1px;
    block-size: 1px;
  }
  .choice-art {
    position: relative;
    display: grid;
    flex-shrink: 0;
    place-items: center;
    width: 68px;
    height: 68px;
    color: var(--primary);
    border: 1px solid color-mix(in oklch, var(--primary) 20%, transparent);
    border-radius: 50%;
  }
  .art-detail {
    position: absolute;
    inset-inline-end: -5px;
    bottom: 1px;
    background: var(--card);
    padding: 3px;
    border-radius: 50%;
  }
  .choice-copy {
    display: grid;
    gap: 7px;
    padding-inline-end: 10px;
  }
  .choice-copy strong {
    font-size: 16px;
    font-weight: 600;
    line-height: 1.45;
  }
  .choice-copy > span,
  .profile-note,
  .existing-owner p {
    font-size: 13px;
    line-height: 1.7;
    color: var(--muted-foreground);
  }
  .choice-check {
    position: absolute;
    inset-inline-end: 14px;
    top: 14px;
    width: 21px;
    height: 21px;
    display: grid;
    place-items: center;
    border: 1px solid var(--border);
    border-radius: 50%;
  }
  .selected .choice-check {
    background: var(--primary);
    border-color: var(--primary);
    color: var(--primary-foreground);
  }
  .owner-detail {
    padding-block: 25px 10px;
  }
  .profile-summary,
  .existing-owner {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 15px;
  }
  .avatar {
    display: grid;
    place-items: center;
    width: 42px;
    height: 42px;
    border-radius: 50%;
    background: var(--muted);
    color: var(--foreground);
    font-size: 17px;
    font-weight: 600;
  }
  .profile-name {
    display: grid;
    gap: 3px;
  }
  .profile-name strong {
    font-size: 14px;
  }
  .profile-name > span {
    font-size: 13px;
    color: var(--muted-foreground);
  }
  .verified {
    margin-inline-start: auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--muted-foreground);
    font-size: 11px;
  }
  .profile-note {
    margin-top: 14px;
  }
  .needs-verification {
    color: var(--warning);
  }
  .existing-owner h3 {
    font-size: 18px;
    margin-bottom: 6px;
  }
  @media (max-width: 700px) {
    .owner-options {
      grid-template-columns: 1fr;
    }
    .owner-choice {
      padding: 22px;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .owner-choice {
      transition: none;
    }
  }
</style>
