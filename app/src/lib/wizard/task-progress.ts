/**
 * kombify-TechStack creation progress
 *
 * Time-weighted progress for the creation view. Each phase contributes its
 * typical duration to the bar, so a phase that usually takes minutes fills
 * most of it and the instant Unifier steps barely move it. Inside the active
 * phase the bar keeps creeping with elapsed time but never reaches the phase
 * end before the backend confirms it, so the bar only moves forward while the
 * task list does.
 */

import type { GroupStatus, Task } from "./task-status";
import { computeGroupStatuses } from "./task-status";
import {
  APPLY_TASK_ID,
  ownerFraction,
  type OwnerProgress,
} from "./rollout-progress";

/** Client-observed start and finish times per task id (epoch ms). */
export type TaskTimeline = Record<
  string,
  { startedAt?: number; completedAt?: number }
>;

// Share of the remaining phase the time-based creep may fill at most.
const MAX_CREEP = 0.95;

/**
 * Record when each task was first seen running or completed. A task that is
 * pending again (a retry reset the list) loses its times.
 */
export function stampTaskTimeline(
  tasks: Task[],
  previous: TaskTimeline,
  now: number,
): TaskTimeline {
  const next: TaskTimeline = {};
  for (const task of tasks) {
    const seen = previous[task.id] ?? {};
    if (task.status === "pending") continue;
    const startedAt = seen.startedAt ?? now;
    next[task.id] =
      task.status === "completed"
        ? { startedAt, completedAt: seen.completedAt ?? now }
        : { startedAt };
  }
  return next;
}

export interface PhaseTiming {
  /** Seconds since the phase was first seen active; undefined while pending. */
  elapsedSeconds?: number;
  typicalSeconds: number;
}

export function phaseTiming(
  gs: GroupStatus,
  timeline: TaskTimeline,
  now: number,
): PhaseTiming {
  const typicalSeconds = gs.group.typicalSeconds;
  const times = gs.memberTasks.map((task) => timeline[task.id] ?? {});
  const starts = times
    .map((t) => t.startedAt)
    .filter((t): t is number => t !== undefined);
  if (gs.status === "pending" || starts.length === 0) {
    return { typicalSeconds };
  }
  const startedAt = Math.min(...starts);
  let endedAt = now;
  if (gs.status === "completed") {
    const ends = times
      .map((t) => t.completedAt)
      .filter((t): t is number => t !== undefined);
    if (ends.length > 0) endedAt = Math.max(...ends);
  }
  return {
    elapsedSeconds: Math.max(0, Math.floor((endedAt - startedAt) / 1000)),
    typicalSeconds,
  };
}

/**
 * Overall progress in percent (0-100), weighted by typical phase duration.
 * While the apply runs and reports its runtime owners, `owner` drives the
 * rollout phase instead of elapsed time.
 */
export function computeOverallProgress(
  tasks: Task[],
  timeline: TaskTimeline,
  now: number,
  owner?: OwnerProgress | null,
): number {
  const groups = computeGroupStatuses(tasks);
  if (groups.length === 0) {
    const done = tasks.filter((t) => t.status === "completed").length;
    return tasks.length > 0 ? Math.round((done / tasks.length) * 100) : 0;
  }
  const totalWeight = groups.reduce(
    (sum, gs) => sum + gs.group.typicalSeconds,
    0,
  );
  const filled = groups.reduce(
    (sum, gs) =>
      sum + gs.group.typicalSeconds * phaseFraction(gs, timeline, now, owner),
    0,
  );
  const percent = (filled / totalWeight) * 100;
  // 100 % only when every task is confirmed.
  return tasks.every((t) => t.status === "completed")
    ? 100
    : Math.min(99, Math.floor(percent));
}

function phaseFraction(
  gs: GroupStatus,
  timeline: TaskTimeline,
  now: number,
  owner?: OwnerProgress | null,
): number {
  if (gs.status === "completed") return 1;
  if (gs.status === "pending") return 0;
  const done =
    gs.memberTasks.filter((t) => t.status === "completed").length /
    gs.memberTasks.length;
  if (gs.status === "failed") return done;
  if (owner && applyIsRunning(gs)) {
    // The owners fill what is left of the phase, starting where the time-paced
    // bar stood when they first reported (captured per job), so the bar does
    // not step back when owner pacing takes over. Without a captured share it
    // starts where the bar stood when the apply began.
    const start =
      owner.floor ?? paceShare(gs, secondsBeforeApply(gs, timeline));
    return (
      done + (1 - done) * (start + (MAX_CREEP - start) * ownerFraction(owner))
    );
  }
  return done + (1 - done) * timePaceShare(gs, timeline, now);
}

// The share of the remaining phase that time pacing shows after `seconds`.
function paceShare(gs: GroupStatus, seconds: number): number {
  return MAX_CREEP * (1 - Math.exp(-seconds / gs.group.typicalSeconds));
}

function timePaceShare(
  gs: GroupStatus,
  timeline: TaskTimeline,
  now: number,
): number {
  return paceShare(gs, phaseTiming(gs, timeline, now).elapsedSeconds ?? 0);
}

/**
 * The time-paced share the rollout phase shows at `now`: what owner pacing
 * continues from when the owners first report. 0 while the phase is not running.
 */
export function rolloutPaceShare(
  tasks: Task[],
  timeline: TaskTimeline,
  now: number,
): number {
  const rollout = computeGroupStatuses(tasks).find(
    (gs) => gs.group.id === "rollout" && gs.status === "running",
  );
  return rollout ? timePaceShare(rollout, timeline, now) : 0;
}

function applyIsRunning(gs: GroupStatus): boolean {
  return gs.memberTasks.some(
    (task) => task.id === APPLY_TASK_ID && task.status === "running",
  );
}

// Seconds from the first started task of the phase to the start of the apply.
function secondsBeforeApply(gs: GroupStatus, timeline: TaskTimeline): number {
  const applyStart = timeline[APPLY_TASK_ID]?.startedAt;
  const starts = gs.memberTasks
    .map((task) => timeline[task.id]?.startedAt)
    .filter((t): t is number => t !== undefined);
  if (applyStart === undefined || starts.length === 0) return 0;
  return Math.max(0, (applyStart - Math.min(...starts)) / 1000);
}
