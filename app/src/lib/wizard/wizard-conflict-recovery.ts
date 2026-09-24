import { ApiRequestError } from "#lib/api/client.js";

export type WizardConflictRecovery = {
  stackId: string;
  jobId: string;
  pairingJobId: string;
  completedRunId: string;
};

function asRecord(value: unknown): Record<string, unknown> | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return null;
  }
  return value as Record<string, unknown>;
}

function readString(
  source: Record<string, unknown>,
  ...keys: string[]
): string {
  for (const key of keys) {
    const value = source[key];
    if (typeof value === "string" && value.trim() !== "") return value;
  }
  return "";
}

function conflictDetailRecord(details: unknown): Record<string, unknown> {
  const root = asRecord(details);
  const nested = asRecord(root?.details);
  return nested ?? root ?? {};
}

export function isWizardIdempotencyConflict(error: unknown): boolean {
  if (!(error instanceof ApiRequestError) || error.status !== 409) {
    return false;
  }
  const reason = readString(
    conflictDetailRecord(error.details),
    "reason_code",
  );
  return (
    reason === "wizard_idempotency_conflict" ||
    reason === "idempotency_conflict"
  );
}

export function parseWizardConflictRecovery(
  details: unknown,
): WizardConflictRecovery {
  const source = conflictDetailRecord(details);
  return {
    stackId: readString(source, "kit_deployment_id", "stack_id"),
    jobId: readString(source, "job_id"),
    pairingJobId: readString(source, "pairing_job_id"),
    completedRunId: readString(source, "completed_run_id", "run_id"),
  };
}
