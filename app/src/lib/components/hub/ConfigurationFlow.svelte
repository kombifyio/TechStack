<script lang="ts">
  import { tick, untrack } from "svelte";
  import { goto as svelteGoto, replaceState } from "$app/navigation";
  import { page } from "$app/state";
  import { getClientBootstrap } from "#lib/client/bootstrap.js";
  import { API_BASE } from "#lib/api/client.js";
  import { parseApiError, type ParsedApiError } from "#lib/api/errors.js";
  import { appCommit, appVersion } from "#lib/config.js";
  import { authHandler } from "#lib/stores/authHandler.svelte.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import SessionRenewalPanel from "#lib/components/SessionRenewalPanel.svelte";
  import { loadFeatures } from "#lib/stores/features.js";
  import { getHomelab } from "#lib/api/homelab.js";
  import { storedBlockedWizardConfig } from "#lib/wizard/stored-decisions.js";
  import { WizardInputError } from "#lib/wizard/wizardRunRequest.js";
  import {
    CREATION_PROCESSES,
    withServerChoice,
  } from "#lib/creation/core/index.js";
  import { listCanonicalServers } from "#lib/api/registry.js";
  import { listKitDeployments } from "#lib/api/stacks.js";
  import {
    homelabServerChoices,
    type HomelabServerChoice,
    type HomelabServerSelection,
  } from "#lib/creation/homelab-servers.js";
  import { AddUseCasesRun } from "#lib/creation/add-use-cases.svelte.js";
  import { clearWizardDraft } from "#lib/wizard/draft-storage.js";
  import type { LayoutRecommendationAcceptance } from "#lib/api/layout.js";
  import UseCasesAddedResult from "./UseCasesAddedResult.svelte";
  import {
    creationScreenHref,
    forgetCreationAttempt,
    submitCreationRun,
  } from "#lib/creation/submit.js";
  import { refreshEmbeddedCloudSession } from "#lib/auth/embedded-session.js";
  import { CircleAlert, ExternalLink, Lock } from "@lucide/svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import { CreationPageFrame } from "#lib/components/wizard/index.js";
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
  import SelfDisclosureReview from "./SelfDisclosureReview.svelte";

  interface Props {
    /** Remove page-owned spacing/title so the host can mount the full flow. */
    embedded?: boolean;
    /** Host-owned navigation keeps the flow usable in a panel or modal shell. */
    onNavigate?: (href: string) => void | Promise<void>;
    returnTo?: string;
    /** Ephemeral creation intent selected by the caller, without profile storage. */
    initialConfig?: StackConfig;
    /** Already enrolled canonical server selected for its first StackKit. */
    existingServerId?: string;
    /** Voluntary answers a link handed over (the kombify Cloud capture surface). */
    selfDisclosure?: OperatorSelfDisclosure | null;
    /** Called once the handed-over answers were stored, discarded or given up on. */
    onSelfDisclosureSettled?: () => void;
    /**
     * Offer the person's existing servers on the Server step. A server that
     * runs a StackKit takes the use cases through it; one without gets its
     * first StackKit; otherwise a new server is set up.
     */
    chooseServer?: boolean;
    /** The server the person started from, chosen first when it can be used. */
    preferredServerId?: string;
  }

  let {
    embedded = false,
    onNavigate,
    returnTo,
    initialConfig,
    existingServerId,
    selfDisclosure = null,
    onSelfDisclosureSettled,
    chooseServer = false,
    preferredServerId = "",
  }: Props = $props();

  // -- the person's existing servers, chosen on the Server step ---------------
  // The choice is part of the address (`?target=server:<id>`, or
  // `?target=new` for a new server), so a reload keeps it; it is replaced, not
  // pushed, since the step owns the history.
  const TARGET_PREFIX = "server:";
  const currentUrl = $derived(page.shallow?.url ?? page.url);
  let homelabServers = $state<HomelabServerChoice[]>([]);
  let deploymentNames = $state(new Map<string, string>());
  let homelabLoading = $state(untrack(() => chooseServer));
  let homelabFailed = $state(false);
  let homelabNotice = $state("");
  let homelabRequested = false;
  const newServerChosen = untrack(
    () => page.url.searchParams.get("target") === "new",
  );
  let selectedServerId = $state(
    untrack(() => {
      const target = page.url.searchParams.get("target") ?? "";
      return target.startsWith(TARGET_PREFIX)
        ? target.slice(TARGET_PREFIX.length)
        : "";
    }),
  );
  const chosenServer = $derived(
    selectedServerId
      ? homelabServers.find((server) => server.id === selectedServerId)
      : undefined,
  );
  const chosenDeployment = $derived(chosenServer?.deploymentId);
  // One homelab per person: once they have a server, a new one adds use cases
  // to it rather than creating a homelab.
  const addsToHomelab = $derived(
    Boolean(chosenDeployment) || homelabServers.length > 0,
  );

  function selectServer(id: string) {
    selectedServerId = id;
    const url = new URL(currentUrl.href);
    url.searchParams.set("target", id ? `${TARGET_PREFIX}${id}` : "new");
    if (url.href !== currentUrl.href)
      replaceState(`${url.pathname}${url.search}${url.hash}`, page.state);
  }

  async function loadHomelabServers() {
    homelabLoading = true;
    homelabFailed = false;
    try {
      const [deployments, servers] = await Promise.all([
        listKitDeployments(),
        listCanonicalServers(),
      ]);
      const stackkits = new Map(
        deployments.map((deployment) => [
          deployment.id,
          deployment.stackkit_id ?? "",
        ]),
      );
      deploymentNames = new Map(
        deployments.map((deployment) => [deployment.id, deployment.name]),
      );
      homelabServers = homelabServerChoices(
        servers,
        (id) => stackkits.get(id) || undefined,
      );
      const usable = (id: string) =>
        homelabServers.some(
          (server) => server.id === id && server.state === "ready",
        );
      // The server the person started from and cannot be used is named, not
      // silently replaced by another.
      const preferred = homelabServers.find(
        (server) => server.id === preferredServerId,
      );
      homelabNotice =
        !preferredServerId || usable(preferredServerId)
          ? ""
          : preferred
            ? tr("wizard.server.homelab.preferredUnavailable", {
                name: preferred.name,
              })
            : tr("wizard.server.homelab.preferredMissing");
      // Existing servers come first: the one the person started from, or
      // else the first that can take use cases. A new server stays one click
      // away on the same step.
      if (!usable(selectedServerId) && !newServerChosen)
        selectServer(
          usable(preferredServerId)
            ? preferredServerId
            : (homelabServers.find((server) => server.state === "ready")?.id ??
                ""),
        );
    } catch {
      // A new server stays possible while the servers cannot be read.
      homelabFailed = true;
      selectedServerId = "";
    } finally {
      homelabLoading = false;
    }
  }

  $effect(() => {
    if (!chooseServer || homelabRequested) return;
    if (authStore.loading || !authStore.modeDetected) return;
    homelabRequested = true;
    if (!authStore.isAuthenticated) {
      homelabLoading = false;
      selectedServerId = "";
      return;
    }
    void loadHomelabServers();
  });

  const homelab: HomelabServerSelection | undefined = untrack(() => chooseServer)
    ? {
        get servers() {
          return homelabServers;
        },
        get loading() {
          return homelabLoading;
        },
        get failed() {
          return homelabFailed;
        },
        get notice() {
          return homelabNotice;
        },
        get selected() {
          return chosenServer ? selectedServerId : "";
        },
        select: selectServer,
        retry: () => void loadHomelabServers(),
      }
    : undefined;

  // Use cases for a server that runs a StackKit go through that StackKit.
  const addRun = new AddUseCasesRun();
  $effect(() => {
    if (addRun.result) clearWizardDraft("stack-create");
  });

  // A link only prefills. Anyone can craft one, so handed-over answers are
  // shown for review and stored only when the operator chooses "Use these
  // answers"; "Discard" drops them. The wizard mounts after that choice, so a
  // confirmed profile already shapes its first recommendation. A failed write
  // never blocks creation: the wizard then opens without the stored profile.
  const handedOver = untrack(() => selfDisclosure);
  let selfDisclosureState = $state<
    "none" | "review" | "applying" | "applied" | "failed" | "discarded"
  >(handedOver ? "review" : "none");
  let savedConfig = $state<StackConfig>();
  let savedDecisionsLoading = $state(true);
  let savedDecisionsError = $state(false);
  let savedDecisionsRequested = $state(false);
  $effect(() => {
    if (authStore.loading || !authStore.modeDetected || savedDecisionsRequested)
      return;
    savedDecisionsRequested = true;
    if (initialConfig || handedOver || !authStore.isAuthenticated) {
      savedDecisionsLoading = false;
      return;
    }
    void getHomelab()
      .then((view) => {
        savedConfig = storedBlockedWizardConfig(view?.homelab);
        savedDecisionsLoading = false;
      })
      .catch(() => {
        savedDecisionsError = true;
        savedDecisionsLoading = false;
      });
  });
  function retrySavedDecisions() {
    savedDecisionsRequested = false;
    savedDecisionsError = false;
    savedDecisionsLoading = true;
  }
  const wizardPrefill = $derived.by(() => {
    if (!handedOver || selfDisclosureState === "discarded")
      return initialConfig ?? savedConfig;
    const prefill = selfDisclosureWizardConfig(handedOver);
    // A profile import must not replace an explicit creation selection.
    return initialConfig
      ? { ...prefill, ...initialConfig, household: prefill.household }
      : prefill;
  });

  // Only a signed-in operator has a profile to store into. A signed-out
  // visitor gets the prefilled wizard at once; the sign-in redirect keeps the
  // URL, so the review appears on the way back.
  const reviewSelfDisclosure = $derived(
    selfDisclosureState === "review" &&
      !authStore.loading &&
      authStore.modeDetected &&
      authStore.isAuthenticated,
  );
  const holdWizardForSelfDisclosure = $derived(
    selfDisclosureState === "applying" ||
      (selfDisclosureState === "review" &&
        (authStore.loading ||
          !authStore.modeDetected ||
          authStore.isAuthenticated)),
  );

  function settleSelfDisclosure(state: "applied" | "failed" | "discarded") {
    selfDisclosureState = state;
    onSelfDisclosureSettled?.();
  }

  function useHandedOverSelfDisclosure(notes: string) {
    if (selfDisclosureState !== "review" || !handedOver) return;
    selfDisclosureState = "applying";
    const typedNotes = notes.trim();
    void putOperatorSelfDisclosure(
      typedNotes ? { ...handedOver, notes: typedNotes } : handedOver,
      "link",
    ).then(
      () => settleSelfDisclosure("applied"),
      () => settleSelfDisclosure("failed"),
    );
  }

  function discardHandedOverSelfDisclosure() {
    if (selfDisclosureState !== "review") return;
    settleSelfDisclosure("discarded");
  }

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

  // A failed creation is told in three layers: a headline and an explanation
  // in plain language, what the person can do next, and, collapsed, the raw
  // codes and diagnostics that support needs.
  let deployError = $state<string | null>(null);
  let deployErrorMessage = $state<string | null>(null);
  let deployErrorSteps = $state<string[]>([]);
  let deployErrorTechnical = $state<string | null>(null);
  let deployErrorRetryable = $state(false);
  let deployErrorHandover = $state<ErrorAiHandoverContext | null>(null);
  let conflictStackId = $state<string | null>(null);
  let conflictStackName = $state<string | null>(null);
  let showLoginHint = $state(false);
  let lastAttempt: StackConfig | null = null;
  let errorRegion = $state<HTMLDivElement>();
  let renewalRegion = $state<HTMLDivElement>();

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
      unfulfilledGoals: stringArrayField(record, "unfulfilled_goals"),
      stackId: stringField(record, "stack_id"),
      requestId: stringField(record, "request_id"),
      retryable:
        typeof record?.retryable === "boolean" ? record.retryable : undefined,
    };
  }

  /** What the server told the person to do next, as a short list. */
  function nextStepsOf(details: ReturnType<typeof createErrorDetails>) {
    if (details.nextSteps.length > 0) return details.nextSteps;
    return details.remediation ? [details.remediation] : [];
  }

  /**
   * The raw codes and diagnostics for the collapsed technical details, one per
   * line. `extra` leads (the backend's own wording, a field list).
   */
  function technicalLines(
    parsed: ParsedApiError,
    ...extra: (string | null | undefined)[]
  ): string[] {
    const details = createErrorDetails(parsed.details);
    const lines = extra.flatMap((line) => (line?.trim() ? [line.trim()] : []));
    if (details.phaseLabel) {
      lines.push(tr("ui.configFlow.step", { value: details.phaseLabel }));
    }
    if (details.errorCode) {
      lines.push(tr("ui.configFlow.errorCode", { value: details.errorCode }));
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
    if (parsed.status > 0) lines.push(`HTTP ${parsed.status}`);
    return lines;
  }

  function currentRoute() {
    if (typeof window === "undefined") return "/stacks/new";
    return `${window.location.pathname}${window.location.search}`;
  }

  function buildCreateErrorHandover(
    parsed: ParsedApiError,
    attemptedStackName: string,
  ) {
    if (!deployError || showLoginHint) return null;

    const details = createErrorDetails(parsed.details);
    return createErrorAiHandoverContext({
      surface: "stacks.create",
      title: deployError,
      userMessage: [deployErrorMessage, deployErrorTechnical]
        .filter(Boolean)
        .join("\n"),
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
    deployErrorMessage = null;
    deployErrorSteps = [];
    deployErrorTechnical = null;
    deployErrorRetryable = false;
    deployErrorHandover = null;
    conflictStackId = null;
    conflictStackName = null;
    showLoginHint = false;
  }

  function showCreateError(failure: {
    title: string;
    message?: string | null;
    steps?: string[];
    technical?: string[];
    retryable?: boolean;
  }) {
    deployError = failure.title;
    deployErrorMessage = failure.message?.trim() || null;
    deployErrorSteps = failure.steps ?? [];
    deployErrorTechnical = failure.technical?.length
      ? failure.technical.join("\n")
      : null;
    deployErrorRetryable = failure.retryable === true;
  }

  // The error appears where the person pressed "Create", not at the top of
  // the page: bring it into view and hand it the focus so it is also heard.
  async function revealCreateError() {
    await tick();
    const region = errorRegion ?? renewalRegion;
    region?.focus({ preventScroll: true });
    region?.scrollIntoView({
      block: "center",
      behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches
        ? "auto"
        : "smooth",
    });
  }

  function retryCreate() {
    if (lastAttempt && !isDeploying) void handleCreate(lastAttempt);
  }

  const process = $derived(
    chosenDeployment
      ? withServerChoice(CREATION_PROCESSES["add-use-case"])
      : chosenServer
        ? withServerChoice(CREATION_PROCESSES["place-on-server"])
        : existingServerId
          ? CREATION_PROCESSES["place-on-server"]
          : CREATION_PROCESSES.stack,
  );
  const targetServerId = $derived(
    chosenServer && !chosenDeployment ? chosenServer.id : existingServerId,
  );

  function openConflictStack() {
    if (conflictStackId) {
      void navigate(`/stacks/${encodeURIComponent(conflictStackId)}`);
    }
  }

  // The Wizard run is the single creation authority: projection, pinned
  // StackKits validation, persistence, and dispatch happen server-side. A
  // A blocked run may save desired decisions without dispatching a runtime.
  async function createViaWizardRun(config: StackConfig) {
    validationStage = tr("ui.configurationFlow.validatingWithStackkits");
    const { run, request } = await submitCreationRun(process, config, {
      existingServerId: targetServerId,
    });
    captureStackCreationEvent(
      "techstack:stack_created",
      stackCreationAnalyticsInput(config, {
        stackId: run.kit_deployment_id,
        jobId: run.job_id,
        serverId: run.server_id,
      }),
    );

    await navigate(
      creationScreenHref(process, run, run.name || request.intent.name),
    );
  }

  async function handleCreate(
    config: StackConfig,
    extras?: { layoutRecommendation?: LayoutRecommendationAcceptance },
  ) {
    if (chosenDeployment) {
      clearCreateError();
      await addRun.submit(
        {
          stackId: chosenDeployment,
          stackName: deploymentNames.get(chosenDeployment) ?? "",
        },
        config,
        extras?.layoutRecommendation,
      );
      return;
    }
    isDeploying = true;
    sessionRenewalRequired = false;
    clearCreateError();
    lastAttempt = config;
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

      if (error instanceof WizardInputError) {
        // An answer that cannot become a run; its text is already for people.
        showCreateError({
          title: tr("wizard.error.input.title"),
          message: error.message,
        });
      } else if (isNetworkError) {
        const apiBase = API_BASE;
        showCreateError({
          title: tr("ui.configurationFlow.cannotConnectToBackend"),
          message: tr("wizard.error.network.body"),
          technical: [
            apiBase
              ? tr("ui.configFlow.apiUnreachableAt", { base: apiBase })
              : tr("ui.configFlow.apiUnreachable"),
            tr("ui.configFlow.technicalDetails", {
              value: error instanceof Error ? error.message : String(error),
            }),
          ],
          retryable: true,
        });
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
          void revealCreateError();
        }
        return; // Don't show error UI, auth handler manages the flow
      } else if (!authStore.isAuthenticated) {
        showLoginHint = true;
        showCreateError({
          title: tr("ui.configurationFlow.authenticationRequired"),
          message: tr("ui.configurationFlow.youMustBeLoggedIn2"),
        });
      } else if (parsed.isForbidden) {
        const details = createErrorDetails(parsed.details);
        if (details.errorCode === "managed_runtime_feature_disabled") {
          showCreateError({
            title:
              details.guidanceTitle ||
              tr("ui.configurationFlow.managedServerIsNotActive"),
            message: details.guidanceBody ?? undefined,
            steps: nextStepsOf(details),
            technical: technicalLines(parsed, parsed.message),
          });
        } else if (details.guidanceTitle || details.guidanceBody) {
          // Structured denial with user_guidance (e.g. owner_bootstrap_denied):
          // lead with the actionable guidance instead of a generic headline.
          showCreateError({
            title:
              details.guidanceTitle ||
              tr("ui.configurationFlow.permissionDenied"),
            message: details.guidanceBody ?? undefined,
            steps: nextStepsOf(details),
            technical: technicalLines(parsed, parsed.message),
          });
        } else {
          // The backend's own wording and every code stay available, collapsed,
          // to diagnose which gate denied the request (ENTITLEMENTS-
          // ARCHITECTURE.md §13); the explanation itself is in plain language.
          showCreateError({
            title: tr("ui.configurationFlow.permissionDenied"),
            message: tr("ui.configurationFlow.youDonTHavePermission"),
            steps: nextStepsOf(details),
            technical: technicalLines(parsed, parsed.message),
          });
        }
      } else if (parsed.status === 409 || (error as any)?.status === 409) {
        const conflictDetails = createErrorDetails(parsed.details);
        if (
          conflictDetails.reasonCode === "wizard_desired_projection_blocked"
        ) {
          // The owner decisions are durably saved. A later edited decision is
          // a new request, while this saved request keeps its original key.
          forgetCreationAttempt(process, {});
          conflictStackId = null;
          conflictStackName = null;
          showCreateError({
            title:
              conflictDetails.guidanceTitle || tr("wizard.error.blocked.title"),
            message:
              conflictDetails.guidanceBody ?? tr("wizard.error.blocked.body"),
            steps: nextStepsOf(conflictDetails),
            technical: technicalLines(parsed, parsed.message),
          });
        } else if (
          conflictDetails.reasonCode === "wizard_idempotency_conflict" ||
          conflictDetails.reasonCode === "idempotency_conflict"
        ) {
          // The session's attempt key already completed a DIFFERENT run.
          // Discard it so the next submit mints a fresh key instead of
          // dead-ending every retry on the same 409.
          forgetCreationAttempt(process, {});
          conflictStackId = conflictDetails.stackId || null;
          conflictStackName = null;
          showCreateError({
            title: tr(
              "ui.configurationFlow.aPreviousSubmissionAlreadyCompleted",
            ),
            message: tr("ui.configurationFlow.anEarlierSubmissionFromThis"),
            technical: technicalLines(parsed, parsed.message),
            retryable: true,
          });
        } else {
          const conflict = extractConflictDetails(error, parsed.details);
          conflictStackId = conflict.stackId;
          conflictStackName = conflict.name || attemptedStackName;
          showCreateError({
            title: tr(
              "ui.configurationFlow.stackkitDeploymentNameAlreadyExists",
            ),
            message: conflictStackName
              ? tr("ui.configFlow.deploymentNameExists", {
                  name: conflictStackName,
                })
              : tr("ui.configurationFlow.theBackendRejectedThisStackkit"),
            technical: technicalLines(parsed, parsed.message),
          });
        }
      } else if (parsed.isValidationError) {
        const validationDetails = createErrorDetails(parsed.details);
        // Field names and the release's own refusal are backend vocabulary:
        // they help support, so they sit in the technical details.
        const fieldMessages = Object.entries(parsed.fieldErrors).map(
          ([field, { message }]) => `${field}: ${message}`,
        );
        const validateError =
          stringField(recordFrom(parsed.details), "validate_error") ||
          stringField(
            recordFrom(recordFrom(parsed.details)?.details),
            "validate_error",
          );
        // Keep the configured goals and attempt key for a corrected submit.
        // The release's refusal is never a successful node-only creation.
        showCreateError({
          title:
            validationDetails.guidanceTitle ||
            tr("wizard.error.validation.title"),
          message:
            validationDetails.guidanceBody ??
            tr("wizard.error.validation.body"),
          steps: nextStepsOf(validationDetails),
          technical: technicalLines(
            parsed,
            parsed.message,
            ...fieldMessages,
            validationDetails.unfulfilledGoals.length > 0
              ? `${tr("ui.configFlow.pleaseFix")} ${validationDetails.unfulfilledGoals.join(", ")}`
              : "",
            validateError
              ? `${tr("ui.configFlow.stackkitsValidation")} ${validateError}`
              : "",
          ),
        });
      } else if (parsed.status >= 500) {
        const details = createErrorDetails(parsed.details);
        showCreateError({
          title: details.phaseLabel
            ? tr("ui.configFlow.createFailedAt", { phase: details.phaseLabel })
            : tr("ui.configurationFlow.serverError"),
          message:
            details.retryable === false
              ? tr("ui.configurationFlow.retryingIsUnlikelyToHelp")
              : tr("ui.configurationFlow.youCanRetryIfThis"),
          steps: nextStepsOf(details),
          technical: technicalLines(
            parsed,
            tr("ui.configFlow.serverEncountered", {
              status: parsed.status,
              message: parsed.message,
            }),
          ),
          retryable: details.retryable !== false,
        });
      } else {
        // Anything else is unexpected: say so plainly, keep the original text
        // for support.
        showCreateError({
          title: tr("ui.configurationFlow.deploymentFailed"),
          message: tr("ui.configurationFlow.anUnexpectedErrorOccurredPlease"),
          technical: technicalLines(parsed, parsed.message),
          retryable: true,
        });
      }

      deployErrorHandover = buildCreateErrorHandover(
        parsed,
        attemptedStackName,
      );
      isDeploying = false;
      void revealCreateError();
    }
  }
</script>

<div
  class={embedded
    ? "w-full min-w-0 overflow-x-hidden p-6"
    : "w-full min-w-0 overflow-x-hidden px-3 py-6 sm:px-6 lg:p-8"}
  data-flow="configuration"
  data-presentation={embedded ? "embedded" : "page"}
>
  {#if isHydrated}
    <span class="hidden" aria-hidden="true" data-testid="hydrated"></span>
  {/if}

  {#if reviewSelfDisclosure && handedOver}
    <SelfDisclosureReview
      disclosure={handedOver}
      onuse={useHandedOverSelfDisclosure}
      ondiscard={discardHandedOverSelfDisclosure}
    />
  {/if}

  {#if selfDisclosureState === "applying" || selfDisclosureState === "applied" || selfDisclosureState === "failed"}
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
  {#if savedDecisionsLoading}
    <LoadingNote label={tr("common.loading")} />
  {:else if savedDecisionsError}
    <ErrorCallout testId="homelab-load-error">
      <div
        class="flex min-w-0 flex-1 flex-wrap items-center justify-between gap-3"
      >
        <p class="text-foreground">{tr("wizard.settings.loadError")}</p>
        <Button variant="secondary" onclick={retrySavedDecisions}>
          {tr("common.retry")}
        </Button>
      </div>
    </ErrorCallout>
  {:else if addRun.result && chosenDeployment}
    <UseCasesAddedResult
      result={addRun.result}
      stackId={chosenDeployment}
      progressHref={addRun.progressHref(
        chosenDeployment,
        deploymentNames.get(chosenDeployment) ?? "",
      )}
      {embedded}
    />
  {:else if !holdWizardForSelfDisclosure}
    <CreationPageFrame
      {process}
      oncreate={handleCreate}
      isDeploying={isDeploying || addRun.submitting}
      initialConfig={wizardPrefill}
      draftKey={existingServerId
        ? `stack-install-${existingServerId}`
        : "stack-create"}
      {homelab}
      layoutStackId={chosenDeployment}
      layoutReplacement={chosenDeployment ? addRun.replacement : undefined}
      layoutRefresh={addRun.refresh}
      submitLabel={addsToHomelab
        ? tr("layout.addUseCases.submit")
        : undefined}
      submittingLabel={addsToHomelab
        ? tr("layout.addUseCases.submitting")
        : undefined}
    >
      {#snippet footerNotice()}
        {#if addRun.notice && chosenDeployment}
          <ErrorCallout
            tone={addRun.notice.tone}
            title={addRun.notice.title}
            message={addRun.notice.body}
            details={addRun.notice.details ?? null}
            testId="add-use-cases-notice"
          />
        {/if}
        {#if isDeploying && validationStage}
          <div
            class="flex items-center gap-3 rounded-lg border border-primary/30 bg-primary/10 p-4"
            role="status"
            data-testid="validation-progress"
          >
            <svg
              class="h-5 w-5 shrink-0 animate-spin text-primary"
              fill="none"
              viewBox="0 0 24 24"
              aria-hidden="true"
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
            <p class="font-medium text-foreground">{validationStage}</p>
          </div>
        {/if}

        {#if sessionRenewalRequired}
          <div
            bind:this={renewalRegion}
            tabindex="-1"
            class="scroll-mt-6 rounded-xl outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <SessionRenewalPanel returnTo={returnTo ?? currentRoute()} />
          </div>
        {/if}

        {#if deployError}
          <div
            bind:this={errorRegion}
            tabindex="-1"
            class="scroll-mt-6 rounded-xl outline-none focus-visible:ring-2 focus-visible:ring-ring"
            data-testid="deploy-error-region"
          >
            <ErrorCallout
              tone={showLoginHint ? "warning" : "error"}
              testId="deploy-error"
              aiHandoverContext={deployErrorHandover}
              onRetry={deployErrorRetryable ? retryCreate : null}
              retryLabel={tr("common.retry")}
              retrying={isDeploying}
            >
              {#if showLoginHint}
                <Lock class="mt-0.5 size-4 shrink-0" aria-hidden="true" />
              {:else}
                <CircleAlert
                  class="mt-0.5 size-4 shrink-0"
                  aria-hidden="true"
                />
              {/if}
              <div class="min-w-0 flex-1 space-y-2">
                <p class="font-semibold text-foreground">{deployError}</p>
                {#if deployErrorMessage}
                  <p
                    class="whitespace-pre-line break-words text-sm text-foreground/90"
                  >
                    {deployErrorMessage}
                  </p>
                {/if}
                {#if deployErrorSteps.length > 0}
                  <div class="text-sm text-foreground/90">
                    <p class="font-medium">{tr("ui.configFlow.nextSteps")}</p>
                    <ul class="mt-1 list-disc space-y-0.5 ps-5">
                      {#each deployErrorSteps as step (step)}
                        <li>{step}</li>
                      {/each}
                    </ul>
                  </div>
                {/if}
                {#if showLoginHint}
                  <div class="rounded-lg border border-border bg-muted/50 p-3">
                    <p class="mb-2 text-sm text-muted-foreground">
                      {tr("ui.configFlow.defaultCredentials")}
                    </p>
                    <div class="space-y-1 font-mono text-xs">
                      <p class="text-primary">admin@techstack.local</p>
                      <p class="text-muted-foreground">
                        {tr(
                          "ui.configurationFlow.useTheAdminPasswordConfigured",
                        )}
                      </p>
                    </div>
                    <a
                      href="/login"
                      data-kx="control"
                      data-variant="primary"
                      class="mt-3 inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                    >
                      {tr("ui.configFlow.continueSetup")}
                    </a>
                  </div>
                {/if}
                {#if deployErrorTechnical}
                  <details
                    class="text-xs text-muted-foreground"
                    data-testid="deploy-error-details"
                  >
                    <summary class="cursor-pointer select-none">
                      {tr("wizard.error.technicalDetails")}
                    </summary>
                    <p
                      class="mt-1 whitespace-pre-line break-words font-mono text-foreground/80"
                    >
                      {deployErrorTechnical}
                    </p>
                  </details>
                {/if}
                <div class="flex flex-wrap items-center gap-3">
                  {#if conflictStackId}
                    <Button variant="secondary" onclick={openConflictStack}>
                      <ExternalLink class="h-4 w-4" aria-hidden="true" />
                      {tr("ui.configFlow.openExisting")}
                    </Button>
                  {/if}
                  <button
                    type="button"
                    class="text-sm text-muted-foreground underline underline-offset-2 hover:text-foreground"
                    onclick={clearCreateError}
                  >
                    {tr("ui.common.dismiss")}
                  </button>
                </div>
              </div>
            </ErrorCallout>
          </div>
        {/if}
      {/snippet}
    </CreationPageFrame>
  {/if}
</div>
