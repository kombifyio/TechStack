/**
 * kombify-TechStack Provider Error Classification
 *
 * Parsing, classification, and user-facing detail rendering for Unifier and
 * managed-runtime/cloud-provider errors. Split out of tasks.ts so the error
 * domain language lives in one focused module that task-updates.ts consumes.
 */

/**
 * Error troubleshooting guide for common Unifier errors
 */
export const ERROR_TROUBLESHOOTING: Record<
  string,
  { message: string; details: string; steps: string[] }
> = {
  validation_failed: {
    message: "Configuration could not be validated",
    details: "The submitted configuration contains invalid or missing values.",
    steps: [
      "Check that the submitted values are plausible",
      "Make sure a StackKit was selected or detected",
      "Check that the stack name is valid (letters, numbers, and hyphens only)",
      "For authentication errors, make sure the passwords match",
    ],
  },
  network_error: {
    message: "Network error while saving",
    details: "The kombify TechStack server could not be reached.",
    steps: [
      "Check your internet connection",
      "Make sure the kombify TechStack server is running",
      "Check that port 5260 is reachable",
      "With Docker, use 'docker ps' to verify that all containers are running",
    ],
  },
  stackkit_files_missing: {
    message: "StackKit files missing",
    details:
      "A StackKit was selected (e.g. basement-kit or cloud-kit), but the StackKit files are not available on the server.",
    steps: [
      "With Docker: rebuild the image (ensures pkg/stackkits/ is present in the container)",
      "Check the server logs: 'docker compose logs techstack'",
      "If running the binary outside the repo: set TECHSTACK_STACKKITS_DIR to a folder with StackKits",
      "Verify the StackKit directory exists on the server (e.g. /app/pkg/stackkits/basement-kit or /app/pkg/stackkits/cloud-kit)",
    ],
  },
  stackkit_artifact_generation: {
    message: "StackKit artifacts could not be generated",
    details:
      "The managed VM was prepared, but StackKits could not generate the rollout artifacts or kombify.me routing data.",
    steps: [
      "Do not create another provider VM; reuse the existing stack and lease for the next rollout attempt",
      "Check the error details for kombify.me registration, quota, or StackKits CLI output",
      "Resolve the kombify.me or StackKits blocker, then retry only the StackKit rollout",
    ],
  },
  stackkit_not_found: {
    message: "No matching StackKit found",
    details:
      "No compatible StackKit is available for the selected configuration.",
    steps: [
      "Try selecting fewer services",
      "Switch to a different access mode (Home/Anywhere)",
      "Check that StackKit files are present in pkg/stackkits/",
      "For custom StackKits: validate the CUE syntax",
    ],
  },
  stackkit_identity_handoff_missing: {
    message: "StackKit identity handoff is missing",
    details:
      "The rollout did not return the owner login, login gateway, and recovery outputs required to use the stack.",
    steps: [
      "Check the Runtime Action response for stackkit_outputs.identity.owner.username",
      "Check the Runtime Action response for stackkit_outputs.login_gateway.url",
      "Check the Runtime Action response for stackkit_outputs.identity.recovery",
      "Retry only after the StackKit runtime action contract returns those outputs",
    ],
  },
  stackkit_rollout_failed: {
    message: "StackKit rollout could not be applied",
    details:
      "The VM was prepared, but the StackKits Runtime Action could not apply the selected StackKit rollout.",
    steps: [
      "Check the error details for backend error, target bootstrap, and runtime diagnostics",
      "Check the runtime logs for the same stack, job, lease, and provider",
      "Do not create another provider VM until the existing job has a diagnostic artifact or a clear skip reason",
      "Retry the rollout only after SSH, Docker, and the StackKits Runtime Action are stable on the target server",
    ],
  },
  database_error: {
    message: "Database error",
    details: "The configuration could not be saved to the database.",
    steps: [
      "Check that the PocketBase database is running",
      "Verify write permissions for the pb_data/ directory",
      "With Docker: ensure the volume is mounted correctly",
      "Try restarting the kombify-TechStack server",
    ],
  },
  unifier_error: {
    message: "Unifier processing error",
    details:
      "The configuration could not be transformed into a valid deployment spec.",
    steps: [
      "Check that CUE is installed correctly",
      "Review the logs with 'docker compose logs techstack'",
      "Validate the stack-spec.yaml manually with the StackKits validator",
      "For persistent errors: create a GitHub issue with the logs",
    ],
  },
  service_conflict: {
    message: "Service conflict detected",
    details: "The selected services have conflicting requirements.",
    steps: [
      "Disable conflicting services",
      "Check port conflicts in the error message",
      "For VPN services: only one VPN provider can be active at a time",
      "For monitoring: VictoriaMetrics retention requires persistent storage",
    ],
  },
  managed_runtime_decommission_failed: {
    message: "This deployment could not be decommissioned",
    details:
      "The teardown stopped because TechStack could not match the request to an authoritative provider lease. Nothing was force-removed, so provider resources may still exist.",
    steps: [
      "Open the latest destroy job for the provider's own error",
      "Check whether the server still exists at Centron or IONOS",
      "Use the server's force decommission only after confirming the provider resources are gone",
    ],
  },
  managed_runtime_pending: {
    message: "Managed Runtime is not ready yet",
    details:
      "The VM lease has not reported an SSH host or public IP. Creation stopped so the operation cannot remain in provisioning indefinitely.",
    steps: [
      "Check the VM lease enrollment events and Sentry for the provider error",
      "Check whether Centron or IONOS actually created the server",
      "Retry creation only after the lease reports runtime_ssh_host or runtime_public_ip",
    ],
  },
  managed_runtime_bootstrap_failed: {
    message: "Managed Runtime could not be prepared",
    details:
      "The VM is reachable, but TechStack could not prepare Docker or the bootstrap baseline reliably on the Managed Runtime server.",
    steps: [
      "Check the provider portal to see whether the server is still starting or was rebooted",
      "Check cloud-init, Docker status, and SSH reachability on the Managed Runtime server",
      "Retry creation only after SSH is stable and Docker starts without errors",
    ],
  },
  managed_runtime_provider_error: {
    message: "Managed Runtime could not be created",
    details:
      "The cloud provider rejected or did not complete VM creation. The provider error is included in the details below.",
    steps: [
      "Check the provider error code in the error details",
      "Wait for rate limits or create/delete throttling to clear before retrying",
      "Check the lifecycle receipt and automatic cleanup status; do not start another server until definitive absence is confirmed",
      "If provider support is required, include the error code from the details",
    ],
  },
  unknown_error: {
    message: "Unexpected error",
    details: "An unknown error occurred.",
    steps: [
      "Reload the page and try again",
      "Check the browser console for JavaScript errors",
      "Review the server logs",
      "Create a GitHub issue with the reproduction steps",
    ],
  },
};

export interface ManagedRuntimeProviderErrorInfo {
  provider?: string;
  providerLabel?: string;
  code?: string;
  category?: string;
  retryHint?: string;
  summary?: string;
  isProviderError: boolean;
}

export const PROVIDER_ERROR_CODE_PATTERN = /\b[A-Z][A-Z0-9]+-\d+(?:-\d+)+\b/;

/**
 * Ordered substring buckets that map a provider error to a category + retry
 * hint. The FIRST bucket whose substrings appear in the combined error text
 * wins, mirroring the original if/else-if order. Encapsulating them as data
 * keeps {@link parseManagedRuntimeProviderError} flat.
 */
interface ProviderErrorCategory {
  category: string;
  retryHint: string;
  any: string[];
}

export const PROVIDER_ERROR_CATEGORIES: ProviderErrorCategory[] = [
  {
    category: "provider_throttle",
    retryHint: "retry_after_provider_cooldown",
    any: [
      "vdc-5-1091",
      "too many recent create and delete operations",
      "too many recent create/delete operations",
      "rate limit",
      "rate-limit",
      "ratelimit",
      "throttl",
    ],
  },
  {
    category: "provider_quota",
    retryHint: "free_provider_resources",
    any: [
      "vdc-5-1051",
      "would be exhausted",
      "personal limit",
      "quota exceeded",
      "quota exhausted",
      "insufficient quota",
    ],
  },
  {
    category: "provider_auth",
    retryHint: "contact_provider_support",
    any: [
      "unauthorized",
      "forbidden",
      "invalid credential",
      "invalid api key",
      "authentication failed",
    ],
  },
  {
    category: "provider_conflict",
    retryHint: "contact_provider_support",
    any: [
      "service conflict",
      "already exists",
      "name is already in use",
      "resource conflict",
    ],
  },
];

// classifyProviderError maps the combined error text to a category + retry hint.
// hasProviderSignal carries the provider/code/"simulate enroll" fallback the
// original chain applied when no substring bucket matched.
export function classifyProviderError(
  combined: string,
  hasProviderSignal: boolean,
): { category: string; retryHint: string } {
  for (const rule of PROVIDER_ERROR_CATEGORIES) {
    if (containsAny(combined, rule.any)) {
      return { category: rule.category, retryHint: rule.retryHint };
    }
  }
  if (hasProviderSignal) {
    return { category: "provider_error", retryHint: "" };
  }
  return { category: "", retryHint: "" };
}

// resolveProviderErrorCode returns the first provider-error code found in the
// raw message, falling back to the cleaned summary.
function resolveProviderErrorCode(raw: string, summary: string): string {
  return (
    raw.match(PROVIDER_ERROR_CODE_PATTERN)?.[0] ??
    summary.match(PROVIDER_ERROR_CODE_PATTERN)?.[0] ??
    ""
  );
}

export function parseManagedRuntimeProviderError(
  message: string,
): ManagedRuntimeProviderErrorInfo {
  const raw = message.trim();
  const lower = raw.toLowerCase();
  const summary = stripProviderErrorPrefixes(extractNestedProviderMessage(raw));
  const combined = `${lower}\n${summary.toLowerCase()}`;
  const provider = providerFromError(raw);
  const code = resolveProviderErrorCode(raw, summary);

  const hasProviderSignal = Boolean(
    provider || code || lower.includes("simulate enroll returned"),
  );
  const classified = classifyProviderError(combined, hasProviderSignal);
  const category = classified.category;
  let retryHint = classified.retryHint;
  if (!retryHint && combined.includes("contact support")) {
    retryHint = "contact_provider_support";
  }

  return {
    provider,
    providerLabel: providerLabel(provider),
    code,
    category,
    retryHint,
    summary,
    isProviderError: Boolean(provider || code || category),
  };
}

// retryHintGuidance maps a parsed retry hint to the English "next step" line that
// is appended to the provider-error details block.
function retryHintGuidance(retryHint?: string): string {
  switch (retryHint) {
    case "retry_after_provider_cooldown":
      return "Next step: Wait for the provider create/delete cooldown to end, then retry creation.";
    case "free_provider_resources":
      return "Next step: Check limits, quota, and running servers in the provider portal, then free the required resources.";
    case "contact_provider_support":
      return "Next step: Check the provider portal. If the error persists, send the error code to provider support.";
    default:
      return "Next step: Check the lifecycle receipt and automatic cleanup status. Retry only after definitive absence is confirmed.";
  }
}

export function buildManagedRuntimeProviderErrorDetails(
  errorMessage: string,
  backendDetails?: string,
): string {
  const info = parseManagedRuntimeProviderError(
    `${errorMessage}\n${backendDetails ?? ""}`,
  );
  if (!info.isProviderError) return "";

  const lines = ["The cloud provider rejected server creation."];
  if (info.providerLabel) lines.push(`Provider: ${info.providerLabel}`);
  if (info.code) lines.push(`Error code: ${info.code}`);
  if (info.summary) lines.push(`Provider message: ${info.summary}`);

  lines.push(retryHintGuidance(info.retryHint));

  lines.push("");
  lines.push("Technical details:");
  lines.push(errorMessage);
  return lines.join("\n");
}

export function extractNestedProviderMessage(message: string): string {
  const start = message.indexOf("{");
  const end = message.lastIndexOf("}");
  if (start >= 0 && end > start) {
    try {
      const parsed = JSON.parse(message.slice(start, end + 1)) as {
        error?: { message?: unknown };
        message?: unknown;
      };
      const nested =
        typeof parsed.error?.message === "string"
          ? parsed.error.message
          : typeof parsed.message === "string"
            ? parsed.message
            : "";
      if (nested.trim()) return nested.trim();
    } catch {
      // fall through to plain text cleanup
    }
  }
  return message.trim();
}

export function stripProviderErrorPrefixes(message: string): string {
  let out = message.trim();
  let changed = true;
  while (changed) {
    changed = false;
    const before = out;
    out = out
      .replace(/^simulate enroll returned\s+\d+\s*:\s*/i, "")
      .replace(/^create\s+ionos-managed\s+node:\s*/i, "")
      .replace(/^create\s+centron-managed\s+node:\s*/i, "")
      .replace(/^ionos create server request failed:\s*/i, "")
      .replace(/^centron create server request failed:\s*/i, "")
      .trim();
    changed = out !== before;
  }
  return out;
}

export function providerFromError(message: string): string {
  const lower = message.toLowerCase();
  if (lower.includes("ionos-managed") || lower.includes("ionos")) {
    return "ionos-managed";
  }
  if (lower.includes("centron-managed") || lower.includes("centron")) {
    return "centron-managed";
  }
  return "";
}

export function providerLabel(provider?: string): string {
  switch (provider) {
    case "ionos-managed":
      return "IONOS";
    case "centron-managed":
      return "Centron";
    default:
      return provider || "";
  }
}

export function containsAny(value: string, needles: string[]): boolean {
  return needles.some((needle) => value.includes(needle));
}

/**
 * Ordered match rules for {@link getTroubleshootingForError}. The FIRST rule
 * whose `custom` predicate or `any` substring matches the error wins, so the
 * order here is significant and mirrors the original branch order. Encapsulating
 * the conditions as data keeps the lookup free of the deep if/complex-conditional
 * chain it used to carry.
 */
interface TroubleshootingRule {
  any: string[];
  entry: keyof typeof ERROR_TROUBLESHOOTING;
  custom?: (rawError: string) => boolean;
}

export const TROUBLESHOOTING_RULES: TroubleshootingRule[] = [
  // Teardown comes first because the headline has to be right about WHICH
  // operation failed before it can be right about why. Every later rule can
  // shadow a decommission error on an incidental substring - the provider rule
  // matches any text naming "ionos"/"centron" through its custom predicate,
  // which troubleshootingRuleMatches evaluates unconditionally, and the
  // network/conflict rules match on words a teardown error routinely carries.
  // The concrete provider text still reaches the operator through the panel
  // body, which renders the raw failure.
  {
    any: [
      "decommission",
      "decommissioning",
      "destroy request",
      "teardown",
      "custody is incomplete",
    ],
    entry: "managed_runtime_decommission_failed",
  },
  {
    any: ["kit directory not found", "base stackkit not found"],
    entry: "stackkit_files_missing",
  },
  {
    any: [
      "stackkits artifact generation failed",
      "stackkit artifact generation failed",
      "stackkits cli generate failed",
      "stackkit cli generate failed",
      "kombify.me registration failed",
      "base subdomain limit reached",
      "api error 429",
      "no subdomainprefix is configured",
    ],
    entry: "stackkit_artifact_generation",
  },
  {
    any: [
      "identity handoff",
      "owner login",
      "login gateway",
      "stackkit_outputs",
    ],
    entry: "stackkit_identity_handoff_missing",
  },
  {
    any: [
      "stackkits rollout failed",
      "stackkit rollout failed",
      "stackkits could not apply",
      "stackkit_rollout",
      "runtime action stackkit_rollout",
      "opentofu_apply_failed",
    ],
    entry: "stackkit_rollout_failed",
  },
  { any: ["validation", "invalid"], entry: "validation_failed" },
  { any: ["network", "connection", "fetch"], entry: "network_error" },
  { any: ["stackkit", "kit not found"], entry: "stackkit_not_found" },
  { any: ["database", "pocketbase"], entry: "database_error" },
  { any: ["unifier", "cue"], entry: "unifier_error" },
  {
    any: [
      "lease enrollment failed",
      "create ionos-managed node",
      "create centron-managed node",
      "ionos create server",
      "centron create server",
      "storage creation",
      "too many recent create and delete operations",
      "vdc-5-1091",
    ],
    entry: "managed_runtime_provider_error",
    custom: (rawError) =>
      parseManagedRuntimeProviderError(rawError).isProviderError,
  },
  {
    any: [
      "target_bootstrap",
      "target bootstrap",
      "bootstrap managed runtime target",
      "managed runtime target bootstrap failed",
      "remote command exited without exit status",
      "without exit status or exit signal",
      "docker_ready=failed",
      "phase=docker_status status=failed",
    ],
    entry: "managed_runtime_bootstrap_failed",
  },
  { any: ["conflict", "port"], entry: "service_conflict" },
  {
    any: [
      "managed runtime",
      "vm lease",
      "runtime_ssh_host",
      "runtime_public_ip",
    ],
    entry: "managed_runtime_pending",
  },
];

// troubleshootingRuleMatches reports whether a rule applies to the error: its
// custom predicate matches the raw error, or one of its substrings appears in
// the lower-cased error.
function troubleshootingRuleMatches(
  rule: TroubleshootingRule,
  rawError: string,
  lowerError: string,
): boolean {
  if (rule.custom?.(rawError) === true) return true;
  return rule.any.some((needle) => lowerError.includes(needle));
}

/**
 * Map error messages to troubleshooting categories
 */
export function getTroubleshootingForError(error: string): {
  message: string;
  details: string;
  steps: string[];
} {
  const lowerError = error.toLowerCase();
  for (const rule of TROUBLESHOOTING_RULES) {
    if (troubleshootingRuleMatches(rule, error, lowerError)) {
      return ERROR_TROUBLESHOOTING[rule.entry];
    }
  }
  return ERROR_TROUBLESHOOTING.unknown_error;
}
