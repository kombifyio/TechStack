/**
 * Canonical, render-facing model for kombify server-lifecycle outcomes.
 *
 * The binding FEATURE-ENTITLEMENT-UX-STANDARD requires every denial / blocked /
 * degraded / pending state to carry one structured envelope (error_code,
 * reason_code, retryable, user_guidance{title,body,next_steps}, ...). This module
 * is the single frontend representation of that envelope. It normalizes whatever
 * the backend sends (snake_case today, camelCase tolerated), bridges to the
 * existing AI-handover context, and — until the backend emits full guidance for
 * every action — synthesizes actionable guidance from the reason code via the
 * provider-error troubleshooting map.
 */

import {
  createErrorAiHandoverContext,
  type ErrorAiHandoverContext,
} from "$lib/support/error-handover";
import { getTroubleshootingForError } from "$lib/wizard/provider-errors";
import type { StackLatestFailure } from "$lib/api/stacks";

export type ServerOutcomeStatus =
  | "available"
  | "disabled"
  | "blocked"
  | "pending"
  | "degraded"
  | "failed";

export type GuidanceStepKind =
  | "note"
  | "link"
  | "external"
  | "retry"
  | "connect"
  | "handoff"
  | "open-oneliner";

export interface GuidanceStep {
  id: string;
  label: string;
  kind: GuidanceStepKind;
  href?: string;
  description?: string;
}

export interface UserGuidance {
  title: string;
  body: string;
  nextSteps: GuidanceStep[];
}

export interface ServerOutcome {
  status: ServerOutcomeStatus;
  errorCode?: string;
  reasonCode?: string;
  capability?: string;
  providerId?: string;
  requiredFeatures?: string[];
  missingFeatures?: string[];
  retryable: boolean;
  userGuidance?: UserGuidance;
  remediation?: string;
  providerDiagnostics?: Record<string, unknown>;
  occurredAt?: string;
  requestId?: string;
}

const OUTCOME_STATUSES: readonly ServerOutcomeStatus[] = [
  "available",
  "disabled",
  "blocked",
  "pending",
  "degraded",
  "failed",
];

/** A non-`available` outcome is something the user must be told about. */
export function isActionableOutcome(
  outcome: ServerOutcome | null | undefined,
): outcome is ServerOutcome {
  return !!outcome && outcome.status !== "available";
}

function asRecord(value: unknown): Record<string, unknown> | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return null;
  }
  return value as Record<string, unknown>;
}

function readString(
  source: Record<string, unknown>,
  ...keys: string[]
): string | undefined {
  for (const key of keys) {
    const value = source[key];
    if (typeof value === "string" && value.trim() !== "") return value;
  }
  return undefined;
}

function readStringArray(
  source: Record<string, unknown>,
  ...keys: string[]
): string[] | undefined {
  for (const key of keys) {
    const value = source[key];
    if (Array.isArray(value)) {
      const cleaned = value.filter(
        (item): item is string =>
          typeof item === "string" && item.trim() !== "",
      );
      if (cleaned.length > 0) return cleaned;
    }
  }
  return undefined;
}

function readStatus(value: unknown): ServerOutcomeStatus | undefined {
  if (
    typeof value === "string" &&
    (OUTCOME_STATUSES as readonly string[]).includes(value)
  ) {
    return value as ServerOutcomeStatus;
  }
  return undefined;
}

function readGuidance(value: unknown): UserGuidance | undefined {
  const record = asRecord(value);
  if (!record) return undefined;
  const title = readString(record, "title") ?? "";
  const body = readString(record, "body") ?? "";
  const rawSteps = record["next_steps"] ?? record["nextSteps"];
  const steps: GuidanceStep[] = [];
  if (Array.isArray(rawSteps)) {
    rawSteps.forEach((step, index) => {
      if (typeof step === "string" && step.trim() !== "") {
        steps.push({ id: `step-${index}`, label: step, kind: "note" });
        return;
      }
      const stepRecord = asRecord(step);
      if (!stepRecord) return;
      const label = readString(stepRecord, "label", "text");
      if (!label) return;
      steps.push({
        id: readString(stepRecord, "id") ?? `step-${index}`,
        label,
        kind:
          (readString(stepRecord, "kind") as GuidanceStepKind | undefined) ??
          "note",
        href: readString(stepRecord, "href"),
        description: readString(stepRecord, "description"),
      });
    });
  }
  if (!title && !body && steps.length === 0) return undefined;
  return { title, body, nextSteps: steps };
}

/**
 * Tolerant reader for a structured outcome envelope. Returns `null` when `raw`
 * carries no recognizable outcome fields (so callers can distinguish "no
 * outcome" from "malformed outcome"). When it IS an outcome, missing/invalid
 * fields fail closed: unknown `status` -> `failed`, missing `retryable` -> false.
 */
export function normalizeServerOutcome(raw: unknown): ServerOutcome | null {
  const record = asRecord(raw);
  if (!record) return null;

  const status = readStatus(record["status"]);
  const reasonCode = readString(record, "reason_code", "reasonCode");
  const errorCode = readString(record, "error_code", "errorCode");
  const guidance = readGuidance(
    record["user_guidance"] ?? record["userGuidance"],
  );

  // Not an outcome envelope, just some other details object.
  if (!status && !reasonCode && !errorCode && !guidance) return null;

  const retryableRaw = record["retryable"];

  return {
    status: status ?? "failed",
    errorCode,
    reasonCode,
    capability: readString(record, "capability"),
    providerId: readString(record, "provider_id", "providerId"),
    requiredFeatures: readStringArray(
      record,
      "required_features",
      "requiredFeatures",
    ),
    missingFeatures: readStringArray(
      record,
      "missing_features",
      "missingFeatures",
    ),
    retryable: typeof retryableRaw === "boolean" ? retryableRaw : false,
    userGuidance: guidance,
    remediation: readString(record, "remediation"),
    providerDiagnostics:
      asRecord(
        record["provider_diagnostics"] ?? record["providerDiagnostics"],
      ) ?? undefined,
    occurredAt: readString(record, "occurred_at", "occurredAt"),
    requestId: readString(record, "request_id", "requestId"),
  };
}

/**
 * Resolve the guidance to render. Backend guidance wins; otherwise synthesize
 * from the reason/error code via the shared troubleshooting map so even a bare
 * `{status, reasonCode}` outcome renders an actionable panel.
 */
export function resolveGuidance(outcome: ServerOutcome): UserGuidance {
  if (
    outcome.userGuidance &&
    (outcome.userGuidance.title || outcome.userGuidance.body)
  ) {
    return outcome.userGuidance;
  }
  const key =
    outcome.reasonCode ?? outcome.errorCode ?? outcome.remediation ?? "";
  const troubleshooting = getTroubleshootingForError(key);
  return {
    title: troubleshooting.message,
    body: outcome.remediation ?? troubleshooting.details,
    nextSteps: troubleshooting.steps.map((step, index) => ({
      id: `fallback-${index}`,
      label: step,
      kind: "note" as const,
    })),
  };
}

/**
 * Bridge a ServerOutcome into the existing AI-handover context so the
 * "Ask Kombify AI" button keeps working unchanged.
 */
export function toAiHandoverContext(
  outcome: ServerOutcome,
  context: {
    surface: string;
    resourceId?: string;
    resourceName?: string;
    route?: string;
  },
): ErrorAiHandoverContext {
  const guidance = resolveGuidance(outcome);
  return createErrorAiHandoverContext({
    surface: context.surface,
    title: guidance.title,
    userMessage: guidance.body,
    resourceId: context.resourceId,
    resourceName: context.resourceName,
    route: context.route,
    errorCode: outcome.errorCode,
    reasonCode: outcome.reasonCode,
    capability: outcome.capability,
    providerId: outcome.providerId,
    retryable: outcome.retryable,
    requiredFeatures: outcome.requiredFeatures,
    missingFeatures: outcome.missingFeatures,
    requestId: outcome.requestId,
    occurredAt: outcome.occurredAt,
  });
}

/**
 * Map the bespoke `stackLatestFailure` (already surfaced by the operations
 * endpoint) into a ServerOutcome, synthesizing guidance from its reason text.
 * This is what lets the dashboard render the already-plumbed failure with a
 * structured panel before the backend emits the full envelope.
 */
/**
 * How the dashboard would dispatch a retry for this failure, or null when it
 * cannot. This is the single authority for both the retry step's visibility
 * and the call the dashboard makes, so the two can never disagree.
 *
 * The lease dimension is what makes a bare type insufficient: the rollout
 * retry endpoint only accepts a deploy source job (pkg/orchestrator/
 * rollout_retry.go), so a provision that already minted a lease and then
 * failed has no safe automatic re-run - re-provisioning would strand the
 * lease it already paid for. A failed teardown or update has no retry at all.
 */
export type RetryDispatch =
  | { kind: "rollout"; sourceJobId: string; leaseId: string }
  | { kind: "provision" }
  | { kind: "deploy" };

export function retryDispatchFor(
  failure: StackLatestFailure,
): RetryDispatch | null {
  const type = (failure.type ?? "").trim().toLowerCase();
  const leaseId = failure.lease_id?.trim();
  if (type === "deploy") {
    return leaseId
      ? { kind: "rollout", sourceJobId: failure.job_id, leaseId }
      : { kind: "deploy" };
  }
  if (type === "provision" && !leaseId) return { kind: "provision" };
  return null;
}

export function failureIsRetryable(failure: StackLatestFailure): boolean {
  return retryDispatchFor(failure) !== null;
}

export function outcomeFromLatestFailure(
  failure: StackLatestFailure,
): ServerOutcome {
  const combined = [failure.reason, failure.message, failure.error]
    .filter((part): part is string => typeof part === "string" && part !== "")
    .join("\n");
  const troubleshooting = getTroubleshootingForError(combined);
  const body = failure.runtime_ip
    ? `${troubleshooting.details} (Server: ${failure.runtime_ip})`
    : troubleshooting.details;

  const nextSteps: GuidanceStep[] = troubleshooting.steps.map(
    (step, index) => ({
      id: `failure-step-${index}`,
      label: step,
      kind: "note" as const,
    }),
  );
  const retryable = failureIsRetryable(failure);
  if (retryable) {
    nextSteps.push({
      id: "failure-retry",
      label: "Rollout erneut starten",
      kind: "retry",
    });
  }

  const diagnostics: Record<string, unknown> = {};
  if (failure.target_bootstrap)
    diagnostics.target_bootstrap = failure.target_bootstrap;
  if (failure.runtime_diagnostics)
    diagnostics.runtime_diagnostics = failure.runtime_diagnostics;
  if (failure.job_id) diagnostics.job_id = failure.job_id;
  if (failure.step) diagnostics.step = failure.step;

  return {
    status: "failed",
    reasonCode: failure.reason,
    capability: "techstack.managed.runtime",
    retryable,
    userGuidance: {
      title: troubleshooting.message,
      body,
      nextSteps,
    },
    providerDiagnostics:
      Object.keys(diagnostics).length > 0 ? diagnostics : undefined,
    occurredAt: failure.updated_at ?? failure.created_at,
  };
}
