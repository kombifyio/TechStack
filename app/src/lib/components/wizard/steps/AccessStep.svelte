<script lang="ts">
  import { tick } from "svelte";
  import { ChevronDown, LockKeyhole, RadioTower } from "@lucide/svelte";
  import AccessRouteIllustration from "../AccessRouteIllustration.svelte";
  import type {
    AccessModeValue,
    BundleChoiceDefinition,
    StackConfig,
  } from "#lib/wizard/index.js";
  import { tr } from "#lib/i18n.svelte.js";
  import { revealWizardDetails } from "#lib/wizard/reveal-details.js";

  interface Props {
    config: StackConfig;
    accessQuestions?: BundleChoiceDefinition[];
    vpnQuestions?: BundleChoiceDefinition[];
    onAccessModeSelect: (value: AccessModeValue) => void;
    onDomainBaseChange?: (value: string) => void;
  }

  let { config, onAccessModeSelect, onDomainBaseChange }: Props = $props();
  let detailsOpen = $state(false);
  let activeTab = $state<"address" | "connection" | "publication">("address");
  let detailsElement = $state<HTMLElement>();
  const tabs = ["address", "connection", "publication"] as const;
  const choices = ["home", "anywhere"] as const;
  const managedCloud = $derived(
    config.serverProvisioning.mode === "kombify-cloud",
  );
  const routeDisplayMode = $derived(
    managedCloud ? "anywhere" : config.network.accessMode,
  );
  const contextGoal = $derived(
    (["photos", "files", "vault", "media", "smart-home"] as const).find(
      (goal) => config.goals?.[goal],
    ) ?? "default",
  );
  const domainBase = $derived(
    (
      config.network as typeof config.network & {
        domainBase?: string;
      }
    ).domainBase ?? "",
  );
  let addressMode = $state<"automatic" | "custom">("automatic");

  $effect(() => {
    if (domainBase.trim()) addressMode = "custom";
  });

  async function revealDetails() {
    await tick();
    revealWizardDetails(
      detailsElement?.querySelector<HTMLElement>('[role="tabpanel"]'),
    );
  }

  async function toggleDetails() {
    detailsOpen = !detailsOpen;
    if (detailsOpen) await revealDetails();
  }

  async function selectTab(tab: (typeof tabs)[number]) {
    activeTab = tab;
    await revealDetails();
  }

  async function moveTab(event: KeyboardEvent) {
    if (
      event.key !== "ArrowLeft" &&
      event.key !== "ArrowRight" &&
      event.key !== "Home" &&
      event.key !== "End"
    )
      return;
    event.preventDefault();
    const current = tabs.indexOf(activeTab);
    const next =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? tabs.length - 1
          : (current + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) %
            tabs.length;
    activeTab = tabs[next];
    requestAnimationFrame(() =>
      document.getElementById(`access-tab-${activeTab}`)?.focus(),
    );
    await revealDetails();
  }
</script>

<div class="step" data-testid="easy-step-3">
  <header class="heading">
    <p class="eyebrow">{tr("wizard.access.eyebrow")}</p>
    <h2>{tr("wizard.access.heading")}</h2>
    <p class="intro">{tr("wizard.access.intro")}</p>
  </header>

  <aside class="use-case-guidance">
    <strong>{tr("wizard.access.forYou")}</strong>
    <p>{tr(`wizard.access.context.${contextGoal}`)}</p>
  </aside>

  <fieldset class="choice-grid">
    <legend class="sr-only">{tr("wizard.access.heading")}</legend>
    {#each choices as mode (mode)}
      <label
        class:selected={config.network.accessMode === mode}
        class:disabled={managedCloud && mode === "home"}
        class="choice"
        data-testid={`easy-access-${mode}`}
      >
        <input
          class="sr-only"
          type="radio"
          name="access"
          value={mode}
          checked={config.network.accessMode === mode}
          disabled={managedCloud && mode === "home"}
          onchange={() => onAccessModeSelect(mode)}
        />
        <span class="choice-copy">
          <span class="choice-title">{tr(`wizard.access.${mode}.label`)}</span>
          <span class="choice-body"
            >{tr(
              managedCloud && mode === "anywhere"
                ? "wizard.access.anywhere.managedBody"
                : `wizard.access.${mode}.body`,
            )}</span
          >
          {#if managedCloud && mode === "home"}
            <span class="choice-reason"
              >{tr("wizard.access.home.managedReason")}</span
            >
          {:else if managedCloud && mode === "anywhere"}
            <span class="choice-badge">{tr("wizard.access.recommended")}</span>
          {/if}
        </span>
        <span class="choice-visual"><AccessRouteIllustration {mode} /></span>
        <span class="radio-dot" aria-hidden="true"><span></span></span>
      </label>
    {/each}
  </fieldset>

  <section class="route-summary" aria-labelledby="access-route-title">
    <div class="route-visual">
      <AccessRouteIllustration mode={routeDisplayMode} />
    </div>
    <div class="route-copy">
      <p class="route-kicker">{tr("wizard.access.route.planned")}</p>
      <h3 id="access-route-title">
        {tr(`wizard.access.route.${routeDisplayMode}`)}
      </h3>
      <p>{tr(`wizard.access.route.${routeDisplayMode}Note`)}</p>
    </div>
    <div class="tool-row">
      <span class="tool-icon"><LockKeyhole size={17} aria-hidden="true" /></span
      >
      <span
        ><small>{tr("wizard.access.route.tool")}</small><strong
          >{tr(`wizard.access.route.${routeDisplayMode}Tool`)}</strong
        ></span
      >
    </div>
  </section>

  <section class="details">
    <button
      type="button"
      class="details-trigger"
      aria-expanded={detailsOpen}
      aria-controls="access-details"
      onclick={toggleDetails}
    >
      <RadioTower size={16} aria-hidden="true" /><span
        >{tr("wizard.access.customize")}</span
      ><ChevronDown
        class={detailsOpen ? "rotated" : ""}
        size={16}
        aria-hidden="true"
      />
    </button>
    {#if detailsOpen}
      <div
        id="access-details"
        class="details-content"
        bind:this={detailsElement}
      >
        <div
          class="tabs"
          role="tablist"
          aria-label={tr("wizard.access.details.label")}
        >
          {#each tabs as tab (tab)}
            <button
              id={`access-tab-${tab}`}
              type="button"
              role="tab"
              aria-selected={activeTab === tab}
              aria-controls={`access-panel-${tab}`}
              tabindex={activeTab === tab ? 0 : -1}
              onclick={() => selectTab(tab)}
              onkeydown={moveTab}>{tr(`wizard.access.details.${tab}`)}</button
            >
          {/each}
        </div>
        {#if activeTab === "address"}
          <div
            id="access-panel-address"
            class="tab-panel address-panel"
            role="tabpanel"
            aria-labelledby="access-tab-address"
          >
            <h3>{tr("wizard.access.address.title")}</h3>
            <fieldset class="address-options">
              <label>
                <input
                  type="radio"
                  name="access-address-mode"
                  value="automatic"
                  checked={addressMode === "automatic"}
                  onchange={() => {
                    addressMode = "automatic";
                    onDomainBaseChange?.("");
                  }}
                />
                <span
                  ><strong>{tr("wizard.access.address.automatic")}</strong
                  ><small>{tr("wizard.access.address.automaticBody")}</small
                  ></span
                >
              </label>
              <label>
                <input
                  type="radio"
                  name="access-address-mode"
                  value="custom"
                  disabled={managedCloud}
                  checked={addressMode === "custom"}
                  onchange={() => (addressMode = "custom")}
                />
                <span
                  ><strong>{tr("wizard.access.address.custom")}</strong><small
                    >{tr(
                      managedCloud
                        ? "wizard.access.address.managedBody"
                        : "wizard.access.address.customBody",
                    )}</small
                  ></span
                >
              </label>
            </fieldset>
            {#if addressMode === "custom"}
              <label class="domain-field">
                <span>{tr("wizard.access.address.domainLabel")}</span>
                <input
                  type="text"
                  inputmode="url"
                  autocapitalize="none"
                  spellcheck="false"
                  value={domainBase}
                  placeholder={tr("wizard.access.address.domainPlaceholder")}
                  oninput={(event) =>
                    onDomainBaseChange?.(event.currentTarget.value)}
                />
              </label>
              <div class="prerequisites">
                <strong>{tr("wizard.access.address.prerequisites")}</strong>
                <p>{tr("wizard.access.address.prerequisitesBody")}</p>
              </div>
            {/if}
          </div>
        {:else if activeTab === "connection"}
          <div
            id="access-panel-connection"
            class="tab-panel"
            role="tabpanel"
            aria-labelledby="access-tab-connection"
          >
            <h3>
              {tr(`wizard.access.connection.${routeDisplayMode}Title`)}
            </h3>
            <p>
              {tr(`wizard.access.connection.${routeDisplayMode}Body`)}
            </p>
          </div>
        {:else}
          <div
            id="access-panel-publication"
            class="tab-panel"
            role="tabpanel"
            aria-labelledby="access-tab-publication"
          >
            <h3>
              {tr(
                `wizard.access.publication.${config.network.publicAccess ? "pending" : "private"}Title`,
              )}
            </h3>
            <p>
              {tr(
                `wizard.access.publication.${config.network.publicAccess ? "pending" : "private"}Body`,
              )}
            </p>
          </div>
        {/if}
      </div>
    {/if}
  </section>
</div>

<style>
  .use-case-guidance {
    border-left: 2px solid var(--primary);
    padding-left: 16px;
    max-width: 76ch;
  }
  .use-case-guidance strong {
    color: var(--primary);
    font-size: 12px;
    font-weight: 600;
  }
  .use-case-guidance p {
    color: var(--muted-foreground);
    font-size: 14px;
    line-height: 1.7;
    margin-top: 6px;
  }
  .step {
    display: grid;
    gap: 32px;
  }
  .heading {
    padding-block: 14px 2px;
  }
  .eyebrow {
    margin-bottom: 9px;
    color: var(--primary);
    font-size: 10px;
    font-weight: 650;
    letter-spacing: 0.12em;
    text-transform: uppercase;
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
    margin-top: 14px;
    color: var(--muted-foreground);
    font-size: 14px;
    line-height: 1.75;
  }
  .choice-grid {
    display: grid;
    gap: 14px;
    padding: 0;
    border: 0;
  }
  .choice {
    position: relative;
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(150px, 220px);
    min-height: 176px;
    overflow: hidden;
    border: 1px solid var(--border);
    border-radius: 16px;
    background: var(--card);
    cursor: pointer;
    transition:
      border-color 160ms ease,
      background 160ms ease,
      box-shadow 160ms ease;
  }
  .choice:hover {
    border-color: color-mix(in oklch, var(--primary) 42%, var(--border));
  }
  .choice.disabled {
    cursor: not-allowed;
    opacity: 0.62;
  }
  .choice.disabled:hover {
    border-color: var(--border);
  }
  .choice:focus-within {
    outline: 2px solid var(--ring);
    outline-offset: 3px;
  }
  .choice.selected {
    border-color: color-mix(in oklch, var(--primary) 70%, var(--border));
    background: color-mix(in oklch, var(--primary) 4%, var(--card));
    box-shadow: 0 1px 3px color-mix(in oklch, var(--foreground) 8%, transparent);
  }
  .choice-copy {
    align-self: center;
    padding: 28px 18px 28px 26px;
  }
  .choice-title,
  .choice-body {
    display: block;
  }
  .choice-reason {
    display: block;
    margin-top: 9px;
    color: var(--muted-foreground);
    font-size: 11px;
    line-height: 1.5;
  }
  .choice-badge {
    display: inline-flex;
    margin-top: 12px;
    padding: 4px 8px;
    border: 1px solid color-mix(in oklch, var(--primary) 24%, var(--border));
    border-radius: 999px;
    color: var(--primary);
    background: color-mix(in oklch, var(--primary) 6%, var(--card));
    font-size: 10px;
    font-weight: 650;
  }
  .choice-title {
    font-size: 18px;
    font-weight: 620;
    letter-spacing: -0.015em;
  }
  .choice-body {
    max-width: 39ch;
    margin-top: 8px;
    color: var(--muted-foreground);
    font-size: 13px;
    line-height: 1.6;
  }
  .choice-visual {
    align-self: center;
    padding: 14px 22px 14px 0;
    opacity: 0.82;
  }
  .radio-dot {
    position: absolute;
    top: 18px;
    right: 18px;
    width: 20px;
    height: 20px;
    padding: 4px;
    border: 1.5px solid var(--border);
    border-radius: 999px;
    background: var(--card);
  }
  .selected .radio-dot {
    border-color: var(--primary);
  }
  .selected .radio-dot span {
    display: block;
    width: 100%;
    height: 100%;
    border-radius: inherit;
    background: var(--primary);
  }
  .route-summary {
    display: grid;
    grid-template-columns: minmax(120px, 180px) minmax(0, 1fr) minmax(
        190px,
        auto
      );
    align-items: center;
    gap: 24px;
    padding-block: 22px;
    border-block: 1px solid var(--border);
  }
  .route-visual {
    opacity: 0.72;
  }
  .route-kicker {
    margin-bottom: 6px;
    color: var(--primary);
    font-size: 10px;
    font-weight: 650;
    letter-spacing: 0.1em;
    text-transform: uppercase;
  }
  .route-copy h3 {
    font-size: 15px;
    font-weight: 620;
  }
  .route-copy > p:last-child {
    margin-top: 6px;
    color: var(--muted-foreground);
    font-size: 12px;
    line-height: 1.55;
  }
  .tool-row {
    display: flex;
    align-items: center;
    gap: 11px;
    min-width: 0;
  }
  .tool-icon {
    display: grid;
    width: 36px;
    height: 36px;
    place-items: center;
    border: 1px solid color-mix(in oklch, var(--primary) 22%, var(--border));
    border-radius: 10px;
    color: var(--primary);
    background: color-mix(in oklch, var(--primary) 6%, var(--card));
  }
  .tool-row small,
  .tool-row strong {
    display: block;
  }
  .tool-row small {
    color: var(--muted-foreground);
    font-size: 10px;
  }
  .tool-row strong {
    margin-top: 2px;
    font-size: 12px;
    font-weight: 600;
  }
  .details {
    border-top: 1px solid var(--border);
    padding-top: 18px;
  }
  .details-trigger {
    display: flex;
    align-items: center;
    gap: 9px;
    color: var(--muted-foreground);
    font-size: 12px;
    cursor: pointer;
  }
  .details-trigger :global(svg:last-child) {
    margin-left: 2px;
    transition: transform 160ms ease;
  }
  .details-trigger :global(svg.rotated) {
    transform: rotate(180deg);
  }
  .details-trigger:focus-visible,
  .tabs button:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 4px;
  }
  .details-content {
    padding-top: 22px;
  }
  .tabs {
    display: flex;
    gap: 24px;
    border-bottom: 1px solid var(--border);
  }
  .tabs button {
    position: relative;
    padding: 0 1px 10px;
    color: var(--muted-foreground);
    font-size: 12px;
    cursor: pointer;
  }
  .tabs button[aria-selected="true"] {
    color: var(--foreground);
    font-weight: 600;
  }
  .tabs button[aria-selected="true"]::after {
    position: absolute;
    right: 0;
    bottom: -1px;
    left: 0;
    height: 2px;
    border-radius: 2px;
    background: var(--primary);
    content: "";
  }
  .tab-panel {
    padding: 20px 0 2px;
  }
  .tab-panel h3 {
    font-size: 14px;
    font-weight: 620;
  }
  .tab-panel p {
    max-width: 68ch;
    margin-top: 7px;
    color: var(--muted-foreground);
    font-size: 13px;
    line-height: 1.65;
  }
  .address-options {
    display: grid;
    gap: 0;
    max-width: 640px;
    margin-top: 14px;
    padding: 0;
    border-block: 1px solid var(--border);
  }
  .address-options label {
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr);
    gap: 10px;
    align-items: start;
    padding: 13px 2px;
    border-bottom: 1px solid var(--border);
    cursor: pointer;
  }
  .address-options label:last-child {
    border-bottom: 0;
  }
  .address-options input {
    margin-top: 2px;
    accent-color: var(--primary);
  }
  .address-options strong,
  .address-options small {
    display: block;
  }
  .address-options strong {
    font-size: 12px;
    font-weight: 620;
  }
  .address-options small {
    margin-top: 3px;
    color: var(--muted-foreground);
    font-size: 11px;
    line-height: 1.5;
  }
  .domain-field {
    display: grid;
    gap: 7px;
    max-width: 520px;
    margin-top: 18px;
    color: var(--muted-foreground);
    font-size: 11px;
    font-weight: 600;
  }
  .domain-field input {
    min-height: 40px;
    padding: 8px 11px;
    border: 1px solid var(--border);
    border-radius: 9px;
    color: var(--foreground);
    background: var(--input);
    font-size: 13px;
    font-weight: 400;
  }
  .domain-field input:focus {
    border-color: var(--ring);
    outline: 2px solid color-mix(in oklch, var(--ring) 22%, transparent);
  }
  .prerequisites {
    max-width: 640px;
    margin-top: 16px;
    padding-left: 13px;
    border-left: 2px solid
      color-mix(in oklch, var(--primary) 32%, var(--border));
  }
  .prerequisites strong {
    font-size: 11px;
    font-weight: 650;
  }
  .prerequisites p {
    margin-top: 4px;
    font-size: 11px;
  }
  @media (min-width: 720px) {
    .choice-grid {
      grid-template-columns: 1fr 1fr;
    }
    .choice {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto 125px;
    }
    .choice-visual {
      padding: 0 28px 14px;
    }
  }
  @media (max-width: 700px) {
    .route-summary {
      grid-template-columns: 100px minmax(0, 1fr);
    }
    .tool-row {
      grid-column: 1 / -1;
    }
  }
  @media (max-width: 520px) {
    .choice {
      grid-template-columns: 1fr;
    }
    .choice-visual {
      padding: 0 24px 14px;
    }
    .route-summary {
      grid-template-columns: 1fr;
    }
    .route-visual {
      max-width: 180px;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .choice,
    .details-trigger :global(svg:last-child) {
      transition: none;
    }
  }
</style>
