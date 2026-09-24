import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  createWizardRun,
  getActiveWizardRun,
  WIZARD_INTENT_SCHEMA,
} from "./wizardRuns";

function json(data: unknown): Response {
  return new Response(JSON.stringify(data), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

describe("wizard runs API", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.stubGlobal("window", {
      location: { origin: "https://techstack.test" },
    });
  });

  afterEach(() => vi.unstubAllGlobals());

  it("maps the create response to the canonical deployment scope", async () => {
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(json({ token: "csrf-token" }))
      .mockResolvedValueOnce(
        json({
          data: {
            run_id: "run-1",
            run_kind: "first-run",
            homelab_id: "homelab-1",
            kit_assignment_mode: "found",
            stack_id: "deployment-1",
            node_id: "node-1",
            state: "provisioning",
          },
        }),
      );

    const run = await createWizardRun({
      intent: {
        schema: WIZARD_INTENT_SCHEMA,
        run_kind: "first-run",
        name: "Demo",
        server: {},
        kit_assignment: { mode: "found" },
      },
    });

    expect(run.kit_deployment_id).toBe("deployment-1");
    expect(run).not.toHaveProperty("stack_id");
  });

  it("maps the active run to the canonical deployment scope", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      json({
        data: {
          run: {
            run_id: "run-1",
            status: "completed",
            run_kind: "first-run",
            stack_id: "deployment-1",
          },
        },
      }),
    );

    const run = await getActiveWizardRun();

    expect(run?.kit_deployment_id).toBe("deployment-1");
    expect(run).not.toHaveProperty("stack_id");
  });
});
