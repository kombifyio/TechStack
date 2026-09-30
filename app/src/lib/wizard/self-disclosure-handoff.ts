/**
 * kombify Cloud → creation wizard self-disclosure handoff.
 *
 * The Cloud dashboard captures a voluntary self-description and opens
 * `/stacks/new` with the raw answers as query parameters. This module reads
 * them back into the closed Techstack vocabulary, drops anything unknown, and
 * derives the wizard prefill. It never computes a capability score: the answers
 * are stored through the self-disclosure API and the Unifier derives the
 * profile (CREATION-EXPERIENCE-STANDARD §4).
 */
import type {
  OperatorSelfDisclosure,
  SelfDisclosureExperience,
} from "#lib/api/operatorProfile.js";
import { CANONICAL_USE_CASE_GOALS } from "./standardBundle.js";
import { createDefaultConfig, type StackConfig } from "./types.js";

const EXPERIENCES: readonly SelfDisclosureExperience[] = [
  "hands-off",
  "guided",
  "curious",
  "techie",
  "expert",
];
const PLACEMENTS = ["home", "rented", "both", "unsure"] as const;
const HARDWARE = [
  "none-yet",
  "old-pc",
  "mini-pc",
  "nas",
  "raspberry-pi",
  "rented-server",
] as const;
const HOUSEHOLDS = [
  "just-me",
  "household",
  "friends-family",
  "small-team",
] as const;
const MOTIVATIONS = [
  "privacy",
  "replace-subscriptions",
  "learning",
  "family",
  "projects",
] as const;
const NOTES_MAX_CHARS = 2_000;

/** Query parameters written by the Cloud capture surface. */
export const SELF_DISCLOSURE_PARAMS = [
  "experience",
  "goals",
  "placement",
  "hardware",
  "household",
  "motivations",
  "intent",
] as const;

/** Read access is all the parser needs; SvelteKit's `page.url` is read-only. */
type HandoffParams = Pick<URLSearchParams, "get">;

function one<T extends string>(
  params: HandoffParams,
  key: string,
  allowed: readonly T[],
): T | undefined {
  const value = params.get(key)?.trim().toLowerCase();
  return allowed.find((candidate) => candidate === value);
}

function many<T extends string>(
  params: HandoffParams,
  key: string,
  allowed: readonly T[],
): T[] | undefined {
  const values = (params.get(key) ?? "")
    .split(",")
    .map((value) => value.trim().toLowerCase());
  const result = allowed.filter((candidate) => values.includes(candidate));
  return result.length > 0 ? result : undefined;
}

/**
 * Read a Cloud handoff from the page URL. Returns null when the URL carries no
 * recognizable answer, so a plain `/stacks/new` visit stays untouched.
 */
export function readSelfDisclosureHandoff(
  params: HandoffParams,
): OperatorSelfDisclosure | null {
  const notes = (params.get("intent") ?? "").trim().slice(0, NOTES_MAX_CHARS);
  const disclosure: OperatorSelfDisclosure = {
    experience: one(params, "experience", EXPERIENCES),
    goals: many(params, "goals", CANONICAL_USE_CASE_GOALS),
    placement: one(params, "placement", PLACEMENTS),
    hardware: many(params, "hardware", HARDWARE),
    household: one(params, "household", HOUSEHOLDS),
    motivations: many(params, "motivations", MOTIVATIONS),
    notes: notes || undefined,
  };
  const answered = Object.values(disclosure).some(
    (value) => value !== undefined,
  );
  return answered ? disclosure : null;
}

/**
 * Wizard prefill for a handed-off self-disclosure. Only answers that map onto
 * an existing wizard question are applied; everything stays editable.
 */
export function selfDisclosureWizardConfig(
  disclosure: OperatorSelfDisclosure,
): StackConfig {
  const config = createDefaultConfig();
  if (config.goals && disclosure.goals?.length) {
    for (const goal of CANONICAL_USE_CASE_GOALS) {
      config.goals[goal] = disclosure.goals.includes(goal);
    }
  } else if (config.goals) {
    config.goals.files = true;
  }
  if (disclosure.household && disclosure.household !== "just-me") {
    config.household = {
      profile: "shared",
      people: config.household?.people ?? [],
    };
  }
  return config;
}

/** The page URL without the handoff parameters, for history replacement. */
export function withoutSelfDisclosureParams(url: Pick<URL, "href">): string {
  const next = new URL(url.href);
  for (const key of SELF_DISCLOSURE_PARAMS) next.searchParams.delete(key);
  return `${next.pathname}${next.search}${next.hash}`;
}
