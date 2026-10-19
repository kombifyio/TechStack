/**
 * kombify Cloud → creation wizard self-disclosure handoff.
 *
 * The Cloud dashboard captures a voluntary self-description and opens
 * `/stacks/new` with the raw answers as query parameters. This module reads
 * them back into the closed Techstack vocabulary, drops anything unknown, and
 * derives the wizard prefill and the review the operator confirms. A link only
 * prefills: anyone can craft one, so it carries no free text and nothing is
 * stored until the operator confirms the answers. It never computes a
 * capability score: the Unifier derives the profile from the stored answers
 * (KOMBIFY-DESIGN-SYSTEM-STANDARD.md §9.4).
 */
import type {
  OperatorSelfDisclosure,
  SelfDisclosureExperience,
} from "#lib/api/operatorProfile.js";
import {
  CANONICAL_USE_CASE_GOALS,
  getEasyGoalQuestions,
} from "./standardBundle.js";
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
/** Upper bound of the free-text note the operator types in the review. */
export const SELF_DISCLOSURE_NOTES_MAX_CHARS = 2_000;

/**
 * Query parameters a handoff link may carry. `intent` is free text that older
 * Cloud links sent; it is never read, only stripped from the URL.
 */
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
 * recognizable answer, so a plain `/stacks/new` visit stays untouched. Free
 * text is ignored: notes come only from what the operator types in Techstack.
 */
export function readSelfDisclosureHandoff(
  params: HandoffParams,
): OperatorSelfDisclosure | null {
  const disclosure: OperatorSelfDisclosure = {
    experience: one(params, "experience", EXPERIENCES),
    goals: many(params, "goals", CANONICAL_USE_CASE_GOALS),
    placement: one(params, "placement", PLACEMENTS),
    hardware: many(params, "hardware", HARDWARE),
    household: one(params, "household", HOUSEHOLDS),
    motivations: many(params, "motivations", MOTIVATIONS),
  };
  const answered = Object.values(disclosure).some(
    (value) => value !== undefined,
  );
  return answered ? disclosure : null;
}

/** The canonical creation selection, excluding profile and host parameters. */
export function creationUseCaseHandoffKey(params: HandoffParams): string {
  return one(params, "use-case", CANONICAL_USE_CASE_GOALS) ?? "";
}

const SETUP_ID = /^[a-z0-9][a-z0-9-]{0,63}$/;
const SETUP_MAX_PAIRS = 64;

/**
 * Guided-setup answers a link carries as `setup=<question>:<choice>,…`, e.g.
 * from the Cloud dashboard's use-case card. Ids are closed tokens; the card
 * resolves them against its own flow and drops whatever it does not know.
 */
export function creationSetupHandoff(
  params: HandoffParams,
): Record<string, string[]> | undefined {
  const answers: Record<string, string[]> = {};
  const pairs = (params.get("setup") ?? "")
    .split(",")
    .slice(0, SETUP_MAX_PAIRS);
  for (const pair of pairs) {
    const [question, choice, ...rest] = pair.trim().toLowerCase().split(":");
    if (rest.length > 0 || !SETUP_ID.test(question ?? "")) continue;
    if (!SETUP_ID.test(choice ?? "")) continue;
    const list = (answers[question] ??= []);
    if (!list.includes(choice)) list.push(choice);
  }
  return Object.keys(answers).length > 0 ? answers : undefined;
}

/** A use-case navigation choice seeds only this creation draft, never a profile. */
export function creationUseCaseWizardConfig(
  params: HandoffParams,
): StackConfig | undefined {
  const selected = creationUseCaseHandoffKey(params);
  if (!selected) return undefined;
  const config = createDefaultConfig();
  if (config.goals) {
    for (const goal of CANONICAL_USE_CASE_GOALS) {
      config.goals[goal] = goal === selected;
    }
    config.goals.everything = false;
  }
  const setup = creationSetupHandoff(params);
  if (setup) config.useCaseSetupAnswers = { [selected]: setup };
  return config;
}

/** One reviewed question with the message keys of its handed-over answers. */
export interface SelfDisclosureReviewRow {
  question: string;
  questionKey: string;
  answerKeys: string[];
}

/**
 * The handed-over answers as message keys, in capture order, so the operator
 * sees exactly what a confirmation would store.
 */
export function selfDisclosureReviewRows(
  disclosure: OperatorSelfDisclosure,
): SelfDisclosureReviewRow[] {
  const goalTitles = new Map<string, string>(
    getEasyGoalQuestions().map((question) => [question.key, question.titleKey]),
  );
  const rows: [string, string[]][] = [
    [
      "experience",
      disclosure.experience
        ? [`wizard.selfDisclosure.experience.${disclosure.experience}`]
        : [],
    ],
    [
      "goals",
      (disclosure.goals ?? []).flatMap((goal) => goalTitles.get(goal) ?? []),
    ],
    [
      "placement",
      disclosure.placement
        ? [`wizard.selfDisclosure.placement.${disclosure.placement}`]
        : [],
    ],
    [
      "hardware",
      (disclosure.hardware ?? []).map(
        (item) => `wizard.selfDisclosure.hardware.${item}`,
      ),
    ],
    [
      "household",
      disclosure.household
        ? [`wizard.selfDisclosure.household.${disclosure.household}`]
        : [],
    ],
    [
      "motivations",
      (disclosure.motivations ?? []).map(
        (item) => `wizard.selfDisclosure.motivation.${item}`,
      ),
    ],
  ];
  return rows
    .filter(([, answerKeys]) => answerKeys.length > 0)
    .map(([question, answerKeys]) => ({
      question,
      questionKey: `wizard.selfDisclosure.question.${question}`,
      answerKeys,
    }));
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
