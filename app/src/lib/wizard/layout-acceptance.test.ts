/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  acceptableLayoutRecommendation,
  type LayoutRecommendationView,
  type LayoutSelection,
} from "#lib/api/layout.js";
import { applyServerProvisioningMode } from "./types";
import { createExpansionConfig } from "./expansion-config";
import {
  buildExtendRunRequest,
  buildJoinRunRequest,
  buildLayoutSelection,
  WizardInputError,
} from "./wizardRunRequest";
import {
  LayoutPreviewController,
  acceptedLayoutFromPreview,
  type LayoutPreviewState,
} from "./LayoutPreviewController";
import { mintExtendWizardIdempotencyKey } from "./idempotency-keys";

function view(
  overrides: Partial<LayoutRecommendationView> = {},
): LayoutRecommendationView {
  return {
    id: "rec-1",
    api_version: "techstack.kombify.io/recommendation/v1",
    state: "open",
    scope: "stack:stack-1",
    stack_id: "stack-1",
    flow: "add-use-case",
    outcome: "feasible",
    proposal_digest: "sha256:abc",
    snapshot_digest: "sha256:snap",
    catalog_release: "v1.0.0",
    planner: { name: "baseline", version: "1" },
    nodes: [],
    assignments: [],
    verification: { status: "verified" },
    ...overrides,
  };
}

function selection(goals: string[]): LayoutSelection {
  return { flow: "add-use-case", stack_id: "stack-1", goals };
}

describe("layout recommendation acceptance", () => {
  it("is offered only for a recorded, open recommendation verified against the hardware", () => {
    expect(acceptableLayoutRecommendation(view())).toEqual({
      id: "rec-1",
      proposal_digest: "sha256:abc",
    });
    // Advice that was not recorded (no id) or not verified is information only.
    expect(
      acceptableLayoutRecommendation(
        view({
          id: undefined,
          state: "unverified",
          proposal_digest: undefined,
        }),
      ),
    ).toBeUndefined();
    for (const status of [
      "infeasible",
      "unknown",
      "unavailable",
      "skipped",
    ] as const) {
      expect(
        acceptableLayoutRecommendation(view({ verification: { status } })),
      ).toBeUndefined();
    }
    expect(
      acceptableLayoutRecommendation(view({ state: "expired" })),
    ).toBeUndefined();
  });

  it("rides on the run request only when acceptable", () => {
    const config = createExpansionConfig();
    config.goals!["smart-home"] = true;
    const accepted = acceptableLayoutRecommendation(view());

    const extend = buildExtendRunRequest(config, "stack-1", "Home", accepted);
    expect(extend.intent.layout_recommendation).toEqual({
      id: "rec-1",
      proposal_digest: "sha256:abc",
    });
    expect(extend.intent.kit_assignment).toEqual({
      mode: "extend",
      kit_deployment_id: "stack-1",
    });
    expect(extend.intent.server).toEqual({});
    expect(
      "layout_recommendation" in
        buildExtendRunRequest(config, "stack-1", "Home").intent,
    ).toBe(false);

    const join = buildJoinRunRequest(config, "stack-1", "Home", [], accepted);
    expect(join.intent.layout_recommendation?.id).toBe("rec-1");
    expect(
      "layout_recommendation" in
        buildJoinRunRequest(config, "stack-1", "Home", []).intent,
    ).toBe(false);
  });

  it("refuses an extend run that adds no use case", () => {
    expect(() =>
      buildExtendRunRequest(createExpansionConfig(), "stack-1", "Home"),
    ).toThrow(WizardInputError);
  });

  it("names a hypervisor guest's sizing as the planned node, and nothing else does", () => {
    const config = createExpansionConfig();
    config.goals!["smart-home"] = true;
    expect(
      buildLayoutSelection(config, "stack-1", "add-server"),
    ).not.toHaveProperty("planned_node");

    applyServerProvisioningMode(config, "hypervisor");
    Object.assign(config.serverProvisioning.substrate!, {
      serverId: "substrate-1",
      storage: "local",
      bridge: "vmbr0",
      cpu: 4,
      memoryMiB: 8192,
      diskGiB: 64,
    });
    const planned = buildLayoutSelection(
      config,
      "stack-1",
      "add-server",
    )?.planned_node;
    expect(planned).toMatchObject({
      cpu_cores: 4,
      ram_gb: 8,
      storage_gb: 64,
      source: "guest-sizing",
    });
    expect(
      buildLayoutSelection(config, "stack-1", "add-use-case"),
    ).not.toHaveProperty("planned_node");

    const empty = createExpansionConfig();
    expect(buildLayoutSelection(empty, "stack-1", "add-server")).toBeNull();
  });
});

describe("layout preview controller", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("drops a response that belongs to an earlier selection and withholds acceptance while loading", async () => {
    const pending = new Map<
      string,
      (value: LayoutRecommendationView) => void
    >();
    const load = vi.fn(
      (next: LayoutSelection) =>
        new Promise<LayoutRecommendationView>((resolve) => {
          pending.set(next.goals.join(","), resolve);
        }),
    );
    let state: LayoutPreviewState = { phase: "idle" };
    const controller = new LayoutPreviewController(
      (next) => (state = next),
      load,
      10,
    );

    controller.update(selection(["vault"]));
    await vi.advanceTimersByTimeAsync(10);
    controller.update(selection(["vault", "photos"]));
    expect(state.phase).toBe("loading");
    expect(acceptedLayoutFromPreview(state)).toBeUndefined();
    await vi.advanceTimersByTimeAsync(10);
    expect(load).toHaveBeenCalledTimes(2);

    pending.get("vault,photos")!(
      view({ id: "rec-new", proposal_digest: "sha256:new" }),
    );
    await vi.advanceTimersByTimeAsync(0);
    pending.get("vault")!(
      view({ id: "rec-old", proposal_digest: "sha256:old" }),
    );
    await vi.advanceTimersByTimeAsync(0);

    expect(state.phase).toBe("resolved");
    expect(acceptedLayoutFromPreview(state)).toEqual({
      id: "rec-new",
      proposal_digest: "sha256:new",
    });
    controller.destroy();
  });
});

describe("extend run idempotency key", () => {
  beforeEach(() => window.sessionStorage.clear());

  it("keeps the key of a repeated request and mints a new one for a changed request", () => {
    const first = mintExtendWizardIdempotencyKey("stack-1", "request-a");
    expect(mintExtendWizardIdempotencyKey("stack-1", "request-a")).toBe(first);
    expect(mintExtendWizardIdempotencyKey("stack-1", "request-b")).not.toBe(
      first,
    );
  });
});
