/**
 * The `activation:*` telemetry of the onboarding journey
 * (ONBOARDING-JOURNEY-STANDARD §9).
 *
 * Every event literal lives in THIS file so the workspace catalog audit can
 * find them by reading one place. The property sets are exactly the catalog's
 * required + optional fields for each event — anything else is a forbidden
 * property, and progress telemetry must not become a second copy of the state.
 */
import type { ResolvedJourney } from "@kombiverselabs/onboarding-core";

import {
  createPostHogClient,
  toTechstackAnalyticsUser,
} from "#lib/analytics/posthog.js";
import { getClientBootstrap } from "#lib/client/bootstrap.js";
import { appVersion } from "#lib/config.js";
import { authStore } from "#lib/stores/auth.svelte.js";

const EVENT_STEP_COMPLETED = "activation:onboarding_step_completed";
const EVENT_STEP_SKIPPED = "activation:onboarding_step_skipped";
const EVENT_STEP_BLOCKED = "activation:onboarding_step_blocked";
const EVENT_STARTED = "activation:onboarding_started";
const EVENT_COMPLETED = "activation:onboarding_completed";
const EVENT_DISMISSED = "activation:onboarding_dismissed";
const EVENT_REOPENED = "activation:onboarding_reopened";
const EVENT_COACHMARK_SHOWN = "activation:coachmark_shown";

function client() {
  const bootstrap = getClientBootstrap();
  return createPostHogClient({
    apiKey: bootstrap.telemetry.posthog.key || undefined,
    host: bootstrap.telemetry.posthog.host || undefined,
    environment: bootstrap.telemetry.posthog.environment || undefined,
    edition:
      bootstrap.kombifyEdition ||
      (authStore.deploymentMode === "saas" ? "saas-embedded" : "selfhost-oss"),
    appVersion,
    location: typeof window === "undefined" ? undefined : window.location,
  });
}

/** Journey identity, required on every activation event. */
function journeyProps(journey: ResolvedJourney | null) {
  if (!journey) return null;
  return {
    journey_id: journey.journeyId,
    journey_version: journey.journeyVersion,
  };
}

// Every activation event is identity-mode auth0-sub, so an event without a
// signed-in subject is not a measurement — it is noise with no cohort.
function capture(event: string, properties: Record<string, unknown>): void {
  const user = toTechstackAnalyticsUser(authStore.cloudUser);
  if (!user?.authSubject) return;
  // Self-host has no PostHog key, so the client is a no-op there by
  // construction rather than by a branch here.
  void client().capture(event, { user, properties });
}

export function trackOnboardingStep(
  action: "complete" | "skip" | "dismiss" | "resume",
  stepId: string,
  journey: ResolvedJourney | null,
): void {
  const base = journeyProps(journey);
  if (!base) return;
  if (action === "complete")
    capture(EVENT_STEP_COMPLETED, { ...base, step_id: stepId });
  else if (action === "skip")
    capture(EVENT_STEP_SKIPPED, { ...base, step_id: stepId, source: "manual" });
}

/**
 * A step the user can see but cannot reach. Its reason_code is the whole
 * point: "how many people stall here, and on what" is the question this
 * journey exists to answer.
 */
export function trackOnboardingBlocked(
  stepId: string,
  reasonCode: string,
  journey: ResolvedJourney | null,
): void {
  const base = journeyProps(journey);
  if (!base) return;
  capture(EVENT_STEP_BLOCKED, {
    ...base,
    step_id: stepId,
    reason_code: reasonCode,
  });
}

export function trackOnboardingJourney(
  phase: "started" | "completed" | "dismissed" | "resumed" | "reset",
  journey: ResolvedJourney | null,
): void {
  const base = journeyProps(journey);
  if (!base) return;
  switch (phase) {
    case "started":
      capture(EVENT_STARTED, base);
      break;
    case "completed":
      capture(EVENT_COMPLETED, base);
      break;
    case "dismissed":
      capture(EVENT_DISMISSED, base);
      break;
    // A reset is a re-entry from the user's point of view, which is what
    // `reopened` measures; there is no separate reset event in the catalog.
    case "resumed":
    case "reset":
      capture(EVENT_REOPENED, base);
      break;
  }
}
