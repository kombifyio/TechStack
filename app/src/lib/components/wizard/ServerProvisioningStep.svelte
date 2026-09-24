<script lang="ts">
  import { onMount, tick, untrack } from "svelte";
  import { page } from "$app/state";
  import {
    ArrowRight,
    ArrowUpRight,
    Check,
    Cloud,
    Laptop,
    ListChecks,
    PlugZap,
    SlidersHorizontal,
    Terminal,
  } from "@lucide/svelte";
  import HypervisorSelection from "./HypervisorSelection.svelte";
  import RemoteServerConnection from "./RemoteServerConnection.svelte";
  import ManagedServerOptions from "./ManagedServerOptions.svelte";
  import NodePathIllustration from "./NodePathIllustration.svelte";
  import BrandLogoScope from "../BrandLogoScope.svelte";
  import BrandLogoIcon from "../BrandLogoIcon.svelte";
  import {
    applyServerProvisioningMode,
    getServerProvisioningModeChoices,
    normalizeIonosDatacenter,
    type ServerProvisioningMode,
    type StackConfig,
    type ManagedProviderID,
  } from "#lib/wizard/index.js";
  import { tr } from "#lib/i18n.svelte.js";
  import { features, loadFeatures } from "#lib/stores/features.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { shouldDowngradeManagedRuntimeSelection } from "#lib/wizard/managed-runtime-selection.js";
  import { managedProviders } from "#lib/wizard/managed-providers.js";
  import { revealWizardDetails } from "#lib/wizard/reveal-details.js";

  let {
    config = $bindable(),
    selectionReady = $bindable(true),
    onmanagedproviderselect,
    reviewMode = false,
    joinSurface = false,
  }: {
    config: StackConfig;
    selectionReady?: boolean;
    reviewMode?: boolean;
    joinSurface?: boolean;
    onmanagedproviderselect?: (provider: ManagedProviderID) => void;
  } = $props();
  const provisioningModes = getServerProvisioningModeChoices();
  const partnerChoice = {
    value: "partner",
    titleKey: "wizard.server.partner.title",
    descriptionKey: "wizard.server.partner.description",
    testId: "server-mode-partner",
  };
  const allFeatures = features.allFeatures;
  const featuresReady = features.ready;
  const featuresLoading = features.loading;
  const featuresErrorKind = features.errorKind;
  const entitlementVerificationFailed = $derived(Boolean($featuresErrorKind));
  const monthlyRuntimeAvailable = $derived(
    $featuresReady &&
      !$featuresLoading &&
      !entitlementVerificationFailed &&
      $allFeatures.get("monthly_runtime")?.enabled === true &&
      $allFeatures.get("monthly_runtime_cloudkit")?.enabled === true,
  );
  const visibleManagedProviders = $derived(
    monthlyRuntimeAvailable
      ? managedProviders.filter(
          (provider) => $allFeatures.get(provider.featureKey)?.enabled === true,
        )
      : [],
  );
  const managedRuntimeSelectable = $derived(visibleManagedProviders.length > 0);
  const managedSummary = $derived(
    entitlementVerificationFailed
      ? tr("wizard.server.managed.verificationFailedCaption")
      : !$featuresReady || $featuresLoading
        ? tr("wizard.server.managed.checking")
        : tr("wizard.server.managed.unavailable"),
  );
  const missingBaseFeatures = $derived(
    [
      { key: "monthly_runtime", label: "Monthly Runtime" },
      { key: "monthly_runtime_cloudkit", label: "Cloud Kit rollout" },
    ].filter((feature) => $allFeatures.get(feature.key)?.enabled !== true),
  );
  const managedUnavailableReason = $derived.by(() => {
    if ($featuresErrorKind === "auth")
      return tr("wizard.server.managed.authFailed");
    if (entitlementVerificationFailed)
      return tr("wizard.server.managed.verificationFailed");
    if (!$featuresReady || $featuresLoading)
      return tr("wizard.server.managed.checking");
    if (missingBaseFeatures.length)
      return tr("wizard.server.managed.baseMissing").replace(
        "{features}",
        missingBaseFeatures.map((feature) => feature.label).join(", "),
      );
    return tr("wizard.server.managed.providersMissing").replace(
      "{providers}",
      managedProviders.map((provider) => provider.label).join(", "),
    );
  });

  let ownEntry = $state<"install-command" | "connect-remote">(
    untrack(() =>
      config.serverProvisioning.mode === "connect-remote"
        ? "connect-remote"
        : "install-command",
    ),
  );
  let ownSystem = $state<"ubuntu" | "proxmox">(
    untrack(() =>
      config.serverProvisioning.mode === "hypervisor" ? "proxmox" : "ubuntu",
    ),
  );
  let branch = $state<"owned" | "new">(
    untrack(() =>
      config.serverProvisioning.mode === "kombify-cloud" ? "new" : "owned",
    ),
  );
  let shopping = $state(false);
  const selectedEntry = $derived(
    config.serverProvisioning.mode === "hypervisor"
      ? ownEntry
      : config.serverProvisioning.mode,
  );
  const pathReady = $derived(
    !shopping &&
      (branch === "new"
        ? selectedEntry === "kombify-cloud" && managedRuntimeSelectable
        : selectedEntry !== "kombify-cloud"),
  );
  const choices = $derived(
    branch === "owned"
      ? provisioningModes.filter((mode) => mode.value !== "kombify-cloud")
      : [
          ...provisioningModes.filter((mode) => mode.value === "kombify-cloud"),
          partnerChoice,
        ],
  );
  let systemDetailsOpen = $state(
    untrack(() => config.serverProvisioning.mode === "hypervisor"),
  );
  let detailHeading = $state<HTMLElement>();
  let selectionRevision = 0;

  onMount(() => {
    void loadFeatures();
  });

  async function selectMode(mode: string, reveal = true) {
    if (mode === "kombify-cloud" && !managedRuntimeSelectable) return;
    if (mode !== "partner") {
      const owned = mode === "install-command" || mode === "connect-remote";
      const runtimeMode =
        owned && ownSystem === "proxmox" ? "hypervisor" : mode;
      const changed =
        shopping ||
        selectedEntry !== mode ||
        config.serverProvisioning.mode !== runtimeMode;
      if (owned) ownEntry = mode;
      if (changed) {
        applyServerProvisioningMode(
          config,
          runtimeMode as ServerProvisioningMode,
        );
        systemDetailsOpen = runtimeMode === "hypervisor";
      }
    }
    shopping = mode === "partner";
    const revision = ++selectionRevision;
    await tick();
    if (reveal && revision === selectionRevision)
      revealWizardDetails(detailHeading);
  }

  function selectBranch(value: "owned" | "new") {
    if (branch === value) return;
    branch = value;
    if (value === "owned") void selectMode(ownEntry, false);
    else
      void selectMode(
        managedRuntimeSelectable ? "kombify-cloud" : "partner",
        false,
      );
  }

  function selectSystem(system: "ubuntu" | "proxmox") {
    ownSystem = system;
    applyServerProvisioningMode(
      config,
      system === "proxmox" ? "hypervisor" : ownEntry,
    );
  }

  function selectManagedProvider(provider: ManagedProviderID) {
    if (!visibleManagedProviders.some((item) => item.value === provider))
      return;
    config.providerId = provider;
    onmanagedproviderselect?.(provider);
    if (provider === "ionos")
      config.ionosDatacenter = normalizeIonosDatacenter(config.ionosDatacenter);
  }

  $effect(() => {
    selectionReady = pathReady;
    if (
      config.serverProvisioning.mode === "kombify-cloud" &&
      shouldDowngradeManagedRuntimeSelection({
        authenticated: authStore.isAuthenticated,
        featuresReady: $featuresReady,
        featuresLoading: $featuresLoading,
        verificationFailed: entitlementVerificationFailed,
        selectable: managedRuntimeSelectable,
      })
    )
      applyServerProvisioningMode(config, "install-command");
    if (
      config.serverProvisioning.mode === "kombify-cloud" &&
      managedRuntimeSelectable &&
      !visibleManagedProviders.some((item) => item.value === config.providerId)
    )
      selectManagedProvider(visibleManagedProviders[0].value);
  });
</script>

<div class="node-sources" data-testid="server-provisioning-step">
  <div
    class="branch-choices"
    role="group"
    aria-label={tr("wizard.server.branch.question")}
  >
    <button
      type="button"
      aria-pressed={branch === "owned"}
      onclick={() => selectBranch("owned")}
      data-testid="server-branch-owned"
    >
      <Laptop size={24} strokeWidth={1.5} aria-hidden="true" />
      <span
        ><strong>{tr("wizard.server.owned.title")}</strong><small
          >{tr("wizard.server.owned.subtitle")}</small
        ></span
      >
      <ArrowRight class="branch-arrow" size={21} aria-hidden="true" />
    </button>
    <button
      type="button"
      aria-pressed={branch === "new"}
      onclick={() => selectBranch("new")}
      data-testid="server-branch-new"
    >
      <Cloud size={25} strokeWidth={1.5} aria-hidden="true" />
      <span
        ><strong>{tr("wizard.server.new.title")}</strong><small
          >{tr("wizard.server.new.subtitle")}</small
        ></span
      >
      <ArrowRight class="branch-arrow" size={21} aria-hidden="true" />
    </button>
  </div>

  <svg
    class="decision-path"
    viewBox="0 0 1000 48"
    fill="none"
    preserveAspectRatio="none"
    aria-hidden="true"
    focusable="false"
  >
    <path
      d={branch === "owned"
        ? "M250 0C250 25 500 4 500 24"
        : "M750 0C750 25 500 4 500 24"}
    />
    <path d="M250 46C250 21 500 45 500 24C500 45 750 21 750 46" />
    <circle cx="250" cy="46" r="2.5" /><circle cx="750" cy="46" r="2.5" />
  </svg>

  {#key branch}
    <div class="source-options">
      {#each choices as mode (mode.value)}
        {@const selected = shopping
          ? mode.value === "partner"
          : selectedEntry === mode.value}
        {@const unavailable =
          mode.value === "kombify-cloud" && !managedRuntimeSelectable}
        <button
          type="button"
          class="source-card"
          class:selected
          aria-pressed={selected}
          aria-describedby={unavailable
            ? "managed-runtime-availability"
            : undefined}
          disabled={unavailable}
          data-testid={mode.testId}
          onclick={() => void selectMode(mode.value)}
        >
          <span class="option-copy"
            ><span class="card-title">{tr(mode.titleKey)}</span><span
              class="card-copy">{tr(mode.descriptionKey)}</span
            ></span
          >
          <span class="option-art"
            ><NodePathIllustration kind={mode.value} /></span
          >
          <span class="card-action">
            {#if unavailable}<span>{managedSummary}</span>
            {:else if selected}<span
                ><Check size={15} aria-hidden="true" />{tr(
                  "wizard.server.choice.selected",
                )}</span
              >
            {:else}<span
                >{tr(
                  mode.value === "partner"
                    ? "wizard.server.partner.explore"
                    : "wizard.server.choice.choose",
                )}<ArrowRight size={15} aria-hidden="true" /></span
              >{/if}
          </span>
        </button>
      {/each}
    </div>
  {/key}

  {#if branch === "new" && !managedRuntimeSelectable}
    <div
      class="availability-note"
      id="managed-runtime-availability"
      role="status"
      data-testid="managed-runtime-unavailable"
    >
      <p>{managedUnavailableReason}</p>
      {#if entitlementVerificationFailed}<button
          type="button"
          onclick={() => void loadFeatures(true)}
          disabled={$featuresLoading}
          data-testid="retry-entitlement-check"
          >{tr("wizard.server.managed.retry")}</button
        >{/if}
    </div>
  {/if}

  {#if shopping}
    <section
      class="path-details partner-workspace"
      aria-labelledby="node-path-heading"
    >
      <div class="detail-heading" bind:this={detailHeading}>
        <h3 id="node-path-heading">{tr("wizard.server.partner.next")}</h3>
        <p>{tr("wizard.server.partner.hint")}</p>
      </div>
      <div class="partner-directory">
        {#each managedProviders as provider (provider.value)}
          <a href={provider.website} target="_blank" rel="noopener noreferrer"
            ><BrandLogoScope domain={provider.domain}
              ><BrandLogoIcon
                class="h-9 w-9"
                fallbackLabel={provider.label}
              /></BrandLogoScope
            ><strong>{provider.label}</strong><span
              >{tr("wizard.server.partner.visit")}</span
            ><ArrowUpRight size={20} aria-hidden="true" /></a
          >
        {/each}
      </div>
      <button
        type="button"
        class="text-action"
        onclick={() => selectBranch("owned")}
        >{tr("wizard.server.partner.ready")}<ArrowRight
          size={16}
          aria-hidden="true"
        /></button
      >
    </section>
  {:else if pathReady}
    <section class="path-details" aria-labelledby="node-path-heading">
      <div class="detail-heading" bind:this={detailHeading}>
        <h3 id="node-path-heading">
          {tr(
            selectedEntry === "install-command"
              ? "wizard.server.next.command"
              : selectedEntry === "connect-remote"
                ? "wizard.server.next.remote"
                : "wizard.server.next.managed",
          )}
        </h3>
        {#if selectedEntry !== "kombify-cloud"}
          <button
            type="button"
            class="system-trigger"
            aria-expanded={systemDetailsOpen}
            aria-controls="node-system-settings"
            onclick={() => (systemDetailsOpen = !systemDetailsOpen)}
          >
            <SlidersHorizontal size={15} aria-hidden="true" />{tr(
              config.serverProvisioning.mode === "hypervisor"
                ? "wizard.server.system.proxmox"
                : "wizard.server.system.ubuntu",
            )}<span>{tr("wizard.server.choice.change")}</span>
          </button>
        {/if}
      </div>

      {#if selectedEntry === "kombify-cloud"}
        <ManagedServerOptions
          bind:config
          providers={visibleManagedProviders}
          onselect={selectManagedProvider}
        />
      {:else}
        {#if systemDetailsOpen}
          <div class="system-workspace" id="node-system-settings">
            <fieldset>
              <legend>{tr("wizard.server.system.title")}</legend>
              <p>{tr("wizard.server.system.hint")}</p>
              <div class="system-options">
                <label
                  ><input
                    type="radio"
                    name="node-system"
                    checked={config.serverProvisioning.mode !== "hypervisor"}
                    onchange={() => selectSystem("ubuntu")}
                  /><span
                    ><strong>{tr("wizard.server.system.ubuntu")}</strong><small
                      >{tr("wizard.server.system.ubuntuBody")}</small
                    ></span
                  ></label
                >
                <label
                  ><input
                    type="radio"
                    name="node-system"
                    checked={config.serverProvisioning.mode === "hypervisor"}
                    onchange={() => selectSystem("proxmox")}
                    data-testid="server-mode-hypervisor"
                  /><span
                    ><strong>{tr("wizard.server.system.proxmox")}</strong><small
                      >{tr("wizard.server.system.proxmoxBody")}</small
                    ></span
                  ></label
                >
              </div>
            </fieldset>
          </div>
        {/if}
        {#if config.serverProvisioning.mode === "hypervisor"}
          <p class="path-intro">{tr("wizard.server.system.proxmoxSequence")}</p>
          {#if reviewMode}<a class="text-action" href="/stacks/new"
              >{tr("wizard.preview.back")}</a
            >{:else}<HypervisorSelection
              bind:config
              deploymentId={page.params.id}
            />{/if}
        {:else if selectedEntry === "install-command"}
          <p class="path-intro">{tr("wizard.server.oneliner.info")}</p>
          <ul class="command-journey">
            <li>
              <span><ListChecks size={22} aria-hidden="true" /></span>
              <div>
                <strong
                  >{tr(
                    joinSurface
                      ? "wizard.server.command.join.review"
                      : "wizard.server.command.review",
                  )}</strong
                >
                <p>
                  {tr(
                    joinSurface
                      ? "wizard.server.command.join.reviewBody"
                      : "wizard.server.command.reviewBody",
                  )}
                </p>
              </div>
            </li>
            <li>
              <span><Terminal size={22} aria-hidden="true" /></span>
              <div>
                <strong>{tr("wizard.server.command.run")}</strong>
                <p>{tr("wizard.server.command.runBody")}</p>
              </div>
            </li>
            <li>
              <span><PlugZap size={22} aria-hidden="true" /></span>
              <div>
                <strong>{tr("wizard.server.command.connected")}</strong>
                <p>{tr("wizard.server.command.connectedBody")}</p>
              </div>
            </li>
          </ul>
        {:else}<RemoteServerConnection bind:config {reviewMode} />{/if}
      {/if}
    </section>
  {/if}
</div>

<style>
  .node-sources {
    container: node-sources / inline-size;
    min-width: 0;
  }
  .branch-choices {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 36px;
    margin-top: 10px;
  }
  .branch-choices button {
    display: flex;
    align-items: center;
    gap: 16px;
    text-align: start;
    padding: 18px 4px 20px;
    border-bottom: 2px solid var(--border);
    cursor: pointer;
    transition:
      color 180ms,
      border-color 180ms;
    color: var(--muted-foreground);
  }
  .branch-choices button[aria-pressed="true"] {
    color: var(--foreground);
    border-color: var(--primary);
  }
  .branch-choices button > :global(svg:first-child) {
    color: var(--primary);
  }
  .branch-choices button span {
    flex: 1;
  }
  .branch-choices strong {
    display: block;
    font-size: clamp(18px, 2cqi, 24px);
    font-weight: 550;
    letter-spacing: -0.025em;
  }
  .branch-choices small {
    display: block;
    color: var(--muted-foreground);
    font-size: 12px;
    line-height: 1.6;
    margin-top: 6px;
  }
  .branch-choices :global(.branch-arrow) {
    transition: transform 180ms;
  }
  .branch-choices button[aria-pressed="true"] :global(.branch-arrow) {
    transform: rotate(90deg);
    color: var(--primary);
  }
  .decision-path {
    width: 100%;
    height: 46px;
    display: block;
    color: var(--primary);
    opacity: 0.45;
  }
  .decision-path path {
    stroke: currentColor;
    stroke-width: 1.25;
    vector-effect: non-scaling-stroke;
  }
  .decision-path circle {
    fill: currentColor;
  }
  .source-options {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 24px;
    animation: choices-in 200ms ease-out;
  }
  .source-card {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(115px, 30%);
    grid-template-rows: 1fr auto;
    align-items: center;
    column-gap: 18px;
    text-align: start;
    padding: 22px 26px 18px;
    border: 1px solid var(--border);
    border-radius: var(--radius-panel, 18px);
    background: var(--card);
    min-height: 210px;
    overflow: hidden;
    cursor: pointer;
    transition:
      border-color 180ms,
      background 180ms,
      transform 180ms;
  }
  .source-card:hover:not(:disabled) {
    transform: translateY(-2px);
    border-color: color-mix(in oklch, var(--primary) 65%, var(--border));
  }
  .source-card.selected {
    border-color: var(--primary);
    background: color-mix(in oklch, var(--primary) 5%, var(--card));
  }
  .source-card:disabled {
    cursor: not-allowed;
    color: var(--muted-foreground);
  }
  .source-card:disabled .option-art {
    opacity: 0.45;
  }
  .option-copy {
    display: grid;
    gap: 12px;
  }
  .card-title {
    font-size: clamp(19px, 2cqi, 25px);
    font-weight: 550;
    letter-spacing: -0.025em;
    line-height: 1.2;
    text-wrap: balance;
  }
  .card-copy {
    color: var(--muted-foreground);
    font-size: 13px;
    line-height: 1.75;
    max-width: 37ch;
  }
  .option-art {
    display: block;
    margin-inline-end: -6px;
  }
  .card-action {
    grid-column: 1 / -1;
    padding-top: 20px;
    font-size: 12px;
    color: var(--primary);
  }
  .card-action > span {
    display: inline-flex;
    gap: 8px;
    align-items: center;
  }
  .source-card:disabled .card-action {
    color: var(--muted-foreground);
  }
  .path-details {
    margin-top: 32px;
    padding-top: 28px;
    border-top: 1px solid var(--border);
  }
  .detail-heading {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: center;
    gap: 14px 24px;
    scroll-margin-top: 110px;
    margin-bottom: 22px;
  }
  .detail-heading h3 {
    font-size: 20px;
    font-weight: 550;
    letter-spacing: -0.025em;
  }
  .system-trigger,
  .text-action {
    display: inline-flex;
    align-items: center;
    gap: 9px;
    font-size: 12px;
    cursor: pointer;
  }
  .system-trigger {
    color: var(--muted-foreground);
    padding-block: 8px;
  }
  .system-trigger span,
  .text-action {
    color: var(--primary);
  }
  .system-trigger span {
    text-decoration: underline;
    text-underline-offset: 4px;
    margin-inline-start: 5px;
  }
  .system-workspace {
    margin-bottom: 24px;
    animation: choices-in 180ms ease-out;
  }
  legend {
    font-size: 13px;
    font-weight: 550;
  }
  fieldset > p,
  .path-intro,
  .partner-workspace p {
    color: var(--muted-foreground);
    font-size: 13px;
    line-height: 1.7;
    max-width: 76ch;
    margin-top: 8px;
  }
  .system-options {
    display: flex;
    flex-wrap: wrap;
    gap: 24px 40px;
    padding-block: 18px;
  }
  .system-options label {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    cursor: pointer;
    max-width: 340px;
  }
  .system-options input {
    accent-color: var(--primary);
    margin-top: 3px;
  }
  .system-options strong {
    display: block;
    font-size: 13px;
    font-weight: 500;
  }
  .system-options small {
    display: block;
    color: var(--muted-foreground);
    font-size: 12px;
    line-height: 1.6;
    margin-top: 4px;
  }
  .command-journey {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 30px;
    padding-block: 22px 4px;
    list-style: none;
  }
  .command-journey li {
    display: flex;
    align-items: flex-start;
    gap: 15px;
  }
  .command-journey li > span {
    color: color-mix(in oklch, var(--primary) 60%, var(--muted-foreground));
    flex-shrink: 0;
  }
  .command-journey strong {
    font-size: 13px;
    font-weight: 550;
  }
  .command-journey p {
    font-size: 12px;
    line-height: 1.7;
    color: var(--muted-foreground);
    margin-top: 7px;
    max-width: 38ch;
  }
  .availability-note {
    margin-top: 17px;
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 6px 16px;
    font-size: 12px;
    line-height: 1.7;
    color: var(--muted-foreground);
  }
  .availability-note p {
    max-width: 110ch;
  }
  .availability-note button {
    color: var(--primary);
    text-decoration: underline;
    cursor: pointer;
  }
  .partner-workspace .detail-heading {
    display: block;
  }
  .partner-directory {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 36px;
    margin-block: 22px;
  }
  .partner-directory a {
    display: flex;
    align-items: center;
    gap: 14px;
    padding-block: 18px;
    border-bottom: 1px solid var(--border);
  }
  .partner-directory strong {
    font-size: 17px;
    font-weight: 550;
  }
  .partner-directory a > span {
    margin-inline-start: auto;
    font-size: 12px;
    color: var(--muted-foreground);
  }
  .partner-directory a > :global(svg) {
    color: var(--primary);
  }
  button:focus-visible,
  a:focus-visible,
  input:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 5px;
  }
  @keyframes choices-in {
    from {
      opacity: 0;
      transform: translateY(5px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .source-options,
    .system-workspace {
      animation: none;
    }
    button,
    .branch-choices :global(.branch-arrow) {
      transition: none;
    }
    .source-card:hover:not(:disabled) {
      transform: none;
    }
  }
  @container node-sources (max-width: 700px) {
    .branch-choices {
      gap: 22px;
    }
    .branch-choices button {
      align-items: flex-start;
      gap: 9px;
    }
    .branch-choices :global(.branch-arrow) {
      display: none;
    }
    .source-card {
      padding: 18px;
      grid-template-columns: 1fr;
    }
    .option-art {
      grid-row: 1;
      width: 150px;
      margin: auto;
    }
    .command-journey {
      grid-template-columns: 1fr;
      gap: 20px;
    }
    .partner-directory {
      grid-template-columns: 1fr;
      gap: 0;
    }
  }
  @container node-sources (max-width: 420px) {
    .branch-choices button > :global(svg:first-child) {
      display: none;
    }
    .branch-choices strong {
      font-size: 16px;
    }
    .branch-choices small {
      font-size: 11px;
    }
    .source-options {
      grid-template-columns: 1fr;
      gap: 14px;
    }
    .source-card {
      grid-template-columns: 1fr 90px;
      min-height: 185px;
    }
    .option-art {
      grid-row: auto;
      width: 100%;
    }
    .decision-path {
      height: 26px;
    }
  }
</style>
