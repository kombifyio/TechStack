<script lang="ts">
  import { Check, Clock, Info } from "@lucide/svelte";
  import BrandLogoIcon from "../BrandLogoIcon.svelte";
  import BrandLogoScope from "../BrandLogoScope.svelte";
  import { brandDomainForTool } from "#lib/brand-logo.js";
  import {
    installsService,
    maturityLabelled,
    type UseCaseCatalogComponent,
  } from "#lib/api/useCaseCatalog.js";
  import { tr } from "#lib/i18n.svelte.js";

  let {
    components,
    selectedId,
    disabled = false,
    disabledIds = [],
    label,
    compact = false,
    summary = false,
    describe,
    savedForLater = {},
    openLabel = "",
    onopen,
    onselect,
    oninfo,
  }: {
    components: UseCaseCatalogComponent[];
    selectedId?: string;
    disabled?: boolean;
    disabledIds?: string[];
    label: string;
    /** The closed card's app stack instead of the full picker. */
    compact?: boolean;
    /**
     * The expanded card with a guided setup: the chosen app with its
     * description, and the apps to switch to as one compact column, so the
     * setup steps below keep the full card width.
     */
    summary?: boolean;
    /** The chosen app's description in the summary. */
    describe?: (component: UseCaseCatalogComponent) => string;
    /** Tooltip per component id for apps that are recorded but not installed yet. */
    savedForLater?: Record<string, string>;
    /** Accessible action of the compact stack (it opens the expanded card). */
    openLabel?: string;
    onopen?: () => void;
    onselect: (id: string) => void;
    oninfo: (
      component: UseCaseCatalogComponent,
      trigger: HTMLButtonElement,
    ) => void;
  } = $props();

  const MAX_AVATARS = 4;
  const chosen = $derived(
    components.find((component) => component.id === selectedId) ??
      components[0],
  );
  // Chosen app first, then the others in catalog order.
  const stack = $derived(
    chosen
      ? [chosen, ...components.filter((component) => component !== chosen)]
      : [],
  );
  const others = $derived(Math.max(stack.length - 1, 0));
  const chosenLater = $derived(chosen ? laterLabel(chosen) : undefined);

  function laterLabel(component: UseCaseCatalogComponent) {
    return savedForLater[component.id];
  }

  function roleLabel(component: UseCaseCatalogComponent) {
    const status = maturityLabelled(component.maturity)
      ? ` · ${tr(`wizard.status.${component.maturity}`)}`
      : "";
    if (component.role === "primary")
      return `${tr("wizard.preview.defaultService")}${status}`;
    const role = tr("wizard.preview.alternative");
    return installsService(component)
      ? `${role}${status}`
      : `${role} · ${tr("wizard.preview.savedForLater")}`;
  }
</script>

{#if compact}
  <!-- Closed card: an avatar stack of the use case's apps, chosen app first,
       then the alternatives. Choosing happens in the expanded card. -->
  <button
    type="button"
    class="app-stack"
    disabled={disabled || !onopen}
    aria-label={`${label}: ${chosen?.name ?? ""}${others > 0 ? ` +${others}` : ""}${chosenLater ? ` · ${chosenLater}` : ""} · ${openLabel}`}
    title={openLabel}
    onclick={() => onopen?.()}
  >
    <span class="avatars" aria-hidden="true">
      {#each stack.slice(0, MAX_AVATARS) as component (component.id)}
        {@const later = laterLabel(component)}
        <span
          class="avatar"
          class:unselected={component.id !== chosen?.id}
          title={later ? `${component.name} · ${later}` : component.name}
          ><BrandLogoScope
            domain={brandDomainForTool(component.id, component.name)}
            ><BrandLogoIcon
              class="h-full w-full"
              circular
              fallbackLabel={component.name}
            /></BrandLogoScope
          >{#if later}<span class="later-badge"
              ><Clock size={9} strokeWidth={2.75} /></span
            >{/if}</span
        >
      {/each}
    </span>
    <span class="stack-name">{chosen?.name}</span>
    {#if others > 0}<span class="stack-more" aria-hidden="true">+{others}</span
      >{/if}
  </button>
{:else if summary && chosen}
  <div class="service-summary">
    <div class="summary-chosen">
      <span class="summary-mark"
        ><BrandLogoScope domain={brandDomainForTool(chosen.id, chosen.name)}
          ><BrandLogoIcon
            class="h-7 w-7"
            fallbackLabel={chosen.name}
          /></BrandLogoScope
        ></span
      >
      <div class="summary-copy">
        <p class="summary-name">
          <strong>{chosen.name}</strong><small>{roleLabel(chosen)}</small>
        </p>
        {#if describe}<p class="summary-desc">{describe(chosen)}</p>{/if}
      </div>
      <button
        type="button"
        class="service-info"
        aria-label={`${tr("wizard.preview.aboutService")} ${chosen.name}`}
        onclick={(event) => oninfo(chosen, event.currentTarget)}
        title={`${tr("wizard.preview.aboutService")} ${chosen.name}`}
      >
        <Info size={16} aria-hidden="true" />
      </button>
    </div>
    {#if components.length > 1}
      <div class="summary-switch" role="group" aria-label={label}>
        {#each components as component (component.id)}
          <button
            type="button"
            class="switch-option"
            class:chosen={component.id === chosen.id}
            disabled={disabled || disabledIds.includes(component.id)}
            aria-pressed={component.id === chosen.id}
            aria-label={`${component.name} ${roleLabel(component)}`}
            title={`${component.name} · ${roleLabel(component)}`}
            onclick={() => onselect(component.id)}
          >
            <span class="switch-mark"
              ><BrandLogoScope
                domain={brandDomainForTool(component.id, component.name)}
                ><BrandLogoIcon
                  class="h-4 w-4"
                  fallbackLabel={component.name}
                /></BrandLogoScope
              ></span
            >
            <span class="switch-name">{component.name}</span>
            {#if laterLabel(component)}<Clock
                size={12}
                class="switch-later"
                aria-hidden="true"
              />{/if}
            {#if component.id === chosen.id}<Check
                size={14}
                class="service-check"
                aria-hidden="true"
              />{/if}
          </button>
        {/each}
      </div>
    {/if}
  </div>
{:else}
  <div class="service-picker" role="group" aria-label={label}>
    {#each components as component (component.id)}
      <div class="service-option" class:chosen={component.id === selectedId}>
        <button
          type="button"
          class="service-choice"
          disabled={disabled || disabledIds.includes(component.id)}
          aria-pressed={component.id === selectedId}
          onclick={() => onselect(component.id)}
        >
          <span class="service-mark"
            ><BrandLogoScope
              domain={brandDomainForTool(component.id, component.name)}
              ><BrandLogoIcon
                class="h-5 w-5"
                fallbackLabel={component.name}
              /></BrandLogoScope
            ></span
          >
          <span class="service-identity"
            ><strong>{component.name}</strong><small
              data-testid={`service-status-${component.id}`}
              data-maturity={component.maturity ?? "unknown"}
              >{roleLabel(component)}</small
            ></span
          >
          {#if component.id === selectedId}<Check
              size={15}
              class="service-check"
              aria-hidden="true"
            />{/if}
        </button>
        <button
          type="button"
          class="service-info"
          aria-label={`${tr("wizard.preview.aboutService")} ${component.name}`}
          onclick={(event) => oninfo(component, event.currentTarget)}
          title={`${tr("wizard.preview.aboutService")} ${component.name}`}
        >
          <Info size={16} aria-hidden="true" />
        </button>
      </div>
    {/each}
  </div>
{/if}

<style>
  .app-stack {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    min-width: 0;
    height: 42px;
    padding: 0;
    text-align: start;
    font-size: 12px;
    font-weight: 600;
    border-radius: 999px;
  }
  .avatars {
    display: flex;
    flex: none;
    align-items: center;
  }
  .avatar {
    position: relative;
    display: grid;
    place-items: center;
    width: 34px;
    height: 34px;
    border-radius: 50%;
    background: var(--background);
    /* The ring in the card color separates the overlapping avatars. */
    box-shadow: 0 0 0 2px var(--card);
  }
  .avatar + .avatar {
    margin-inline-start: -9px;
  }
  .avatar.unselected {
    filter: grayscale(1);
    opacity: 0.45;
  }
  .avatar:first-child {
    z-index: 4;
  }
  .avatar:nth-child(2) {
    z-index: 3;
  }
  .avatar:nth-child(3) {
    z-index: 2;
  }
  .later-badge {
    position: absolute;
    right: -3px;
    bottom: -3px;
    display: grid;
    place-items: center;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    color: var(--background);
    background: var(--muted-foreground);
    box-shadow: 0 0 0 2px var(--card);
  }
  .stack-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .stack-more {
    flex: none;
    color: var(--muted-foreground);
    font-weight: 500;
    font-variant-numeric: tabular-nums;
  }
  .service-summary {
    display: grid;
    grid-template-columns: minmax(0, 1.3fr) minmax(150px, 1fr);
    gap: 14px;
    align-items: start;
    min-width: 0;
  }
  .service-summary:not(:has(.summary-switch)) {
    grid-template-columns: minmax(0, 1fr);
  }
  .summary-chosen {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    min-width: 0;
  }
  .summary-mark {
    display: grid;
    place-items: center;
    width: 40px;
    height: 40px;
    flex-shrink: 0;
    background: var(--background);
    border: 1px solid var(--border);
    border-radius: 11px;
  }
  .summary-copy {
    display: grid;
    gap: 3px;
    min-width: 0;
  }
  .summary-name {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 2px 8px;
    margin: 0;
  }
  .summary-name strong {
    font-size: 15px;
  }
  .summary-desc {
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    overflow: hidden;
    margin: 0;
    font-size: 12.5px;
    line-height: 1.45;
    color: var(--muted-foreground);
  }
  .service-summary .service-info {
    min-height: 26px;
    height: 26px;
  }
  .summary-switch {
    display: flex;
    flex-wrap: wrap;
    align-content: flex-start;
    gap: 5px;
    min-width: 0;
  }
  .switch-option {
    display: flex;
    align-items: center;
    gap: 7px;
    min-width: 0;
    max-width: 100%;
    height: 27px;
    padding: 0 8px 0 3px;
    text-align: start;
    font-size: 12px;
    font-weight: 600;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--card);
  }
  .switch-option.chosen {
    border-color: color-mix(in oklch, var(--primary) 45%, var(--border));
    background: color-mix(in oklch, var(--primary) 6%, var(--card));
  }
  .switch-mark {
    display: grid;
    place-items: center;
    width: 21px;
    height: 21px;
    flex-shrink: 0;
    background: var(--background);
    border-radius: 50%;
  }
  .switch-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  :global(.switch-later) {
    flex-shrink: 0;
    color: var(--muted-foreground);
  }
  @container creation-goals (max-width: 620px) {
    .service-summary {
      grid-template-columns: minmax(0, 1fr);
      gap: 10px;
    }
  }
  .service-picker {
    display: flex;
    flex-wrap: wrap;
    gap: 7px;
    min-width: 0;
  }
  .service-option {
    display: flex;
    flex: 1 1 150px;
    min-width: 0;
    border: 1px solid var(--border);
    border-radius: var(--radius-control, 10px);
    background: var(--card);
  }
  .service-option.chosen {
    border-color: color-mix(in oklch, var(--primary) 45%, var(--border));
    background: color-mix(in oklch, var(--primary) 4%, var(--card));
  }
  button {
    color: inherit;
    cursor: pointer;
  }
  .service-choice {
    display: flex;
    flex: 1;
    min-width: 0;
    align-items: center;
    gap: 6px;
    padding: 8px 3px 8px 7px;
    text-align: start;
    border-radius: inherit;
  }
  .service-mark {
    display: grid;
    place-items: center;
    width: 24px;
    height: 24px;
    flex-shrink: 0;
    color: var(--foreground);
    background: var(--background);
    border-radius: 8px;
  }
  .service-identity {
    min-width: 0;
    display: grid;
    gap: 1px;
  }
  strong {
    font-size: 12px;
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  small {
    color: var(--muted-foreground);
    font-size: 10px;
  }
  :global(.service-check) {
    flex-shrink: 0;
    margin-inline-start: auto;
    color: var(--primary);
  }
  .service-info {
    display: grid;
    place-items: center;
    width: 26px;
    min-height: 44px;
    flex-shrink: 0;
    color: var(--muted-foreground);
    border-radius: inherit;
  }
  :disabled {
    cursor: not-allowed;
  }
  button:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 3px;
  }
  @media (hover: hover) and (pointer: fine) {
    button:hover:not(:disabled) {
      color: var(--primary);
      background: color-mix(in oklch, var(--primary) 5%, transparent);
    }
  }
</style>
