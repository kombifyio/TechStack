import type { ServerProvisioningMode } from "#lib/wizard/index.js";
import { sanitizeSensitiveText } from "#lib/security/sensitive-data.js";

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
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "medium",
  }).format(new Date(timestamp));
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
): "stack" | "add-server" | "" {
  if (typeof value !== "string") return "";
  const normalized = value.trim().toLowerCase().replace(/_/g, "-");
  return normalized === "add-server" || normalized === "addserver"
    ? "add-server"
    : normalized === "stack"
      ? "stack"
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
    return `${cleanDetails}\n\nBackend error:\n${cleanError}`;
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
    lines.push("Incident context:");
    if (job.id) lines.push(`Job ID: ${job.id}`);
    if (stackId) lines.push(`Stack ID: ${stackId}`);
  }

  const runtimeProof = asRecord(result?.runtime_proof);
  const targetBootstrap =
    asRecord(result?.target_bootstrap) ||
    asRecord(runtimeProof?.target_bootstrap);
  if (targetBootstrap) {
    lines.push("");
    lines.push("Target bootstrap:");
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
    lines.push("Runtime diagnostics:");
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
      getString(loginGateway, ["label", "name"]) || "Open first login",
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
