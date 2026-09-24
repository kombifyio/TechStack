import { describe, expect, it, vi, beforeEach } from "vitest";

import type { OnboardingPayload } from "./api.js";

const api = vi.hoisted(() => ({
  getOnboarding: vi.fn(),
  putOnboardingAction: vi.fn(),
  resetOnboarding: vi.fn(),
  isOnboardingConflict: vi.fn(() => false),
  TECHSTACK_JOURNEY_ID: "techstack.platform",
}));

vi.mock("./api.js", () => api);
vi.mock("./analytics.js", () => ({
  trackOnboardingStep: () => {},
  trackOnboardingJourney: () => {},
  trackOnboardingBlocked: () => {},
}));

const { onboardingStore } = await import("./store.svelte.js");

const descriptor = {
  version: "1",
  journey_id: "techstack.platform",
  journey_version: 1,
  stage: "product",
  product: "techstack",
  modes: ["cloud", "self_hosted", "local"],
  reopen_on_version_bump: true,
  steps: [
    {
      id: "techstack.deploy_service",
      message_id: "onboarding.a.title",
      body_message_id: "onboarding.a.body",
      cta_message_id: "onboarding.a.cta",
      route: "/services",
      surface: ["dashboard"],
      order: 0,
      optional: false,
      completion: {
        kind: "derived",
        predicate_id: "techstack.service_running",
      },
      required_capability: null,
      cost_bearing: false,
      coach_anchor: null,
      analytics_id: "techstack.deploy_service",
    },
  ],
} as unknown as OnboardingPayload["journey"];

function payload(revision: number, completed: string[]): OnboardingPayload {
  return {
    journey: descriptor,
    state: {
      schema_version: 1,
      journey_id: "techstack.platform",
      journey_version: 1,
      completed: completed.map((id) => ({
        step_id: id,
        at: "2026-08-21T10:00:00Z",
        source: "manual" as const,
      })),
      seen_steps: [],
      status: "active" as const,
      dismissed_at: null,
      reopened_at: null,
      revision,
      updated_at: "2026-08-21T10:00:00Z",
    },
    availability: {},
    revision,
  };
}

describe("getting started store", () => {
  beforeEach(() => {
    onboardingStore.clear();
    vi.clearAllMocks();
    api.isOnboardingConflict.mockReturnValue(false);
  });

  it("shows what the server returned after an action", async () => {
    api.getOnboarding.mockResolvedValueOnce(payload(0, []));
    await onboardingStore.load("someone@example.com");
    expect(onboardingStore.resolved?.steps[0]?.done).toBe(false);

    api.putOnboardingAction.mockResolvedValueOnce(
      payload(1, ["techstack.deploy_service"]),
    );
    await onboardingStore.skip("techstack.deploy_service");

    expect(onboardingStore.revision).toBe(1);
    expect(onboardingStore.resolved?.steps[0]?.done).toBe(true);
  });

  // A tick the server refused must never appear. There is no optimistic layer
  // precisely so this cannot happen, and the reload proves the server wins.
  it("takes the server's version when the write loses a race", async () => {
    api.getOnboarding.mockResolvedValueOnce(payload(0, []));
    await onboardingStore.load("someone@example.com");

    api.putOnboardingAction.mockRejectedValueOnce(new Error("412"));
    api.isOnboardingConflict.mockReturnValue(true);
    api.getOnboarding.mockResolvedValueOnce(payload(3, []));

    await onboardingStore.skip("techstack.deploy_service");

    expect(onboardingStore.revision).toBe(3);
    expect(onboardingStore.resolved?.steps[0]?.done).toBe(false);
  });

  it("stays invisible when the journey cannot be loaded", async () => {
    api.getOnboarding.mockRejectedValueOnce(new Error("network down"));
    await onboardingStore.load("someone@example.com");

    expect(onboardingStore.visible).toBe(false);
    expect(onboardingStore.badge).toBeNull();
    expect(onboardingStore.error).toContain("network down");
  });
});
