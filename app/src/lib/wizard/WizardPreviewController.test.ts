import { describe, expect, it, vi } from "vitest";
import type {
  WizardRecommendationRequest,
  WizardRecommendationResult,
} from "#lib/api/unifier.js";
import {
  WizardPreviewController,
  type WizardPreviewState,
} from "./WizardPreviewController";

const request = (goals: string[]): WizardRecommendationRequest => ({
  goals,
  services: [],
  deployment_lane: "self-hosted",
  surface: "easy",
});

const result = (stackkit: string): WizardRecommendationResult => ({
  status: "ready",
  generated_at: "2026-08-26T00:00:00Z",
  catalog_source: "test",
  missing_inputs: [],
  stale_inputs: [],
  recommendations: [
    {
      id: stackkit,
      stackkit,
      rank: 1,
      score: 100,
      recommended: true,
      reasons: [],
      alternatives: [],
      deep_link: { step: "server", section: "stackkit-foundation" },
    },
  ],
});

describe("WizardPreviewController", () => {
  it("forwards changed Smart Home choices instead of reusing another installation recommendation", async () => {
    vi.useFakeTimers();
    const load = vi.fn(async (_value: WizardRecommendationRequest) =>
      result("basement-kit"),
    );
    const controller = new WizardPreviewController(() => {}, load, 1);
    controller.update({
      ...request(["smart-home"]),
      smart_home_settings: { "operating-form": "haos" },
    });
    await vi.advanceTimersByTimeAsync(1);
    controller.update({
      ...request(["smart-home"]),
      smart_home_settings: { "instance-origin": "existing" },
      smart_home_context: { existing: true },
    });
    await vi.advanceTimersByTimeAsync(1);
    expect(load.mock.lastCall?.[0].smart_home_context?.existing).toBe(true);
    expect(
      load.mock.lastCall?.[0].smart_home_settings?.["instance-origin"],
    ).toBe("existing");
    controller.destroy();
    vi.useRealTimers();
  });
  it("publishes only the latest debounced recommendation", async () => {
    vi.useFakeTimers();
    const states: WizardPreviewState[] = [];
    const load = vi.fn(async (value: WizardRecommendationRequest) =>
      result(value.goals[0] ?? "none"),
    );
    const controller = new WizardPreviewController(
      (state) => states.push(state),
      load,
      100,
    );

    controller.update(request(["photos"]));
    controller.update(request(["vault"]));
    await vi.advanceTimersByTimeAsync(100);

    expect(load).toHaveBeenCalledTimes(1);
    expect(load.mock.calls[0][0].goals).toEqual(["vault"]);
    expect(states.at(-1)).toMatchObject({
      phase: "resolved",
      result: { recommendations: [{ stackkit: "vault" }] },
    });
    controller.destroy();
    vi.useRealTimers();
  });

  it("reuses a resolved answer without another request", async () => {
    vi.useFakeTimers();
    const load = vi.fn(async () => result("basement-kit"));
    const controller = new WizardPreviewController(() => {}, load, 25);

    controller.update(request(["files"]));
    await vi.advanceTimersByTimeAsync(25);
    controller.update(request(["files"]));

    expect(load).toHaveBeenCalledTimes(1);
    controller.destroy();
    vi.useRealTimers();
  });
});
