<script lang="ts">
  import { Rocket, FolderOpen, Briefcase } from "@lucide/svelte";
  import { creationGoals } from "#lib/wizard/creationGoals.js";
  import {
    CREATION_TEMPLATES,
    templateActive,
    templateSelection,
    recommendedTemplateGoals,
    type CreationTemplateIcon,
  } from "#lib/wizard/creationTemplates.js";
  import type {
    BundleQuestionDefinition,
    GoalConfigKey,
  } from "#lib/wizard/index.js";
  import type { UseCaseCatalogView } from "#lib/api/useCaseCatalog.js";
  import { tr } from "#lib/i18n.svelte.js";
  import { recommendWizard } from "#lib/api/unifier.js";
  import { useCreationContext } from "#lib/creation/context.js";
  import type { GoalsSlice } from "#lib/creation/core/index.js";

  let {
    config,
    questions,
    catalog,
    onGoalChange,
  }: {
    config: GoalsSlice;
    questions: BundleQuestionDefinition<GoalConfigKey>[];
    catalog: Map<string, UseCaseCatalogView>;
    onGoalChange: (goal: GoalConfigKey, value: boolean) => void;
  } = $props();

  // Templates name their icon in data, so a new template needs no change here.
  const icons = {
    folder: FolderOpen,
    briefcase: Briefcase,
    rocket: Rocket,
  } satisfies Record<CreationTemplateIcon, unknown>;

  const creation = useCreationContext();
  const cards = $derived(creationGoals(questions, catalog));
  const templates = $derived(
    CREATION_TEMPLATES.map((template) => {
      const selection = templateSelection(template, cards);
      return {
        template,
        selection,
        active: templateActive(selection, config.goals),
      };
    }).filter(({ selection }) => selection.goals.length > 0),
  );

  function apply(goals: GoalConfigKey[], value: boolean) {
    for (const goal of goals) onGoalChange(goal, value);
  }

  // "To infinity and beyond" asks the recommendation authority what fits what
  // is known about the operator. Without an answer it keeps selecting every
  // available card, which is also the server's answer for an unknown operator.
  async function applyInfinity(goals: GoalConfigKey[]) {
    const offered = cards.filter(
      (card) => card.availability === "available" && card.question,
    );
    try {
      const result = await recommendWizard({
        goals: [],
        services: [],
        deployment_lane: creation.auth.lane === "saas" ? "saas" : "self-hosted",
        surface: "easy",
        selection_intent: "all",
        available_goals: offered.map((card) => card.id),
        use_case_settings: config.useCaseSettings,
        use_case_capabilities: config.useCaseCapabilities,
      });
      const preselect = recommendedTemplateGoals(offered, result.selection);
      if (preselect) {
        apply(preselect, true);
        return;
      }
    } catch {
      // Guidance is optional; the selection below is the documented default.
    }
    apply(goals, true);
  }
</script>

{#if templates.length > 0}
  <section
    class="creation-templates"
    aria-labelledby="creation-templates-title"
    data-testid="creation-templates"
  >
    <p id="creation-templates-title" class="eyebrow">
      {tr("wizard.templates.title")}
    </p>
    <div class="template-row">
      {#each templates as { template, selection, active } (template.id)}
        {@const Icon = icons[template.icon]}
        <button
          type="button"
          class="template"
          class:infinity={template.id === "infinity"}
          aria-pressed={active}
          data-testid={`creation-template-${template.id}`}
          onclick={() =>
            template.id === "infinity" && !active
              ? applyInfinity(selection.goals)
              : apply(selection.goals, !active)}
        >
          <Icon size={18} aria-hidden="true" />
          <span>
            <strong>{tr(template.titleKey)}</strong>
            <small>{tr(template.descriptionKey)}</small>
            {#if selection.skipped.length > 0}
              <small class="skipped"
                >{tr("wizard.templates.notYetAvailable", {
                  count: selection.skipped.length,
                })}</small
              >
            {/if}
          </span>
        </button>
      {/each}
    </div>
  </section>
{/if}

<style>
  .creation-templates {
    display: grid;
    gap: 10px;
    padding-top: 12px;
  }
  .eyebrow {
    font-size: 10px;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.12em;
    color: var(--muted-foreground);
    margin: 0;
  }
  .template-row {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 10px;
  }
  .template {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    text-align: start;
    padding: 14px 16px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--card);
    color: var(--foreground);
    transition:
      border-color 120ms ease,
      background-color 120ms ease;
  }
  .template:hover {
    border-color: var(--primary);
  }
  .template:focus-visible {
    outline: 2px solid var(--ring, var(--primary));
    outline-offset: 2px;
  }
  .template[aria-pressed="true"] {
    border-color: var(--primary);
    background: color-mix(in oklab, var(--primary) 8%, var(--card));
  }
  .template :global(svg) {
    flex: none;
    margin-top: 2px;
    color: var(--primary);
  }
  .template strong {
    display: block;
    font-size: 13px;
    font-weight: 600;
  }
  .template small {
    display: block;
    color: var(--muted-foreground);
    font-size: 11px;
    margin-top: 4px;
    line-height: 1.4;
  }
  .template .skipped {
    font-style: italic;
  }
  @media (prefers-reduced-motion: reduce) {
    .template {
      transition: none;
    }
  }
</style>
