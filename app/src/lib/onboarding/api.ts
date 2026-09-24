/**
 * The onboarding wire (ONBOARDING-JOURNEY-STANDARD §7, §8).
 *
 * The server owns state and availability; this module only carries them. It
 * deliberately cannot push a state record: the endpoint takes actions, so a
 * client bug can never overwrite someone's progress with a stale blob.
 */
import type {
  OnboardingJourneyDescriptor,
  OnboardingStateRecord,
  StepAvailability,
} from "@kombiverselabs/onboarding-core";

import { ApiRequestError, fetchApi } from "#lib/api/client.js";

export const TECHSTACK_JOURNEY_ID = "techstack.platform";

/** The closed action vocabulary the endpoint accepts. */
export type OnboardingAction =
  "complete" | "skip" | "dismiss" | "resume" | "reset" | "coach_seen";

export interface OnboardingPayload {
  journey: OnboardingJourneyDescriptor;
  state: OnboardingStateRecord;
  availability: Readonly<Record<string, StepAvailability>>;
  revision: number;
}

/** HTTP status for a failed compare-and-swap; the caller reloads and retries. */
export const ONBOARDING_CONFLICT_STATUS = 412;

function endpoint(journeyId: string, suffix = ""): string {
  return `/api/v1/onboarding/${encodeURIComponent(journeyId)}${suffix}`;
}

export async function getOnboarding(
  journeyId: string = TECHSTACK_JOURNEY_ID,
): Promise<OnboardingPayload> {
  const response = await fetchApi<OnboardingPayload>(endpoint(journeyId));
  return response.data;
}

/**
 * `expectRevision` rides as `If-Match`. Omitting it makes the write
 * unconditional, which is only correct for actions the user cannot race
 * against themselves.
 */
export async function putOnboardingAction(
  action: OnboardingAction,
  options: {
    journeyId?: string;
    stepId?: string;
    source?: string;
    expectRevision?: number;
  } = {},
): Promise<OnboardingPayload> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  if (typeof options.expectRevision === "number") {
    headers["If-Match"] = `"${options.expectRevision}"`;
  }
  const response = await fetchApi<OnboardingPayload>(
    endpoint(options.journeyId ?? TECHSTACK_JOURNEY_ID),
    {
      method: "PUT",
      headers,
      body: JSON.stringify({
        action,
        step_id: options.stepId ?? "",
        source: options.source ?? "",
      }),
    },
  );
  return response.data;
}

export async function resetOnboarding(
  journeyId: string = TECHSTACK_JOURNEY_ID,
): Promise<OnboardingPayload> {
  const response = await fetchApi<OnboardingPayload>(
    endpoint(journeyId, "/reset"),
    { method: "POST" },
  );
  return response.data;
}

/** True when the write lost a race and the caller must reload before retrying. */
export function isOnboardingConflict(error: unknown): boolean {
  return (
    error instanceof ApiRequestError &&
    error.status === ONBOARDING_CONFLICT_STATUS
  );
}
