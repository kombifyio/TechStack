import type { HomelabSummary } from "#lib/api/homelab.js";
import { CANONICAL_USE_CASE_GOALS } from "./standardBundle.js";
import { createDefaultConfig, type StackConfig } from "./types.js";

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? Object.fromEntries(Object.entries(value))
    : undefined;
}

function decisions<T>(
  value: unknown,
  parse: (value: unknown) => T | undefined,
) {
  const result: Record<string, Record<string, T>> = {};
  for (const [slug, values] of Object.entries(record(value) ?? {})) {
    const entries: Record<string, T> = {};
    for (const [id, value] of Object.entries(record(values) ?? {})) {
      const parsed = parse(value);
      if (parsed !== undefined) entries[id] = parsed;
    }
    result[slug] = entries;
  }
  return result;
}

/** Reopen the canonical saved wishes; executable defaults remain catalog-owned. */
export function storedBlockedWizardConfig(
  homelab: HomelabSummary | null | undefined,
): StackConfig | undefined {
  const wizard = record(homelab?.intent.wizard);
  if (!homelab || wizard?.projection_status !== "blocked") return undefined;
  const config = createDefaultConfig();
  config.name = homelab.name;
  const goals = Array.isArray(wizard.goals) ? wizard.goals : [];
  if (config.goals) {
    for (const goal of CANONICAL_USE_CASE_GOALS) {
      config.goals[goal] = goals.includes(goal);
    }
    config.goals.everything = false;
  }
  config.useCaseSettings = decisions(wizard.use_case_settings, (value) =>
    typeof value === "string" || typeof value === "boolean" ? value : undefined,
  );
  config.useCaseCapabilities = decisions(
    wizard.use_case_capabilities,
    (value) => (typeof value === "string" ? value : undefined),
  );
  config.useCaseSetupAnswers = decisions(
    wizard.use_case_setup_answers,
    (value) =>
      Array.isArray(value) &&
      value.every((answer) => typeof answer === "string")
        ? value.filter((answer): answer is string => typeof answer === "string")
        : undefined,
  );
  const mode = record(wizard.access)?.mode;
  if (mode === "local" || mode === "remote-private") {
    config.network.accessMode = mode === "local" ? "home" : "anywhere";
    config.network.accessModeSelected = true;
  }
  const publications = record(wizard.access)?.publications;
  if (Array.isArray(publications)) {
    config.network.publications = publications.map((value) => {
      const publication = record(value);
      if (
        typeof publication?.service_id !== "string" ||
        publication.exposure !== "public"
      ) {
        throw new Error("Stored access publication is invalid");
      }
      return { service_id: publication.service_id, exposure: "public" };
    });
  }
  const household = record(wizard.household);
  const profile = household?.profile;
  if (profile === "solo" || profile === "shared") {
    const people = Array.isArray(household?.planned_people)
      ? household.planned_people.map((value) => {
          const person = record(value);
          if (typeof person?.client_ref !== "string") {
            throw new Error("Stored household person has no identity");
          }
          return {
            id: person.client_ref,
            name: typeof person.name === "string" ? person.name : "",
            email: typeof person.email === "string" ? person.email : "",
          };
        })
      : [];
    config.household = { profile, people };
    config.audience = {
      onlyMe: profile === "solo",
      familyFriends: profile === "shared",
      public: false,
    };
  }
  return config;
}
