/**
 * kombify-TechStack Task Update Pipeline
 *
 * Translates backend job-progress and job-failure payloads into task-list
 * transitions. Split out of tasks.ts so the dispatch rules live next to each
 * other and stay independently testable. Consumes the task model from
 * task-status.ts and the error classification from provider-errors.ts.
 */

import type { Task } from "./task-status";
import {
  DEFAULT_TASKS,
  RUNTIME_TASKS,
  createAddServerTaskList,
} from "./task-status";
import {
  ERROR_TROUBLESHOOTING,
  buildManagedRuntimeProviderErrorDetails,
  getTroubleshootingForError,
} from "./provider-errors";

/**
 * Job progress payload from the backend. `state` mirrors `status` (the API uses
 * `state`); both step fields are tolerated so the UI can read either shape.
 */
export interface JobProgress {
  step?: string;
  progress?: number;
  status?: string;
  state?: string; // API uses 'state' instead of 'status'
  type?: string;
  message?: string;
  current_step?: string;
  result?: Record<string, unknown> | string;
}

export interface JobTaskContext {
  currentStepId: string | undefined;
  jobStatus: string;
  currentMessage: string;
  currentStepIndex: number;
}

// resolveJobTaskContext derives the per-update inputs (resolved step id/index,
// effective status, message) the dispatcher branches on.
function resolveJobTaskContext(
  tasks: Task[],
  job: JobProgress,
): JobTaskContext {
  const explicitStepId =
    resolveKnownTaskId(job.step, tasks) ??
    resolveKnownTaskId(job.current_step, tasks);
  const messageStepId = resolveKnownTaskId(job.message, tasks);
  const currentStepId = shouldPreferMessageStep(messageStepId, explicitStepId)
    ? messageStepId
    : explicitStepId || messageStepId;
  const jobStatus = job.status || job.state || "pending";
  const currentMessage = resolveJobMessage(job, tasks);
  const currentStepIndex = currentStepId
    ? tasks.findIndex((task) => task.id === currentStepId)
    : -1;
  return { currentStepId, jobStatus, currentMessage, currentStepIndex };
}

function shouldPreferMessageStep(
  messageStepId: string | undefined,
  explicitStepId: string | undefined,
): boolean {
  if (!messageStepId || !explicitStepId || messageStepId === explicitStepId) {
    return false;
  }
  if (
    explicitStepId === "prepare_rollout" &&
    (messageStepId === "runtime_connected" ||
      messageStepId === "telemetry_handshake")
  ) {
    return true;
  }
  if (
    explicitStepId === "stackkit_rollout" &&
    [
      "stackkit_prepare",
      "docker_ready",
      "opentofu_ready",
      "terramate_ready",
      "telemetry_ready",
    ].includes(messageStepId)
  ) {
    return true;
  }
  return false;
}

function isRunningStatus(jobStatus: string): boolean {
  return (
    jobStatus === "running" ||
    jobStatus === "in_progress" ||
    jobStatus === "waiting"
  );
}

// isFailedStepIndex reports whether the task at `index` is the one that should
// be flagged failed: the resolved failure index, or task 0 when no step could
// be resolved. Encapsulates the complex conditional shared by the failure
// transition helpers.
function isFailedStepIndex(index: number, failedIndex: number): boolean {
  return index === failedIndex || (failedIndex < 0 && index === 0);
}

// failedTaskIndex prefers the backend's explicit failed step over any stale
// frontend "running" marker from the previous poll.
function failedTaskIndex(
  tasks: Task[],
  currentStepId: string | undefined,
): number {
  if (currentStepId) {
    const explicitIndex = tasks.findIndex((task) => task.id === currentStepId);
    if (explicitIndex >= 0) return explicitIndex;
  }
  return tasks.findIndex((task) => task.status === "running");
}

/**
 * Update tasks based on job progress from backend. Dispatches to a phase helper
 * per job state so each transition rule stays small and independently testable.
 */
export function updateTasksFromJob(tasks: Task[], job: JobProgress): Task[] {
  const result = parseJobResult(job.result);
  const scopedTasks = isAddServerJob(result)
    ? createAddServerTaskList(
        getString(result, [
          "server_provisioning_mode",
          "serverProvisioningMode",
        ]),
      )
    : tasks;
  const { currentStepId, jobStatus, currentMessage, currentStepIndex } =
    resolveJobTaskContext(scopedTasks, job);

  if (jobStatus === "completed" || jobStatus === "success") {
    return completeTasksFromJob(scopedTasks, result, currentStepIndex);
  }
  if (jobStatus === "failed" || jobStatus === "error") {
    return failTasksFromJob(scopedTasks, currentStepId);
  }
  if (currentStepIndex >= 0) {
    return advanceTasksToStep(
      scopedTasks,
      currentStepIndex,
      currentMessage,
      job.progress,
    );
  }
  if (currentMessage && isRunningStatus(jobStatus)) {
    return advanceTasksToActiveMessage(
      scopedTasks,
      currentMessage,
      job.progress,
    );
  }

  // No explicit step transition yet. Refuse to fabricate progress from a global
  // percent value: tasks only advance when the backend emits a real step id or
  // when a running job needs an active placeholder for the current backend
  // status message.
  return scopedTasks;
}

// isFullyCompletedRollout reports whether a completed job should mark EVERY task
// done: true when there are no managed-runtime tasks, or when the backend
// returned verified rollout proofs (not a preparation-only result).
function isFullyCompletedRollout(
  tasks: Task[],
  result: Record<string, unknown> | null,
): boolean {
  const runtimeVerified =
    result?.runtime_phase === "verified" &&
    result?.verification_status === "verified" &&
    Boolean(result?.e2e_proof);
  const hasRuntimeTasks = tasks.some((task) =>
    RUNTIME_TASKS.some((runtimeTask) => runtimeTask.id === task.id),
  );
  return !hasRuntimeTasks || runtimeVerified;
}

// completeUpToIndex marks every task up to and including currentStepIndex as
// completed and resets the rest to pending.
function completeUpToIndex(tasks: Task[], currentStepIndex: number): Task[] {
  return tasks.map((task, index) =>
    index <= currentStepIndex
      ? { ...task, status: "completed" as const }
      : { ...task, status: "pending" as const },
  );
}

// completeDefaultTasks marks the Unifier (DEFAULT_TASKS) phases as completed and
// resets any remaining runtime tasks to pending.
function completeDefaultTasks(tasks: Task[]): Task[] {
  return tasks.map((task) =>
    DEFAULT_TASKS.some((defaultTask) => defaultTask.id === task.id)
      ? { ...task, status: "completed" as const }
      : { ...task, status: "pending" as const },
  );
}

// A completed job only completes the phases that actually ran.
function completeTasksFromJob(
  tasks: Task[],
  result: Record<string, unknown> | null,
  currentStepIndex: number,
): Task[] {
  if (isAddServerJob(result) || isFullyCompletedRollout(tasks, result)) {
    return tasks.map((task) => ({ ...task, status: "completed" as const }));
  }
  if (currentStepIndex >= 0) {
    return completeUpToIndex(tasks, currentStepIndex);
  }
  return completeDefaultTasks(tasks);
}

function isAddServerJob(result: Record<string, unknown> | null): boolean {
  return (
    result?.creation_operation === "add-server" ||
    result?.managed_runtime_addition === true
  );
}

function getString(
  record: Record<string, unknown> | null,
  keys: string[],
): string | undefined {
  for (const key of keys) {
    const value = record?.[key];
    if (typeof value === "string" && value.trim()) return value;
  }
  return undefined;
}

// Mark completed tasks as completed, the current/running one as failed, and the
// rest as pending.
function failTasksFromJob(
  tasks: Task[],
  currentStepId: string | undefined,
): Task[] {
  const failedIndex = failedTaskIndex(tasks, currentStepId);
  return tasks.map((task, index) => {
    if (failedIndex >= 0 && index < failedIndex) {
      return { ...task, status: "completed" as const };
    }
    if (isFailedStepIndex(index, failedIndex)) {
      return { ...task, status: "failed" as const };
    }
    return { ...task, status: "pending" as const };
  });
}

// runningTask marks a task as the active/running step, attaching the backend
// status message (falling back to the task's own) and the current progress.
function runningTask(
  task: Task,
  currentMessage: string,
  progress: number | undefined,
): Task {
  return {
    ...task,
    status: "running" as const,
    message: currentMessage || task.message,
    progress,
  };
}

// Running job with a resolved step id: complete everything before it, mark it
// running, reset the rest to pending.
function advanceTasksToStep(
  tasks: Task[],
  currentStepIndex: number,
  currentMessage: string,
  progress: number | undefined,
): Task[] {
  return tasks.map((task, index) => {
    if (index < currentStepIndex) {
      return { ...task, status: "completed" as const };
    }
    if (index === currentStepIndex) {
      return runningTask(task, currentMessage, progress);
    }
    return { ...task, status: "pending" as const };
  });
}

// Running job with a status message but no resolved step id: drive the first
// active (running, else pending) task as a placeholder without disturbing the
// already-completed prefix.
function advanceTasksToActiveMessage(
  tasks: Task[],
  currentMessage: string,
  progress: number | undefined,
): Task[] {
  const activeIndex = firstActiveTaskIndex(tasks);
  if (activeIndex < 0) return tasks;
  return tasks.map((task, index) => {
    if (index < activeIndex) {
      return task.status === "completed"
        ? task
        : { ...task, status: "completed" as const };
    }
    if (index === activeIndex) {
      return runningTask(task, currentMessage, progress);
    }
    return task.status === "completed"
      ? { ...task, status: "pending" as const }
      : task;
  });
}

function resolveKnownTaskId(
  value: string | undefined,
  tasks: Task[],
): string | undefined {
  const candidate = (value ?? "").trim();
  if (!candidate) return undefined;
  if (isExactTaskId(candidate, tasks)) return candidate;
  const alias = resolveTaskIdAlias(candidate);
  return alias && tasks.some((task) => task.id === alias) ? alias : undefined;
}

function isExactTaskId(value: string | undefined, tasks: Task[]): boolean {
  const candidate = (value ?? "").trim();
  return Boolean(candidate && tasks.some((task) => task.id === candidate));
}

function resolveTaskIdAlias(value: string): string | undefined {
  const text = value.toLowerCase();
  if (!text) return undefined;
  if (text.includes("service inventory")) return "service_inventory";
  if (text.includes("terramate")) return "terramate_ready";
  if (text.includes("opentofu") || text.includes("open tofu")) {
    return "opentofu_ready";
  }
  if (
    text.includes("docker") ||
    text.includes("apt_wait") ||
    text.includes("apt ")
  ) {
    return "docker_ready";
  }
  if (text.includes("telemetry handoff")) return "telemetry_handshake";
  if (
    text.includes("telemetry") ||
    text.includes("otel") ||
    text.includes("metrics")
  ) {
    return "telemetry_ready";
  }
  if (
    text.includes("resolved managed vm lease") ||
    text.includes("runtime target resolved") ||
    text.includes("connecting to runtime")
  ) {
    return "runtime_connected";
  }
  if (
    text.includes("managed vm lease") ||
    text.includes("provider") ||
    text.includes("vps target")
  ) {
    return "prepare_rollout";
  }
  if (
    text.includes("stackkits cli") ||
    text.includes("stackkit prepare") ||
    text.includes("preparing managed vps")
  ) {
    return "stackkit_prepare";
  }
  if (text.includes("simulat")) return "simulate_update";
  if (
    text.includes("stackkits rollout") ||
    text.includes("stackkit services")
  ) {
    return "stackkit_rollout";
  }
  return undefined;
}

function normalizeJobMessage(value: string | undefined): string {
  return (value ?? "").trim();
}

function resolveJobMessage(
  job: {
    message?: string;
    current_step?: string;
  },
  tasks: Task[],
): string {
  const explicitMessage = normalizeJobMessage(job.message);
  if (explicitMessage) return explicitMessage;

  if (isExactTaskId(job.current_step, tasks)) return "";
  return normalizeJobMessage(job.current_step);
}

function firstActiveTaskIndex(tasks: Task[]): number {
  const runningIndex = tasks.findIndex((task) => task.status === "running");
  if (runningIndex >= 0) return runningIndex;
  return tasks.findIndex((task) => task.status === "pending");
}

function parseJobResult(
  result: Record<string, unknown> | string | undefined,
): Record<string, unknown> | null {
  if (!result) return null;
  if (typeof result === "string") {
    try {
      const parsed = JSON.parse(result);
      return parsed && typeof parsed === "object" && !Array.isArray(parsed)
        ? (parsed as Record<string, unknown>)
        : null;
    } catch {
      return null;
    }
  }
  return result;
}

/**
 * Update tasks with error details from job failure
 * Includes intelligent troubleshooting based on error type
 */
export function updateTasksWithError(
  tasks: Task[],
  job: {
    step?: string;
    error?: string;
    error_details?: string;
    state?: string;
  },
): Task[] {
  const currentStepId =
    resolveKnownTaskId(job.step, tasks) ??
    resolveKnownTaskId(job.error_details, tasks) ??
    resolveKnownTaskId(job.error, tasks);
  const failedIndex = failedTaskIndex(tasks, currentStepId);

  // Intelligent troubleshooting + the most useful detail string for the error.
  const errorMessage = job.error || "An unexpected error occurred";
  const troubleshootingInfo = getTroubleshootingForError(errorMessage);
  const details = resolveErrorDetails(job, troubleshootingInfo, errorMessage);

  return tasks.map((task, index) => {
    if (failedIndex >= 0 && index < failedIndex) {
      return { ...task, status: "completed" as const };
    }
    if (isFailedStepIndex(index, failedIndex)) {
      return {
        ...task,
        status: "failed" as const,
        errorMessage: troubleshootingInfo.message,
        errorDetails: details,
        troubleshooting: troubleshootingInfo.steps,
      };
    }
    return { ...task, status: "pending" as const };
  });
}

// isManagedRuntimeTroubleshooting reports whether a resolved troubleshooting
// entry is one of the managed-runtime cases that wants the raw backend cause
// appended to the user-facing detail string.
function isManagedRuntimeTroubleshooting(troubleshootingInfo: {
  message: string;
  details: string;
  steps: string[];
}): boolean {
  return (
    troubleshootingInfo === ERROR_TROUBLESHOOTING.managed_runtime_pending ||
    troubleshootingInfo ===
      ERROR_TROUBLESHOOTING.managed_runtime_bootstrap_failed ||
    troubleshootingInfo ===
      ERROR_TROUBLESHOOTING.managed_runtime_provider_error ||
    troubleshootingInfo ===
      ERROR_TROUBLESHOOTING.stackkit_artifact_generation ||
    troubleshootingInfo === ERROR_TROUBLESHOOTING.stackkit_rollout_failed
  );
}

// resolveErrorDetails picks the most useful error-detail string: provider-error
// guidance when available, else the raw backend detail appended to the message
// for managed-runtime failures, else the best single available text.
function resolveErrorDetails(
  job: { error_details?: string },
  troubleshootingInfo: { message: string; details: string; steps: string[] },
  errorMessage: string,
): string {
  const providerErrorDetails =
    troubleshootingInfo === ERROR_TROUBLESHOOTING.managed_runtime_provider_error
      ? buildManagedRuntimeProviderErrorDetails(errorMessage, job.error_details)
      : "";
  if (providerErrorDetails) return providerErrorDetails;

  const shouldAppendRawError = Boolean(
    job.error_details &&
    job.error_details !== errorMessage &&
    isManagedRuntimeTroubleshooting(troubleshootingInfo),
  );
  if (shouldAppendRawError) return `${job.error_details}\n\n${errorMessage}`;
  return job.error_details || troubleshootingInfo.details || errorMessage;
}
