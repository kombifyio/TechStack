import type { CreationOperation } from "#lib/creation/core/index.js";
import type { ServerProvisioningMode, Task } from "#lib/wizard/index.js";
import { sanitizeSensitiveText } from "#lib/security/sensitive-data.js";

import { tr, formatDateTime } from "#lib/i18n.svelte.js";
export type StackKitIdentityHandoff = {
  ownerUsername?: string;
  ownerEmail?: string;
  ownerDisplayName?: string;
  loginGatewayUrl?: string;
  loginGatewayLabel?: string;
  recoveryRef?: string;
  recoveryHashPresent?: boolean;
};

export type RuntimeProofEntry = {
  action?: string;
  status?: string;
  mode?: string;
};

export type LeaseSummary = {
  id?: string;
  provider?: string;
  offering?: string;
  host?: string;
  publicIp?: string;
  privateIp?: string;
  sshUser?: string;
  sshPort?: number | null;
  desiredState?: string;
  billingMode?: string;
};

export function isRunningJobStatus(status: string): boolean {
  return status === "running" || status === "in_progress";
}

export function formatNextResumeAt(value: string): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return value;
  return formatDateTime(timestamp, {
    dateStyle: "medium",
    timeStyle: "medium",
  });
}

export function normalizedServerIdentity(value?: string): string {
  return (value || "").trim().toLowerCase();
}

export function sanitizeSentrySummary(value?: string): string {
  return sanitizeSensitiveText(value).slice(0, 1000);
}

export function isLocalhostHost(host: string): boolean {
  const lower = (host || "").toLowerCase();
  return (
    lower === "localhost" ||
    lower === "127.0.0.1" ||
    lower === "::1" ||
    lower === "0.0.0.0"
  );
}

export function isServerProvisioningMode(
  value: unknown,
): value is ServerProvisioningMode {
  return (
    value === "kombify-cloud" ||
    value === "connect-remote" ||
    value === "hypervisor" ||
    value === "install-command"
  );
}

export function normalizeCreationOperation(
  value: unknown,
): CreationOperation | "" {
  if (typeof value !== "string") return "";
  const normalized = value.trim().toLowerCase().replace(/_/g, "-");
  if (normalized === "add-server" || normalized === "addserver")
    return "add-server";
  return normalized === "stack" || normalized === "add-use-case"
    ? normalized
    : "";
}

export function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

export function getString(
  record: Record<string, unknown> | null,
  keys: string[],
): string {
  if (!record) return "";
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string" && value.trim()) {
      return value;
    }
  }
  return "";
}

export function getBoolean(
  record: Record<string, unknown> | null,
  keys: string[],
): boolean {
  if (!record) return false;
  for (const key of keys) {
    if (record[key] === true) {
      return true;
    }
  }
  return false;
}

export function buildFailureFallback(
  error?: string,
  details?: string,
): string | undefined {
  const cleanError = error?.trim() ?? "";
  const cleanDetails = details?.trim() ?? "";
  if (cleanError && cleanDetails && cleanError !== cleanDetails) {
    return tr("ui.stacksCreatingCreation-results.backendError", {
      cleanDetails,
      cleanError,
    });
  }
  return cleanDetails || cleanError || undefined;
}

export function normalizeJobResult(
  result: Record<string, unknown> | string | undefined,
): Record<string, unknown> | null {
  if (!result) return null;
  if (typeof result === "string") return safeJsonParse(result);
  return result;
}

export function buildFailureDetails(
  job: { id?: string; error_details?: string },
  result: Record<string, unknown> | null,
  fallback: string | undefined,
  stackId: string,
): string | undefined {
  const lines: string[] = [];
  if (fallback) lines.push(fallback);
  if (job.id || stackId) {
    lines.push("");
    lines.push(tr("ui.stacksCreatingCreation-results.incidentContext"));
    if (job.id)
      lines.push(tr("ui.stacksCreatingCreation-results.jobID", { id: job.id }));
    if (stackId)
      lines.push(tr("ui.stacksCreatingCreation-results.stackID", { stackId }));
  }

  const runtimeProof = asRecord(result?.runtime_proof);
  const targetBootstrap =
    asRecord(result?.target_bootstrap) ||
    asRecord(runtimeProof?.target_bootstrap);
  if (targetBootstrap) {
    lines.push("");
    lines.push(tr("ui.stacksCreatingCreation-results.targetBootstrap"));
    appendDetailLine(lines, "Status", targetBootstrap.status);
    appendDetailLine(lines, "Reason", targetBootstrap.reason_code);
    appendDetailLine(lines, "Attempts", targetBootstrap.attempts);
    appendDetailLine(lines, "Duration", targetBootstrap.duration_ms, "ms");
    appendDetailLine(lines, "Message", targetBootstrap.message);
    appendDetailLine(lines, "Output", targetBootstrap.output_snippet);
  }

  const diagnostics = asRecord(result?.runtime_diagnostics);
  if (diagnostics) {
    const commands = Array.isArray(diagnostics.commands)
      ? diagnostics.commands.length
      : 0;
    lines.push("");
    lines.push(tr("ui.stacksCreatingCreation-results.runtimeDiagnostics"));
    appendDetailLine(lines, "Status", diagnostics.status);
    appendDetailLine(lines, "Reason", diagnostics.reason);
    if (commands > 0) lines.push(`Commands: ${commands}`);
    appendDetailLine(lines, "Error", diagnostics.error);
  }

  const details = lines.join("\n").trim();
  return details || fallback;
}

export function appendDetailLine(
  lines: string[],
  label: string,
  value: unknown,
  suffix = "",
) {
  if (value === undefined || value === null || value === "") return;
  lines.push(`${label}: ${String(value)}${suffix}`);
}

export function hasVerifiedRuntimeResult(
  result: Record<string, unknown>,
): boolean {
  return (
    getString(result, ["runtime_phase"]) === "verified" &&
    getString(result, ["verification_status"]) === "verified" &&
    Boolean(asRecord(result.e2e_proof))
  );
}

export function extractStackKitIdentityHandoff(
  result: Record<string, unknown>,
): StackKitIdentityHandoff | null {
  const outputs =
    asRecord(result.stackkit_outputs) ||
    asRecord(result.stackkitOutputs) ||
    asRecord(result.stackkit_identity_outputs) ||
    asRecord(result.identity_outputs);
  if (!outputs) return null;

  const identity = asRecord(outputs.identity);
  const owner = asRecord(identity?.owner) || asRecord(outputs.owner);
  const loginGateway =
    asRecord(outputs.login_gateway) ||
    asRecord(outputs.loginGateway) ||
    asRecord(outputs.login);
  const recovery =
    asRecord(identity?.recovery) ||
    asRecord(outputs.recovery) ||
    asRecord(outputs.recovery_bundle);

  const handoff: StackKitIdentityHandoff = {
    ownerUsername: getString(owner, ["username", "user", "login"]),
    ownerEmail: getString(owner, ["email"]),
    ownerDisplayName: getString(owner, ["display_name", "displayName"]),
    loginGatewayUrl: getString(loginGateway, ["url", "login_url", "loginUrl"]),
    loginGatewayLabel:
      getString(loginGateway, ["label", "name"]) ||
      tr("ui.stacksCreatingCreationLease.openFirstLogin"),
    recoveryRef: getString(recovery, [
      "bundle_ref",
      "bundleRef",
      "recovery_bundle_ref",
      "recoveryBundleRef",
      "secret_ref",
      "secretRef",
      "machine_secret_ref",
      "machineSecretRef",
    ]),
    recoveryHashPresent: getBoolean(recovery, [
      "passphrase_hash_present",
      "passphraseHashPresent",
    ]),
  };

  return Object.values(handoff).some(Boolean) ? handoff : null;
}

export function extractLeaseSummary(
  result: Record<string, unknown>,
): LeaseSummary | null {
  const leaseId = getString(result, ["lease_id", "leaseId"]);
  if (!leaseId) return null;

  const host =
    getString(result, [
      "runtime_ssh_host",
      "runtimeSshHost",
      "runtime_public_ip",
      "runtimePublicIp",
      "runtime_private_ip",
      "runtimePrivateIp",
    ]) || "";

  const sshPortRaw = result["runtime_ssh_port"] ?? result["runtimeSshPort"];
  let sshPort: number | null = null;
  if (typeof sshPortRaw === "number" && sshPortRaw > 0) {
    sshPort = sshPortRaw;
  } else if (typeof sshPortRaw === "string" && sshPortRaw.trim()) {
    const parsed = Number(sshPortRaw);
    if (Number.isFinite(parsed) && parsed > 0) sshPort = parsed;
  }

  return {
    id: leaseId,
    provider: getString(result, [
      "provider_id",
      "providerId",
      "lease_provider",
      "leaseProvider",
    ]),
    offering: getString(result, ["runtime_offering_id", "runtimeOfferingId"]),
    host,
    publicIp: getString(result, ["runtime_public_ip", "runtimePublicIp"]),
    privateIp: getString(result, ["runtime_private_ip", "runtimePrivateIp"]),
    sshUser: getString(result, ["runtime_ssh_user", "runtimeSshUser"]),
    sshPort,
    desiredState: getString(result, ["desired_state", "desiredState"]),
    billingMode: getString(result, ["billing_mode", "billingMode"]),
  };
}

export function safeJsonParse(value: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(value);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : {};
  } catch {
    return {};
  }
}

export type DegradedRepairStatus =
  "scheduled" | "running" | "repaired" | "failed";

export type DegradedRepair = {
  mode: "automatic";
  status?: DegradedRepairStatus;
  jobId?: string;
  nextAttemptAt?: string;
  attempts?: number;
};

export type DegradedComponent = {
  component: string;
  reasonCode: string;
  stage?: string;
  retryable: boolean;
  detail?: string;
  repair?: DegradedRepair;
};

export type DegradedCompletion = {
  components: DegradedComponent[];
};

const DEGRADED_REPAIR_STATUSES: readonly DegradedRepairStatus[] = [
  "scheduled",
  "running",
  "repaired",
  "failed",
];

function parseDegradedRepair(value: unknown): DegradedRepair | undefined {
  const record = asRecord(value);
  if (!record) return undefined;
  const status = getString(record, ["status"]) as DegradedRepairStatus;
  const attempts = record.attempts;
  return {
    mode: "automatic",
    status: DEGRADED_REPAIR_STATUSES.includes(status) ? status : undefined,
    jobId: getString(record, ["job_id", "jobId"]) || undefined,
    nextAttemptAt:
      getString(record, ["next_attempt_at", "nextAttemptAt"]) || undefined,
    attempts:
      typeof attempts === "number" && Number.isFinite(attempts)
        ? attempts
        : undefined,
  };
}

function parseDegradedComponents(value: unknown): DegradedComponent[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((raw) => {
    const record = asRecord(raw);
    const component = getString(record, ["component"]).trim();
    if (!record || !component) return [];
    const detail = getString(record, ["detail"]);
    return [
      {
        component,
        reasonCode: getString(record, ["reason_code", "reasonCode"]).trim(),
        stage: getString(record, ["stage"]).trim() || undefined,
        retryable: record.retryable === true,
        detail: detail
          ? sanitizeSensitiveText(detail).slice(0, 300)
          : undefined,
        repair: parseDegradedRepair(record.repair),
      },
    ];
  });
}

/**
 * A completed job whose core applied while an independent component could not
 * be finished. The runtime lifecycle status is the authority; the component
 * list is read tolerantly and may be absent.
 */
export function extractDegradedCompletion(
  jobState: string,
  result: Record<string, unknown> | null,
): DegradedCompletion | null {
  if (jobState !== "completed" && jobState !== "success") return null;
  if (!result) return null;
  const lifecycle = asRecord(result.runtime_lifecycle);
  const components = parseDegradedComponents(result.degraded_components);
  const lifecycleDegraded =
    getString(lifecycle, ["status"]) === "completed_degraded";
  if (!lifecycleDegraded && components.length === 0) return null;
  return { components };
}

// Backend stage ids (job steps and runtime lifecycle phases) mapped onto the
// creation task that carries the warning.
const DEGRADED_STAGE_TASK_ALIASES: Record<string, string> = {
  prepare_apply: "stackkit_rollout",
  generate: "generate_iac",
  verify: "verify_rollout",
  monitoring_ready: "telemetry_ready",
};
const DEFAULT_DEGRADED_TASK_ID = "stackkit_rollout";

/**
 * Completes every task and marks the ones that carry a degraded component as
 * completed-with-warning.
 */
export function markDegradedTasks(
  tasks: Task[],
  degraded: DegradedCompletion,
): Task[] {
  const known = new Set(tasks.map((task) => task.id));
  const resolve = (stage?: string): string => {
    const candidate = stage ? DEGRADED_STAGE_TASK_ALIASES[stage] || stage : "";
    if (candidate && known.has(candidate)) return candidate;
    if (known.has(DEFAULT_DEGRADED_TASK_ID)) return DEFAULT_DEGRADED_TASK_ID;
    return tasks[tasks.length - 1]?.id ?? "";
  };
  const stages = degraded.components.length
    ? degraded.components.map((component) => component.stage)
    : [undefined];
  const attentionIds = new Set(stages.map(resolve));
  return tasks.map((task) => ({
    ...task,
    status: "completed" as const,
    attention: attentionIds.has(task.id),
  }));
}

/** Short localized title of a degraded component. */
export function degradedComponentTitle(component: string): string {
  return component === "public_tls"
    ? tr("ui.stacksCreatingCreationDegraded.publicTlsTitle")
    : tr("ui.stacksCreatingCreationDegraded.genericTitle");
}

/** One line: what the degraded component means for the user right now. */
export function degradedComponentMeaning(component: DegradedComponent): string {
  if (component.reasonCode === "acme_rate_limited") {
    return tr("ui.stacksCreatingCreationDegraded.reasonAcmeRateLimited");
  }
  if (component.component === "public_tls") {
    return tr("ui.stacksCreatingCreationDegraded.publicTlsMeaning");
  }
  return component.retryable || component.repair
    ? tr("ui.stacksCreatingCreationDegraded.genericMeaningRepair")
    : tr("ui.stacksCreatingCreationDegraded.genericMeaning");
}

/** Repair state the UI shows: a tracked repair job wins over the job result. */
export function effectiveRepairStatus(
  component: DegradedComponent,
  tracked?: DegradedRepairStatus,
): DegradedRepairStatus | undefined {
  return tracked ?? component.repair?.status;
}

export type OperationReferences = {
  serverId?: string;
  agentId?: string;
  reasonCodes: string[];
};

/**
 * Identifiers and reason codes an operator handoff needs. Reads only plain
 * identifiers and codes, never credentials, certificates or command output.
 */
export function extractOperationReferences(
  job: { reason_code?: string; payload?: Record<string, unknown> },
  result: Record<string, unknown> | null,
): OperationReferences {
  const payload = asRecord(job.payload);
  const supportContext = asRecord(result?.support_context);
  const runtimeProof = asRecord(result?.runtime_proof);
  const targetBootstrap =
    asRecord(result?.target_bootstrap) ||
    asRecord(runtimeProof?.target_bootstrap);
  const diagnostics = asRecord(result?.runtime_diagnostics);
  const serverKeys = ["runtime_server_id", "server_id"];
  const agentKeys = ["runtime_agent_id", "agent_id"];
  const reasonCodes = [
    job.reason_code,
    getString(result, ["reason_code"]),
    getString(supportContext, ["stackkit_reason_code"]),
    getString(targetBootstrap, ["reason_code"]),
    // The "Reason:" line the failure page shows under runtime diagnostics.
    getString(diagnostics, ["reason"]),
  ]
    .map((code) => code?.trim() ?? "")
    .filter((code, index, all) => code && all.indexOf(code) === index);
  return {
    serverId:
      getString(result, serverKeys) ||
      getString(payload, serverKeys) ||
      undefined,
    agentId:
      getString(result, agentKeys) ||
      getString(payload, agentKeys) ||
      undefined,
    reasonCodes,
  };
}
