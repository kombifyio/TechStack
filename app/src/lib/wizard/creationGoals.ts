import type {
  UseCaseCatalogView,
  UseCaseCatalogComponent,
} from "#lib/api/useCaseCatalog.js";
import type {
  BundleQuestionDefinition,
  GoalConfigKey,
} from "./standardBundle.js";

export type CreationVariant = "discover" | "focus";

/** Intent presentation only. Never use this to decide deployment eligibility.
 * Runtime facts come from the released catalog; the preview has its own snapshot.
 * Target choices: StackKits docs/use-case-expansion at e29d4561.
 * Owner correction, 2026-09-19: Documents/Files together; Paperless is an add-on.
 * Remote belongs to Dev. An additional twelfth main intent is still unresolved.
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
] as const;
export type CreationGoalId = (typeof CREATION_PORTFOLIO)[number];

export interface CreationGoal {
  id: CreationGoalId;
  question?: BundleQuestionDefinition<GoalConfigKey>;
  catalog?: UseCaseCatalogView;
  availability: "available" | "coming-soon";
}

/** Recovered product targets are display-only until the runtime advertises them.
 * Source: StackKits use-case-expansion plan e29d4561 and owner decision 2026-09-19.
 * Keep the original runtime catalog for every eligibility/setting decision.
 */
export function withCreationTargets(
  source: Map<string, UseCaseCatalogView>,
  preview = false,
) {
  const result = new Map(source);
  const component = (
    id: string,
    name: string,
    role: UseCaseCatalogComponent["role"],
  ): UseCaseCatalogComponent => ({ id, name, role, kind: "application" });
  const files = source.get("files");
  if (
    files &&
    !files.components.some((entry) => entry.id === "paperless-ngx")
  ) {
    result.set("files", {
      ...files,
      components: [
        ...files.components,
        component("paperless-ngx", "Paperless-ngx", "supporting"),
      ],
    });
  }
  const targets = new Map<string, UseCaseCatalogComponent[]>([
    [
      "mail",
      [
        component("paperwork", "kombify Paperwork", "primary"),
        component("roundcube", "Roundcube", "alternative"),
        component("stalwart", "Stalwart", "supporting"),
        component("mailcow", "mailcow", "supporting"),
      ],
    ],
    ["game", [component("pterodactyl", "Pterodactyl", "primary")]],
  ]);
  for (const [id, components] of targets) {
    const released = source.get(id);
    if (preview || released?.deliverable === false)
      result.set(id, {
        ...released,
        components,
        computeTiers: released?.computeTiers ?? {},
        settings: released?.settings ?? [],
      });
  }
  return result;
}

export function creationGoals(
  questions: BundleQuestionDefinition<GoalConfigKey>[],
  catalog: Map<string, UseCaseCatalogView>,
): CreationGoal[] {
  return CREATION_PORTFOLIO.map((id) => ({
    id,
    question: questions.find((entry) => entry.configKey === id),
    catalog: catalog.get(id),
    availability:
      ["network", "automation", "ai"].includes(id) ||
      !questions.some((question) => question.configKey === id)
        ? "coming-soon"
        : "available",
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

export function backendChoices(catalog?: UseCaseCatalogView) {
  return (catalog?.components ?? []).filter(
    (component) =>
      component.role === "primary" || component.role === "alternative",
  );
}

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
