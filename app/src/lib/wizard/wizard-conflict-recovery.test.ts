import { describe, expect, it } from "vitest";
import { ApiRequestError } from "#lib/api/client.js";
import {
  isWizardIdempotencyConflict,
  parseWizardConflictRecovery,
} from "./wizard-conflict-recovery";

describe("wizard conflict recovery", () => {
  it("detects wizard idempotency conflicts from structured API errors", () => {
    const error = new ApiRequestError("conflict", {
      status: 409,
      details: {
        reason_code: "wizard_idempotency_conflict",
        retryable: true,
      },
    });
    expect(isWizardIdempotencyConflict(error)).toBe(true);
  });

  it("reads recovery ids from nested error details", () => {
    expect(
      parseWizardConflictRecovery({
        details: {
          reason_code: "wizard_idempotency_conflict",
          stack_id: "stack-1",
          job_id: "job-provision",
          pairing_job_id: "job-remote",
          completed_run_id: "run-1",
        },
      }),
    ).toEqual({
      stackId: "stack-1",
      jobId: "job-provision",
      pairingJobId: "job-remote",
      completedRunId: "run-1",
    });
  });

  it("ignores non-conflict errors", () => {
    expect(
      isWizardIdempotencyConflict(
        new ApiRequestError("bad request", { status: 400 }),
      ),
    ).toBe(false);
  });
});
