/**
 * kombify-TechStack rollout progress
 *
 * The pinned StackKits CLI reports every runtime owner of an apply on the
 * job's live log (phase `apply.owner`): it starts, reports `running` every 15
 * seconds, then succeeds or fails. This module reads the latest of those
 * records so the creation view can show where the apply stands. A StackKits
 * release that reports no owners yields nothing, and the view keeps its time
 * pacing.
 */

import { tr } from "#lib/i18n.svelte.js";
import type { RuntimeLogEntry } from "#lib/api/runtime-logs.js";
import type { Task } from "./task-status";

const OWNER_PHASE = "apply.owner";

/** The task whose work is the apply itself. */
export const APPLY_TASK_ID = "stackkit_rollout";

export interface OwnerProgress {
  /** 1-based place of the current owner in the serial order. */
  position: number;
  total: number;
  /** Workload the owner sets up; empty when it has none. */
  name: string;
  status: "started" | "running" | "succeeded" | "failed";
  /**
   * The time-paced share of the rollout phase that was already shown when the
   * owners first reported for this job (see `rolloutPaceShare`). Owner pacing
   * continues from it, so the bar does not step back when it takes over.
   */
  floor?: number;
}

const OWNER_STATUSES: ReadonlySet<string> = new Set([
  "started",
  "running",
  "succeeded",
  "failed",
]);

/**
 * The newest owner record of a chronological runtime log, or null when the
 * apply has reported none.
 */
export function latestOwnerProgress(
  entries: RuntimeLogEntry[],
): OwnerProgress | null {
  for (let index = entries.length - 1; index >= 0; index--) {
    const fields = entries[index]?.fields;
    if (fields?.phase !== OWNER_PHASE) continue;
    const position = Number(fields.position);
    const total = Number(fields.total);
    const status = fields.status ?? "";
    if (
      !Number.isInteger(position) ||
      !Number.isInteger(total) ||
      position < 1 ||
      position > total ||
      !OWNER_STATUSES.has(status)
    ) {
      continue;
    }
    return {
      position,
      total,
      name: fields.workload ?? "",
      status: status as OwnerProgress["status"],
    };
  }
  return null;
}

/** Share of the owners that are finished: the current one counts once done. */
export function ownerFraction(progress: OwnerProgress): number {
  const finished =
    progress.position - 1 + (progress.status === "succeeded" ? 1 : 0);
  return finished / progress.total;
}

export function ownerProgressText(progress: OwnerProgress): string {
  const { position, total, name } = progress;
  return name
    ? tr("ui.taskStatus.settingUpPartNamed", { position, total, name })
    : tr("ui.taskStatus.settingUpPart", { position, total });
}

/** The apply task, while it runs, says which part is being set up. */
export function withOwnerProgress(
  tasks: Task[],
  progress: OwnerProgress | null,
): Task[] {
  if (!progress || progress.status === "failed") return tasks;
  const message = ownerProgressText(progress);
  return tasks.map((task) =>
    task.id === APPLY_TASK_ID && task.status === "running"
      ? { ...task, message }
      : task,
  );
}
