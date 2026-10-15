import { describe, expect, it } from "vitest";
import { RUNTIME_TASKS, createRuntimeTaskList, type Task } from "./task-status";
import { computeOverallProgress } from "./task-progress";
import { ERROR_TROUBLESHOOTING } from "./provider-errors";
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

  it("never moves the reached position back when a resumed job replays earlier steps", () => {
    const leasing = updateTasksFromJob(createRuntimeTaskList(), {
      step: "create_lease",
      message: "Requesting managed cloud Node",
      state: "waiting",
    });
    const replayed = updateTasksFromJob(leasing, {
      step: "create_spec",
      message: "Persisting intent + requirements...",
      state: "running",
    });

    const lease = replayed.find((task) => task.id === "create_lease");
    expect(lease?.status).toBe("running");
    expect(lease?.message).toBe("Requesting managed cloud Node");
    expect(computeOverallProgress(replayed, {}, 0)).toBeGreaterThanOrEqual(
      computeOverallProgress(leasing, {}, 0),
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

  it("tells a customer whose apply the node ended as unresponsive that the setup can be restarted", () => {
    const failed = updateTasksWithError(runtimeTasks(), {
      step: "stackkit_rollout",
      error:
        "StackKits rollout failed: typed StackKits apply command job-1-apply failed: StackKits apply stopped responding (stackkit_apply_stalled owner=runtime/tofu/main position=3 total=10 idle_seconds=180)",
    }).find((task) => task.id === "stackkit_rollout");

    expect(failed?.errorMessage).toBe(
      ERROR_TROUBLESHOOTING.stackkit_apply_stalled.message,
    );
    expect(failed?.errorMessage).not.toBe(
      ERROR_TROUBLESHOOTING.stackkit_rollout_failed.message,
    );
  });

  it("keeps the raw message of an unclassified failure visible", () => {
    const failed = updateTasksWithError(runtimeTasks(), {
      step: "stackkit_rollout",
      error: "lease broker replied 502 Bad Gateway",
    }).find((task) => task.id === "stackkit_rollout");

    expect(failed?.errorDetails).toContain(
      "lease broker replied 502 Bad Gateway",
    );
  });
});
