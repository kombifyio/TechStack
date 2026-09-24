<script lang="ts">
  import { Check, Info } from "@lucide/svelte";
  import BrandLogoIcon from "../BrandLogoIcon.svelte";
  import BrandLogoScope from "../BrandLogoScope.svelte";
  import { brandDomainForTool } from "#lib/brand-logo.js";
  import type { UseCaseCatalogComponent } from "#lib/api/useCaseCatalog.js";
  import { tr } from "#lib/i18n.svelte.js";

  let {
    components,
    selectedId,
    disabled = false,
    disabledIds = [],
    label,
    onselect,
    oninfo,
  }: {
    components: UseCaseCatalogComponent[];
    selectedId?: string;
    disabled?: boolean;
    disabledIds?: string[];
    label: string;
    onselect: (id: string) => void;
    oninfo: (
      component: UseCaseCatalogComponent,
      trigger: HTMLButtonElement,
    ) => void;
  } = $props();
</script>

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
            >{tr(
              component.role === "primary"
                ? "wizard.preview.defaultService"
                : "wizard.preview.alternative",
            )}</small
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

<style>
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
    gap: 8px;
    padding: 9px 5px 9px 10px;
    text-align: start;
    border-radius: inherit;
  }
  .service-mark {
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
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
    width: 34px;
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
