<script lang="ts">
  import { onMount } from "svelte";
  import {
    ArrowUpRight,
    Check,
    Cpu,
    HardDrive,
    MemoryStick,
    MapPin,
  } from "@lucide/svelte";
  import BrandLogoScope from "../BrandLogoScope.svelte";
  import BrandLogoIcon from "../BrandLogoIcon.svelte";
  import {
    getMonthlyRuntimeOfferings,
    type MonthlyRuntimeOffering,
  } from "#lib/api/stacks.js";
  import {
    getIonosDatacenterChoices,
    type ManagedProviderID,
    type StackConfig,
  } from "#lib/wizard/index.js";
  import { managedProviders } from "#lib/wizard/managed-providers.js";
  import { tr } from "#lib/i18n.svelte.js";
  import { NODE_GUIDES } from "#lib/docs-links.js";
  import InfoTip from "./InfoTip.svelte";

  let {
    config = $bindable(),
    providers,
    onselect,
  }: {
    config: StackConfig;
    providers: ReadonlyArray<(typeof managedProviders)[number]>;
    onselect: (provider: ManagedProviderID) => void;
  } = $props();
  let offerings = $state<MonthlyRuntimeOffering[]>([]);
  let loading = $state(true);
  let loadFailed = $state(false);
  const provider = $derived(
    providers.find((item) => item.value === config.providerId),
  );
  const offering = $derived(
    offerings.find((item) => item.id === config.runtimeOfferingId),
  );
  const datacenters = getIonosDatacenterChoices();
  const region = $derived(
    config.providerId === "ionos"
      ? datacenters.find((item) => item.value === config.ionosDatacenter)?.label
      : offering?.region,
  );

  async function loadOfferings() {
    loading = true;
    loadFailed = false;
    try {
      offerings = await getMonthlyRuntimeOfferings();
    } catch {
      loadFailed = true;
    } finally {
      loading = false;
    }
  }
  onMount(() => {
    void loadOfferings();
  });
</script>

<div class="managed-options" data-testid="managed-provider-selector">
  <section class="server-summary">
    <header class="server-identity">
      {#if provider}
        <span class="provider-logo"
          ><BrandLogoScope domain={provider.domain}
            ><BrandLogoIcon
              class="h-9 w-9"
              fallbackLabel={provider.label}
            /></BrandLogoScope
          ></span
        >
      {/if}
      <div>
        <h4>
          {config.runtimeOfferingId === "monthly-runtime-premium"
            ? tr("wizard.server.managed.premium")
            : tr("wizard.server.managed.standard")}
        </h4>
        {#if provider}<a
            href={provider.website}
            target="_blank"
            rel="noopener noreferrer"
            >{tr("wizard.server.managed.poweredBy")}
            {provider.label}<ArrowUpRight size={13} aria-hidden="true" /></a
          >{/if}
      </div>
      <span class="included"
        ><Check size={13} aria-hidden="true" />{tr(
          "wizard.server.managed.available",
        )}</span
      >
    </header>
    <p class="description">{tr("wizard.server.cloud.info")}</p>
    {#if loading}
      <p class="loading" role="status">
        {tr("wizard.server.managed.loadingSpecs")}
      </p>
    {:else if offering}
      <dl class="server-specs">
        {#if offering.vcpus}<div>
            <dt><Cpu size={15} aria-hidden="true" />vCPU</dt>
            <dd>{offering.vcpus}</dd>
          </div>{/if}
        {#if offering.memory_mb}<div>
            <dt><MemoryStick size={15} aria-hidden="true" />RAM</dt>
            <dd>{tr("ui.managed.sizeGiB", { size: offering.memory_mb / 1024 })}</dd>
          </div>{/if}
        {#if offering.disk_gb}<div>
            <dt>
              <HardDrive size={15} aria-hidden="true" />{tr(
                "wizard.server.hypervisor.disk",
              )}
            </dt>
            <dd>{tr("ui.managed.sizeGB", { size: offering.disk_gb })}</dd>
          </div>{/if}
        {#if region}<div>
            <dt>
              <MapPin size={15} aria-hidden="true" />{tr(
                "wizard.server.managed.location",
              )}
            </dt>
            <dd>{region}</dd>
          </div>{/if}
      </dl>
    {:else if loadFailed}
      <p class="loading" role="status">
        {tr("wizard.server.managed.specsUnavailable")}
        <button type="button" onclick={() => void loadOfferings()}
          >{tr("wizard.server.managed.retry")}</button
        >
      </p>
    {/if}
  </section>

  <section class="provider-settings">
    <div class="provider-content">
      <div class="provider-copy">
        <h4 class="tip-heading">
          {tr("wizard.server.managed.provider")}<InfoTip
            topic={tr("wizard.server.managed.provider")}
            text={tr("wizard.tip.managedProvider")}
            docs={NODE_GUIDES.managedProviders}
          />
        </h4>
        <p>
          {tr(
            providers.length > 1
              ? "wizard.server.managed.providerChoice"
              : "wizard.server.managed.providerAssigned",
          )}
        </p>
      </div>
      <fieldset class="providers">
        <legend class="sr-only">{tr("wizard.server.managed.provider")}</legend>
        {#each managedProviders as item (item.value)}
          {@const available = providers.some(
            (provider) => provider.value === item.value,
          )}
          <label
            data-testid={`managed-provider-${item.value}`}
            class:unavailable={!available}
          >
            <input
              type="radio"
              name="managed-provider"
              value={item.value}
              checked={available && config.providerId === item.value}
              disabled={!available}
              onchange={() => onselect(item.value)}
            />
            <BrandLogoScope domain={item.domain}
              ><BrandLogoIcon
                class="h-6 w-6"
                fallbackLabel={item.label}
              /></BrandLogoScope
            >
            <span
              ><strong>{item.label}</strong>
              {#if !available}<small
                  >{tr("wizard.server.managed.providerUnavailable")}</small
                >{/if}
            </span>
          </label>
        {/each}
      </fieldset>
      <p class="scope-note">
        {tr("wizard.server.managed.moreOptionsPending")}
      </p>
    </div>
  </section>
</div>

<style>
  .managed-options {
    display: grid;
    gap: 28px;
    padding-block: 4px 8px;
  }
  .server-summary {
    display: grid;
    gap: 18px;
  }
  .server-identity {
    display: flex;
    gap: 14px;
    flex-wrap: wrap;
    align-items: center;
  }
  .provider-logo {
    display: grid;
    place-items: center;
    width: 42px;
    height: 42px;
  }
  .tip-heading {
    display: flex;
    align-items: center;
    gap: 2px;
  }
  h4 {
    font-size: 17px;
    font-weight: 550;
    letter-spacing: -0.02em;
  }
  .server-identity a {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 12px;
    color: var(--muted-foreground);
    margin-top: 4px;
    text-underline-offset: 3px;
  }
  .server-identity a:hover {
    color: var(--foreground);
    text-decoration: underline;
  }
  .included {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-inline-start: auto;
    font-size: 11px;
    color: var(--primary);
  }
  .description,
  .provider-content p {
    font-size: 13px;
    line-height: 1.65;
    color: var(--muted-foreground);
    max-width: 70ch;
  }
  .server-specs {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
    border-block: 1px solid var(--border);
  }
  .server-specs div {
    position: relative;
    padding: 17px 20px 17px 0;
  }
  .server-specs div + div {
    padding-inline-start: 20px;
  }
  .server-specs div + div::before {
    content: "";
    position: absolute;
    inset-block: 15px;
    inset-inline-start: 0;
    width: 1px;
    background: var(--border);
  }
  dt {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--muted-foreground);
    font-size: 11px;
  }
  dd {
    font-size: 18px;
    font-weight: 550;
    margin-top: 8px;
  }
  .provider-settings {
    display: grid;
    gap: 22px;
  }
  .provider-content {
    display: grid;
    gap: 20px;
  }
  .provider-copy h4 {
    font-size: 14px;
  }
  .provider-copy p {
    margin-top: 5px;
  }
  .providers {
    display: flex;
    flex-wrap: wrap;
    gap: 22px;
    border-bottom: 1px solid var(--border);
  }
  .providers label {
    display: flex;
    align-items: center;
    gap: 9px;
    min-height: 42px;
    padding: 0 2px 10px;
    color: var(--muted-foreground);
    cursor: pointer;
  }
  .providers input {
    accent-color: var(--primary);
  }
  .providers label:has(input:checked) {
    color: var(--foreground);
  }
  .providers label.unavailable {
    cursor: not-allowed;
  }
  .providers small {
    display: block;
    font-size: 11px;
    margin-top: 3px;
  }
  .providers strong {
    font-size: 13px;
  }
  .providers label > :global(svg:last-child) {
    color: var(--primary);
  }
  .scope-note {
    padding-top: 2px;
  }
  .loading {
    color: var(--muted-foreground);
    font-size: 12px;
    padding-block: 12px;
  }
  .loading button {
    color: var(--primary);
    text-decoration: underline;
    text-underline-offset: 3px;
  }
  button:focus-visible,
  a:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 4px;
  }
  .providers input:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 4px;
  }
  @container (max-width: 660px) {
    .included {
      width: 100%;
      margin-inline-start: 56px;
    }
    .server-specs {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .server-specs div:nth-child(odd) {
      padding-inline-start: 0;
    }
    .server-specs div:nth-child(odd)::before {
      display: none;
    }
    .server-specs div:nth-child(n + 3) {
      border-top: 1px solid var(--border);
    }
  }
</style>
