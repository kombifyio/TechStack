<script lang="ts">
  import { onMount, tick } from "svelte";
  import {
    Check,
    ArrowUpRight,
    LayoutGrid,
    List,
    ShieldCheck,
    Info,
    Plus,
    Network,
    Workflow,
    Bot,
    CodeXml,
  } from "@lucide/svelte";
  import { UseCaseCardCompact } from "@kombiverselabs/ui/usecase";
  import { KfDetailSheet } from "@kombiverselabs/ui/card";
  import {
    creationGoals,
    backendChoices,
    selectedBackend,
    supportsCreationChoice,
    type CreationGoal,
    type CreationVariant,
  } from "#lib/wizard/creationGoals.js";
  import {
    getEasyGoalQuestions,
    type GoalConfigKey,
    type StackConfig,
    type BundleQuestionDefinition,
  } from "#lib/wizard/index.js";
  import type {
    UseCaseCatalogView,
    UseCaseCatalogComponent,
  } from "#lib/api/useCaseCatalog.js";
  import { brandDomainForTool } from "#lib/brand-logo.js";
  import { useCaseGuideUrl } from "#lib/docs-links.js";
  import { tr } from "#lib/i18n.svelte.js";
  import GoalIllustration from "./GoalIllustration.svelte";
  import UseCaseServicePicker from "./UseCaseServicePicker.svelte";
  import UseCaseSettings from "./UseCaseSettings.svelte";
  import BrandLogoIcon from "../BrandLogoIcon.svelte";
  import BrandLogoScope from "../BrandLogoScope.svelte";
  import { revealWizardDetails } from "#lib/wizard/reveal-details.js";

  let {
    config = $bindable(),
    catalog,
    onGoalChange,
    onSettingChange,
    variant = $bindable<CreationVariant>("discover"),
    reviewMode = false,
    questions = getEasyGoalQuestions(),
    runtimeCatalog = catalog,
    ondisclose,
  }: {
    config: StackConfig;
    catalog: Map<string, UseCaseCatalogView>;
    onGoalChange: (goal: GoalConfigKey, value: boolean) => void;
    onSettingChange: (
      goal: GoalConfigKey,
      setting: string,
      value: string | boolean,
    ) => void;
    variant?: CreationVariant;
    reviewMode?: boolean;
    questions?: BundleQuestionDefinition<GoalConfigKey>[];
    runtimeCatalog?: Map<string, UseCaseCatalogView>;
    ondisclose?: (event: {
      kind: "detail-level" | "drawer" | "group";
      id?: string;
      open: boolean;
    }) => void;
  } = $props();

  const cards = $derived(creationGoals(questions, catalog));
  const available = $derived(
    cards.filter((card) => card.availability === "available"),
  );
  const comingSoon = $derived(
    cards.filter((card) => card.availability === "coming-soon"),
  );
  const selected = $derived(available.filter(isSelected));
  let expanded = $state<string | null>(null);
  const expandedIndex = $derived(
    available.findIndex((card) => card.id === expanded),
  );
  let serviceInfo = $state<{
    component: UseCaseCatalogComponent;
    card: CreationGoal;
    trigger: HTMLButtonElement;
  } | null>(null);
  let activeTransition: ViewTransition | undefined;
  let goalsGrid = $state<HTMLDivElement>();
  let disclosureRevision = 0;
  let globalAdvanced = $state(false);

  onMount(() => () => activeTransition?.skipTransition());

  function title(card: CreationGoal) {
    if (["files", "mail", "game"].includes(card.id))
      return tr(`wizard.preview.${card.id}.title`);
    return card.question
      ? tr(card.question.titleKey)
      : tr(`wizard.preview.${card.id}.title`);
  }
  function description(card: CreationGoal) {
    if (["files", "mail", "game", "dev", "ai"].includes(card.id))
      return tr(`wizard.preview.${card.id}.description`);
    return card.question
      ? tr(card.question.descriptionKey)
      : tr(`wizard.preview.${card.id}.description`);
  }
  function isSelected(card: CreationGoal) {
    return !!card.question && config.goals?.[card.question.configKey] === true;
  }
  function values(card: CreationGoal) {
    return config.useCaseSettings?.[card.id] ?? {};
  }

  async function morph(change: () => void) {
    activeTransition?.skipTransition();
    if (
      !document.startViewTransition ||
      window.matchMedia("(prefers-reduced-motion: reduce)").matches
    ) {
      change();
      return;
    }
    document.documentElement.dataset.creationMorph = "true";
    const transition = document.startViewTransition(async () => {
      change();
      await tick();
    });
    activeTransition = transition;
    try {
      await transition.finished;
    } catch {
      // A rapid second interaction or navigation may skip the old snapshot.
    } finally {
      if (activeTransition === transition) {
        activeTransition = undefined;
        delete document.documentElement.dataset.creationMorph;
      }
    }
  }
  async function toggleDetails(card: CreationGoal, open: boolean) {
    const revision = ++disclosureRevision;
    await morph(() => {
      if (expanded && expanded !== card.id) {
        ondisclose?.({ kind: "drawer", id: expanded, open: false });
      }
      expanded = open ? card.id : null;
      ondisclose?.({ kind: "drawer", id: card.id, open });
    });
    await tick();
    if (open && expanded === card.id && revision === disclosureRevision) {
      revealWizardDetails(
        goalsGrid?.querySelector(".expanded .detail-story .eyebrow"),
      );
    }
  }

  function cardPosition(index: number, columns: number) {
    const row = Math.floor(index / columns) + 1;
    const column = (index % columns) + 1;
    const expandedRow = Math.floor(expandedIndex / columns) + 1;
    if (expandedIndex < 0 || row < expandedRow) return { row, column };
    const expandedColumn = Math.min((expandedIndex % columns) + 1, columns - 1);
    const rowSpan = columns > 2 ? 2 : 1;
    // Keep the expanded tile anchored, including at the right edge, then
    // fill all remaining slots in reading order without leaving a hole.
    if (index === expandedIndex) {
      return {
        row: `${row} / span ${rowSpan}`,
        column: `${expandedColumn} / span 2`,
      };
    }
    let ordinal =
      index - (expandedRow - 1) * columns - (index > expandedIndex ? 1 : 0);
    for (let slot = 0; ; slot++) {
      const nextRow = expandedRow + Math.floor(slot / columns);
      const nextColumn = (slot % columns) + 1;
      if (
        nextRow < expandedRow + rowSpan &&
        nextColumn >= expandedColumn &&
        nextColumn < expandedColumn + 2
      )
        continue;
      if (ordinal-- === 0) return { row: nextRow, column: nextColumn };
    }
  }
  function choose(card: CreationGoal, id: string) {
    if (!card.question || card.availability !== "available") return;
    if (!canChoose(card, "backend", id)) return;
    onSettingChange(card.question.configKey, "backend", id);
    onGoalChange(card.question.configKey, true);
  }
  function canChoose(
    card: CreationGoal,
    setting: string,
    value?: string | boolean,
  ) {
    return (
      reviewMode ||
      supportsCreationChoice(runtimeCatalog.get(card.id), setting, value)
    );
  }
  function preferenceNote() {
    return tr(
      reviewMode
        ? "wizard.preview.preferenceOnly"
        : "wizard.creation.preferenceOnly",
    );
  }
  async function closeServiceInfo() {
    const trigger = serviceInfo?.trigger;
    serviceInfo = null;
    await tick();
    trigger?.focus();
  }
  function serviceDescription(id: string, role: string) {
    const key = `wizard.preview.service.${id}`;
    const description = tr(key);
    return description === key
      ? tr(`wizard.preview.roleHelp.${role}`)
      : description;
  }
</script>

<div
  class="goals-preview"
  data-testid={reviewMode ? "creation-goals-preview" : "creation-goals"}
  data-variant={variant}
>
  {#if reviewMode}<div class="preview-toolbar">
      <div class="preview-caption">
        <span class="preview-dot"></span><span
          >{tr("wizard.preview.label")}</span
        >
      </div>
      <div
        class="variant-switch"
        role="group"
        aria-label={tr("wizard.preview.compare")}
      >
        <button
          type="button"
          aria-pressed={variant === "discover"}
          onclick={() =>
            morph(() => {
              variant = "discover";
            })}
          ><LayoutGrid size={15} aria-hidden="true" /><span
            >A · {tr("wizard.preview.discover")}</span
          ></button
        >
        <button
          type="button"
          aria-pressed={variant === "focus"}
          onclick={() =>
            morph(() => {
              variant = "focus";
            })}
          ><List size={15} aria-hidden="true" /><span
            >B · {tr("wizard.preview.focus")}</span
          ></button
        >
      </div>
    </div>{/if}

  <header class="goals-heading">
    <div>
      <p class="eyebrow">{tr("wizard.preview.eyebrow")}</p>
      <h2>{tr("wizard.preview.title")}</h2>
      <p class="intro">{tr("wizard.preview.subtitle")}</p>
    </div>
    <span class="step-note">01 / 05</span>
  </header>

  <div class="section-heading">
    <div>
      <h3>{tr("wizard.preview.available")}</h3>
      <span>{tr("wizard.preview.availableHint")}</span>
    </div>
    <span class="selection-count" role="status" aria-live="polite">
      <strong>{selected.length}</strong>
      {tr("wizard.preview.selectionCount")}
    </span>
  </div>

  <div
    bind:this={goalsGrid}
    class="goals-grid"
    class:focus={variant === "focus"}
  >
    {#each available as card, index (card.id)}
      {@const wide = cardPosition(index, 4)}
      {@const narrow = cardPosition(index, 3)}
      {@const medium = cardPosition(index, 2)}
      <div
        class="goal-frame"
        class:expanded={expanded === card.id}
        style:--wide-row={wide.row}
        style:--wide-column={wide.column}
        style:--narrow-row={narrow.row}
        style:--narrow-column={narrow.column}
        style:--medium-row={medium.row}
        style:--medium-column={medium.column}
        style={`view-transition-name: creation-${card.id}`}
      >
        {#snippet visual()}<GoalIllustration
            goal={card.id}
            compact={variant === "focus"}
            priority={index < 4}
          />{/snippet}
        {#snippet controls()}
          <UseCaseServicePicker
            components={backendChoices(card.catalog)}
            selectedId={selectedBackend(card.catalog, values(card).backend)?.id}
            label={`${tr("wizard.preview.serviceFor")} ${title(card)}`}
            disabledIds={backendChoices(card.catalog)
              .filter((component) => !canChoose(card, "backend", component.id))
              .map((component) => component.id)}
            onselect={(id) => choose(card, id)}
            oninfo={(component, trigger) => {
              serviceInfo = { component, card, trigger };
            }}
          />
          {#if values(card).backend && selectedBackend(card.catalog, values(card).backend)?.role === "alternative"}
            <p class="preference-note">{preferenceNote()}</p>
          {/if}
          {#if !reviewMode && runtimeCatalog.get(card.id)?.deliverable === false}
            <p class="preference-note">
              {tr("wizard.goals.notInThisReleaseLong")}
            </p>
          {/if}
        {/snippet}
        {#snippet details()}
          <div class="goal-details">
            <div class="detail-story">
              <p class="eyebrow">{tr("wizard.preview.aboutUseCase")}</p>
              <p>
                {["files", "mail", "game", "dev"].includes(card.id)
                  ? tr(`wizard.preview.${card.id}.help`)
                  : card.question?.helpKey
                    ? tr(card.question.helpKey)
                    : description(card)}
              </p>
              {#if card.question?.tipKey && !["mail", "game", "dev"].includes(card.id)}<p
                  class="detail-tip"
                >
                  <Check size={15} aria-hidden="true" />{tr(
                    card.question.tipKey,
                  )}
                </p>{/if}
              {#if useCaseGuideUrl(card.catalog?.docsPath)}<a
                  href={useCaseGuideUrl(card.catalog?.docsPath)}
                  target="_blank"
                  rel="noreferrer"
                  >{tr("wizard.goals.learnMore")}<ArrowUpRight
                    size={14}
                    aria-hidden="true"
                  /></a
                >{/if}
            </div>
            {#if (card.catalog?.components.length ?? 0) > 0}
              <div class="included-tools">
                <p class="eyebrow">{tr("wizard.preview.inside")}</p>
                {#each card.catalog?.components ?? [] as component (component.id)}
                  <button
                    type="button"
                    class="tool-row"
                    onclick={(event) => {
                      serviceInfo = {
                        card,
                        component,
                        trigger: event.currentTarget,
                      };
                    }}
                  >
                    <BrandLogoScope
                      domain={brandDomainForTool(component.id, component.name)}
                      ><BrandLogoIcon
                        class="h-5 w-5"
                        fallbackLabel={component.name}
                      /></BrandLogoScope
                    >
                    <span
                      >{component.name}<small
                        >{tr(`wizard.preview.role.${component.role}`)}</small
                      ></span
                    ><Info size={15} aria-hidden="true" />
                  </button>
                {/each}
              </div>
            {/if}
          </div>
          {#if card.id === "files"}
            <label class="addon-choice">
              <input
                type="checkbox"
                checked={values(card).paperless === true}
                disabled={!canChoose(card, "paperless")}
                onchange={(event) =>
                  onSettingChange(
                    "files",
                    "paperless",
                    event.currentTarget.checked,
                  )}
              />
              <span
                ><strong>{tr("wizard.preview.paperless.title")}</strong><small
                  >{tr(
                    "wizard.preview.paperless.help",
                  )}{#if !canChoose(card, "paperless")}
                    · {tr("wizard.preview.comingSoon")}{/if}</small
                ></span
              >
            </label>
          {:else if card.id === "mail"}
            <label class="preview-choice"
              >{tr("wizard.preview.mail.hosting")}
              <select
                value={values(card).hosting ?? "existing"}
                disabled={!canChoose(card, "hosting")}
                onchange={(event) =>
                  onSettingChange("mail", "hosting", event.currentTarget.value)}
              >
                <option value="existing"
                  >{tr("wizard.preview.mail.existing")}</option
                >
                <option value="stalwart">Stalwart</option><option
                  value="mailcow">mailcow</option
                >
              </select>
            </label>
          {:else if card.id === "game"}
            <label class="preview-choice"
              >{tr("wizard.preview.game.edition")}
              <select
                value={values(card).edition ?? "java"}
                disabled={!canChoose(card, "edition")}
                onchange={(event) =>
                  onSettingChange("game", "edition", event.currentTarget.value)}
              >
                <option value="java">Minecraft Java</option><option
                  value="bedrock">Minecraft Bedrock</option
                >
              </select>
            </label>
          {/if}
          <UseCaseSettings
            catalog={reviewMode ? card.catalog : runtimeCatalog.get(card.id)}
            preview={reviewMode}
            values={values(card)}
            onchange={(id, value) => {
              if (card.question)
                onSettingChange(card.question.configKey, id, value);
            }}
          />
        {/snippet}
        <UseCaseCardCompact
          name={title(card)}
          description={description(card)}
          planState={isSelected(card) ? "included" : "available"}
          layout={variant === "focus" ? "row" : "tile"}
          {visual}
          {controls}
          {details}
          expanded={expanded === card.id}
          onExpand={(open) => toggleDetails(card, open)}
          expandLabel={tr("wizard.preview.explore")}
          collapseLabel={tr("wizard.preview.collapse")}
          selectionLabel={`${tr(isSelected(card) ? "wizard.goals.remove" : "wizard.goals.add")} ${title(card)}`}
          planLabel={tr(
            isSelected(card)
              ? "wizard.preview.selected"
              : "wizard.preview.optional",
          )}
          onToggle={(next) => {
            if (card.question) onGoalChange(card.question.configKey, next);
          }}
          data-testid={reviewMode
            ? `preview-goal-${card.id}`
            : card.question?.testId}
        />
      </div>
    {/each}
  </div>

  <section class="coming-soon" aria-labelledby="coming-soon-title">
    <div class="section-heading">
      <div>
        <h3 id="coming-soon-title">{tr("wizard.preview.comingSoon")}</h3>
        <span>{tr("wizard.preview.comingSoonHint")}</span>
      </div>
      <span class="soon-count">{comingSoon.length}</span>
    </div>
    <div class="upcoming-grid">
      {#each comingSoon as card (card.id)}
        <UseCaseCardCompact
          name={title(card)}
          description={card.id === "automation"
            ? `${description(card)} · n8n / Activepieces`
            : description(card)}
          planState="unavailable"
          icon={card.id === "network"
            ? Network
            : card.id === "automation"
              ? Workflow
              : card.id === "dev"
                ? CodeXml
                : Bot}
          planLabel={tr("wizard.preview.comingSoon")}
          unavailableReason={tr("wizard.preview.comingSoonHint")}
          selectable={false}
          data-testid={`preview-coming-${card.id}`}
        />
      {/each}
    </div>
  </section>

  <div class="setup-preferences">
    <button
      type="button"
      class="preferences-toggle"
      aria-expanded={globalAdvanced}
      aria-controls="preview-global-settings"
      onclick={() =>
        morph(() => {
          globalAdvanced = !globalAdvanced;
        })}
    >
      <ShieldCheck size={18} aria-hidden="true" /><span
        ><strong>{tr("wizard.preview.setupPreferences")}</strong><small
          >{tr("wizard.preview.setupPreferencesHint")}</small
        ></span
      ><Plus size={17} aria-hidden="true" />
    </button>
    {#if globalAdvanced}<div id="preview-global-settings" class="global-fields">
        <label class="backup-setting"
          ><input
            type="checkbox"
            bind:checked={config.advanced.backupsEnabled}
          />{tr("wizard.advanced.backups")}</label
        >
        <label
          >{tr("wizard.advanced.isolation")}<select
            bind:value={config.advanced.isolation}
            ><option value="isolated"
              >{tr("wizard.advanced.isolation.isolated")}</option
            ><option value="shared"
              >{tr("wizard.advanced.isolation.shared")}</option
            ></select
          ></label
        >
        <label
          >{tr("wizard.advanced.autostart")}<select
            bind:value={config.advanced.autostart}
            ><option value="auto">{tr("wizard.advanced.autostart.auto")}</option
            ><option value="manual"
              >{tr("wizard.advanced.autostart.manual")}</option
            ></select
          ></label
        >
      </div>{/if}
  </div>
</div>

{#if serviceInfo}
  {@const service = serviceInfo.component}
  {@const domain = brandDomainForTool(service.id, service.name)}
  <KfDetailSheet
    open={true}
    onclose={closeServiceInfo}
    title={service.name}
    meta={title(serviceInfo.card)}
    tabs={[{ id: "overview", label: tr("wizard.preview.aboutService") }]}
  >
    {#snippet header()}<div class="service-hero">
        <BrandLogoScope {domain}
          ><BrandLogoIcon
            class="h-12 w-12"
            fallbackLabel={service.name}
          /></BrandLogoScope
        ><span>{tr(`wizard.preview.role.${service.role}`)}</span>
      </div>{/snippet}
    {#snippet body()}
      <div class="service-dossier">
        <p>
          {serviceDescription(service.id, service.role)}
        </p>
        <dl>
          <div>
            <dt>{tr("wizard.preview.partOf")}</dt>
            <dd>{title(serviceInfo!.card)}</dd>
          </div>
        </dl>
        {#if service.role === "alternative"}<p class="service-disclaimer">
            {preferenceNote()}
          </p>{/if}
        {#if domain}<a
            href={`https://${domain}`}
            target="_blank"
            rel="noreferrer"
            >{tr("wizard.preview.website")}<ArrowUpRight
              size={16}
              aria-hidden="true"
            /></a
          >{/if}
      </div>
    {/snippet}
    {#snippet actionBar()}
      {#if service.role === "primary" || service.role === "alternative"}<button
          type="button"
          class="use-service"
          data-kx="control"
          data-variant="primary"
          disabled={!canChoose(serviceInfo!.card, "backend", service.id)}
          onclick={() => {
            choose(serviceInfo!.card, service.id);
            void closeServiceInfo();
          }}>{tr("wizard.preview.useService")} {service.name}</button
        >{/if}
    {/snippet}
  </KfDetailSheet>
{/if}

<style>
  .addon-choice,
  .preview-choice {
    display: flex;
    gap: 12px;
    margin: 16px 0;
    padding: 16px;
    border: 1px solid var(--border);
    border-radius: var(--radius-control, 10px);
    font-size: 13px;
  }
  .addon-choice input {
    width: 18px;
    height: 18px;
    accent-color: var(--primary);
    flex-shrink: 0;
  }
  .addon-choice span {
    display: grid;
    gap: 4px;
  }
  .addon-choice small {
    color: var(--muted-foreground);
    line-height: 1.6;
  }
  .preview-choice {
    flex-direction: column;
  }
  .preview-choice select {
    color: var(--foreground);
    background: var(--input);
    padding: 10px;
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .goals-preview {
    --preview-space: clamp(18px, 3vw, 30px);
    container: creation-goals / inline-size;
    display: grid;
    gap: 24px;
    width: 100%;
    min-width: 0;
  }
  button {
    cursor: pointer;
  }
  button:disabled {
    cursor: not-allowed;
  }
  .preview-toolbar,
  .section-heading,
  .variant-switch,
  .preview-caption {
    display: flex;
    align-items: center;
  }
  .preview-toolbar {
    justify-content: space-between;
    gap: 14px;
    padding-bottom: 20px;
    border-bottom: 1px solid var(--border);
  }
  .preview-caption {
    gap: 8px;
    font-size: 11px;
    color: var(--muted-foreground);
  }
  .preview-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--primary);
  }
  .variant-switch {
    gap: 3px;
    padding: 4px;
    background: var(--muted);
    border: 1px solid var(--border);
    border-radius: var(--radius-control, 10px);
  }
  .variant-switch button {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 7px;
    padding: 8px 14px;
    color: var(--muted-foreground);
    font-size: 12px;
    font-weight: 550;
    border-radius: calc(var(--radius-control, 10px) - 4px);
  }
  .variant-switch button[aria-pressed="true"] {
    color: var(--foreground);
    background: var(--card);
    box-shadow: 0 1px 3px color-mix(in oklch, var(--foreground) 8%, transparent);
  }
  .goals-heading {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 24px;
    padding-block: 14px 10px;
  }
  .eyebrow {
    font-size: 10px;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.12em;
    color: var(--muted-foreground);
    margin: 0 0 9px;
  }
  .goals-heading .eyebrow {
    color: var(--primary);
  }
  h2 {
    font-size: clamp(27px, 3vw, 36px);
    font-weight: 600;
    letter-spacing: -0.035em;
    line-height: 1.15;
    margin: 0;
    text-wrap: balance;
  }
  .intro {
    max-width: 630px;
    font-size: 14px;
    line-height: 1.75;
    color: var(--muted-foreground);
    margin-top: 14px;
  }
  .step-note {
    color: var(--muted-foreground);
    font-size: 11px;
    font-family: var(--font-mono);
    padding-top: 5px;
    white-space: nowrap;
  }
  .section-heading {
    justify-content: space-between;
    gap: 16px;
  }
  .section-heading h3 {
    margin: 0 0 3px;
    font-size: 14px;
    font-weight: 600;
  }
  .section-heading span {
    font-size: 12px;
    color: var(--muted-foreground);
  }
  .goals-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    grid-auto-rows: 1fr;
    gap: 12px;
    align-items: stretch;
  }
  .goal-frame {
    min-width: 0;
    grid-row: var(--wide-row);
    grid-column: var(--wide-column);
  }
  .goals-grid:not(.focus) .goal-frame :global(.kf-uc-enhanced) {
    height: 100%;
  }
  .goals-grid:not(.focus)
    .goal-frame:not(.expanded)
    :global(.kf-uc-enhanced-main) {
    flex: 1;
    grid-template-rows: var(--goal-art-height, 132px) 1fr auto auto auto;
  }
  .goals-grid:not(.focus) .goal-frame :global(.kf-uc-enhanced-main) {
    gap: 8px 12px;
    padding: 14px;
  }
  .goals-grid:not(.focus) .goal-frame :global(.kf-uc-enhanced-visual) {
    min-height: var(--goal-art-height, 132px);
    height: var(--goal-art-height, 132px);
    border: 0;
    border-radius: 9px;
  }
  .goals-grid:not(.focus) .goal-frame :global(.scene) {
    height: 100%;
  }
  .goals-grid:not(.focus) .goal-frame :global(.kf-uc-enhanced-name) {
    position: absolute;
    z-index: 3;
    top: calc(14px + var(--goal-art-height, 132px) - 10px);
    left: 26px;
    width: calc(100% - 82px);
    transform: translateY(-100%);
    color: white;
    font-size: 16px;
    line-height: 1.2;
    text-shadow: 0 2px 8px rgb(5 10 18 / 75%);
  }
  .goals-grid:not(.focus) .goal-frame :global(.kf-uc-enhanced-desc) {
    margin-top: 0;
  }
  .goals-grid:not(.focus) .goal-frame :global(.kf-uc-enhanced-selection) {
    grid-area: auto;
    position: absolute;
    z-index: 3;
    top: 24px;
    right: 24px;
  }
  .goals-grid:not(.focus) .goal-frame :global(.kf-uc-select) {
    color: white;
    background: rgb(15 22 32 / 72%);
    box-shadow: inset 0 0 0 1px rgb(255 255 255 / 40%);
    backdrop-filter: blur(8px);
  }
  .goals-grid:not(.focus) .goal-frame :global(.kf-uc-select[data-on="true"]) {
    background: var(--primary);
    box-shadow: 0 2px 12px rgb(0 0 0 / 30%);
  }
  .goals-grid:not(.focus) .goal-frame.expanded {
    --goal-art-height: 245px;
  }
  .goals-grid:not(.focus) .goal-frame.expanded :global(.kf-uc-enhanced-name) {
    font-size: 22px;
  }
  .goals-grid:not(.focus) .goal-frame.expanded :global(.service-picker) {
    flex-direction: column;
    flex-wrap: nowrap;
  }
  .goals-grid:not(.focus) .goal-frame.expanded :global(.service-option) {
    flex: none;
  }
  .goals-grid:not(.focus) .goal-frame :global(.kf-uc-enhanced-foot) {
    padding-top: 9px;
  }
  .goals-grid.focus {
    grid-template-columns: 1fr;
    grid-template-rows: none;
    gap: 10px;
  }
  .focus .goal-frame {
    grid-area: auto;
  }
  .goal-details {
    display: grid;
    grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr);
    gap: 26px;
    padding-block: 8px 22px;
  }
  .detail-story .eyebrow {
    scroll-margin-block-end: 25vh;
  }
  .detail-story > p:not(.eyebrow) {
    font-size: 13px;
    line-height: 1.75;
    color: var(--muted-foreground);
  }
  .detail-story .detail-tip {
    display: flex;
    align-items: flex-start;
    gap: 7px;
    margin-top: 14px;
  }
  .detail-tip :global(svg) {
    flex-shrink: 0;
    margin-top: 4px;
    color: var(--primary);
  }
  a {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    color: var(--primary);
    font-size: 12px;
    text-underline-offset: 3px;
  }
  a:hover {
    text-decoration: underline;
  }
  .detail-story a {
    margin-top: 14px;
  }
  .tool-row {
    display: flex;
    width: 100%;
    gap: 10px;
    align-items: center;
    padding: 10px 0;
    border-bottom: 1px solid var(--border);
    font-size: 12px;
    text-align: start;
  }
  .tool-row span {
    flex: 1;
  }
  .tool-row small {
    display: block;
    color: var(--muted-foreground);
    font-size: 10px;
    margin-top: 3px;
  }
  .tool-row :global(svg) {
    flex-shrink: 0;
  }
  .preference-note {
    font-size: 11px;
    line-height: 1.55;
    color: var(--muted-foreground);
    margin-top: 8px;
  }
  .section-heading .selection-count {
    flex-shrink: 0;
    text-align: end;
    font-variant-numeric: tabular-nums;
  }
  .selection-count strong {
    color: var(--foreground);
  }
  .coming-soon {
    display: grid;
    gap: 18px;
    border-top: 1px solid var(--border);
    padding-top: 28px;
    margin-top: 8px;
  }
  .soon-count {
    border: 1px solid var(--border);
    border-radius: 99px;
    padding: 3px 9px;
  }
  .upcoming-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
    --kf-uc-tile-height: 195px;
  }
  .setup-preferences {
    border-top: 1px solid var(--border);
  }
  .preferences-toggle {
    display: flex;
    align-items: center;
    width: 100%;
    gap: 12px;
    text-align: start;
    padding-block: 18px;
  }
  .preferences-toggle span {
    flex: 1;
  }
  .preferences-toggle strong {
    font-size: 12px;
    font-weight: 550;
  }
  .preferences-toggle small {
    display: block;
    color: var(--muted-foreground);
    font-size: 11px;
    margin-top: 4px;
  }
  .global-fields {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 18px;
    padding: 6px 0 22px;
  }
  .global-fields label {
    font-size: 12px;
    display: grid;
    gap: 8px;
  }
  .global-fields .backup-setting {
    display: flex;
    align-items: center;
    gap: 9px;
    grid-column: 1/-1;
  }
  input[type="checkbox"] {
    width: 17px;
    height: 17px;
    accent-color: var(--primary);
  }
  select {
    padding: 10px;
    background: var(--input);
    border: 1px solid var(--border);
    border-radius: var(--radius-control, 10px);
  }
  .service-hero {
    display: flex;
    align-items: center;
    gap: 16px;
    padding-block: 10px;
  }
  .service-hero span {
    color: var(--muted-foreground);
    font-size: 12px;
  }
  .service-dossier {
    display: grid;
    gap: 24px;
    font-size: 14px;
    line-height: 1.75;
  }
  .service-dossier dl {
    font-size: 12px;
  }
  .service-dossier dl > div {
    display: flex;
    justify-content: space-between;
    gap: 18px;
    padding: 12px 0;
    border-bottom: 1px solid var(--border);
  }
  dt {
    color: var(--muted-foreground);
  }
  .service-disclaimer {
    padding: 14px;
    background: var(--muted);
    border-radius: var(--radius-lg, 12px);
    font-size: 12px;
  }
  .use-service {
    width: 100%;
    border-radius: var(--radius-control, 10px);
    padding: 12px 16px;
    font-size: 13px;
  }
  button:focus-visible,
  a:focus-visible,
  select:focus-visible,
  input:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 4px;
  }
  @media (hover: hover) and (pointer: fine) {
    .tool-row:hover {
      color: var(--primary);
    }
  }
  @container creation-goals (max-width: 1260px) {
    .goals-grid {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }
    .goal-frame {
      grid-row: var(--narrow-row);
      grid-column: var(--narrow-column);
    }
    .goals-grid:not(.focus) .goal-frame:not(.expanded) {
      --goal-art-height: 142px;
    }
  }
  @container creation-goals (max-width: 1000px) {
    .goals-grid,
    .upcoming-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .goal-frame {
      grid-row: var(--medium-row);
      grid-column: var(--medium-column);
    }
    .goals-grid:not(.focus) .goal-frame:not(.expanded) {
      --goal-art-height: 158px;
    }
    .goals-grid {
      grid-template-rows: none;
    }
  }
  @container creation-goals (max-width: 620px) {
    .goals-grid,
    .upcoming-grid,
    .goal-details,
    .global-fields {
      grid-template-columns: 1fr;
    }
    .goal-frame {
      grid-area: auto;
    }
    .goals-grid:not(.focus) .goal-frame:not(.expanded) {
      --goal-art-height: 168px;
      width: 100%;
      max-width: 430px;
      justify-self: center;
    }
    .preview-toolbar {
      flex-wrap: wrap;
    }
    .variant-switch {
      width: 100%;
    }
    .variant-switch button {
      flex: 1;
    }
    .step-note {
      display: none;
    }
    .section-heading {
      align-items: flex-start;
    }
  }
  @media (prefers-reduced-motion: no-preference) {
    :global(html[data-creation-morph]::view-transition-group(*)) {
      animation-duration: var(--kx-dur-morph, 260ms);
      animation-timing-function: var(--kx-ease-morph, ease-out);
    }
    :global(html[data-creation-morph]::view-transition-old(root)),
    :global(html[data-creation-morph]::view-transition-new(root)) {
      animation: none;
    }
  }
</style>
