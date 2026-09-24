<script lang="ts">
  import { goto as svelteGoto } from "$app/navigation";
  import { getClientBootstrap } from "#lib/client/bootstrap.js";
  import { API_BASE } from "#lib/api/client.js";
  import { parseApiError, type ParsedApiError } from "#lib/api/errors.js";
  import { appCommit, appVersion } from "#lib/config.js";
  import { authHandler } from "#lib/stores/authHandler.svelte.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import SessionRenewalPanel from "#lib/components/SessionRenewalPanel.svelte";
  import { loadFeatures } from "#lib/stores/features.js";
  import { createWizardRun } from "#lib/api/wizardRuns.js";
  import { buildFoundRunRequest } from "#lib/wizard/wizardRunRequest.js";
  import { refreshEmbeddedCloudSession } from "#lib/auth/embedded-session.js";
  import { ExternalLink } from "@lucide/svelte";
  import ErrorAiHandoverButton from "#lib/components/support/ErrorAiHandoverButton.svelte";
  import { EasyWizard } from "#lib/components/wizard/index.js";
  import {
    createErrorAiHandoverContext,
    type ErrorAiHandoverContext,
  } from "#lib/support/error-handover.js";
  import { normalizeServerOutcome } from "#lib/support/server-outcome.js";
  import { type StackConfig } from "#lib/wizard/index.js";
  import {
    buildTechstackCreationEventProperties,
    createPostHogClient,
    toTechstackAnalyticsUser,
    type TechstackCreationEventInput,
  } from "#lib/analytics/posthog.js";
  import { tr } from "#lib/i18n.svelte.js";

  interface Props {
    /** Remove page-owned spacing/title so the host can mount the full flow. */
    embedded?: boolean;
    /** Host-owned navigation keeps the flow usable in a panel or modal shell. */
    onNavigate?: (href: string) => void | Promise<void>;
    returnTo?: string;
  }

  let {
    embedded = false,
    onNavigate,
    returnTo = "/stacks/new",
  }: Props = $props();

  async function navigate(href: string) {
    if (onNavigate) {
      await onNavigate(href);
      return;
    }
    await svelteGoto(href);
  }

  let isHydrated = $state(false);
  let readinessStarted = false;
  $effect(() => {
    if (
      readinessStarted ||
      authStore.loading ||
      !authStore.modeDetected ||
      !authStore.isAuthenticated
    ) {
      return;
    }
    readinessStarted = true;
    void loadFeatures().finally(() => {
      isHydrated = true;
    });
  });

  let isDeploying = $state(false);
  let sessionRenewalRequired = $state(false);
  let deployError = $state<string | null>(null);
  let deployErrorDetails = $state<string | null>(null);
  let deployErrorHandover = $state<ErrorAiHandoverContext | null>(null);
  let conflictStackId = $state<string | null>(null);
  let conflictStackName = $state<string | null>(null);
  let showLoginHint = $state(false);

  let validationStage = $state<string>("");

  function recordFrom(value: unknown): Record<string, unknown> | null {
    return value && typeof value === "object"
      ? (value as Record<string, unknown>)
      : null;
  }

  function stringField(
    record: Record<string, unknown> | null,
    field: string,
  ): string | null {
    const value = record?.[field];
    return typeof value === "string" && value.trim() ? value : null;
  }

  function stringArrayField(
    record: Record<string, unknown> | null,
    field: string,
  ): string[] {
    const value = record?.[field];
    if (!Array.isArray(value)) return [];
    return value
      .filter((item): item is string => typeof item === "string")
      .map((item) => item.trim())
      .filter(Boolean);
  }

  function extractConflictDetails(error: unknown, details: unknown) {
    const errorDetails =
      error && typeof error === "object" && "details" in error
        ? (error as { details?: unknown }).details
        : undefined;
    for (const candidate of [details, errorDetails]) {
      const direct = recordFrom(candidate);
      const nested = recordFrom(direct?.details);
      const record = nested || direct;
      const stackId = stringField(record, "stack_id");
      const name = stringField(record, "name");
      if (stackId || name) {
        return { stackId, name };
      }
    }
    return { stackId: null, name: null };
  }

  function createErrorDetails(details: unknown) {
    const direct = recordFrom(details);
    const nested = recordFrom(direct?.details);
    const record = nested || direct;
    const userGuidance = recordFrom(record?.user_guidance);
    const normalizedOutcome = normalizeServerOutcome(record);
    return {
      phase: stringField(record, "phase"),
      phaseLabel: stringField(record, "phase_label"),
      errorCode: stringField(record, "error_code"),
      reasonCode: stringField(record, "reason_code"),
      providerId: stringField(record, "provider_id"),
      capability: stringField(record, "capability"),
      remediation: stringField(record, "remediation"),
      guidanceTitle: stringField(userGuidance, "title"),
      guidanceBody: stringField(userGuidance, "body"),
      nextSteps:
        normalizedOutcome?.userGuidance?.nextSteps.map((step) => step.label) ??
        stringArrayField(userGuidance, "next_steps"),
      requiredFeatures: stringArrayField(record, "required_features"),
      missingFeatures: stringArrayField(record, "missing_features"),
      stackId: stringField(record, "stack_id"),
      requestId: stringField(record, "request_id"),
      retryable:
        typeof record?.retryable === "boolean" ? record.retryable : undefined,
    };
  }

  function formatAvailabilityLead(
    details: ReturnType<typeof createErrorDetails>,
    fallback: string,
  ) {
    const lines: string[] = [];
    lines.push(details.guidanceBody || fallback);
    if (details.nextSteps.length > 0) {
      lines.push(
        `Next steps:\n${details.nextSteps.map((step) => `- ${step}`).join("\n")}`,
      );
    } else if (details.remediation) {
      lines.push(`Next step: ${details.remediation}`);
    }
    return lines.join("\n\n");
  }

  function formatCreateErrorDetails(parsed: ParsedApiError, lead?: string) {
    const details = createErrorDetails(parsed.details);
    const lines: string[] = [];

    if (details.phaseLabel) {
      lines.push(`Step: ${details.phaseLabel}`);
    }
    if (lead && lead.trim()) {
      lines.push(lead);
    } else if (parsed.message) {
      lines.push(parsed.message);
    }
    const code = details.errorCode;
    if (code) {
      lines.push(`Error code: ${code}`);
    }
    if (details.reasonCode) {
      lines.push(`Reason: ${details.reasonCode}`);
    }
    if (details.providerId) {
      lines.push(`Provider: ${details.providerId}`);
    }
    if (details.missingFeatures.length > 0) {
      lines.push(`Missing features: ${details.missingFeatures.join(", ")}`);
    }
    if (details.requiredFeatures.length > 0) {
      lines.push(`Required features: ${details.requiredFeatures.join(", ")}`);
    }
    if (details.capability) {
      lines.push(`Capability: ${details.capability}`);
    }
    if (details.stackId) {
      lines.push(`Stack ID: ${details.stackId}`);
    }
    if (details.requestId) {
      lines.push(`Request ID: ${details.requestId}`);
    }
    if (details.retryable === true) {
      lines.push(
        "You can retry. If this happens again, share the error code and request ID with support.",
      );
    } else if (details.retryable === false && parsed.status >= 500) {
      lines.push(
        "Retrying is unlikely to help until the server-side issue is fixed.",
      );
    }

    return lines.join("\n") || parsed.message;
  }

  function currentRoute() {
    if (typeof window === "undefined") return "/stacks/new";
    return `${window.location.pathname}${window.location.search}`;
  }

  function buildCreateErrorHandover(
    parsed: ParsedApiError,
    attemptedStackName: string,
  ) {
    if (!deployError || !deployErrorDetails || showLoginHint) return null;

    const details = createErrorDetails(parsed.details);
    return createErrorAiHandoverContext({
      surface: "stacks.create",
      title: deployError,
      userMessage: deployErrorDetails,
      status: parsed.status,
      code: details.errorCode ?? undefined,
      errorCode: details.errorCode ?? undefined,
      action: "create_stack",
      phase: details.phase ?? undefined,
      phaseLabel: details.phaseLabel ?? undefined,
      requestId: details.requestId ?? undefined,
      resourceId: conflictStackId || details.stackId || undefined,
      resourceName: conflictStackName || attemptedStackName,
      route: currentRoute(),
      appVersion,
      appCommit,
      retryable: details.retryable,
      reasonCode: details.reasonCode ?? undefined,
      providerId: details.providerId ?? undefined,
      capability: details.capability ?? undefined,
      missingFeatures: details.missingFeatures,
      requiredFeatures: details.requiredFeatures,
    });
  }

  function countEnabledValues(record: object | undefined) {
    return Object.values(record ?? {}).filter((value) => value === true).length;
  }

  function stackCreationAnalyticsInput(
    config: StackConfig,
    extra: TechstackCreationEventInput = {},
  ): TechstackCreationEventInput {
    return {
      wizardType: config.wizardType,
      provider: config.provider,
      stackMode: config.serverMode,
      serverProvisioningMode: config.serverProvisioning?.mode,
      serviceCount: countEnabledValues(config.services),
      goalCount: countEnabledValues(config.goals),
      ...extra,
    };
  }

  function captureStackCreationEvent(
    event:
      | "techstack:stack_create_started"
      | "techstack:pipeline_previewed"
      | "techstack:stack_created"
      | "techstack:stack_create_failed",
    input: TechstackCreationEventInput,
  ) {
    if (typeof window === "undefined") return;
    const user = toTechstackAnalyticsUser(authStore.cloudUser);
    if (!user?.authSubject) return;

    const bootstrap = getClientBootstrap();
    void createPostHogClient({
      apiKey: bootstrap.telemetry.posthog.key || undefined,
      host: bootstrap.telemetry.posthog.host || undefined,
      environment: bootstrap.telemetry.posthog.environment || undefined,
      edition:
        bootstrap.kombifyEdition ||
        (authStore.deploymentMode === "saas"
          ? "saas-embedded"
          : "selfhost-oss"),
      appVersion,
      location: window.location,
    }).capture(event, {
      user,
      properties: {
        ...buildTechstackCreationEventProperties(input),
        deployment_mode: authStore.deploymentMode,
      },
    });
  }

  function clearCreateError() {
    deployError = null;
    deployErrorDetails = null;
    deployErrorHandover = null;
    conflictStackId = null;
    conflictStackName = null;
    showLoginHint = false;
  }

  const creationIdempotencyStorageKey = "stackCreationIdempotencyKey";

  function creationIdempotencyKey(): string {
    const existing = sessionStorage.getItem(creationIdempotencyStorageKey);
    if (existing) return existing;
    const generated =
      typeof crypto?.randomUUID === "function"
        ? crypto.randomUUID()
        : `create-${Date.now()}-${Math.random().toString(16).slice(2)}`;
    sessionStorage.setItem(creationIdempotencyStorageKey, generated);
    return generated;
  }

  function openConflictStack() {
    if (conflictStackId) {
      void navigate(`/stacks/${encodeURIComponent(conflictStackId)}`);
    }
  }

  // The Wizard run is the single creation authority: projection, pinned
  // StackKits validation, persistence, and dispatch happen server-side. A
  // rejected run is a structured error and leaves no state behind.
  async function createViaWizardRun(config: StackConfig) {
    validationStage = "Validating with StackKits...";
    const request = buildFoundRunRequest(config);
    const idempotencyKey = creationIdempotencyKey();
    const run = await createWizardRun(request, idempotencyKey);
    captureStackCreationEvent(
      "techstack:stack_created",
      stackCreationAnalyticsInput(config, {
        stackId: run.kit_deployment_id,
        jobId: run.job_id,
        serverId: run.server_id,
      }),
    );

    const persistedName = run.name || request.intent.name;
    const jobId = run.job_id || "";
    sessionStorage.removeItem(creationIdempotencyStorageKey);

    const params = new URLSearchParams();
    params.set("operation", "stack");
    params.set("stack_id", run.kit_deployment_id);
    if (jobId) params.set("job_id", jobId);
    params.set("name", persistedName);
    if (run.pairing_job_id) params.set("pairing_job_id", run.pairing_job_id);
    await navigate(`/stacks/creating?${params.toString()}`);
  }

  async function handleCreate(config: StackConfig) {
    isDeploying = true;
    sessionRenewalRequired = false;
    clearCreateError();
    validationStage = "Validating configuration...";
    let attemptedStackName = config.name;

    try {
      captureStackCreationEvent(
        "techstack:stack_create_started",
        stackCreationAnalyticsInput(config),
      );

      // Check authentication FIRST
      if (!authStore.isAuthenticated) {
        const refreshed = await refreshEmbeddedCloudSession();
        if (!refreshed) {
          showLoginHint = true;
          throw new Error("You must be logged in to deploy a StackKit.");
        }
      }

      await createViaWizardRun(config);
    } catch (error) {
      console.error("StackKit deployment creation failed:", error);

      // Normalize the shared API error envelope for structured feedback.
      const parsed = parseApiError(error);
      const parsedDetails = createErrorDetails(parsed.details);
      captureStackCreationEvent(
        "techstack:stack_create_failed",
        stackCreationAnalyticsInput(config, {
          errorStatus: parsed.status,
          requestId: parsedDetails.requestId,
          reasonCode: parsedDetails.reasonCode,
          errorCode:
            parsedDetails.errorCode ??
            (parsed.isAuthError
              ? "auth_error"
              : parsed.isForbidden
                ? "forbidden"
                : parsed.isValidationError
                  ? "validation_error"
                  : undefined),
          retryable: parsedDetails.retryable,
          conflict: parsed.status === 409 || (error as any)?.status === 409,
        }),
      );

      // Log detailed error info for debugging
      console.error("Parsed stack creation failure:", {
        status: parsed.status,
        code: parsedDetails.errorCode,
        reasonCode: parsedDetails.reasonCode,
        requestId: parsedDetails.requestId,
        isAuthError: parsed.isAuthError,
        isForbidden: parsed.isForbidden,
        isValidationError: parsed.isValidationError,
      });

      // Check for network errors (Failed to fetch)
      const isNetworkError =
        error instanceof Error &&
        (error.message.includes("Failed to fetch") ||
          error.message.includes("NetworkError") ||
          error.message.includes("net::ERR_CONNECTION_REFUSED"));

      if (isNetworkError) {
        const apiBase = API_BASE;
        deployError = "Cannot connect to backend";
        deployErrorDetails =
          (apiBase
            ? `The API server at ${apiBase} is not reachable from your browser.\n\n`
            : "The API server is not reachable from your browser.\n\n") +
          `If the backend is running but the browser blocks the request (CORS), open DevTools → Console and look for a CORS error.\n\n` +
          `Technical details: ${error instanceof Error ? error.message : String(error)}`;
      } else if (parsed.isAuthError) {
        // Use global auth handler for 401 errors. Auto-redirect is disabled
        // here: a full-page bounce would destroy the in-memory wizard config.
        isDeploying = false;
        const outcome = await authHandler.handleUnauthorized(
          // Retry function - will be called after successful re-login
          async () => {
            await handleCreate(config);
          },
          "auth.session.expired.default",
          error,
          { allowAutoRedirect: false },
        );
        if (outcome === "reauth_required") {
          sessionRenewalRequired = true;
        }
        return; // Don't show error UI, auth handler manages the flow
      } else if (!authStore.isAuthenticated) {
        showLoginHint = true;
        deployError = "Authentication required";
        deployErrorDetails =
          "You must be logged in to deploy a StackKit. Please log in first.";
      } else if (parsed.isForbidden) {
        const details = createErrorDetails(parsed.details);
        if (details.errorCode === "managed_runtime_feature_disabled") {
          deployError = details.guidanceTitle || "Managed server is not active";
          deployErrorDetails = formatCreateErrorDetails(
            parsed,
            formatAvailabilityLead(details, parsed.message),
          );
        } else if (details.guidanceTitle || details.guidanceBody) {
          // Structured denial with user_guidance (e.g. owner_bootstrap_denied):
          // lead with the actionable guidance instead of a generic headline.
          deployError = details.guidanceTitle || "Permission denied";
          deployErrorDetails = formatCreateErrorDetails(
            parsed,
            formatAvailabilityLead(details, parsed.message),
          );
        } else {
          deployError = "Permission denied";
          // Surface the real backend/upstream reason (status, message,
          // error_code, reason_code, missing/required features) instead of a
          // generic string. Required by FEATURE-ENTITLEMENT-UX-STANDARD, and
          // essential for diagnosing which gate denied the request: passing no
          // lead lets formatCreateErrorDetails fall through to parsed.message +
          // the structured codes. Only fall back to generic text when the
          // server gave no message at all.
          deployErrorDetails = formatCreateErrorDetails(
            parsed,
            parsed.message?.trim()
              ? undefined
              : "You don't have permission to deploy StackKits, and the server did not provide a specific reason. Please share this with support.",
          );
        }
      } else if (parsed.status === 409 || (error as any)?.status === 409) {
        const conflictDetails = createErrorDetails(parsed.details);
        if (
          conflictDetails.reasonCode === "wizard_idempotency_conflict" ||
          conflictDetails.reasonCode === "idempotency_conflict"
        ) {
          // The session's attempt key already completed a DIFFERENT run.
          // Discard it so the next submit mints a fresh key instead of
          // dead-ending every retry on the same 409.
          sessionStorage.removeItem(creationIdempotencyStorageKey);
          conflictStackId = conflictDetails.stackId || null;
          conflictStackName = null;
          deployError = "A previous submission already completed";
          deployErrorDetails = formatCreateErrorDetails(
            parsed,
            "An earlier submission from this browser session already completed with different answers. A fresh attempt key was generated — submit again, or open the existing deployment.",
          );
        } else {
          const conflict = extractConflictDetails(error, parsed.details);
          conflictStackId = conflict.stackId;
          conflictStackName = conflict.name || attemptedStackName;
          deployError = "StackKit deployment name already exists";
          const lead = conflictStackName
            ? `A StackKit deployment named "${conflictStackName}" already exists in your Homelab. Open the existing deployment or choose a different name.`
            : parsed.message ||
              "The backend rejected this StackKit deployment request with a conflict.";
          deployErrorDetails = formatCreateErrorDetails(parsed, lead);
        }
      } else if (parsed.isValidationError) {
        const validationDetails = createErrorDetails(parsed.details);
        deployError = validationDetails.guidanceTitle || "Validation failed";
        // Show field-specific errors if available
        if (Object.keys(parsed.fieldErrors).length > 0) {
          const fieldMessages = Object.entries(parsed.fieldErrors)
            .map(([field, { message }]) => `• ${field}: ${message}`)
            .join("\n");
          deployErrorDetails = formatCreateErrorDetails(
            parsed,
            `Please fix the following:\n${fieldMessages}`,
          );
        } else {
          // The wizard-run facade carries the pinned CLI's rejection in
          // validate_error — the one detail the user needs to fix the run.
          const validateError =
            stringField(recordFrom(parsed.details), "validate_error") ||
            stringField(
              recordFrom(recordFrom(parsed.details)?.details),
              "validate_error",
            );
          deployErrorDetails = formatCreateErrorDetails(
            parsed,
            validateError
              ? `${parsed.message}\n\nStackKits validation:\n${validateError}`
              : parsed.message,
          );
        }
      } else if (parsed.status >= 500) {
        const details = createErrorDetails(parsed.details);
        deployError = details.phaseLabel
          ? `Create failed at: ${details.phaseLabel}`
          : "Server error";
        deployErrorDetails = formatCreateErrorDetails(
          parsed,
          `The server encountered an error (${parsed.status}). ${parsed.message}`,
        );
      } else {
        // Fallback for other errors
        deployError =
          error instanceof Error ? error.message : "Deployment failed";
        deployErrorDetails =
          parsed.message !== deployError
            ? parsed.message
            : "An unexpected error occurred. Please check the browser console for details.";
      }

      deployErrorHandover = buildCreateErrorHandover(
        parsed,
        attemptedStackName,
      );
      isDeploying = false;
    }
  }
</script>

<div
  class={embedded
    ? "w-full min-w-0 overflow-x-hidden"
    : "w-full min-w-0 overflow-x-hidden px-3 py-6 sm:px-6 lg:p-8"}
  data-flow="configuration"
  data-presentation={embedded ? "embedded" : "page"}
>
  {#if isHydrated}
    <span class="hidden" aria-hidden="true" data-testid="hydrated"></span>
  {/if}

  {#if isDeploying && validationStage}
    <div
      class="mb-6 p-4 rounded-lg border border-primary/30 bg-primary/10"
      data-testid="validation-progress"
    >
      <div class="flex items-center gap-3">
        <svg
          class="w-5 h-5 text-primary animate-spin"
          fill="none"
          viewBox="0 0 24 24"
        >
          <circle
            class="opacity-25"
            cx="12"
            cy="12"
            r="10"
            stroke="currentColor"
            stroke-width="4"
          ></circle>
          <path
            class="opacity-75"
            fill="currentColor"
            d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
          ></path>
        </svg>
        <div>
          <p class="font-medium text-foreground">{validationStage}</p>
        </div>
      </div>
    </div>
  {/if}

  {#if sessionRenewalRequired}
    <div class="mb-6">
      <SessionRenewalPanel {returnTo} />
    </div>
  {/if}

  {#if deployError}
    <div
      class="mb-6 p-4 rounded-lg border {showLoginHint
        ? 'border-warning/30 bg-warning/10'
        : 'border-destructive/30 bg-destructive/10'}"
      data-testid="deploy-error"
    >
      <div class="flex items-start gap-3">
        <svg
          class="w-5 h-5 shrink-0 mt-0.5 {showLoginHint
            ? 'text-warning'
            : 'text-destructive'}"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          {#if showLoginHint}
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"
            />
          {:else}
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
            />
          {/if}
        </svg>
        <div class="flex-1">
          <p
            class="font-medium {showLoginHint
              ? 'text-warning'
              : 'text-destructive'}"
          >
            {deployError}
          </p>
          {#if deployErrorDetails}
            <p
              class="text-sm {showLoginHint
                ? 'text-warning/80'
                : 'text-destructive/80'} mt-1 whitespace-pre-line break-words"
            >
              {deployErrorDetails}
            </p>
          {/if}
          <ErrorAiHandoverButton context={deployErrorHandover} />
          {#if showLoginHint}
            <div class="mt-3 p-3 bg-muted/50 rounded-lg border border-border">
              <p class="text-sm text-muted-foreground mb-2">
                Default login credentials:
              </p>
              <div class="font-mono text-xs space-y-1">
                <p class="text-primary">admin@techstack.local</p>
                <p class="text-muted-foreground">
                  (use the admin password configured for this instance)
                </p>
              </div>
              <a
                href="/login"
                data-kx="control"
                data-variant="primary"
                class="mt-3 inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
              >
                Continue setup
              </a>
            </div>
          {/if}
          <button
            onclick={clearCreateError}
            class="mt-2 text-sm {showLoginHint
              ? 'text-warning hover:text-warning/80'
              : 'text-destructive hover:text-destructive/80'} underline"
          >
            Dismiss
          </button>
          {#if conflictStackId}
            <button
              type="button"
              onclick={openConflictStack}
              class="ml-3 mt-2 inline-flex items-center gap-1.5 rounded-md border border-destructive/40 px-3 py-1.5 text-sm font-medium text-destructive hover:bg-destructive/10"
            >
              <ExternalLink class="h-4 w-4" aria-hidden="true" />
              Open existing
            </button>
          {/if}
        </div>
      </div>
    </div>
  {/if}

  <!-- Wizard Content -->
  <EasyWizard oncreate={handleCreate} {isDeploying} />
</div>
