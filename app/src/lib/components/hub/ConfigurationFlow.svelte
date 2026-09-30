<script lang="ts">
  import { untrack } from "svelte";
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
  import {
    putOperatorSelfDisclosure,
    type OperatorSelfDisclosure,
  } from "#lib/api/operatorProfile.js";
  import { selfDisclosureWizardConfig } from "#lib/wizard/self-disclosure-handoff.js";

  interface Props {
    /** Remove page-owned spacing/title so the host can mount the full flow. */
    embedded?: boolean;
    /** Host-owned navigation keeps the flow usable in a panel or modal shell. */
    onNavigate?: (href: string) => void | Promise<void>;
    returnTo?: string;
    /** Voluntary answers handed over from the kombify Cloud capture surface. */
    selfDisclosure?: OperatorSelfDisclosure | null;
    /** Called once the handed-over answers were stored or given up on. */
    onSelfDisclosureSettled?: () => void;
  }

  let {
    embedded = false,
    onNavigate,
    returnTo = "/stacks/new",
    selfDisclosure = null,
    onSelfDisclosureSettled,
  }: Props = $props();

  // The wizard mounts only after a handed-over disclosure is stored, so its
  // first recommendation already uses the profile the Unifier derives from it.
  // A failed write never blocks creation: the wizard then opens without it.
  const handedOver = untrack(() => selfDisclosure);
  let selfDisclosureState = $state<
    "none" | "pending" | "applying" | "applied" | "failed"
  >(handedOver ? "pending" : "none");
  const wizardPrefill = handedOver
    ? selfDisclosureWizardConfig(handedOver)
    : undefined;

  // Hold the wizard back while the answers are (about to be) written; a
  // signed-out visitor gets the wizard at once.
  const holdWizardForSelfDisclosure = $derived(
    selfDisclosureState === "applying" ||
      (selfDisclosureState === "pending" &&
        (authStore.loading ||
          !authStore.modeDetected ||
          authStore.isAuthenticated)),
  );

  function settleSelfDisclosure(state: "applied" | "failed") {
    selfDisclosureState = state;
    onSelfDisclosureSettled?.();
  }

  $effect(() => {
    if (selfDisclosureState !== "pending" || !handedOver) return;
    // Signed out: the sign-in redirect keeps the URL, so the answers are
    // stored on the way back. Until then the wizard stays usable.
    if (
      authStore.loading ||
      !authStore.modeDetected ||
      !authStore.isAuthenticated
    )
      return;
    selfDisclosureState = "applying";
    void putOperatorSelfDisclosure(handedOver, "cloud").then(
      () => settleSelfDisclosure("applied"),
      () => settleSelfDisclosure("failed"),
    );
  });

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
        `${tr("ui.configFlow.nextSteps")}\n${details.nextSteps.map((step) => `- ${step}`).join("\n")}`,
      );
    } else if (details.remediation) {
      lines.push(tr("ui.configFlow.nextStep", { value: details.remediation }));
    }
    return lines.join("\n\n");
  }

  function formatCreateErrorDetails(parsed: ParsedApiError, lead?: string) {
    const details = createErrorDetails(parsed.details);
    const lines: string[] = [];

    if (details.phaseLabel) {
      lines.push(tr("ui.configFlow.step", { value: details.phaseLabel }));
    }
    if (lead && lead.trim()) {
      lines.push(lead);
    } else if (parsed.message) {
      lines.push(parsed.message);
    }
    const code = details.errorCode;
    if (code) {
      lines.push(tr("ui.configFlow.errorCode", { value: code }));
    }
    if (details.reasonCode) {
      lines.push(tr("ui.configFlow.reason", { value: details.reasonCode }));
    }
    if (details.providerId) {
      lines.push(tr("ui.configFlow.provider", { value: details.providerId }));
    }
    if (details.missingFeatures.length > 0) {
      lines.push(
        tr("ui.configFlow.missingFeatures", {
          value: details.missingFeatures.join(", "),
        }),
      );
    }
    if (details.requiredFeatures.length > 0) {
      lines.push(
        tr("ui.configFlow.requiredFeatures", {
          value: details.requiredFeatures.join(", "),
        }),
      );
    }
    if (details.capability) {
      lines.push(tr("ui.configFlow.capability", { value: details.capability }));
    }
    if (details.stackId) {
      lines.push(tr("ui.configFlow.stackId", { value: details.stackId }));
    }
    if (details.requestId) {
      lines.push(tr("ui.configFlow.requestId", { value: details.requestId }));
    }
    if (details.retryable === true) {
      lines.push(
        tr("ui.configurationFlow.youCanRetryIfThis"),
      );
    } else if (details.retryable === false && parsed.status >= 500) {
      lines.push(
        tr("ui.configurationFlow.retryingIsUnlikelyToHelp"),
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
    validationStage = tr("ui.configurationFlow.validatingWithStackkits");
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
    validationStage = tr("ui.configurationFlow.validatingConfiguration");
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
          throw new Error(tr("ui.configurationFlow.youMustBeLoggedIn"));
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
        deployError = tr("ui.configurationFlow.cannotConnectToBackend");
        deployErrorDetails = [
          apiBase
            ? tr("ui.configFlow.apiUnreachableAt", { base: apiBase })
            : tr("ui.configFlow.apiUnreachable"),
          tr("ui.configFlow.corsHint"),
          tr("ui.configFlow.technicalDetails", {
            value: error instanceof Error ? error.message : String(error),
          }),
        ].join("\n\n");
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
        deployError = tr("ui.configurationFlow.authenticationRequired");
        deployErrorDetails =
          tr("ui.configurationFlow.youMustBeLoggedIn2");
      } else if (parsed.isForbidden) {
        const details = createErrorDetails(parsed.details);
        if (details.errorCode === "managed_runtime_feature_disabled") {
          deployError = details.guidanceTitle || tr("ui.configurationFlow.managedServerIsNotActive");
          deployErrorDetails = formatCreateErrorDetails(
            parsed,
            formatAvailabilityLead(details, parsed.message),
          );
        } else if (details.guidanceTitle || details.guidanceBody) {
          // Structured denial with user_guidance (e.g. owner_bootstrap_denied):
          // lead with the actionable guidance instead of a generic headline.
          deployError = details.guidanceTitle || tr("ui.configurationFlow.permissionDenied");
          deployErrorDetails = formatCreateErrorDetails(
            parsed,
            formatAvailabilityLead(details, parsed.message),
          );
        } else {
          deployError = tr("ui.configurationFlow.permissionDenied");
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
              : tr("ui.configurationFlow.youDonTHavePermission"),
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
          deployError = tr("ui.configurationFlow.aPreviousSubmissionAlreadyCompleted");
          deployErrorDetails = formatCreateErrorDetails(
            parsed,
            tr("ui.configurationFlow.anEarlierSubmissionFromThis"),
          );
        } else {
          const conflict = extractConflictDetails(error, parsed.details);
          conflictStackId = conflict.stackId;
          conflictStackName = conflict.name || attemptedStackName;
          deployError = tr("ui.configurationFlow.stackkitDeploymentNameAlreadyExists");
          const lead = conflictStackName
            ? tr("ui.configFlow.deploymentNameExists", { name: conflictStackName })
            : parsed.message ||
              tr("ui.configurationFlow.theBackendRejectedThisStackkit");
          deployErrorDetails = formatCreateErrorDetails(parsed, lead);
        }
      } else if (parsed.isValidationError) {
        const validationDetails = createErrorDetails(parsed.details);
        deployError = validationDetails.guidanceTitle || tr("ui.stackImportExportModal.validationFailed");
        // Show field-specific errors if available
        if (Object.keys(parsed.fieldErrors).length > 0) {
          const fieldMessages = Object.entries(parsed.fieldErrors)
            .map(([field, { message }]) => `• ${field}: ${message}`)
            .join("\n");
          deployErrorDetails = formatCreateErrorDetails(
            parsed,
            `${tr("ui.configFlow.pleaseFix")}\n${fieldMessages}`,
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
              ? `${parsed.message}\n\n${tr("ui.configFlow.stackkitsValidation")}\n${validateError}`
              : parsed.message,
          );
        }
      } else if (parsed.status >= 500) {
        const details = createErrorDetails(parsed.details);
        deployError = details.phaseLabel
          ? tr("ui.configFlow.createFailedAt", { phase: details.phaseLabel })
          : tr("ui.configurationFlow.serverError");
        deployErrorDetails = formatCreateErrorDetails(
          parsed,
          tr("ui.configFlow.serverEncountered", {
            status: parsed.status,
            message: parsed.message,
          }),
        );
      } else {
        // Fallback for other errors
        deployError =
          error instanceof Error ? error.message : tr("ui.configurationFlow.deploymentFailed");
        deployErrorDetails =
          parsed.message !== deployError
            ? parsed.message
            : tr("ui.configurationFlow.anUnexpectedErrorOccurredPlease");
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
                {tr("ui.configFlow.defaultCredentials")}
              </p>
              <div class="font-mono text-xs space-y-1">
                <p class="text-primary">admin@techstack.local</p>
                <p class="text-muted-foreground">
                  {tr("ui.configurationFlow.useTheAdminPasswordConfigured")}
                </p>
              </div>
              <a
                href="/login"
                data-kx="control"
                data-variant="primary"
                class="mt-3 inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
              >
                {tr("ui.configFlow.continueSetup")}
              </a>
            </div>
          {/if}
          <button
            onclick={clearCreateError}
            class="mt-2 text-sm {showLoginHint
              ? 'text-warning hover:text-warning/80'
              : 'text-destructive hover:text-destructive/80'} underline"
          >
            {tr("ui.common.dismiss")}
          </button>
          {#if conflictStackId}
            <button
              type="button"
              onclick={openConflictStack}
              class="ml-3 mt-2 inline-flex items-center gap-1.5 rounded-md border border-destructive/40 px-3 py-1.5 text-sm font-medium text-destructive hover:bg-destructive/10"
            >
              <ExternalLink class="h-4 w-4" aria-hidden="true" />
              {tr("ui.configFlow.openExisting")}
            </button>
          {/if}
        </div>
      </div>
    </div>
  {/if}

  {#if selfDisclosureState !== "none" && selfDisclosureState !== "pending"}
    <p
      class="mb-4 rounded-lg border px-4 py-3 text-sm {selfDisclosureState ===
      'failed'
        ? 'border-warning/30 bg-warning/10 text-foreground'
        : 'border-border bg-muted/40 text-muted-foreground'}"
      role="status"
      data-testid="self-disclosure-status"
      data-state={selfDisclosureState}
    >
      {tr(`wizard.selfDisclosure.${selfDisclosureState}`)}
    </p>
  {/if}

  <!-- Wizard Content -->
  {#if !holdWizardForSelfDisclosure}
    <EasyWizard
      oncreate={handleCreate}
      {isDeploying}
      initialConfig={wizardPrefill}
    />
  {/if}
</div>
