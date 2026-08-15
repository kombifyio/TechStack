<!--
  CloudLinkCard - "Use kombify Cloud profile" owner source for self-hosted
  installs. Connects the operator's kombify Cloud account via the hosted
  login (PKCE popup; new-tab fallback keeps the wizard state alive when
  popups are blocked). Identity always derives server-side from the verified
  link - the wizard never collects cloud credentials or identity fields.
-->
<script lang="ts">
  import { onMount } from "svelte";
  import type { StackConfig } from "$lib/wizard";
  import type { OwnerStepState } from "$lib/wizard/owner-state.svelte";
  import { tr } from "$lib/i18n.svelte";

  interface Props {
    config: StackConfig;
    owner: OwnerStepState;
  }

  let { config, owner }: Props = $props();

  const selected = $derived(config.owner.source === "cloud-linked");
  let fallbackUrl = $state<string | null>(null);
  let popupBlocked = $state(false);

  onMount(() => {
    void owner.refreshCloudLink();
    window.addEventListener("message", owner.handleCloudLinkMessage);
    return () => {
      window.removeEventListener("message", owner.handleCloudLinkMessage);
      owner.stopCloudLinkPolling();
    };
  });

  async function connect() {
    popupBlocked = false;
    fallbackUrl = null;
    const started = await owner.connectCloudLink();
    if (!started) return;
    if (!started.popup) {
      popupBlocked = true;
      fallbackUrl = started.authorizationUrl;
    }
  }

  function selectCard() {
    if (owner.cloudLinkReady) {
      owner.selectOwnerSource("cloud-linked");
    }
  }
</script>

<div
  class="card p-4 text-left transition-all {selected
    ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
    : ''} {owner.cloudLinkState === 'unavailable' ? 'opacity-60' : ''}"
  data-testid="owner-source-cloud-linked"
>
  <button
    type="button"
    class="w-full text-left"
    aria-pressed={selected}
    disabled={owner.cloudLinkState === "unavailable"}
    onclick={selectCard}
    data-testid="owner-source-cloud-linked-select"
  >
    <p class="text-foreground font-semibold">
      {tr("wizard.login.owner.cloudLink.title")}
    </p>
    <p class="text-sm text-muted-foreground mt-1">
      {tr("wizard.login.owner.cloudLink.description")}
    </p>
  </button>

  <div class="mt-4 pt-4 border-t border-border space-y-3">
    {#if owner.cloudLink.linked}
      <div class="space-y-1" data-testid="cloud-link-status">
        <p class="text-sm text-foreground">
          {owner.cloudLink.external_name || owner.cloudLink.external_email}
        </p>
        <p class="text-xs text-muted-foreground">
          {owner.cloudLink.external_email}
          {#if owner.cloudLink.email_verified}
            <span class="text-success"
              >· {tr("wizard.login.owner.cloudLink.verified")}</span
            >
          {:else}
            <span class="text-warning"
              >· {tr("wizard.login.owner.cloudLink.unverified")}</span
            >
          {/if}
        </p>
      </div>
      {#if !owner.cloudLink.email_verified}
        <p class="text-xs text-warning">
          {tr("wizard.login.owner.cloudLink.verifyHint")}
        </p>
      {/if}
      <div class="flex gap-3">
        {#if owner.cloudLinkReady && !selected}
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            onclick={() => owner.selectOwnerSource("cloud-linked")}
            data-testid="cloud-link-use"
          >
            {tr("wizard.login.owner.cloudLink.use")}
          </button>
        {/if}
        <button
          type="button"
          class="btn btn-ghost btn-sm text-muted-foreground"
          onclick={() => owner.disconnectCloudLink()}
          data-testid="cloud-link-unlink"
        >
          {tr("wizard.login.owner.cloudLink.unlink")}
        </button>
      </div>
    {:else if owner.cloudLinkState === "unavailable"}
      <p class="text-sm text-muted-foreground" data-testid="cloud-link-unavailable">
        {owner.cloudLinkGuidance ??
          tr("wizard.login.owner.cloudLink.unavailable")}
      </p>
    {:else}
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        onclick={connect}
        disabled={owner.cloudLinkState === "starting"}
        data-testid="cloud-link-connect"
      >
        {owner.cloudLinkState === "starting" || owner.cloudLinkState === "waiting"
          ? tr("wizard.login.owner.cloudLink.connecting")
          : tr("wizard.login.owner.cloudLink.connect")}
      </button>
      {#if owner.cloudLinkState === "waiting"}
        <p class="text-xs text-muted-foreground">
          {tr("wizard.login.owner.cloudLink.waiting")}
        </p>
      {/if}
      {#if popupBlocked && fallbackUrl}
        <p class="text-xs text-warning">
          {tr("wizard.login.owner.cloudLink.popupBlocked")}
          <a
            href={fallbackUrl}
            target="_blank"
            rel="noopener"
            class="underline"
            data-testid="cloud-link-fallback">{tr(
              "wizard.login.owner.cloudLink.openInTab",
            )}</a
          >
        </p>
      {/if}
      {#if owner.cloudLinkError}
        <p class="text-xs text-destructive" data-testid="cloud-link-error">
          {owner.cloudLinkError}
        </p>
      {/if}
    {/if}
  </div>
</div>
