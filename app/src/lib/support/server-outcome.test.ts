import { describe, expect, it } from "vitest";
import {
  isActionableOutcome,
  normalizeServerOutcome,
  outcomeFromLatestFailure,
  retryDispatchFor,
  resolveGuidance,
} from "./server-outcome";
import type { StackLatestFailure } from "#lib/api/stacks.js";

describe("normalizeServerOutcome", () => {
  it("returns null for values that carry no outcome fields", () => {
    expect(normalizeServerOutcome(null)).toBeNull();
    expect(normalizeServerOutcome("nope")).toBeNull();
    expect(normalizeServerOutcome({ foo: 1 })).toBeNull();
    expect(normalizeServerOutcome(["a"])).toBeNull();
  });

  it("reads the full snake_case entitlement-denial envelope", () => {
    const outcome = normalizeServerOutcome({
      status: "disabled",
      error_code: "managed_runtime_feature_disabled",
      reason_code: "required_feature_disabled",
      capability: "techstack.managed.runtime",
      provider_id: "centron-managed",
      required_features: ["a", "b"],
      missing_features: ["b"],
      retryable: false,
      user_guidance: {
        title: "Not active",
        body: "Not entitled.",
        next_steps: ["Use a user-owned server."],
      },
      request_id: "req_1",
    });
    expect(outcome).not.toBeNull();
    expect(outcome?.status).toBe("disabled");
    expect(outcome?.errorCode).toBe("managed_runtime_feature_disabled");
    expect(outcome?.reasonCode).toBe("required_feature_disabled");
    expect(outcome?.missingFeatures).toEqual(["b"]);
    expect(outcome?.retryable).toBe(false);
    expect(outcome?.userGuidance?.nextSteps).toHaveLength(1);
    expect(outcome?.userGuidance?.nextSteps[0]).toMatchObject({
      label: "Use a user-owned server.",
      kind: "note",
    });
    expect(outcome?.requestId).toBe("req_1");
    expect(isActionableOutcome(outcome)).toBe(true);
  });

  it("tolerates camelCase and fails closed on an unknown status", () => {
    const outcome = normalizeServerOutcome({
      reasonCode: "operations_unavailable",
      status: "not-a-real-status",
      retryable: true,
    });
    expect(outcome?.status).toBe("failed");
    expect(outcome?.reasonCode).toBe("operations_unavailable");
    expect(outcome?.retryable).toBe(true);
  });

  it("defaults retryable to false when the field is absent", () => {
    const outcome = normalizeServerOutcome({ reason_code: "x" });
    expect(outcome?.retryable).toBe(false);
    expect(outcome?.status).toBe("failed");
  });

  it("treats an available outcome as non-actionable", () => {
    const outcome = normalizeServerOutcome({ status: "available" });
    expect(isActionableOutcome(outcome)).toBe(false);
  });
});

describe("retryDispatchFor", () => {
  it("bounds retry dispatch by retryability, job type, and lease identity", () => {
    expect(
      retryDispatchFor({
        job_id: "job-blocked",
        type: "deploy",
        lease_id: "lease-1",
        retryable: false,
      }),
    ).toBeNull();
    expect(
      retryDispatchFor({ job_id: "job-provision", type: "provision" }),
    ).toEqual({ kind: "provision" });
    expect(retryDispatchFor({ job_id: "job-deploy", type: "deploy" })).toEqual({
      kind: "deploy",
    });
    expect(
      retryDispatchFor({
        job_id: "job-rollout",
        type: "deploy",
        lease_id: "lease-1",
        retryable: true,
      }),
    ).toEqual({
      kind: "rollout",
      sourceJobId: "job-rollout",
      leaseId: "lease-1",
    });
    expect(
      retryDispatchFor({
        job_id: "job-remote",
        type: "remote_enrollment",
      }),
    ).toEqual({ kind: "remote_enrollment" });
  });
});

describe("outcomeFromLatestFailure", () => {
  const failure: StackLatestFailure = {
    job_id: "job_1",
    type: "provision",
    state: "failed",
    step: "target_bootstrap",
    reason: "target_bootstrap docker_ready=failed",
    runtime_ip: "203.0.113.4",
    diagnostics_available: true,
    runtime_diagnostics: { status: "failed" },
  };

  it("maps a bespoke failure into a failed, retryable outcome with guidance", () => {
    const outcome = outcomeFromLatestFailure(failure);
    expect(outcome.status).toBe("failed");
    expect(outcome.retryable).toBe(true);
    expect(outcome.reasonCode).toBe("target_bootstrap docker_ready=failed");
    expect(outcome.userGuidance?.title).toBeTruthy();
    // The IP is surfaced so the dashboard can say "created at IP X".
    expect(outcome.userGuidance?.body).toContain("203.0.113.4");
    expect(
      outcome.userGuidance?.nextSteps.some((s) => s.kind === "retry"),
    ).toBe(true);
    expect(outcome.providerDiagnostics).toBeTruthy();
  });

  it("synthesizes renderable guidance via resolveGuidance", () => {
    const guidance = resolveGuidance(outcomeFromLatestFailure(failure));
    expect(guidance.title).toBeTruthy();
    expect(guidance.nextSteps.length).toBeGreaterThan(0);
  });

  it("labels connect-remote stackkit retries separately from SSH enrollment", () => {
    const enrollment = outcomeFromLatestFailure({
      job_id: "pair-1",
      type: "remote_enrollment",
      state: "failed",
      reason: "ssh_auth_failed",
      diagnostics_available: false,
    });
    expect(
      enrollment.userGuidance?.nextSteps.find((step) => step.kind === "retry")
        ?.label,
    ).toBe("SSH-Verbindung erneut versuchen");

    const stackkit = outcomeFromLatestFailure(
      {
        job_id: "job-provision",
        type: "provision",
        state: "failed",
        reason: "prepare_rollout failed",
        diagnostics_available: false,
      },
      {
        serverProvisioningMode: "connect-remote",
        connectedServers: 1,
      },
    );
    expect(
      stackkit.userGuidance?.nextSteps.find((step) => step.kind === "retry")
        ?.label,
    ).toBe("StackKit-Vorbereitung auf verbundenem Node fortsetzen");
  });
});
