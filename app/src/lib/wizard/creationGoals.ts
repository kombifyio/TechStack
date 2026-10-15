import { backendChoices } from "@kombiverselabs/ui/usecase";
import {
  installsService,
  type UseCaseCatalogView,
} from "#lib/api/useCaseCatalog.js";
import type {
  BundleQuestionDefinition,
  GoalConfigKey,
} from "./standardBundle.js";

export type CreationVariant = "discover" | "focus";

/** Intent presentation only. Never use this to decide deployment eligibility.
 * Runtime facts come from the released catalog; the preview has its own snapshot.
 * Target choices: StackKits docs/use-case-expansion/portfolio.md.
 * Documents/Files stay together; Paperless is an add-on and Remote belongs
 * to Dev. Marketplace is optional and uses the released catalog.
 * Availability follows the pinned release catalog (owner decision 2026-10-07):
 * an intent without a Wizard question or a released application (Network,
 * Automation) stays Coming soon.
 */
export const CREATION_PORTFOLIO = [
  "photos",
  "files",
  "vault",
  "media",
  "smart-home",
  "dev",
  "mail",
  "game",
  "network",
  "automation",
  "ai",
  "marketplace",
] as const;
export type CreationGoalId = (typeof CREATION_PORTFOLIO)[number];

export interface CreationGoal {
  id: CreationGoalId;
  question?: BundleQuestionDefinition<GoalConfigKey>;
  catalog?: UseCaseCatalogView;
  availability: "available" | "coming-soon";
}

/** A portfolio intent is available when the Wizard asks for it and the
 * release catalog builds it from an installing application with a native
 * alternative (its catalog maturity is present); everything else is
 * Coming soon. Beta and experimental use cases stay available and carry a
 * label (owner decision 2026-10-07), so a new release shows up on its own. */
export function creationGoals(
  questions: BundleQuestionDefinition<GoalConfigKey>[],
  catalog: Map<string, UseCaseCatalogView>,
): CreationGoal[] {
  // Intent alone cannot advertise a released application. A missing catalog
  // leaves Marketplace and other cards unavailable.
  const released = (id: CreationGoalId) =>
    backendChoices(catalog.get(id)).some(
      (component) =>
        component.kind === "application" &&
        component.maturity !== undefined &&
        component.realization !== "recorded" &&
        installsService(component),
    );
  return CREATION_PORTFOLIO.map((id) => ({
    id,
    question: questions.find((entry) => entry.configKey === id),
    catalog: catalog.get(id),
    availability:
      questions.some((question) => question.configKey === id) && released(id)
        ? "available"
        : "coming-soon",
  }));
}

/** Only advertised settings and backend identities can enter a real wizard run. */
export function supportsCreationChoice(
  catalog: UseCaseCatalogView | undefined,
  setting: string,
  value?: string | boolean,
): boolean {
  if (!catalog) return false;
  return catalog.settings.some(
    (entry) =>
      entry.id === setting &&
      (entry.kind !== "choice" ||
        value === undefined ||
        entry.options?.some((option) => option.id === value)),
  );
}

/** The default (primary) first, then the alternatives in catalog order. */
export { backendChoices } from "@kombiverselabs/ui/usecase";

export function selectedBackend(
  catalog?: UseCaseCatalogView,
  value?: string | boolean,
) {
  const choices = backendChoices(catalog);
  return (
    choices.find((entry) => entry.id === value) ??
    choices.find((entry) => entry.role === "primary") ??
    choices[0]
  );
}
