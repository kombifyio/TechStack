import { describe, expect, it } from "vitest";
import { RUNTIME_TASKS, type Task } from "./task-status";
import { updateTasksFromJob, updateTasksWithError } from "./task-updates";

function runtimeTasks(): Task[] {
  return RUNTIME_TASKS.map((task) => ({ ...task, status: "pending" }));
}

describe("creation task updates", () => {
  it("prefers a structured phase and keeps message inference as fallback", () => {
    const structured = updateTasksFromJob(runtimeTasks(), {
      phase: "prepare_rollout",
      step: "prepare_rollout",
      message: "Runtime target resolved",
      state: "running",
    });
    const legacy = updateTasksFromJob(runtimeTasks(), {
      step: "prepare_rollout",
      message: "Runtime target resolved",
      state: "running",
    });

    expect(
      structured.find((task) => task.id === "prepare_rollout")?.status,
    ).toBe("running");
    expect(
      structured.find((task) => task.id === "runtime_connected")?.status,
    ).toBe("pending");
    expect(legacy.find((task) => task.id === "runtime_connected")?.status).toBe(
      "running",
    );
  });

  it("uses structured server guidance in the existing failure model", () => {
    const failed = updateTasksWithError(runtimeTasks(), {
      step: "stackkit_rollout",
      error: "provider request failed",
      error_details: "request id: req-1",
      user_guidance: {
        title: "Provider is still starting",
        body: "Keep this server and retry the rollout shortly.",
        next_steps: [
          { id: "wait", label: "Wait for provider readiness.", kind: "note" },
          { id: "retry", label: "Retry this rollout.", kind: "retry" },
        ],
      },
    }).find((task) => task.id === "stackkit_rollout");

    expect(failed).toMatchObject({
      status: "failed",
      errorMessage: "Provider is still starting",
      errorDetails:
        "Keep this server and retry the rollout shortly.\n\nrequest id: req-1",
      troubleshooting: ["Wait for provider readiness.", "Retry this rollout."],
    });
  });
});
