/**
 * The product's half of the journey contract: message resolution.
 *
 * The descriptor carries only message ids (ONBOARDING-JOURNEY-STANDARD §2), so
 * a step's prose is this product's i18n, not the server's payload. Denials are
 * localized by `reason_code`; the server's own guidance is the fallback, so a
 * reason we have not translated yet still explains itself instead of going
 * blank.
 */
import type { ResolvedStep } from "@kombiverselabs/onboarding-core";

import { t } from "#lib/i18n.js";

/**
 * Resolve one message id for a component. A missing key returns the id, so a
 * gap shows up as an obviously wrong string rather than as an empty card.
 */
export function resolveOnboardingMessage(messageId: string): string {
  return t(messageId);
}

/** Empty when this locale has no entry — the caller then uses a fallback. */
function localized(messageId: string): string {
  const translated = t(messageId);
  return translated === messageId ? "" : translated;
}

/** The i18n key a denial reason maps to, e.g. `policy.selfhost_byos`. */
export function denialMessageKey(reasonCode: string, field: string): string {
  return `onboarding.denied.${reasonCode.replace(/\./g, "_")}.${field}`;
}

export interface DenialCopy {
  title: string;
  body: string;
  nextSteps: readonly string[];
}

/**
 * Localized denial copy for a step the user cannot reach, or the server's own
 * guidance when this locale has no translation for that reason.
 */
export function denialCopy(step: ResolvedStep): DenialCopy | null {
  const { reason_code: reasonCode, user_guidance: guidance } =
    step.availability;
  if (!reasonCode && !guidance) return null;

  const localizedTitle = reasonCode
    ? localized(denialMessageKey(reasonCode, "title"))
    : "";
  const localizedBody = reasonCode
    ? localized(denialMessageKey(reasonCode, "body"))
    : "";
  const localizedNextStep = reasonCode
    ? localized(denialMessageKey(reasonCode, "next_step"))
    : "";

  return {
    title: localizedTitle || guidance?.title || "",
    body: localizedBody || guidance?.body || "",
    nextSteps: localizedNextStep
      ? [localizedNextStep]
      : (guidance?.next_steps ?? []),
  };
}
