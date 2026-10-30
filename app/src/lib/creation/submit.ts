/**
 * The one way a creation becomes a run: the process names its request builder
 * and its attempt key, every frame posts through here, and the same durable run
 * then drives the Creation Screen or inline progress. The run stays on the
 * server, so a failed attempt is retried from the wizard with the same key.
 */
import {
  createWizardRun,
  getActiveWizardRun,
  wizardRunNeedsAttention,
  type WizardRunRequest,
  type WizardRunResponse,
} from "#lib/api/wizardRuns.js";
import type { LayoutRecommendationAcceptance } from "#lib/api/layout.js";
import {
  clearExtendWizardIdempotencyKey,
  clearJoinWizardIdempotencyKeys,
  clearStackCreationIdempotencyKey,
  clearSubmittedJoinWizardIdempotencyKey,
  mintExtendWizardIdempotencyKey,
  mintJoinWizardIdempotencyKey,
  mintStackCreationIdempotencyKey,
} from "#lib/wizard/idempotency-keys.js";
import {
  buildExtendRunRequest,
  buildFoundRunRequest,
  buildJoinRunRequest,
} from "#lib/wizard/wizardRunRequest.js";
import { parseWizardConflictRecovery } from "#lib/wizard/wizard-conflict-recovery.js";
import { parseApiError } from "#lib/api/errors.js";
import type { StackConfig } from "#lib/wizard/types.js";
import type { CreationProcess } from "./core/index";

export interface CreationRunTarget {
  /** The deployment the process adds to. */
  stackId?: string;
  stackName?: string;
  /** Already enrolled server a new deployment starts on. */
  existingServerId?: string;
  layoutRecommendation?: LayoutRecommendationAcceptance;
}

function selectedServiceKeys(config: StackConfig): string[] {
  return Object.entries(config.services)
    .filter(([, enabled]) => enabled)
    .map(([name]) => name);
}

function requestFor(
  process: CreationProcess,
  config: StackConfig,
  target: CreationRunTarget,
): WizardRunRequest {
  switch (process.run) {
    case "join":
      return buildJoinRunRequest(
        config,
        target.stackId ?? "",
        target.stackName ?? "",
        selectedServiceKeys(config),
        target.layoutRecommendation,
      );
    case "extend":
      return buildExtendRunRequest(
        config,
        target.stackId ?? "",
        target.stackName ?? "",
        target.layoutRecommendation,
      );
    case "found":
      return buildFoundRunRequest(config, target.existingServerId);
    case "none":
      throw new Error(`The process ${process.key} starts no run.`);
  }
}

function attemptKey(
  process: CreationProcess,
  request: WizardRunRequest,
  target: CreationRunTarget,
): string {
  const stackId = target.stackId ?? "";
  switch (process.key) {
    case "add-server":
      return mintJoinWizardIdempotencyKey(stackId, "join");
    case "add-server-found":
      return mintJoinWizardIdempotencyKey(stackId, "found");
    case "add-use-case":
      // The same request keeps its key (a retry replays the attempt); a changed
      // selection or recommendation gets a new one.
      return mintExtendWizardIdempotencyKey(stackId, JSON.stringify(request));
    default:
      return mintStackCreationIdempotencyKey();
  }
}

/** Ends the attempt, so the next submit is a new run. */
export function forgetCreationAttempt(
  process: CreationProcess,
  target: CreationRunTarget,
  submittedKey?: string,
): void {
  const stackId = target.stackId ?? "";
  switch (process.key) {
    case "add-server":
      clearJoinWizardIdempotencyKeys(stackId);
      break;
    case "add-server-found":
      if (submittedKey) clearSubmittedJoinWizardIdempotencyKey(submittedKey);
      else clearJoinWizardIdempotencyKeys(stackId);
      break;
    case "add-use-case":
      clearExtendWizardIdempotencyKey(stackId);
      break;
    default:
      clearStackCreationIdempotencyKey();
  }
}

export async function submitCreationRun(
  process: CreationProcess,
  config: StackConfig,
  target: CreationRunTarget = {},
): Promise<{ run: WizardRunResponse; request: WizardRunRequest }> {
  const request = requestFor(process, config, target);
  const key = attemptKey(process, request, target);
  const run = await createWizardRun(request, key);
  forgetCreationAttempt(process, target, key);
  return { run, request };
}

/** The Creation Screen for an accepted run; its ledger is the authority from here. */
export function creationScreenHref(
  process: CreationProcess,
  run: { kit_deployment_id?: string; job_id?: string; pairing_job_id?: string },
  name: string,
): string {
  const params = new URLSearchParams();
  if (process.operation) params.set("operation", process.operation);
  if (run.kit_deployment_id) params.set("stack_id", run.kit_deployment_id);
  // A server that still has to connect answers with its pairing job only.
  const job = run.job_id || run.pairing_job_id;
  if (job) params.set("job_id", job);
  params.set("name", name);
  if (run.pairing_job_id) params.set("pairing_job_id", run.pairing_job_id);
  return `/stacks/creating?${params.toString()}`;
}

/**
 * The Creation Screen for an attempt the server already knows: the run its answer
 * names, or else the owner's active run. Null when there is nothing to follow.
 */
export async function creationScreenHrefForConflict(
  process: CreationProcess,
  error: unknown,
  target: CreationRunTarget,
): Promise<string | null> {
  const name = target.stackName ?? "";
  const recovery = parseWizardConflictRecovery(parseApiError(error).details);
  if (recovery.jobId || recovery.pairingJobId) {
    return creationScreenHref(
      process,
      {
        // A new deployment is not the one this page started from.
        kit_deployment_id:
          recovery.stackId ||
          (process.run === "join" ? target.stackId : "") ||
          "",
        job_id: recovery.jobId,
        pairing_job_id: recovery.pairingJobId,
      },
      name,
    );
  }
  const active = await getActiveWizardRun().catch(() => null);
  if (!active || !wizardRunNeedsAttention(active)) return null;
  if (!active.job_id && !active.pairing_job_id) return null;
  return creationScreenHref(process, active, name);
}
