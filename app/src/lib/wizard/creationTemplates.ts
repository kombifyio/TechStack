import type { CreationGoal, CreationGoalId } from "./creationGoals.js";
import type { GoalConfigKey } from "./standardBundle.js";
import type { WizardSelectionGuidance } from "../api/unifier.js";

/** Selection templates below the use case cards: one click preselects a
 * sensible set of cards (workspace KOMBIFY-DESIGN-SYSTEM-STANDARD.md §9.3c). A
 * template only ever selects cards; it never decides deployment eligibility,
 * so a goal the release catalog does not offer yet is skipped and reported. */
export type CreationTemplateIcon = "folder" | "briefcase" | "rocket";

export const CREATION_TEMPLATES = [
  {
    id: "documents-media",
    icon: "folder",
    titleKey: "wizard.templates.documentsMedia.title",
    descriptionKey: "wizard.templates.documentsMedia.description",
    goals: ["files", "photos", "media"],
  },
  {
    id: "self-employed",
    icon: "briefcase",
    titleKey: "wizard.templates.selfEmployed.title",
    descriptionKey: "wizard.templates.selfEmployed.description",
    goals: ["files", "vault", "mail", "ai", "automation"],
  },
  {
    // "To infinity and beyond": the full homelab in one click. It reuses the
    // existing `everything` goal, which the sizing requirements already read.
    id: "infinity",
    icon: "rocket",
    titleKey: "wizard.goals.everything.title",
    descriptionKey: "wizard.goals.everything.description",
    goals: "all",
  },
] as const satisfies readonly {
  id: string;
  icon: CreationTemplateIcon;
  titleKey: string;
  descriptionKey: string;
  goals: readonly CreationGoalId[] | "all";
}[];

export type CreationTemplate = (typeof CREATION_TEMPLATES)[number];
export type CreationTemplateId = CreationTemplate["id"];

export interface TemplateSelection {
  /** Goal keys the template turns on, in card order. */
  goals: GoalConfigKey[];
  /** Template goals the current catalog cannot offer yet. */
  skipped: CreationGoalId[];
}

export function templateSelection(
  template: CreationTemplate,
  cards: CreationGoal[],
): TemplateSelection {
  const wanted = (id: CreationGoalId) =>
    template.goals === "all" ||
    (template.goals as readonly CreationGoalId[]).includes(id);
  const goals = cards
    .filter((card) => card.availability === "available" && wanted(card.id))
    .flatMap((card) => (card.question ? [card.question.configKey] : []));
  const skipped = cards
    .filter(
      (card) =>
        wanted(card.id) &&
        (card.availability !== "available" || !card.question),
    )
    .map((card) => card.id);
  return {
    goals: template.goals === "all" ? [...goals, "everything"] : goals,
    skipped,
  };
}

/** An empty capacity answer still applies; only an absent answer falls back. */
export function recommendedTemplateGoals(
  cards: CreationGoal[],
  guidance: WizardSelectionGuidance | undefined,
): GoalConfigKey[] | undefined {
  if (!guidance || (!guidance.preselect && !guidance.withheld?.length)) {
    return undefined;
  }
  return cards
    .filter(
      (card) =>
        card.availability === "available" &&
        guidance.preselect?.includes(card.id),
    )
    .flatMap((card) => (card.question ? [card.question.configKey] : []));
}

/** A template is active while every goal it would select is selected, so the
 * same button turns it off again. */
export function templateActive(
  selection: TemplateSelection,
  selected: Partial<Record<GoalConfigKey, boolean>> | undefined,
): boolean {
  const own = selection.goals.filter((goal) => goal !== "everything");
  return own.length > 0 && own.every((goal) => selected?.[goal] === true);
}
