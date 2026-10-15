import { describe, expect, it } from "vitest";
import { createRuntimeTaskList } from "#lib/wizard/index.js";
import {
  extractDegradedCompletion,
  markDegradedTasks,
} from "./creation-results";

describe("degraded completion", () => {
  it("turns a completed rollout with a degraded component into a completion with attention, not a failure", () => {
    const result = {
      runtime_lifecycle: { status: "completed_degraded" },
      degraded_components: [
        {
          component: "public_tls",
          status: "degraded",
          reason_code: "origin_certificate_unavailable",
          stage: "stackkit_rollout",
          retryable: true,
          repair: { mode: "automatic", status: "scheduled", job_id: "job-r" },
        },
      ],
    };

    const degraded = extractDegradedCompletion("completed", result);
    expect(degraded?.components[0]?.repair?.status).toBe("scheduled");

    const tasks = markDegradedTasks(createRuntimeTaskList(), degraded!);
    expect(tasks.every((task) => task.status === "completed")).toBe(true);
    expect(tasks.find((task) => task.attention)?.id).toBe("stackkit_rollout");
  });
});
