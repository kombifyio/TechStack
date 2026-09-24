<script lang="ts">
  import { UseCaseAdvanced } from "@kombiverselabs/ui/usecase";
  import {
    Check,
    Clock3,
    SlidersHorizontal,
    HardDrive,
    Cpu,
    KeyRound,
    Sparkles,
    Boxes,
  } from "@lucide/svelte";
  import type {
    UseCaseCatalogView,
    UseCaseSettingGroup,
  } from "#lib/api/useCaseCatalog.js";
  import { tr } from "#lib/i18n.svelte.js";
  import { tick } from "svelte";
  import { revealWizardDetails } from "#lib/wizard/reveal-details.js";

  let {
    catalog,
    values = {},
    onchange,
    disabled = false,
    preview = false,
  }: {
    catalog?: UseCaseCatalogView;
    values?: Record<string, string | boolean>;
    onchange: (id: string, value: string | boolean) => void;
    disabled?: boolean;
    preview?: boolean;
  } = $props();

  const instanceId = $props.id();
  let advancedElement = $state<HTMLDivElement>();
  const groupIcons = {
    profile: SlidersHorizontal,
    storage: HardDrive,
    hardware: Cpu,
    access: KeyRound,
    features: Sparkles,
    backend: Boxes,
  };
  const order: UseCaseSettingGroup[] = [
    "profile",
    "storage",
    "hardware",
    "access",
    "features",
    "backend",
  ];
  const settings = $derived(catalog?.settings ?? []);
  const tiers = $derived(
    (preview && !settings.some((setting) => setting.id === "profile")
      ? (["low", "standard", "high"] as const)
      : []
    ).filter((id) => catalog?.computeTiers[id]?.included),
  );
  const defaultTier = $derived(
    tiers.includes("standard") ? "standard" : tiers[0],
  );
  const groups = $derived(
    order
      .filter(
        (id) =>
          (id === "profile" && tiers.length > 0) ||
          settings.some((setting) => setting.group === id),
      )
      .map((id) => ({
        id,
        label: tr(`wizard.goals.group.${id}`),
        icon: groupIcons[id],
        description: tr(`wizard.goals.group.${id}.description`),
        changed:
          settings.some(
            (setting) =>
              setting.group === id &&
              values[setting.id] !== undefined &&
              values[setting.id] !== setting.default,
          ) ||
          (id === "profile" &&
            tiers.length > 0 &&
            values.profile !== undefined &&
            values.profile !== defaultTier),
      })),
  );
</script>

{#snippet body(group: string)}
  {@const fields = settings.filter((setting) => setting.group === group)}
  <fieldset {disabled} class="settings-fields">
    <legend class="sr-only">{tr(`wizard.goals.group.${group}`)}</legend>
    {#if group === "profile" && tiers.length > 0}
      <fieldset class="setting">
        <legend>{tr("wizard.goals.spec.tiers")}</legend>
        <div class="choice-grid">
          {#each tiers as tier}
            <label
              class="choice"
              class:chosen={(values.profile ?? defaultTier) === tier}
            >
              <input
                type="radio"
                name={`${instanceId}-profile`}
                value={tier}
                checked={(values.profile ?? defaultTier) === tier}
                onchange={() => onchange("profile", tier)}
              />
              <span>{tr(`wizard.goals.tier.${tier}`)}</span>
            </label>
          {/each}
        </div>
      </fieldset>
    {/if}
    {#each fields as setting (setting.id)}
      {@const current = values[setting.id] ?? setting.default}
      {@const helpId = `${instanceId}-${setting.id}-help`}
      <fieldset class="setting">
        <legend>
          {setting.name}
          {#if setting.realization === "recorded"}<span class="later-badge"
              >{tr("wizard.goals.setting.later")}</span
            >{/if}
        </legend>
        {#if setting.help}<p id={helpId} class="setting-note">
            {setting.help}
          </p>{/if}
        {#if setting.kind === "toggle"}
          <label class="toggle-choice">
            <span
              >{tr(
                current === true
                  ? "wizard.goals.setting.on"
                  : "wizard.goals.setting.off",
              )}</span
            >
            <input
              type="checkbox"
              aria-label={setting.name}
              aria-describedby={setting.help ? helpId : undefined}
              checked={current === true}
              onchange={(event) =>
                onchange(setting.id, event.currentTarget.checked)}
            />
          </label>
        {:else if setting.kind === "choice" && setting.options?.length === 1}
          <div class="fixed-choice">
            <Check size={16} aria-hidden="true" /><span
              >{setting.options[0].name}</span
            >
          </div>
          {#if setting.options[0].note}<p class="setting-note">
              {setting.options[0].note}
            </p>{/if}
        {:else if setting.kind === "choice" && (setting.options?.length ?? 0) <= 3}
          <div
            class="choice-grid"
            aria-describedby={setting.help ? helpId : undefined}
          >
            {#each setting.options ?? [] as option (option.id)}
              <label class="choice" class:chosen={current === option.id}>
                <input
                  type="radio"
                  name={`${instanceId}-${setting.id}`}
                  value={option.id}
                  checked={current === option.id}
                  onchange={() => onchange(setting.id, option.id)}
                />
                <span
                  ><strong>{option.name}</strong>{#if option.note}<small
                      >{option.note}</small
                    >{/if}</span
                >
              </label>
            {/each}
          </div>
        {:else if setting.kind === "choice"}
          <select
            aria-label={setting.name}
            aria-describedby={setting.help ? helpId : undefined}
            value={String(current)}
            onchange={(event) =>
              onchange(setting.id, event.currentTarget.value)}
          >
            {#each setting.options ?? [] as option (option.id)}<option
                value={option.id}>{option.name}</option
              >{/each}
          </select>
          {#each setting.options ?? [] as option (option.id)}
            {#if option.note && option.id === current}<p class="setting-note">
                {option.note}
              </p>{/if}
          {/each}
        {:else}
          <input
            type="text"
            aria-label={setting.name}
            aria-describedby={setting.help ? helpId : undefined}
            placeholder={setting.placeholder}
            value={String(current)}
            oninput={(event) => onchange(setting.id, event.currentTarget.value)}
          />
        {/if}
      </fieldset>
    {/each}
    {#if fields.some((setting) => setting.realization === "recorded") || (group === "profile" && tiers.length > 0)}
      <p class="recorded">
        <Clock3 size={14} aria-hidden="true" /><span
          >{tr("wizard.goals.setting.recorded")}</span
        >
      </p>
    {/if}
  </fieldset>
{/snippet}

{#if groups.length > 0}
  <div bind:this={advancedElement} class="advanced-settings">
    <UseCaseAdvanced
      {groups}
      label={tr("wizard.goals.advanced.label")}
      subtitle={groups.map((group) => group.label).join(" · ")}
      presentation="tabs"
      tabsLabel={tr("wizard.goals.advanced.tabsLabel")}
      ondisclose={async (event) => {
        if (!event.open) return;
        await tick();
        revealWizardDetails(advancedElement?.querySelector('[role="tablist"]'));
      }}
      {body}
    />
  </div>
{/if}

<style>
  .advanced-settings :global([role="tablist"]) {
    scroll-margin-block-end: 25vh;
  }
  .settings-fields {
    display: grid;
    gap: 22px;
    margin: 0;
    padding: 4px 0 0;
    border: 0;
    min-width: 0;
  }
  .setting {
    min-width: 0;
    margin: 0;
    padding: 0;
    border: 0;
  }
  legend {
    font-size: 13px;
    font-weight: 600;
    margin-bottom: 7px;
    line-height: 1.6;
  }
  .later-badge {
    display: inline-block;
    margin-inline-start: 6px;
    padding: 1px 6px;
    border-radius: 4px;
    background: var(--muted);
    color: var(--muted-foreground);
    font-size: 10px;
    font-weight: 500;
    vertical-align: middle;
  }
  .choice-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 145px), 1fr));
    gap: 8px;
    margin-top: 10px;
  }
  .choice {
    display: flex;
    align-items: flex-start;
    gap: 9px;
    min-width: 0;
    padding: 13px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius-control, 10px);
    background: var(--card);
    cursor: pointer;
    font-size: 12px;
  }
  .choice.chosen {
    border-color: color-mix(in oklch, var(--primary) 60%, var(--border));
    background: color-mix(in oklch, var(--primary) 5%, var(--card));
  }
  .choice strong {
    display: block;
    font-weight: 550;
    line-height: 1.5;
  }
  .choice small {
    display: block;
    margin-top: 5px;
    font-size: 11px;
    line-height: 1.6;
    color: var(--muted-foreground);
  }
  input[type="radio"],
  input[type="checkbox"] {
    width: 16px;
    height: 16px;
    flex-shrink: 0;
    margin-top: 1px;
    accent-color: var(--primary);
  }
  .toggle-choice,
  .fixed-choice {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius-control, 10px);
    font-size: 12px;
    margin-top: 8px;
  }
  .toggle-choice {
    justify-content: space-between;
    cursor: pointer;
  }
  .fixed-choice :global(svg) {
    color: var(--primary);
    flex-shrink: 0;
  }
  select,
  input[type="text"] {
    width: 100%;
    min-width: 0;
    padding: 11px 12px;
    color: var(--foreground);
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: var(--radius-control, 10px);
    font-size: 13px;
    margin-top: 8px;
  }
  .setting-note {
    margin: 0 0 6px;
    color: var(--muted-foreground);
    font-size: 12px;
    line-height: 1.6;
  }
  .recorded {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding-top: 14px;
    border-top: 1px solid var(--border);
    color: var(--muted-foreground);
    font-size: 11px;
    line-height: 1.6;
    margin: 0;
  }
  .recorded :global(svg) {
    flex-shrink: 0;
    margin-top: 2px;
  }
  fieldset:disabled .choice,
  fieldset:disabled .toggle-choice {
    cursor: not-allowed;
    opacity: 0.6;
  }
  :is(input, select):focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 3px;
  }
  .choice:focus-within {
    outline: 2px solid var(--ring);
    outline-offset: 3px;
  }
  .choice input:focus-visible {
    outline: none;
  }
  @media (hover: hover) and (pointer: fine) {
    fieldset:not(:disabled) > .choice-grid .choice:hover {
      border-color: var(--primary);
    }
  }
</style>
