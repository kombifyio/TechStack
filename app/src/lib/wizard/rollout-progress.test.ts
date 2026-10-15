import { describe, expect, it } from "vitest";
import type { RuntimeLogEntry } from "#lib/api/runtime-logs.js";
import { latestOwnerProgress, type OwnerProgress } from "./rollout-progress";
import { computeOverallProgress, rolloutPaceShare } from "./task-progress";
import { createRuntimeTaskList, type Task } from "./task-status";

function logRecord(fields: Record<string, string>): RuntimeLogEntry {
  return {
    timestamp: "2026-10-09T08:00:00Z",
    level: "info",
    message: "runtime owner",
    fields,
  };
}

// The rollout phase while its apply runs: everything before it is done.
function applyingTasks(): Task[] {
  const tasks = createRuntimeTaskList();
  const apply = tasks.findIndex((task) => task.id === "stackkit_rollout");
  return tasks.map((task, index) => ({
    ...task,
    status:
      index < apply ? "completed" : index === apply ? "running" : "pending",
  }));
}

const owner = (position: number): OwnerProgress => ({
  position,
  total: 10,
  name: "basement-core",
  status: "running",
});

describe("apply progress from the live log", () => {
  it("reads the newest runtime owner record and ignores everything else", () => {
    const progress = latestOwnerProgress([
      logRecord({
        phase: "apply.owner",
        status: "started",
        position: "2",
        total: "10",
        workload: "basement-core",
      }),
      logRecord({
        phase: "apply.owner",
        status: "running",
        position: "3",
        total: "10",
        workload: "dns",
      }),
      logRecord({ phase: "docker.install", status: "running" }),
      logRecord({
        phase: "apply.owner",
        status: "running",
        position: "11",
        total: "10",
      }),
    ]);

    expect(progress).toEqual({
      position: 3,
      total: 10,
      name: "dns",
      status: "running",
    });
    expect(
      latestOwnerProgress([logRecord({ phase: "docker.install" })]),
    ).toBeNull();
  });

  it("lets the reported owners, not elapsed time, drive the rollout phase without stepping the bar back", () => {
    const tasks = applyingTasks();
    const timeline = {
      simulate_update: { startedAt: 0 },
      stackkit_rollout: { startedAt: 0 },
    };
    const minute = 60_000;

    // Without owner records the bar keeps creeping with time.
    const silent = computeOverallProgress(tasks, timeline, 4 * minute);
    expect(silent).toBeGreaterThan(
      computeOverallProgress(tasks, timeline, minute),
    );
    // Four silent minutes pass before the first owner reports. Owner pacing
    // takes over from where the time-paced bar stood, not from the apply start.
    const floor = rolloutPaceShare(tasks, timeline, 4 * minute);
    const at = (position: number, now = 4 * minute) =>
      computeOverallProgress(tasks, timeline, now, {
        ...owner(position),
        floor,
      });
    expect(at(1)).toBeGreaterThanOrEqual(silent);
    // From there time no longer moves it; the position does.
    expect(at(2, 9 * minute)).toBe(at(2));
    expect(at(8)).toBeGreaterThan(at(2));

    // Records of an apply that is no longer running are not used.
    const applied = tasks.map((task) =>
      task.id === "stackkit_rollout"
        ? { ...task, status: "completed" as const }
        : task,
    );
    expect(computeOverallProgress(applied, timeline, minute, owner(2))).toBe(
      computeOverallProgress(applied, timeline, minute),
    );
  });
});
