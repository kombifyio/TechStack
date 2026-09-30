import { tr } from "#lib/i18n.svelte.js";
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
    get message() {
      return tr("ui.providerErrors.configurationCouldNotBeValidated");
    },
    get details() {
      return tr("ui.providerErrors.theSubmittedConfigurationContainsInvalid");
    },
    get steps() {
      return [
        tr("ui.providerErrors.checkThatTheSubmittedValues"),
        tr("ui.providerErrors.makeSureAStackKitWas"),
        tr("ui.providerErrors.checkThatTheStackName"),
        tr("ui.providerErrors.forAuthenticationErrorsMakeSure"),
      ];
    },
  },
  network_error: {
    get message() {
      return tr("ui.providerErrors.networkErrorWhileSaving");
    },
    get details() {
      return tr("ui.providerErrors.theKombifyTechstackServerCould");
    },
    get steps() {
      return [
        tr("ui.providerErrors.checkYourInternetConnection"),
        tr("ui.providerErrors.makeSureTheKombifyTechstack"),
        tr("ui.providerErrors.checkThatPort5260Is"),
        tr("ui.providerErrors.withDockerUseDockerPs"),
      ];
    },
  },
  stackkit_files_missing: {
    get message() {
      return tr("ui.providerErrors.stackkitFilesMissing");
    },
    get details() {
      return tr("ui.providerErrors.aStackKitWasSelectedE");
    },
    get steps() {
      return [
        tr("ui.providerErrors.withDockerRebuildTheImage"),
        tr("ui.providerErrors.checkTheServerLogsDocker"),
        tr("ui.providerErrors.ifRunningTheBinaryOutside"),
        tr("ui.providerErrors.verifyTheStackKitDirectoryExists"),
      ];
    },
  },
  stackkit_artifact_generation: {
    get message() {
      return tr("ui.providerErrors.stackkitArtifactsCouldNotBe");
    },
    get details() {
      return tr("ui.providerErrors.theManagedVMWasPrepared");
    },
    get steps() {
      return [
        tr("ui.providerErrors.doNotCreateAnotherProvider"),
        tr("ui.providerErrors.checkTheErrorDetailsFor"),
        tr("ui.providerErrors.resolveTheKombifyMeOr"),
      ];
    },
  },
  stackkit_not_found: {
    get message() {
      return tr("ui.providerErrors.noMatchingStackKitFound");
    },
    get details() {
      return tr("ui.providerErrors.noCompatibleStackKitIsAvailable");
    },
    get steps() {
      return [
        tr("ui.providerErrors.trySelectingFewerServices"),
        tr("ui.providerErrors.switchToADifferentAccess"),
        tr("ui.providerErrors.checkThatStackKitFilesAre"),
        tr("ui.providerErrors.forCustomStackKitsValidateThe"),
      ];
    },
  },
  wizard_projection_rejected: {
    get message() {
      return tr("ui.providerErrors.thisNodeCouldNotBe");
    },
    get details() {
      return tr("ui.providerErrors.theAdditionalNodeIntentCould");
    },
    get steps() {
      return [
        tr("ui.providerErrors.retryAdditionalNodeAFoundation"),
        tr("ui.providerErrors.ifThisIsANew"),
      ];
    },
  },
  stackspec_v1_rejected: {
    get message() {
      return tr("ui.providerErrors.thisDeploymentIsNotArchitecture");
    },
    get details() {
      return tr("ui.providerErrors.stackkitsOnlyAcceptsAnArchitecture");
    },
    get steps() {
      return [
        tr("ui.providerErrors.foundANewKitFor"),
        tr("ui.providerErrors.doNotRetryAdditionalNode"),
        tr("ui.providerErrors.ifThisIsANew2"),
      ];
    },
  },
  stackkit_identity_handoff_missing: {
    get message() {
      return tr(
        "ui.stacksCreatingCreationLease.stackkitIdentityHandoffIsMissing",
      );
    },
    get details() {
      return tr("ui.providerErrors.theRolloutDidNotReturn");
    },
    get steps() {
      return [
        tr("ui.providerErrors.checkTheRuntimeActionResponse"),
        tr("ui.providerErrors.checkTheRuntimeActionResponse2"),
        tr("ui.providerErrors.checkTheRuntimeActionResponse3"),
        tr("ui.providerErrors.retryOnlyAfterTheStackKit"),
      ];
    },
  },
  stackkit_rollout_failed: {
    get message() {
      return tr("ui.providerErrors.stackkitRolloutCouldNotBe");
    },
    get details() {
      return tr("ui.providerErrors.theVMWasPreparedBut");
    },
    get steps() {
      return [
        tr("ui.providerErrors.checkTheErrorDetailsFor2"),
        tr("ui.providerErrors.checkTheRuntimeLogsFor"),
        tr("ui.providerErrors.doNotCreateAnotherProvider2"),
        tr("ui.providerErrors.retryTheRolloutOnlyAfter"),
      ];
    },
  },
  database_error: {
    get message() {
      return tr("ui.providerErrors.databaseError");
    },
    get details() {
      return tr("ui.providerErrors.theConfigurationCouldNotBe");
    },
    get steps() {
      return [
        tr("ui.providerErrors.checkThatThePocketBaseDatabase"),
        tr("ui.providerErrors.verifyWritePermissionsForThe"),
        tr("ui.providerErrors.withDockerEnsureTheVolume"),
        tr("ui.providerErrors.tryRestartingTheKombifyTechstack"),
      ];
    },
  },
  unifier_error: {
    get message() {
      return tr("ui.providerErrors.unifierProcessingError");
    },
    get details() {
      return tr("ui.providerErrors.theConfigurationCouldNotBe2");
    },
    get steps() {
      return [
        tr("ui.providerErrors.checkThatCUEIsInstalled"),
        tr("ui.providerErrors.reviewTheLogsWithDocker"),
        tr("ui.providerErrors.validateTheStackSpecYaml"),
        tr("ui.providerErrors.forPersistentErrorsCreateA"),
      ];
    },
  },
  service_conflict: {
    get message() {
      return tr("ui.providerErrors.serviceConflictDetected");
    },
    get details() {
      return tr("ui.providerErrors.theSelectedServicesHaveConflicting");
    },
    get steps() {
      return [
        tr("ui.providerErrors.disableConflictingServices"),
        tr("ui.providerErrors.checkPortConflictsInThe"),
        tr("ui.providerErrors.forVPNServicesOnlyOne"),
        tr("ui.providerErrors.forMonitoringVictoriaMetricsRetentionRequires"),
      ];
    },
  },
  managed_runtime_decommission_failed: {
    get message() {
      return tr("ui.providerErrors.thisDeploymentCouldNotBe");
    },
    get details() {
      return tr("ui.providerErrors.theTeardownStoppedBecauseTechstack");
    },
    get steps() {
      return [
        tr("ui.providerErrors.openTheLatestDestroyJob"),
        tr("ui.providerErrors.checkWhetherTheServerStill"),
        tr("ui.providerErrors.useTheServerSForce"),
      ];
    },
  },
  managed_runtime_pending: {
    get message() {
      return tr("ui.providerErrors.managedRuntimeIsNotReady");
    },
    get details() {
      return tr("ui.providerErrors.theVMLeaseHasNot");
    },
    get steps() {
      return [
        tr("ui.providerErrors.checkTheVMLeaseEnrollment"),
        tr("ui.providerErrors.checkWhetherCentronOrIONOS"),
        tr("ui.providerErrors.retryCreationOnlyAfterThe"),
      ];
    },
  },
  managed_runtime_bootstrap_failed: {
    get message() {
      return tr("ui.providerErrors.managedRuntimeCouldNotBe");
    },
    get details() {
      return tr("ui.providerErrors.theVMIsReachableBut");
    },
    get steps() {
      return [
        tr("ui.providerErrors.checkTheProviderPortalTo"),
        tr("ui.providerErrors.checkCloudInitDockerStatus"),
        tr("ui.providerErrors.retryCreationOnlyAfterSSH"),
      ];
    },
  },
  managed_runtime_provider_error: {
    get message() {
      return tr("ui.providerErrors.managedRuntimeCouldNotBe2");
    },
    get details() {
      return tr("ui.providerErrors.theCloudProviderRejectedOr");
    },
    get steps() {
      return [
        tr("ui.providerErrors.checkTheProviderErrorCode"),
        tr("ui.providerErrors.waitForRateLimitsOr"),
        tr("ui.providerErrors.checkTheLifecycleReceiptAnd"),
        tr("ui.providerErrors.ifProviderSupportIsRequired"),
      ];
    },
  },
  unknown_error: {
    get message() {
      return tr("ui.providerErrors.unexpectedError");
    },
    get details() {
      return tr("ui.providerErrors.anUnknownErrorOccurred");
    },
    get steps() {
      return [
        tr("ui.providerErrors.reloadThePageAndTry"),
        tr("ui.providerErrors.checkTheBrowserConsoleFor"),
        tr("ui.providerErrors.reviewTheServerLogs"),
        tr("ui.providerErrors.createAGitHubIssueWith"),
      ];
    },
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
    get any() {
      return [
        "vdc-5-1091",
        tr("ui.providerErrors.tooManyRecentCreateAnd"),
        tr("ui.providerErrors.tooManyRecentCreateDelete"),
        "rate limit",
        "rate-limit",
        "ratelimit",
        "throttl",
      ];
    },
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
    get any() {
      return [
        "service conflict",
        "already exists",
        tr("ui.providerErrors.nameIsAlreadyInUse"),
        "resource conflict",
      ];
    },
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
      return tr("ui.providerErrors.nextStepWaitForThe");
    case "free_provider_resources":
      return tr("ui.providerErrors.nextStepCheckLimitsQuota");
    case "contact_provider_support":
      return tr("ui.providerErrors.nextStepCheckTheProvider");
    default:
      return tr("ui.providerErrors.nextStepCheckTheLifecycle");
  }
}

export function buildManagedRuntimeProviderErrorDetails(
  errorMessage: string,
  backendDetails?: string,
): string {
  const info = parseManagedRuntimeProviderError(
    `${errorMessage}
${backendDetails ?? ""}`,
  );
  if (!info.isProviderError) return "";

  const lines = [tr("ui.providerErrors.theCloudProviderRejectedServer")];
  if (info.providerLabel) lines.push(`Provider: ${info.providerLabel}`);
  if (info.code)
    lines.push(tr("ui.providerErrors.errorCode", { code: info.code }));
  if (info.summary)
    lines.push(
      tr("ui.providerErrors.providerMessage", { summary: info.summary }),
    );

  lines.push(retryHintGuidance(info.retryHint));

  lines.push("");
  lines.push(tr("ui.providerErrors.technicalDetails"));
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
    get any() {
      return [
        tr("ui.providerErrors.kitDirectoryNotFound"),
        tr("ui.providerErrors.baseStackkitNotFound"),
      ];
    },
    entry: "stackkit_files_missing",
  },
  {
    get any() {
      return [
        "wizard_projection_rejected",
        "projection rejected",
        tr("ui.providerErrors.joiningASecondController"),
        tr("ui.providerErrors.thisNodeCouldNotBe2"),
      ];
    },
    entry: "wizard_projection_rejected",
  },
  {
    get any() {
      return [
        "v1.unknown-fields",
        "migration_blockers",
        "architecture v2",
        "stackkit/v2alpha1",
        tr("ui.providerErrors.v1StackspecCannotCarry"),
        tr("ui.providerErrors.joinRequiresAnArchitectureV2"),
        "canonical v2",
      ];
    },
    entry: "stackspec_v1_rejected",
  },
  {
    get any() {
      return [
        tr("ui.providerErrors.stackkitsArtifactGenerationFailed"),
        tr("ui.providerErrors.stackkitArtifactGenerationFailed"),
        tr("ui.providerErrors.stackkitsCliGenerateFailed"),
        tr("ui.providerErrors.stackkitCliGenerateFailed"),
        tr("ui.providerErrors.kombifyMeRegistrationFailed"),
        tr("ui.providerErrors.baseSubdomainLimitReached"),
        "api error 429",
        tr("ui.providerErrors.noSubdomainprefixIsConfigured"),
      ];
    },
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
    get any() {
      return [
        "stackkits rollout failed",
        "stackkit rollout failed",
        tr("ui.providerErrors.stackkitsCouldNotApply"),
        "stackkit_rollout",
        tr("ui.providerErrors.runtimeActionStackkitRollout"),
        "opentofu_apply_failed",
      ];
    },
    entry: "stackkit_rollout_failed",
  },
  { any: ["validation", "invalid"], entry: "validation_failed" },
  { any: ["network", "connection", "fetch"], entry: "network_error" },
  {
    any: ["no matching stackkit", "kit not found"],
    entry: "stackkit_not_found",
  },
  { any: ["database", "pocketbase"], entry: "database_error" },
  { any: ["unifier", "cue"], entry: "unifier_error" },
  {
    get any() {
      return [
        "lease enrollment failed",
        "create ionos-managed node",
        "create centron-managed node",
        "ionos create server",
        "centron create server",
        "storage creation",
        tr("ui.providerErrors.tooManyRecentCreateAnd"),
        "vdc-5-1091",
      ];
    },
    entry: "managed_runtime_provider_error",
    custom: (rawError) =>
      parseManagedRuntimeProviderError(rawError).isProviderError,
  },
  {
    get any() {
      return [
        "target_bootstrap",
        "target bootstrap",
        tr("ui.providerErrors.bootstrapManagedRuntimeTarget"),
        tr("ui.providerErrors.managedRuntimeTargetBootstrapFailed"),
        "remote command exited without exit status",
        tr("ui.providerErrors.withoutExitStatusOrExit"),
        "docker_ready=failed",
        "phase=docker_status status=failed",
      ];
    },
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
