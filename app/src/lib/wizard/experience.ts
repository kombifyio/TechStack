import {
  resolveSetup,
  type UseCaseSetupAnswers,
  type UseCaseSetupFlow,
  type UseCaseSetupQuestion,
} from "@kombiverselabs/ui/usecase";
import type { SelfDisclosureExperience } from "#lib/api/operatorProfile.js";

/**
 * How many guided setup steps a user sees, by how hands-on they said they want
 * to be (the first question of the self-description, CREATION-EXPERIENCE-
 * STANDARD §4). Nothing is withheld: every step above the user's level stays
 * one click away under "More settings", and a step that is not shown still
 * applies its recommendation.
 */
export const EXPERIENCE_LEVELS: readonly SelfDisclosureExperience[] = [
  "hands-off",
  "guided",
  "curious",
  "techie",
  "expert",
];

/** Level 1 (hands-off) to 5 (expert). Unanswered reads as "guide me". */
export function experienceLevel(
  experience: SelfDisclosureExperience | undefined | null,
): number {
  const index = experience ? EXPERIENCE_LEVELS.indexOf(experience) : -1;
  return index < 0 ? 2 : index + 1;
}

/** A guided setup question with the lowest level that sees it as a step. */
export type TieredQuestion = UseCaseSetupQuestion & { level?: number };

/**
 * The lowest level that sees a catalog setting as its own step. Decisions a
 * user can answer from their own life (which game edition, whether they
 * already have a mail provider) are for everyone; machine sizing and
 * installation mechanics are for people who asked for that control.
 */
const SETTING_LEVEL: Record<string, number> = {
  edition: 1,
  hosting: 1,
  "instance-origin": 2,
  "inference-location": 2,
  accelerator: 3,
  "hardware-transcoding": 3,
  profile: 4,
  "operating-form": 4,
  "library-volume": 4,
  "model-size": 4,
};

export function settingLevel(setting: {
  id: string;
  kind: string;
  depth?: string;
}): number {
  const known = SETTING_LEVEL[setting.id];
  if (known !== undefined) return known;
  if (setting.depth === "advanced") return 4;
  return setting.kind === "toggle" ? 2 : 3;
}

export interface TieredFlow {
  /** The steps this level walks through. */
  flow: UseCaseSetupFlow;
  /** Steps kept under "More settings" for this level. */
  more: number;
}

/**
 * The flow one level walks through. When a level would see no step at all,
 * the gentlest steps of the use case stay, so every card still asks
 * something rather than showing an empty review.
 *
 * A shown step whose condition names a step kept under "More settings" is
 * decided by that step's current answer, which the shown flow cannot change.
 */
export function flowForLevel(
  flow: UseCaseSetupFlow,
  level: number,
  answers: UseCaseSetupAnswers = {},
  showAll = false,
): TieredFlow {
  const questions = flow.questions as TieredQuestion[];
  if (showAll) return { flow, more: 0 };
  let shown = questions.filter((question) => (question.level ?? 1) <= level);
  if (shown.length === 0 && questions.length > 0) {
    const gentlest = Math.min(...questions.map((entry) => entry.level ?? 1));
    shown = questions.filter((question) => (question.level ?? 1) === gentlest);
  }
  const shownIds = new Set(shown.map((question) => question.id));
  const full = resolveSetup(flow, answers).answers;
  const holds = (question: string, anyOf: string[]) =>
    [full[question] ?? []].flat().some((id) => anyOf.includes(id));
  const steps = shown.flatMap((question) => {
    if (!question.when) return [question];
    const hidden = question.when.filter(
      (entry) => !shownIds.has(entry.question),
    );
    if (!hidden.every((entry) => holds(entry.question, entry.anyOf))) return [];
    const when = question.when.filter((entry) => shownIds.has(entry.question));
    return [{ ...question, when: when.length > 0 ? when : undefined }];
  });
  return {
    flow: { ...flow, questions: steps },
    more: questions.length - shown.length,
  };
}
