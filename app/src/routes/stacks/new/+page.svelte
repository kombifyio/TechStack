<script lang="ts">
  import { goto } from "$app/navigation";
  import { getClientBootstrap } from "$lib/client/bootstrap";
  import { API_BASE } from "$lib/api/client";
  import { parseApiError, type ParsedApiError } from "$lib/api/errors";
  import type { StackMode } from "$lib/stores/stacks";
  import { createStack } from "$lib/api/stacks";
  import {
    preflightPipeline,
    type PipelinePreviewResult,
  } from "$lib/api/unifier";
  import { appCommit, appVersion } from "$lib/config";
  import { authHandler } from "$lib/stores/authHandler.svelte";
  import { authStore } from "$lib/stores/auth.svelte";
  import SessionRenewalPanel from "$lib/components/SessionRenewalPanel.svelte";
  import { loadFeatures, isNativeV2WizardEnabled } from "$lib/stores/features";
  import { createWizardRun } from "$lib/api/wizardRuns";
  import { buildFoundRunRequest } from "$lib/wizard/wizardRunRequest";
  import { refreshEmbeddedCloudSession } from "$lib/auth/embedded-session";
  import { ExternalLink } from "@lucide/svelte";
  import ErrorAiHandoverButton from "$lib/components/support/ErrorAiHandoverButton.svelte";
  import { EasyWizard, TechieWizard } from "$lib/components/wizard";
  import {
    createErrorAiHandoverContext,
    type ErrorAiHandoverContext,
  } from "$lib/support/error-handover";
  import {
    type StackConfig,
    buildPayload,
    buildStackKitSpecFromStackConfig,
  } from "$lib/wizard";
  import {
    buildTechstackCreationEventProperties,
    createPostHogClient,
    toTechstackAnalyticsUser,
    type TechstackCreationEventInput,
  } from "$lib/analytics/posthog";
  import { tr } from "$lib/i18n.svelte";

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

  let activeTab: "easy" | "techie" = $state("easy");
  let isDeploying = $state(false);
  let sessionRenewalRequired = $state(false);
  let lastCreateConfig = $state<StackConfig | null>(null);
  let deployError = $state<string | null>(null);
  let deployErrorDetails = $state<string | null>(null);
  let deployErrorHandover = $state<ErrorAiHandoverContext | null>(null);
  let conflictStackId = $state<string | null>(null);
  let conflictStackName = $state<string | null>(null);
  let showLoginHint = $state(false);

  // Pipeline preview state
  let pipelineResult = $state<PipelinePreviewResult | null>(null);
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
      nextSteps: stringArrayField(userGuidance, "next_steps"),
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
      goto(`/stacks/${encodeURIComponent(conflictStackId)}`);
    }
  }

  // Serializes the wizard answers for the creating page's resume rendering
  // (never includes the recovery hash).
  function buildStoredCreationConfig(config: StackConfig): string {
    return JSON.stringify({
      provider: config.provider,
      serverMode: config.serverMode,
      runtimeOfferingId: config.runtimeOfferingId,
      providerId: config.providerId,
      serverProvisioning: config.serverProvisioning,
      network: config.network,
      goals: config.goals,
      services: config.services,
      owner: {
        bootstrapMode: config.owner.bootstrapMode,
        source: config.owner.source,
        username: config.owner.username,
        email: config.owner.email,
        displayName: config.owner.displayName,
        recoveryMaterialRef: config.owner.recoveryMaterialRef,
      },
      auth: {
        requirePassword: config.auth.requirePassword,
        allowPasswordless: config.auth.allowPasswordless,
        requireMfa: config.auth.requireMfa,
      },
    });
  }

  // Native v2 lane (flag native_v2_wizard): one wizard-run request executes
  // projection + pinned StackKits validation + persistence server-side; a
  // rejected run is a structured error and leaves no state behind. Lands
  // directly on the progress page (plan D6).
  async function createViaWizardRun(config: StackConfig) {
    validationStage = "Validating with StackKits...";
    const request = buildFoundRunRequest(config);
    const idempotencyKey = creationIdempotencyKey();
    const run = await createWizardRun(request, idempotencyKey);
    captureStackCreationEvent(
      "techstack:stack_created",
      stackCreationAnalyticsInput(config, {
        stackId: run.stack_id,
        jobId: run.job_id,
        serverId: run.server_id,
      }),
    );

    const persistedName = run.name || request.intent.name;
    const storedCreationConfig = buildStoredCreationConfig(config);
    const jobId = run.job_id || "";
    sessionStorage.setItem("creatingOperation", "stack");
    sessionStorage.setItem("creatingStackName", persistedName);
    sessionStorage.setItem("creatingStackId", run.stack_id);
    sessionStorage.setItem("creatingStackConfig", storedCreationConfig);
    if (jobId) {
      sessionStorage.setItem("creatingJobId", jobId);
      sessionStorage.setItem(`creating:${jobId}:stackName`, persistedName);
      sessionStorage.setItem(`creating:${jobId}:stackId`, run.stack_id);
      sessionStorage.setItem(`creating:${jobId}:config`, storedCreationConfig);
      sessionStorage.setItem(
        `creating:${jobId}:idempotencyKey`,
        idempotencyKey,
      );
      sessionStorage.setItem(`creating:${jobId}:runId`, run.run_id);
      if (run.pairing_job_id) {
        sessionStorage.setItem(
          `creating:${jobId}:pairingJobId`,
          run.pairing_job_id,
        );
      }
    }
    sessionStorage.removeItem(creationIdempotencyStorageKey);

    const params = new URLSearchParams();
    params.set("stack_id", run.stack_id);
    if (jobId) params.set("job_id", jobId);
    params.set("name", persistedName);
    if (run.pairing_job_id) params.set("pairing_job_id", run.pairing_job_id);
    goto(`/stacks/creating?${params.toString()}`);
  }

  async function handleCreate(config: StackConfig) {
    isDeploying = true;
    lastCreateConfig = config;
    sessionRenewalRequired = false;
    clearCreateError();
    pipelineResult = null;
    validationStage = "Validating configuration...";
    let attemptedStackName = config.name;

    try {
      const payload = buildPayload(config);
      const stackSpec = buildStackKitSpecFromStackConfig(config);
      attemptedStackName = stackSpec.name || config.name;
      captureStackCreationEvent(
        "techstack:stack_create_started",
        stackCreationAnalyticsInput(config),
      );

      // Check authentication FIRST
      if (!authStore.isAuthenticated) {
        const refreshed = await refreshEmbeddedCloudSession();
        if (!refreshed) {
          showLoginHint = true;
          throw new Error("You must be logged in to create a stack.");
        }
      }

      if ($isNativeV2WizardEnabled) {
        await createViaWizardRun(config);
        return;
      }

      // Validate payload before sending
      if (!payload.name || payload.name.trim() === "") {
        throw new Error(
          "Stack name is required. Please enter a name for your stack.",
        );
      }

      validationStage = "Running pipeline preview...";
      pipelineResult = await preflightPipeline(stackSpec);
      validationStage = `StackKit: ${pipelineResult.resolved_stackkit}`;
      captureStackCreationEvent(
        "techstack:pipeline_previewed",
        stackCreationAnalyticsInput(config, {
          pipelineValid: pipelineResult.valid === true,
          resolvedStackkit: pipelineResult.resolved_stackkit,
          detectedAddonCount: pipelineResult.detected_addons?.length ?? 0,
        }),
      );

      if (pipelineResult.detected_addons?.length > 0) {
        console.log("Detected add-ons:", pipelineResult.detected_addons);
      }

      validationStage = "Creating stack...";

      // 1. Persist raw config and start Unifier job (server-side)
      const idempotencyKey = creationIdempotencyKey();
      const createResult = await createStack(
        {
          name: stackSpec.name,
          mode: (config.wizardType || "easy") as StackMode,
          provider: payload.provider,
          services: payload.services,
          stack_spec: stackSpec as unknown as Record<string, unknown>,
          options: payload.options as Record<string, unknown>,
          user_config_format: "json",
        },
        idempotencyKey,
      );
      captureStackCreationEvent(
        "techstack:stack_created",
        stackCreationAnalyticsInput(config, {
          pipelineValid: pipelineResult.valid === true,
          resolvedStackkit: pipelineResult.resolved_stackkit,
          detectedAddonCount: pipelineResult.detected_addons?.length ?? 0,
          stackId: createResult.stack_id,
          jobId: createResult.job_id,
          serverId: createResult.server_id,
        }),
      );

      // Store for the creating page
      const storedCreationConfig = buildStoredCreationConfig(config);

      sessionStorage.setItem("creatingStackName", config.name);
      sessionStorage.setItem("creatingStackId", createResult.stack_id);
      sessionStorage.setItem("creatingStackConfig", storedCreationConfig);

      sessionStorage.setItem("creatingJobId", createResult.job_id);
      sessionStorage.setItem(
        `creating:${createResult.job_id}:stackName`,
        config.name,
      );
      sessionStorage.setItem(
        `creating:${createResult.job_id}:stackId`,
        createResult.stack_id,
      );
      sessionStorage.setItem(
        `creating:${createResult.job_id}:config`,
        storedCreationConfig,
      );
      sessionStorage.setItem(
        `creating:${createResult.job_id}:idempotencyKey`,
        idempotencyKey,
      );
      sessionStorage.removeItem(creationIdempotencyStorageKey);

      // Hand off immediately to the durable operations dashboard. It keeps the
      // stack, server intent, jobs, failures, retry, and cleanup visible even if
      // the browser leaves while provisioning continues.
      const params = new URLSearchParams();
      params.set("stack_id", createResult.stack_id);
      params.set("job_id", createResult.job_id);
      params.set("creation", "1");

      goto(`/stacks?${params.toString()}`);
    } catch (error) {
      console.error("Stack creation failed:", error);

      // Parse PocketBase error for structured feedback
      const parsed = parseApiError(error);
      const parsedDetails = createErrorDetails(parsed.details);
      captureStackCreationEvent(
        "techstack:stack_create_failed",
        stackCreationAnalyticsInput(config, {
          pipelineValid:
            pipelineResult?.valid === true
              ? true
              : pipelineResult?.valid === false
                ? false
                : undefined,
          resolvedStackkit: pipelineResult?.resolved_stackkit,
          detectedAddonCount: pipelineResult?.detected_addons?.length,
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
        const apiBase = API_BASE || "http://localhost:5261";
        deployError = "Cannot connect to backend";
        deployErrorDetails =
          `The API server at ${apiBase} is not reachable from your browser.\n\n` +
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
          "You must be logged in to create a stack. Please login first.";
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
              : "You don't have permission to create stacks, and the server did not provide a specific reason. Please share this with support.",
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
          deployError = "TechStack name already exists";
          const lead = conflictStackName
            ? `A TechStack named "${conflictStackName}" already exists for your account. Open the existing TechStack or choose a different name.`
            : parsed.message ||
              "The backend rejected this stack creation request with a conflict.";
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

<svelte:head>
  <title>{tr("wizard.title")} | kombify-TechStack</title>
</svelte:head>

<div
  class="mx-auto w-full max-w-4xl overflow-x-hidden px-3 py-6 sm:px-6 lg:p-8"
>
  {#if isHydrated}
    <span class="hidden" aria-hidden="true" data-testid="hydrated"></span>
  {/if}

  <!-- Tab Switcher -->
  <div
    class="mb-8 flex w-full max-w-md gap-2 rounded-lg bg-muted/50 p-1 sm:w-fit"
  >
    <button
      onclick={() => (activeTab = "easy")}
      class="flex-1 rounded-md px-4 py-2 text-sm font-medium transition-all sm:flex-none sm:px-6 {activeTab ===
      'easy'
        ? 'bg-card text-foreground shadow-sm'
        : 'text-muted-foreground hover:text-foreground'}"
      data-testid="tab-easy"
    >
      {tr("wizard.easy")}
    </button>
    <button
      onclick={() => (activeTab = "techie")}
      class="flex-1 rounded-md px-4 py-2 text-sm font-medium transition-all sm:flex-none sm:px-6 {activeTab ===
      'techie'
        ? 'bg-card text-foreground shadow-sm'
        : 'text-muted-foreground hover:text-foreground'}"
      data-testid="tab-techie"
    >
      {tr("wizard.techie")}
    </button>
  </div>

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
          {#if pipelineResult}
            <div class="text-sm text-muted-foreground mt-1">
              <span
                >StackKit: <strong>{pipelineResult.resolved_stackkit}</strong
                ></span
              >
              {#if pipelineResult.detected_addons?.length > 0}
                <span class="ml-3"
                  >Add-ons: {pipelineResult.detected_addons.join(", ")}</span
                >
              {/if}
            </div>

            {#if pipelineResult.valid === false && pipelineResult.errors?.length}
              <div
                class="mt-3 p-3 rounded-lg border border-amber-700/50 bg-amber-900/20"
                data-testid="pipeline-warnings"
              >
                <p class="text-sm text-amber-200 font-medium">
                  Validation blockers
                </p>
                <ul class="mt-2 text-xs text-amber-300/80 space-y-1">
                  {#each pipelineResult.errors as e}
                    <li>• {e.message}</li>
                  {/each}
                </ul>
              </div>
            {/if}
          {/if}
        </div>
      </div>
    </div>
  {/if}

  {#if sessionRenewalRequired}
    <div class="mb-6">
      <SessionRenewalPanel
        onretry={() => {
          sessionRenewalRequired = false;
          if (lastCreateConfig) handleCreate(lastCreateConfig);
        }}
        busy={isDeploying}
        returnTo="/stacks/new"
      />
    </div>
  {/if}

  {#if deployError}
    <div
      class="mb-6 p-4 rounded-lg border {showLoginHint
        ? 'border-amber-600 bg-amber-900/30'
        : 'border-red-700 bg-red-900/30'}"
      data-testid="deploy-error"
    >
      <div class="flex items-start gap-3">
        <svg
          class="w-5 h-5 shrink-0 mt-0.5 {showLoginHint
            ? 'text-amber-400'
            : 'text-red-400'}"
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
              ? 'text-amber-200'
              : 'text-red-200'}"
          >
            {deployError}
          </p>
          {#if deployErrorDetails}
            <p
              class="text-sm {showLoginHint
                ? 'text-amber-300'
                : 'text-red-300'} mt-1 whitespace-pre-line break-words"
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
              <a href="/login" class="btn btn-primary btn-sm mt-3">
                Go to Login
              </a>
            </div>
          {/if}
          <button
            onclick={clearCreateError}
            class="mt-2 text-sm {showLoginHint
              ? 'text-amber-400 hover:text-amber-300'
              : 'text-red-400 hover:text-red-300'} underline"
          >
            Dismiss
          </button>
          {#if conflictStackId}
            <button
              type="button"
              onclick={openConflictStack}
              class="ml-3 mt-2 inline-flex items-center gap-1.5 rounded-md border border-red-500/40 px-3 py-1.5 text-sm font-medium text-red-100 hover:bg-red-500/10"
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
  {#if activeTab === "easy"}
    <EasyWizard oncreate={handleCreate} {isDeploying} />
  {:else}
    <TechieWizard oncreate={handleCreate} {isDeploying} />
  {/if}
</div>
