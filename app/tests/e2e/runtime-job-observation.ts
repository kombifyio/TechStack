export type JobObservationPhase = "preparation" | "rollout" | "verification";

export interface JobObservationSnapshot {
  phase: JobObservationPhase;
  deadline_ms: number;
  remaining_ms: number;
  expired: boolean;
  terminal_observed: boolean;
  last_job?: Record<string, unknown>;
  rollout_started_at_ms?: number;
  verification_started_at_ms?: number;
}

export interface JobObservation {
  observe(job: Record<string, unknown>, nowMs?: number): JobObservationSnapshot;
  remaining(nowMs?: number): number;
  snapshot(nowMs?: number): JobObservationSnapshot;
}

export interface ManagedRuntimeObservationBudgets {
  setupMs: number;
  preparationMs: number;
  rolloutMs: number;
  backendVerificationMs: number;
  verificationMs: number;
  cleanupMs: number;
  totalMs: number;
}

const terminalStates = new Set([
  "canceled",
  "cancelled",
  "completed",
  "failed",
]);

function boundedPositiveInteger(
  value: string | undefined,
  fallback: number,
  max: number,
): number {
  const parsed = Number.parseInt(String(value ?? ""), 10);
  if (!Number.isFinite(parsed) || parsed <= 0) return fallback;
  return Math.min(parsed, max);
}

function requireFiniteTime(value: number | undefined, label: string): number {
  const resolved = value ?? Date.now();
  if (!Number.isFinite(resolved)) {
    throw new TypeError(`${label} must be a finite number`);
  }
  return resolved;
}

function requirePositiveTimeout(value: number, label: string): number {
  if (!Number.isFinite(value) || value <= 0) {
    throw new RangeError(`${label} must be greater than zero`);
  }
  return value;
}

function jobStep(job: Record<string, unknown>): string {
  return String(job.current_step ?? job.step ?? "")
    .trim()
    .toLowerCase();
}

function jobIsTerminal(job: Record<string, unknown>): boolean {
  return terminalStates.has(
    String(job.state ?? "")
      .trim()
      .toLowerCase(),
  );
}

export function createJobObservation(
  preparationTimeoutMs: number,
  rolloutTimeoutMs?: number,
  startedAtMs = Date.now(),
  verificationTimeoutMs?: number,
): JobObservation {
  const preparationTimeout = requirePositiveTimeout(
    preparationTimeoutMs,
    "preparationTimeoutMs",
  );
  const rolloutTimeout =
    rolloutTimeoutMs === undefined
      ? undefined
      : requirePositiveTimeout(rolloutTimeoutMs, "rolloutTimeoutMs");
  const startedAt = requireFiniteTime(startedAtMs, "startedAtMs");
  const verificationTimeout =
    verificationTimeoutMs === undefined
      ? undefined
      : requirePositiveTimeout(verificationTimeoutMs, "verificationTimeoutMs");

  let phase: JobObservationPhase = "preparation";
  let deadlineMs = startedAt + preparationTimeout;
  let expired = false;
  let terminalObserved = false;
  let lastJob: Record<string, unknown> | undefined;
  let rolloutStartedAtMs: number | undefined;
  let verificationStartedAtMs: number | undefined;

  function markExpired(nowMs: number) {
    if (nowMs >= deadlineMs) expired = true;
  }

  function remaining(nowMs = Date.now()): number {
    const now = requireFiniteTime(nowMs, "nowMs");
    markExpired(now);
    return expired ? 0 : Math.max(0, deadlineMs - now);
  }

  function snapshot(nowMs = Date.now()): JobObservationSnapshot {
    const now = requireFiniteTime(nowMs, "nowMs");
    markExpired(now);
    return {
      phase,
      deadline_ms: deadlineMs,
      remaining_ms: expired ? 0 : Math.max(0, deadlineMs - now),
      expired,
      terminal_observed: terminalObserved,
      ...(lastJob === undefined ? {} : { last_job: lastJob }),
      ...(rolloutStartedAtMs === undefined
        ? {}
        : { rollout_started_at_ms: rolloutStartedAtMs }),
      ...(verificationStartedAtMs === undefined
        ? {}
        : { verification_started_at_ms: verificationStartedAtMs }),
    };
  }

  function observe(
    job: Record<string, unknown>,
    nowMs = Date.now(),
  ): JobObservationSnapshot {
    if (job === null || typeof job !== "object" || Array.isArray(job)) {
      throw new TypeError("job must be an object");
    }
    const now = requireFiniteTime(nowMs, "nowMs");

    if (
      !expired &&
      phase !== "verification" &&
      verificationTimeout !== undefined &&
      (jobStep(job) === "verify_rollout" || jobStep(job) === "restore_drill") &&
      now < deadlineMs
    ) {
      // Polling may miss verify_rollout. Restore still proves Apply finished;
      // neither repeated steps nor a backstep may renew this deadline.
      phase = "verification";
      verificationStartedAtMs = now;
      deadlineMs = now + verificationTimeout;
    } else if (
      !expired &&
      phase === "preparation" &&
      rolloutTimeout !== undefined &&
      jobStep(job) === "stackkit_rollout" &&
      now < deadlineMs
    ) {
      phase = "rollout";
      rolloutStartedAtMs = now;
      deadlineMs = now + rolloutTimeout;
    } else {
      markExpired(now);
    }

    lastJob = job;
    terminalObserved ||= jobIsTerminal(job);
    return snapshot(now);
  }

  return { observe, remaining, snapshot };
}

export function managedRuntimeObservationBudgets(
  env: Record<string, string | undefined> = process.env,
): ManagedRuntimeObservationBudgets {
  const setupMs = 420_000;
  // IONOS datacenter creates have taken about 75 minutes (2026-09-23); a
  // shorter preparation window expires before Guard can enroll and the
  // cleanup then races the still-running provision.
  const preparationMs = boundedPositiveInteger(
    env.TECHSTACK_RUNTIME_E2E_MANAGED_PROVISION_TIMEOUT_MS,
    570_000,
    5_400_000,
  );
  const rolloutMs = 450_000;
  // Existing typed product limits after Apply: Verify (300 + 30 grace),
  // node Plan (300 + 30), backup configure (900 + 30), Advanced restore
  // drill (900 + 30), plus 30 seconds for polling and result persistence.
  // See pkg/jobs/stackkit_lifecycle.go and deploy_handler.go's native drill.
  // This is separate from the UI/evidence checks after the job is terminal.
  const backendVerificationMs = (300 + 300 + 900 + 900 + 4 * 30 + 30) * 1_000;
  const verificationMs = 840_000;
  const cleanupMs = 450_000;
  return {
    setupMs,
    preparationMs,
    rolloutMs,
    backendVerificationMs,
    verificationMs,
    cleanupMs,
    totalMs:
      setupMs +
      preparationMs +
      rolloutMs +
      backendVerificationMs +
      verificationMs +
      cleanupMs,
  };
}
