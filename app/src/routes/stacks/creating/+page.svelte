<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { goto } from "$app/navigation";
  import { getClientBootstrap } from "$lib/client/bootstrap";
  import * as Sentry from "@sentry/sveltekit";
  import { sanitizeSentryText } from "$lib/observability/sentry-scrub";
  import { getJob, streamJobProgress } from "$lib/api/jobs";
  import { getBestPublicServerUrl } from "$lib/api/client";
  import {
    retryStackRollout,
    resumeStackEnrollment,
    type StackJobAcceptedResponse,
  } from "$lib/api/stacks";
  import {
    getActiveWizardRun,
    wizardRunNeedsAttention,
  } from "$lib/api/wizardRuns";
  import {
    legacyServerState,
    listCanonicalServers,
    serverRolloutReady,
    type CanonicalServer,
  } from "$lib/api/registry";
  import { getNetworks } from "$lib/discovery/api";
  import { getWorkerRegistryUrl } from "$lib/api/tunnel";
  import {
    belongsToCurrentConnectionAttempt,
    requiresGuardConnection,
  } from "$lib/wizard/rollout-continuation";
  import { appVersion } from "$lib/config";
  import { authStore } from "$lib/stores/auth.svelte";
  import {
    buildTechstackCreationEventProperties,
    createPostHogClient,
    toTechstackAnalyticsUser,
    type TechstackCreationEventInput,
  } from "$lib/analytics/posthog";
  import {
    type Task,
    type DeploymentRequirements,
    type ServerProvisioningMode,
    createTaskList,
    createRuntimeTaskList,
    createAddServerTaskList,
    updateTasksFromJob,
    updateTasksWithError,
    generateInstallCommand,
    generateInstallCommands,
    parseManagedRuntimeProviderError,
    STEP_DETAILS,
    RUNTIME_TASKS,
    isPostLeaseRuntimeTask,
    isStackKitArtifactOrRoutingTask,
  } from "$lib/wizard";
  import MorphingText from "$lib/components/ui/MorphingText.svelte";
  import GroupedTaskList from "$lib/components/ui/GroupedTaskList.svelte";

  type StackKitIdentityHandoff = {
    ownerUsername?: string;
    ownerEmail?: string;
    ownerDisplayName?: string;
    loginGatewayUrl?: string;
    loginGatewayLabel?: string;
    recoveryRef?: string;
    recoveryHashPresent?: boolean;
  };

  type RuntimeProofEntry = {
    action?: string;
    status?: string;
    mode?: string;
  };

  type LeaseSummary = {
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

  const runtimeTaskIds = new Set(RUNTIME_TASKS.map((task) => task.id));

  let stackName = $state("");
  let stackId = $state("");
  let jobId = $state("");
  let creationOperation = $state<"stack" | "add-server">("stack");
  let registrationToken = $state("");
  let stackKitHandoff = $state<StackKitIdentityHandoff | null>(null);
  let lease = $state<LeaseSummary | null>(null);
  let runtimePhase = $state("");
  let verificationStatus = $state("");
  let runtimeProof = $state<Record<string, RuntimeProofEntry>>({});
  let simulationPreviewStatus = $state("");
  let simulationPreviewUrl = $state("");
  let simulationPreviewExpiresAt = $state("");
  let e2eProof = $state<Record<string, unknown> | null>(null);
  let tasks = $state<Task[]>(createTaskList());
  let latestJobMessage = $state("");
  let jobState = $state("");
  let jobWaitReason = $state("");
  let jobNextResumeAt = $state("");
  let jobResumeAvailableAt = $state("");
  let jobResumeAvailable = $state(false);
  let pollingInterval: ReturnType<typeof setInterval> | null = null;
  let requirements = $state<DeploymentRequirements | null>(null);
  let serverProvisioningMode = $state<ServerProvisioningMode | "legacy">(
    "legacy",
  );
  let remoteServerHost = $state("");
  let remoteServerUser = $state("");
  let remoteServerPort = $state<number | null>(null);
  let serverUrl = $state("");
  let copiedCommand = $state<string | null>(null);
  let copiedErrorDetails = $state(false);
  let showAllCommands = $state(false);
  let showInstallCommand = $derived(
    serverProvisioningMode === "install-command" ||
      serverProvisioningMode === "connect-remote" ||
      serverProvisioningMode === "legacy",
  );
  let oneLinerPreviewRequired = $derived(
    serverProvisioningMode === "install-command",
  );
  let backendCompleted = $state(false);
  let jobType = $state("");
  let waitingForProviderProvision = $derived(
    jobState === "waiting" && jobWaitReason === "waiting_provider_provision",
  );
  let waitingForManagedRuntime = $derived(
    jobState === "waiting" &&
      (jobWaitReason === "waiting_enrollment" || waitingForProviderProvision),
  );
  let nextResumeLabel = $derived(formatNextResumeAt(jobNextResumeAt));
  let pairingTokenExpiresAt = $state("");
  let pairingStartedAt = $state("");
  let expectedDeviceName = $state("");
  let existingServerIds = new Set<string>();
  let existingServerBaselineKnown = false;
  let connectedServer = $state<CanonicalServer | null>(null);
  let connectionPollError = $state("");
  let connectionPollingInProgress = false;
  let connectionPollingInterval: ReturnType<typeof setInterval> | null = null;
  let connectionClock = $state(Date.now());
  const CONNECTION_POLL_INTERVAL = 3000;
  const GUARD_HEARTBEAT_FRESH_MS = 90_000;
  const GUARD_HEARTBEAT_FUTURE_SKEW_MS = 30_000;
  let agentPairingRequired = $derived(
    requiresGuardConnection(creationOperation, serverProvisioningMode),
  );
  let agentPairingWaiting = $derived(
    agentPairingRequired && backendCompleted && !connectedServer,
  );
  let pairingTokenExpired = $derived(
    Boolean(pairingTokenExpiresAt) &&
      Date.parse(pairingTokenExpiresAt) <= connectionClock,
  );
  // The wizard seeded a Pocket ID owner (local or cloud-linked custom
  // bootstrap). Rollouts for such stacks must return the identity handoff.
  let ownerSeedExpected = $state(false);
  let ownerSeedSummary = $state<{
    email?: string;
    username?: string;
    displayName?: string;
    source?: string;
  } | null>(null);
  let pollErrorCount = $state(0);
  const MAX_POLL_ERRORS = 5;
  const BASE_POLL_INTERVAL = 3000;
  // While the SSE stream delivers change notifications, polling drops to a
  // slow safety net; a stream failure restores the fast interval.
  const SSE_SAFETY_POLL_INTERVAL = 15_000;
  // Wizard-run lane: the kpt1 registration token lives in the pairing job's
  // result (the provision job still carries a legacy token the Postgres
  // runtime cannot redeem — the pairing job's token always wins).
  let pairingJobId = $state("");
  let pairingTokenFromJob = false;
  let jobStreamClose: (() => void) | null = null;
  let sseActive = false;
  let pageDestroyed = false;
  // Safety: abort polling if job is stuck (e.g. no orchestrator processing it)
  const MAX_PENDING_DURATION_MS = 120_000; // 2 minutes
  const MAX_ADD_SERVER_RUNNING_DURATION_MS = 120_000; // add-server request jobs should be short
  let pollingStartedAt = $state<number>(0);
  let pollingInProgress = $state(false);
  let waitingRecoveryAvailable = $derived(
    waitingForManagedRuntime && jobResumeAvailable,
  );
  let retryingRollout = $state(false);
  let retryError = $state("");
  let overallProgress = $derived(
    agentPairingRequired
      ? Math.round(
          ((tasks.filter((t) => t.status === "completed").length +
            (connectedServer ? 1 : 0)) /
            (tasks.length + 1)) *
            100,
        )
      : Math.round(
          (tasks.filter((t) => t.status === "completed").length /
            tasks.length) *
            100,
        ),
  );
  let hasStackKitIdentityHandoff = $derived(
    Boolean(
      stackKitHandoff?.ownerUsername &&
      stackKitHandoff?.loginGatewayUrl &&
      (stackKitHandoff?.recoveryRef || stackKitHandoff?.recoveryHashPresent),
    ),
  );
  let hasVerifiedRuntimeProof = $derived(
    runtimePhase === "verified" &&
      verificationStatus === "verified" &&
      Boolean(e2eProof),
  );
  // A rollout (deploy) job ran, as opposed to a create/provision job that
  // legitimately stops before StackKit produces any identity outputs.
  let rolloutJobDetected = $derived(
    jobType === "deploy" ||
      runtimePhase === "deployed" ||
      runtimePhase === "verified",
  );
  // Any rollout for a stack with a seeded owner is only really "done" once
  // StackKit returned the identity handoff. Treat a completed-without-handoff
  // backend response as a failure (the user has no owner login). kombify-cloud
  // additionally requires the verified runtime proof; self-hosted rollouts
  // (install-command / connect-remote) require the handoff itself.
  let handoffMissingFailure = $derived(
    backendCompleted &&
      creationOperation === "stack" &&
      (serverProvisioningMode === "kombify-cloud"
        ? !hasVerifiedRuntimeProof || !hasStackKitIdentityHandoff
        : ownerSeedExpected &&
          rolloutJobDetected &&
          !hasStackKitIdentityHandoff),
  );
  let isComplete = $derived(
    backendCompleted &&
      tasks.every((t) => t.status === "completed") &&
      !handoffMissingFailure &&
      (!agentPairingRequired || Boolean(connectedServer)),
  );
  let failedTask = $derived(tasks.find((t) => t.status === "failed"));
  let hasFailed = $derived(
    tasks.some((t) => t.status === "failed") || handoffMissingFailure,
  );
  let failedTaskId = $derived(failedTask?.id || "");
  let hasManagedLeaseReference = $derived(Boolean(lease?.id));
  let creationHeaderLabel = $derived(
    creationOperation === "add-server"
      ? "Additional server"
      : stackName || "Creating Stack",
  );
  let postLeaseManagedCloudFailure = $derived(
    creationOperation === "stack" &&
      serverProvisioningMode === "kombify-cloud" &&
      Boolean(stackId) &&
      hasManagedLeaseReference &&
      isPostLeaseRuntimeTask(failedTaskId),
  );
  let stackKitArtifactOrRoutingFailure = $derived(
    postLeaseManagedCloudFailure &&
      isStackKitArtifactOrRoutingTask(failedTaskId),
  );
  let failurePrimaryActionLabel = $derived(
    postLeaseManagedCloudFailure
      ? retryingRollout
        ? "Retrying rollout..."
        : "Retry rollout"
      : "Try again",
  );
  let failurePrimaryActionDisabled = $derived(
    postLeaseManagedCloudFailure && retryingRollout,
  );
  let completionTitle = $derived(
    creationOperation === "add-server"
      ? agentPairingRequired
        ? "Server connected"
        : serverProvisioningMode === "kombify-cloud"
          ? "Managed server requested"
          : "Server registration ready"
      : serverProvisioningMode === "kombify-cloud"
        ? "Your Homelab is ready"
        : serverProvisioningMode === "connect-remote"
          ? "Configuration prepared"
          : "Server connection ready",
  );
  let completionSubtitle = $derived(
    creationOperation === "add-server"
      ? agentPairingRequired
        ? `${connectedServer?.name || "The additional server"} reported a fresh Guard heartbeat and is visible in the server projection.`
        : serverProvisioningMode === "kombify-cloud"
          ? "kombify requested the additional managed server. You can continue from the dashboard while enrollment finishes."
          : "The additional server registration is ready for the existing TechStack."
      : serverProvisioningMode === "kombify-cloud"
        ? "The managed VM, StackKit rollout, verification, restore drill, and identity handoff are complete."
        : serverProvisioningMode === "connect-remote"
          ? "Your remote server configuration is ready for rollout"
          : "Connect your server to continue the rollout",
  );
  let dashboardHref = $derived(() => {
    const params = ["phase=review"];
    if (stackId) params.unshift(`stack=${encodeURIComponent(stackId)}`);
    return `/stacks?${params.join("&")}`;
  });
  let monitoringHref = $derived(() => {
    const params = ["focus=servers"];
    if (stackId) params.unshift(`stack_id=${encodeURIComponent(stackId)}`);
    return `/monitoring?${params.join("&")}`;
  });
  let servicesHref = $derived(() => {
    return stackId
      ? `/services?stack_id=${encodeURIComponent(stackId)}`
      : "/services";
  });
  let proofRows = $derived(
    [
      ["simulation", "Simulation gate"],
      ["rollout", "StackKit rollout"],
      ["verification", "Service verification"],
      ["restore", "Restore drill"],
    ]
      .map(([key, label]) => ({
        key,
        label,
        status: runtimeProof[key]?.status || "missing",
        action: runtimeProof[key]?.action,
      }))
      .filter((row) => row.status !== "missing" || backendCompleted),
  );
  // Current step info for the right-side detail panel
  let currentTask = $derived(
    tasks.find((t) => t.status === "running") ??
      tasks.find((t) => t.status === "pending"),
  );
  let currentStepInfo = $derived(
    currentTask ? STEP_DETAILS[currentTask.id] : null,
  );
  let currentStepMessage = $derived(
    currentTask?.message || latestJobMessage || "",
  );
  let completedCount = $derived(
    tasks.filter((t) => t.status === "completed").length,
  );

  // If we can't derive a job id, the page must not pretend success.
  let initError = $state<string | null>(null);
  const capturedCreationFailureJobIds = new Set<string>();
  const capturedRolloutResultKeys = new Set<string>();

  function ensureRuntimeTaskList() {
    if (creationOperation === "add-server") {
      replaceTaskListIfShapeChanged(
        createAddServerTaskList(serverProvisioningMode),
      );
      return;
    }
    const hasRuntimeTasks = tasks.some((task) => runtimeTaskIds.has(task.id));
    if (!hasRuntimeTasks) {
      tasks = createRuntimeTaskList();
    }
  }

  function replaceTaskListIfShapeChanged(next: Task[]) {
    const currentIds = tasks.map((task) => task.id).join("|");
    const nextIds = next.map((task) => task.id).join("|");
    if (currentIds !== nextIds) {
      tasks = next;
    }
  }

  function syncTaskListForOperation() {
    if (creationOperation === "add-server") {
      replaceTaskListIfShapeChanged(
        createAddServerTaskList(serverProvisioningMode),
      );
      return;
    }
    if (serverProvisioningMode === "kombify-cloud") {
      ensureRuntimeTaskList();
    }
  }

  function isRunningJobStatus(status: string): boolean {
    return status === "running" || status === "in_progress";
  }

  function formatNextResumeAt(value: string): string {
    const timestamp = Date.parse(value);
    if (!Number.isFinite(timestamp)) return value;
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeStyle: "medium",
    }).format(new Date(timestamp));
  }

  function addServerRequestHasExceededGuard(status: string): boolean {
    return (
      creationOperation === "add-server" &&
      isRunningJobStatus(status) &&
      pollingStartedAt > 0 &&
      Date.now() - pollingStartedAt > MAX_ADD_SERVER_RUNNING_DURATION_MS
    );
  }

  // Generate install command
  let installCommand = $derived(
    showInstallCommand && registrationToken && serverUrl
      ? generateInstallCommand(serverUrl, registrationToken)
      : "",
  );
  let allInstallCommands = $derived(
    showInstallCommand && registrationToken && serverUrl
      ? generateInstallCommands(serverUrl, registrationToken)
      : [],
  );

  function normalizedServerIdentity(value?: string): string {
    return (value || "").trim().toLowerCase();
  }

  function hasFreshGuardHeartbeat(server: CanonicalServer): boolean {
    // Persisted canonical state only. The sweeper is the demotion authority;
    // this page never derives a connection verdict of its own, it only checks
    // that the persisted heartbeat is recent enough to belong to THIS pairing.
    const health = legacyServerState(
      server.connection.state,
      server.health.state,
    );
    const lastSeenAt = Date.parse(server.connection.last_heartbeat_at || "");
    const pairingStartedAtMs = Date.parse(pairingStartedAt);
    const heartbeatAgeMs = connectionClock - lastSeenAt;
    return (
      server.techstack_id === stackId &&
      Boolean(server.worker_id) &&
      serverRolloutReady(server) &&
      health === "healthy" &&
      Number.isFinite(lastSeenAt) &&
      heartbeatAgeMs >= -GUARD_HEARTBEAT_FUTURE_SKEW_MS &&
      heartbeatAgeMs <= GUARD_HEARTBEAT_FRESH_MS &&
      (!Number.isFinite(pairingStartedAtMs) ||
        lastSeenAt >= pairingStartedAtMs - 30_000)
    );
  }

  function selectConnectedServer(
    servers: CanonicalServer[],
  ): CanonicalServer | null {
    const candidates = servers.filter((server) => {
      if (!hasFreshGuardHeartbeat(server)) return false;
      return belongsToCurrentConnectionAttempt({
        serverId: server.id,
        // `stack_id` is omitempty on the canonical response; the heartbeat
        // guard above already required it to equal `stackId`.
        serverStackId: server.techstack_id ?? "",
        stackId,
        existingServerBaselineKnown,
        existingServerIds,
      });
    });
    if (candidates.length === 1) return candidates[0];
    if (candidates.length === 0) return null;

    const expected = normalizedServerIdentity(expectedDeviceName);
    if (!expected) return null;
    return (
      candidates.find(
        (server) => normalizedServerIdentity(server.name) === expected,
      ) || null
    );
  }

  function stopConnectionPolling() {
    if (connectionPollingInterval) {
      clearInterval(connectionPollingInterval);
      connectionPollingInterval = null;
    }
  }

  function expireConnectedServerWithoutFreshHeartbeat() {
    if (!connectedServer || hasFreshGuardHeartbeat(connectedServer)) return;
    connectedServer = null;
    connectionPollError =
      "The previously verified Guard heartbeat is no longer fresh. Waiting for current connection evidence.";
  }

  async function pollGuardConnection() {
    // Advance and apply the local heartbeat TTL even while an earlier registry
    // request is still in flight. A hung or failing projection must never keep
    // a cached connection claim alive beyond the canonical freshness window.
    connectionClock = Date.now();
    expireConnectedServerWithoutFreshHeartbeat();
    if (
      !agentPairingRequired ||
      !backendCompleted ||
      !stackId ||
      connectionPollingInProgress
    ) {
      return;
    }
    connectionPollingInProgress = true;
    try {
      const servers = await listCanonicalServers();
      connectionPollError = "";
      const nextConnectedServer = selectConnectedServer(servers);
      if (nextConnectedServer) {
        connectedServer = nextConnectedServer;
        if (jobId) {
          sessionStorage.removeItem(`creating:${jobId}:registrationToken`);
        }
        registrationToken = "";
      } else if (connectedServer) {
        expireConnectedServerWithoutFreshHeartbeat();
      }
    } catch {
      connectionPollError =
        "The server projection could not be checked. The pairing command remains available, but this page will not claim a connection without a fresh Guard heartbeat.";
    } finally {
      connectionPollingInProgress = false;
    }
  }

  function startConnectionPolling() {
    if (
      !agentPairingRequired ||
      !backendCompleted ||
      connectionPollingInterval
    ) {
      return;
    }
    void pollGuardConnection();
    connectionPollingInterval = setInterval(
      pollGuardConnection,
      CONNECTION_POLL_INTERVAL,
    );
  }

  // Poll job status from API with exponential backoff on errors
  async function pollJobStatus() {
    if (!jobId || pollingInProgress) return;
    pollingInProgress = true;
    connectionClock = Date.now();

    try {
      const job = await getJob(jobId);
      pollErrorCount = 0; // Reset on success

      if (job) {
        jobType = job.type || jobType;
        const jobStatus = job.state || "pending";
        jobState = jobStatus;
        jobWaitReason = job.wait_reason?.trim() || "";
        jobNextResumeAt = job.next_resume_at?.trim() || "";
        jobResumeAvailableAt = job.resume_available_at?.trim() || "";
        jobResumeAvailable = job.resume_available === true;
        const nextJobMessage =
          job.message ||
          (tasks.some((task) => task.id === job.current_step)
            ? ""
            : job.current_step) ||
          "";
        latestJobMessage = nextJobMessage || latestJobMessage || "";
        const result = normalizeJobResult(job.result);
        if (result) {
          applyJobResult(result);
        }
        syncTaskListForOperation();
        if (
          creationOperation !== "add-server" &&
          typeof job.step === "string" &&
          runtimeTaskIds.has(job.step)
        ) {
          ensureRuntimeTaskList();
        }

        if (addServerRequestHasExceededGuard(jobStatus)) {
          tasks = updateTasksWithError(tasks, {
            step: tasks[0]?.id,
            error: "Managed server request is still running",
            error_details:
              "The Add Server request has been running for over 2 minutes. This path should only request or prepare the additional server, not run the full StackKit rollout. Open Operations to check whether the server request was created, then retry Add Server if no new server appears.",
          });
          stopPolling();
          return;
        }

        // Safety: detect genuinely stuck queue entries. A resumable `waiting`
        // job is not stuck; the backend owns its next enrollment checkpoint.
        if (
          (jobStatus === "pending" || jobStatus === "queued") &&
          pollingStartedAt > 0 &&
          Date.now() - pollingStartedAt > MAX_PENDING_DURATION_MS
        ) {
          tasks = updateTasksWithError(tasks, {
            error: "Job is not being processed",
            error_details:
              "The provisioning job has been pending for over 2 minutes without progress. " +
              "This usually means the orchestrator is not running or has crashed.\n\n" +
              "Check the server logs: docker compose logs techstack\n" +
              "Try restarting: docker compose restart techstack",
          });
          stopPolling();
          return;
        }

        // Handle failed jobs with detailed error info
        if (jobStatus === "failed" || jobStatus === "error") {
          const legacyCurrentStep = (job as any)?.current_step as
            | string
            | undefined;
          const legacyErrorMessage = (job as any)?.error_message as
            | string
            | undefined;
          const failureError =
            job.error || job.message || legacyErrorMessage || legacyCurrentStep;
          const backendDetails =
            job.error_details ||
            (job.result as any)?.error_details ||
            legacyErrorMessage;
          const failureDetails = buildFailureDetails(
            job,
            result,
            buildFailureFallback(failureError, backendDetails),
          );

          captureCreationFailure(job, failureError, failureDetails);

          tasks = updateTasksWithError(tasks, {
            step: job.step,
            error: failureError,
            error_details: failureDetails,
            state: job.state,
          });
          stopPolling();
          return;
        }

        // Reflect the backend's reported step transitions on the visible task
        // list - never fabricate progress past what the backend has emitted.
        tasks = updateTasksFromJob(tasks, job);

        if (jobStatus === "completed" || jobStatus === "success") {
          backendCompleted = true;
          const resultRecord = result || {};
          const verifiedRuntime = hasVerifiedRuntimeResult(resultRecord);
          const identityHandoff = extractStackKitIdentityHandoff(resultRecord);
          const missingCloudHandoff =
            creationOperation === "stack" &&
            (serverProvisioningMode === "kombify-cloud"
              ? !verifiedRuntime || !identityHandoff
              : ownerSeedExpected && rolloutJobDetected && !identityHandoff);
          // Rollouts for stacks with a seeded owner must hand back owner
          // login, login gateway, and recovery references. If the backend
          // reports completed but the StackKit response omitted those fields,
          // the deployment is unusable and we surface it as a hard failure
          // rather than a green success.
          if (missingCloudHandoff) {
            tasks = updateTasksWithError(tasks, {
              step: "verify_rollout",
              error: "StackKit identity handoff missing",
              error_details:
                "The runtime job reported completed but the StackKit response did not include owner login, login gateway, or recovery outputs (stackkit_outputs). The deployment is not usable yet - the rollout must be retried or the StackKit runtime action contract repaired.",
            });
            captureRolloutResultOnce(
              "techstack:rollout_failed",
              rolloutAnalyticsInput({
                jobId: (job as { id?: string }).id ?? jobId,
                stackId,
                runtimePhase:
                  getString(resultRecord, ["runtime_phase"]) || runtimePhase,
                verificationStatus:
                  getString(resultRecord, ["verification_status"]) ||
                  verificationStatus,
                errorCode: "identity_handoff_missing",
                identityHandoffPresent: false,
              }),
            );
            stopPolling();
          } else {
            captureRolloutResultOnce(
              "techstack:rollout_completed",
              rolloutAnalyticsInput({
                jobId: (job as { id?: string }).id ?? jobId,
                stackId,
                runtimePhase:
                  getString(resultRecord, ["runtime_phase"]) || runtimePhase,
                verificationStatus:
                  getString(resultRecord, ["verification_status"]) ||
                  verificationStatus,
                proofKeyCount: Object.keys(
                  asRecord(resultRecord.runtime_proof) ?? runtimeProof,
                ).length,
                identityHandoffPresent: Boolean(identityHandoff),
              }),
            );
          }
          stopPolling();
          startConnectionPolling();
          return;
        }

        // Stop polling when done
        if (job.state === "canceled") {
          stopPolling();
        }
      }
    } catch (e) {
      console.error("Failed to poll job status:", e);
      pollErrorCount++;

      // After too many errors, show a connection error but don't stop completely
      if (pollErrorCount >= MAX_POLL_ERRORS) {
        tasks = updateTasksWithError(tasks, {
          error: "Connection to server lost",
          error_details: `After ${MAX_POLL_ERRORS} failed attempts, the backend could not be reached. Please check your network connection and whether the server is running.`,
        });
        stopPolling();
      }
    } finally {
      pollingInProgress = false;
    }
  }

  function stopPolling() {
    if (pollingInterval) {
      clearInterval(pollingInterval);
      pollingInterval = null;
    }
    if (jobStreamClose) {
      jobStreamClose();
      jobStreamClose = null;
    }
  }

  function restoreFastPolling() {
    if (!sseActive) return;
    sseActive = false;
    if (pollingInterval) {
      clearInterval(pollingInterval);
      pollingInterval = setInterval(pollJobStatus, BASE_POLL_INTERVAL);
    }
  }

  // SSE accelerator: events only trigger a full poll (the stream payload
  // lacks the job result the page renders from). While the stream is healthy
  // polling slows to a safety net; a done event or stream failure restores
  // the fast interval (the terminal poll stops everything itself).
  function startJobStream() {
    if (!jobId || jobStreamClose) return;
    let fellBack = false;
    const close = streamJobProgress(jobId, {
      onEvent: (eventName) => {
        if (eventName === "done") {
          jobStreamClose = null;
          restoreFastPolling();
        } else if (!sseActive) {
          sseActive = true;
          if (pollingInterval) {
            clearInterval(pollingInterval);
            pollingInterval = setInterval(
              pollJobStatus,
              SSE_SAFETY_POLL_INTERVAL,
            );
          }
        }
        void pollJobStatus();
      },
      onFallback: () => {
        fellBack = true;
        jobStreamClose = null;
        restoreFastPolling();
      },
    });
    // The handlers may fire synchronously (constructor failure); do not
    // resurrect a stream reference the fallback already cleared.
    if (!fellBack) {
      jobStreamClose = close;
    }
  }

  // Wizard-run lane: fetch the pairing job's registration token (the job is
  // minted completed, so the first attempt normally succeeds; retries cover
  // read-after-write lag).
  async function syncPairingTokenFromJob() {
    if (!pairingJobId || pairingTokenFromJob || pageDestroyed) return;
    try {
      const pairingJob = await getJob(pairingJobId);
      const result = normalizeJobResult(pairingJob?.result);
      const token = result ? getString(result, ["registration_token"]) : "";
      if (token) {
        registrationToken = token;
        pairingTokenExpiresAt =
          getString(result!, ["token_expires_at"]) || pairingTokenExpiresAt;
        pairingTokenFromJob = true;
        if (!pairingStartedAt) pairingStartedAt = new Date().toISOString();
        return;
      }
    } catch (error) {
      console.warn("Pairing job read failed:", error);
    }
    setTimeout(() => void syncPairingTokenFromJob(), BASE_POLL_INTERVAL);
  }

  function rolloutAnalyticsInput(
    extra: TechstackCreationEventInput = {},
  ): TechstackCreationEventInput {
    return {
      stackId,
      jobId,
      serverProvisioningMode,
      creationOperation,
      runtimePhase,
      verificationStatus,
      proofKeyCount: Object.keys(runtimeProof).length,
      identityHandoffPresent: Boolean(stackKitHandoff),
      ...extra,
    };
  }

  function captureRolloutResultOnce(
    event: "techstack:rollout_completed" | "techstack:rollout_failed",
    input: TechstackCreationEventInput,
  ) {
    const id = input.jobId?.trim() || jobId || "unknown";
    const key = `${event}:${id}`;
    if (capturedRolloutResultKeys.has(key)) return;
    capturedRolloutResultKeys.add(key);
    captureRolloutEvent(event, input);
  }

  function captureRolloutEvent(
    event: "techstack:rollout_completed" | "techstack:rollout_failed",
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

  function captureCreationFailure(
    job: { id?: string; state?: string; status?: string; step?: string },
    error?: string,
    details?: string,
  ) {
    const id = job.id?.trim() ?? "";
    if (!id || capturedCreationFailureJobIds.has(id)) return;
    capturedCreationFailureJobIds.add(id);

    const providerInfo = parseManagedRuntimeProviderError(
      `${error ?? ""}\n${details ?? ""}`,
    );
    captureRolloutResultOnce(
      "techstack:rollout_failed",
      rolloutAnalyticsInput({
        jobId: id,
        stackId,
        errorCode:
          providerInfo.code || job.step || job.state || job.status || "failed",
        runtimePhase,
        verificationStatus,
        identityHandoffPresent: Boolean(stackKitHandoff),
      }),
    );
    Sentry.withScope((scope) => {
      scope.setTag("component", "stack_creation_ui");
      scope.setTag("flow", "wizard_creation");
      scope.setTag("job_id", id);
      scope.setTag("job_state", job.state || job.status || "");
      if (stackId) scope.setTag("stack_id", stackId);
      if (job.step) {
        scope.setTag("failed_step", job.step);
      }
      if (providerInfo.provider) {
        scope.setTag("provider_id", providerInfo.provider);
      }
      if (providerInfo.code) {
        scope.setTag("provider_error_code", providerInfo.code);
      }
      if (providerInfo.category) {
        scope.setTag("provider_error_category", providerInfo.category);
      }
      if (providerInfo.retryHint) {
        scope.setTag("provider_retry_hint", providerInfo.retryHint);
      }
      scope.setContext("stack_creation", {
        job_id: id,
        stack_id: stackId,
        mode: serverProvisioningMode,
        failed_step: job.step,
        provider_id: providerInfo.provider,
        provider_error_code: providerInfo.code,
        provider_error_category: providerInfo.category,
        provider_retry_hint: providerInfo.retryHint,
        provider_summary: sanitizeSentrySummary(providerInfo.summary),
        error_summary: sanitizeSentrySummary(error),
      });
      scope.setLevel("error");
      Sentry.captureMessage("stack_creation_failed");
    });
  }

  function sanitizeSentrySummary(value?: string): string {
    return sanitizeSentryText(value).slice(0, 1000);
  }

  async function copyToClipboard(text: string, id?: string) {
    const commandId = id || "main";
    try {
      await navigator.clipboard.writeText(text);
      copiedCommand = commandId;
    } catch {
      copiedCommand = `error:${commandId}`;
    }
    setTimeout(() => (copiedCommand = null), 2000);
  }

  function copyErrorDetails() {
    const details = failedTask?.errorDetails;
    if (!details) return;
    void navigator.clipboard
      .writeText(details)
      .then(() => {
        copiedErrorDetails = true;
        setTimeout(() => (copiedErrorDetails = false), 2000);
      })
      .catch(() => {
        copiedErrorDetails = false;
      });
  }

  function isServerProvisioningMode(
    value: unknown,
  ): value is ServerProvisioningMode {
    return (
      value === "kombify-cloud" ||
      value === "connect-remote" ||
      value === "install-command"
    );
  }

  function normalizeCreationOperation(
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

  function asRecord(value: unknown): Record<string, unknown> | null {
    return value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : null;
  }

  function getString(
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

  function getBoolean(
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

  function buildFailureFallback(
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

  function normalizeJobResult(
    result: Record<string, unknown> | string | undefined,
  ): Record<string, unknown> | null {
    if (!result) return null;
    if (typeof result === "string") return safeJsonParse(result);
    return result;
  }

  function buildFailureDetails(
    job: { id?: string; error_details?: string },
    result: Record<string, unknown> | null,
    fallback?: string,
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

  function appendDetailLine(
    lines: string[],
    label: string,
    value: unknown,
    suffix = "",
  ) {
    if (value === undefined || value === null || value === "") return;
    lines.push(`${label}: ${String(value)}${suffix}`);
  }

  function applyJobResult(result: Record<string, unknown>) {
    // When a dedicated pairing job exists, its kpt1 token is the only
    // redeemable credential; the provision job still carries a legacy token
    // the Postgres runtime rejects, so it must never be applied.
    if (!pairingTokenFromJob && !pairingJobId) {
      registrationToken =
        getString(result, ["registration_token"]) || registrationToken;
      pairingTokenExpiresAt =
        getString(result, ["token_expires_at"]) || pairingTokenExpiresAt;
    }
    expectedDeviceName =
      expectedDeviceName || getString(result, ["server_remote_host"]);
    stackId = getString(result, ["stack_id"]) || stackId;
    runtimePhase = getString(result, ["runtime_phase"]);
    verificationStatus = getString(result, ["verification_status"]);
    stackKitHandoff = extractStackKitIdentityHandoff(result);
    const nextLease = extractLeaseSummary(result);
    lease = nextLease;
    creationOperation =
      normalizeCreationOperation(getString(result, ["creation_operation"])) ||
      (result.managed_runtime_addition === true ? "add-server" : "") ||
      creationOperation;

    const mode = getString(result, ["server_provisioning_mode"]);
    if (isServerProvisioningMode(mode)) {
      serverProvisioningMode = mode;
      if (mode === "kombify-cloud" && creationOperation === "stack") {
        ensureRuntimeTaskList();
      }
    }
    syncTaskListForOperation();

    const proof = asRecord(result.runtime_proof);
    runtimeProof = proof
      ? Object.fromEntries(
          Object.entries(proof)
            .map(([key, value]) => {
              const entry = asRecord(value);
              return entry ? [key, entry as RuntimeProofEntry] : null;
            })
            .filter((entry): entry is [string, RuntimeProofEntry] =>
              Boolean(entry),
            ),
        )
      : {};
    e2eProof = asRecord(result.e2e_proof);
    const simulationProof = asRecord(proof?.simulation);
    simulationPreviewStatus =
      getString(result, ["simulation_preview_status"]) ||
      getString(simulationProof, ["status"]) ||
      simulationPreviewStatus;
    simulationPreviewUrl =
      getString(result, ["simulation_preview_url"]) ||
      getString(simulationProof, ["preview_url"]) ||
      simulationPreviewUrl;
    simulationPreviewExpiresAt =
      getString(result, ["simulation_preview_expires_at"]) ||
      getString(simulationProof, ["expires_at"]) ||
      simulationPreviewExpiresAt;

    const backendRequirements = asRecord(result.requirements);
    if (backendRequirements) {
      requirements = backendRequirements as unknown as DeploymentRequirements;
    }
  }

  function hasVerifiedRuntimeResult(result: Record<string, unknown>): boolean {
    return (
      getString(result, ["runtime_phase"]) === "verified" &&
      getString(result, ["verification_status"]) === "verified" &&
      Boolean(asRecord(result.e2e_proof))
    );
  }

  function extractStackKitIdentityHandoff(
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
      loginGatewayUrl: getString(loginGateway, [
        "url",
        "login_url",
        "loginUrl",
      ]),
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

  function extractLeaseSummary(
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

  function safeJsonParse(value: string): Record<string, unknown> {
    try {
      const parsed = JSON.parse(value);
      return parsed && typeof parsed === "object" && !Array.isArray(parsed)
        ? (parsed as Record<string, unknown>)
        : {};
    } catch {
      return {};
    }
  }

  function isLocalhostHost(host: string): boolean {
    const lower = (host || "").toLowerCase();
    return (
      lower === "localhost" ||
      lower === "127.0.0.1" ||
      lower === "::1" ||
      lower === "0.0.0.0"
    );
  }

  function getJobSessionItem(suffix: string): string | null {
    return jobId ? sessionStorage.getItem(`creating:${jobId}:${suffix}`) : null;
  }

  // Resume plumbing for the wizard-run lane: when the browser lost its
  // sessionStorage handoff (new device, cleared storage, dashboard banner
  // link without params), the server-side run ledger restores the context.
  async function adoptActiveWizardRun(): Promise<boolean> {
    try {
      const run = await getActiveWizardRun();
      // A stale or long-completed run must not re-open the progress page.
      if (!run || !wizardRunNeedsAttention(run)) return false;
      const result = (run.result ?? {}) as Record<string, unknown>;
      jobId = run.job_id || run.pairing_job_id || "";
      if (!jobId) return false;
      stackId = stackId || run.stack_id || "";
      const resultName = typeof result.name === "string" ? result.name : "";
      if (resultName) stackName = resultName;
      if (result.kit_assignment_mode === "join") {
        creationOperation = "add-server";
      }
      if (run.pairing_job_id && run.pairing_job_id !== jobId) {
        pairingJobId = run.pairing_job_id;
      }
      return true;
    } catch (error) {
      console.warn("Active wizard-run lookup failed:", error);
      return false;
    }
  }

  onMount(async () => {
    // Get data from URL params or session storage
    const params = new URLSearchParams(window.location.search);
    jobId =
      params.get("job_id") ||
      params.get("job") ||
      sessionStorage.getItem("creatingJobId") ||
      "";
    stackName =
      params.get("name") ||
      getJobSessionItem("stackName") ||
      sessionStorage.getItem("creatingStackName") ||
      "New Stack";
    stackId =
      params.get("stack_id") ||
      params.get("stack") ||
      getJobSessionItem("stackId") ||
      sessionStorage.getItem("creatingStackId") ||
      "";
    creationOperation =
      normalizeCreationOperation(
        params.get("operation") ||
          getJobSessionItem("operation") ||
          sessionStorage.getItem("creatingOperation"),
      ) || "stack";
    if (!jobId) {
      await adoptActiveWizardRun();
    }
    pairingJobId =
      pairingJobId ||
      params.get("pairing_job_id") ||
      getJobSessionItem("pairingJobId") ||
      "";
    registrationToken = getJobSessionItem("registrationToken") || "";
    pairingTokenExpiresAt = getJobSessionItem("tokenExpiresAt") || "";
    pairingStartedAt = getJobSessionItem("pairingStartedAt") || "";
    expectedDeviceName = getJobSessionItem("expectedDeviceName") || "";
    const storedServerIds = getJobSessionItem("existingServerIds");
    if (storedServerIds !== null) {
      existingServerBaselineKnown = true;
      try {
        const parsedServerIds = JSON.parse(storedServerIds);
        existingServerIds = new Set(
          Array.isArray(parsedServerIds)
            ? parsedServerIds.filter(
                (value): value is string => typeof value === "string",
              )
            : [],
        );
      } catch {
        existingServerIds = new Set();
      }
    }
    syncTaskListForOperation();

    if (!jobId) {
      initError = "Missing job reference. Please start the setup wizard again.";
      tasks = updateTasksWithError(tasks, {
        error: "No job found",
        error_details:
          "This page requires a job_id from the setup wizard. Please restart setup via /stacks/new.",
      });
    }

    // Determine server URL for install command.
    // On a hosted SaaS (techstack.kombify.io, *.onrender.com, custom domain) the
    // window already resolves to a public, worker-reachable URL. Falling back to
    // a LAN/RFC1918 address from getNetworks() in that scenario yields a worker
    // command pointing at an unreachable host (e.g. 10.31.x.x on Render). Only
    // attempt the LAN scan when the UI itself is being served from localhost,
    // which only happens on self-hosted/dev workstations.
    let bestUrl = getBestPublicServerUrl();
    const runningOnLocalhost = isLocalhostHost(window.location.hostname);

    if (
      runningOnLocalhost &&
      (bestUrl.includes("localhost") || bestUrl.includes("127.0.0.1"))
    ) {
      try {
        const networks = await getNetworks();
        let candidate = "";

        // Find first non-loopback IPv4
        for (const net of networks) {
          for (const ip of net.ips) {
            // Simple IPv4 check (contains dot, not 127.x)
            if (ip.includes(".") && !ip.startsWith("127.")) {
              // Prefer standard home/office private ranges over likely Docker ranges (172.x)
              if (ip.startsWith("192.168.") || ip.startsWith("10.")) {
                candidate = ip;
                break;
              }
              // Fallback to any other private IP (e.g. 172.x) if we haven't found a better one yet
              if (!candidate) candidate = ip;
            }
          }
          // If we found a preferred candidate, stop searching
          if (
            candidate &&
            (candidate.startsWith("192.168.") || candidate.startsWith("10."))
          ) {
            break;
          }
        }

        if (candidate) {
          const port = window.location.port ? `:${window.location.port}` : "";
          bestUrl = `${window.location.protocol}//${candidate}${port}`;
        }
      } catch (e) {
        console.warn("Failed to get local networks:", e);
      }
    }

    if (runningOnLocalhost) {
      try {
        const registry = await getWorkerRegistryUrl();
        if (registry.url) bestUrl = registry.url;
      } catch (e) {
        console.warn("Failed to resolve the private-LAN enrollment URL:", e);
      }
    }
    serverUrl = bestUrl || window.location.origin;

    // Try to load requirements from session storage
    const storedConfig =
      getJobSessionItem("config") ||
      sessionStorage.getItem("creatingStackConfig");
    if (storedConfig) {
      try {
        const config = JSON.parse(storedConfig);
        const mode = config?.serverProvisioning?.mode;
        creationOperation =
          normalizeCreationOperation(config?.creationOperation) ||
          creationOperation;
        if (isServerProvisioningMode(mode)) {
          serverProvisioningMode = mode;
          if (mode === "kombify-cloud" && creationOperation === "stack") {
            ensureRuntimeTaskList();
          }
          syncTaskListForOperation();
          remoteServerHost = config.serverProvisioning.remote?.host || "";
          remoteServerUser = config.serverProvisioning.remote?.sshUser || "";
          remoteServerPort = config.serverProvisioning.remote?.sshPort || null;
        }
        const owner = config?.owner;
        if (
          owner &&
          owner.bootstrapMode !== "none" &&
          (owner.source === "local" || owner.source === "cloud-linked")
        ) {
          ownerSeedExpected = true;
          ownerSeedSummary = {
            email: owner.email || undefined,
            username: owner.username || undefined,
            displayName: owner.displayName || undefined,
            source: owner.source,
          };
        }
      } catch (e) {
        console.warn("Could not parse stored config:", e);
      }
    }

    if (jobId) {
      // Real mode: poll job status; the SSE stream accelerates updates and
      // polling stays as the fallback transport.
      pollingStartedAt = Date.now();
      pollJobStatus();
      pollingInterval = setInterval(pollJobStatus, BASE_POLL_INTERVAL);
      startJobStream();
      if (pairingJobId && pairingJobId !== jobId) {
        void syncPairingTokenFromJob();
      }
    }
  });

  onDestroy(() => {
    pageDestroyed = true;
    stopPolling();
    stopConnectionPolling();
  });

  function goToStack() {
    const params = new URLSearchParams();
    if (stackId) params.set("stack", stackId);
    params.set("phase", "review");
    goto(`/stacks?${params.toString()}`);
  }

  function retryFromStart() {
    goto(
      creationOperation === "add-server" && stackId
        ? `/stacks/${encodeURIComponent(stackId)}/servers/new`
        : "/stacks/new",
    );
  }

  async function handleFailurePrimaryAction() {
    if (postLeaseManagedCloudFailure) {
      await retryStackKitRollout();
      return;
    }
    retryFromStart();
  }

  function resetTasksForRolloutRetry(): Task[] {
    const next = createRuntimeTaskList();
    const retryStartIndex = next.findIndex(
      (task) => task.id === "prepare_rollout",
    );
    return next.map((task, index) =>
      retryStartIndex >= 0 && index < retryStartIndex
        ? { ...task, status: "completed" as const }
        : task,
    );
  }

  async function retryStackKitRollout() {
    if (!stackId || retryingRollout) return;
    retryError = "";
    retryingRollout = true;
    try {
      if (!jobId || !lease?.id) {
        retryError =
          "Rollout retry requires the exact failed job and managed VM lease.";
        return;
      }
      const response = await retryStackRollout(stackId, {
        source_job_id: jobId,
        lease_id: lease.id,
      });
      await continueWithAcceptedRollout(
        response,
        "Retrying rollout on the existing managed VM...",
      );
    } catch (error) {
      retryError =
        error instanceof Error
          ? error.message
          : "Could not start rollout retry.";
    } finally {
      retryingRollout = false;
    }
  }

  async function resumeOverdueManagedRuntime() {
    if (!stackId || !jobId || retryingRollout) return;
    const leaseId = lease?.id?.trim() || "";
    if (!leaseId) {
      retryError =
        "Managed rollout recovery was not started because the waiting job has no exact lease reference.";
      return;
    }
    retryError = "";
    retryingRollout = true;
    try {
      const response = await resumeStackEnrollment(stackId, {
        job_id: jobId,
        lease_id: leaseId,
      });
      await continueWithAcceptedRollout(
        response,
        "Recovering rollout on the exact existing managed VM...",
      );
    } catch (error) {
      retryError =
        error instanceof Error
          ? error.message
          : "Could not resume the overdue managed runtime wait.";
    } finally {
      retryingRollout = false;
    }
  }

  async function continueWithAcceptedRollout(
    response: StackJobAcceptedResponse,
    message: string,
  ) {
    if (!response.job_id) {
      throw new Error("Rollout recovery did not return a job_id.");
    }
    stopPolling();
    jobId = response.job_id;
    backendCompleted = false;
    pollErrorCount = 0;
    pollingInProgress = false;
    jobState = "";
    jobWaitReason = "";
    jobNextResumeAt = "";
    jobResumeAvailableAt = "";
    jobResumeAvailable = false;
    latestJobMessage = message;
    tasks = resetTasksForRolloutRetry();

    sessionStorage.setItem("creatingJobId", jobId);
    sessionStorage.setItem("creatingStackId", stackId);
    if (stackName) sessionStorage.setItem("creatingStackName", stackName);
    sessionStorage.setItem(`creating:${jobId}:stackId`, stackId);
    if (stackName) {
      sessionStorage.setItem(`creating:${jobId}:stackName`, stackName);
    }
    sessionStorage.setItem(`creating:${jobId}:operation`, creationOperation);

    const params = new URLSearchParams();
    params.set("job_id", jobId);
    params.set("stack_id", stackId);
    if (stackName) params.set("name", stackName);
    params.set("phase", "rollout");
    await goto(`/stacks/creating?${params.toString()}`, {
      replaceState: true,
      noScroll: true,
    });

    pollingStartedAt = Date.now();
    pollJobStatus();
    pollingInterval = setInterval(pollJobStatus, BASE_POLL_INTERVAL);
  }

  function copyToken() {
    navigator.clipboard.writeText(registrationToken);
  }
</script>

<svelte:head>
  <title>Creating Stack | kombify-TechStack</title>
</svelte:head>

<div class="min-h-full w-full bg-background p-6 lg:p-8 overflow-x-hidden">
  <!-- Side-by-side layout: Task list on left, content on right -->
  <div
    class="mx-auto w-full max-w-6xl min-w-0 grid gap-8 lg:grid-cols-[420px_minmax(0,1fr)]"
  >
    <!-- LEFT SIDE: Progress & Task List -->
    <div class="lg:sticky lg:top-8 lg:self-start min-w-0">
      <!-- Header with morphing text -->
      <div class="text-center lg:text-left mb-4 min-h-[8rem]">
        <div
          class="inline-flex items-center gap-2 px-4 py-2 rounded-full bg-primary/10 text-primary mb-4"
        >
          {#if isComplete}
            <svg
              class="w-4 h-4"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              stroke-width="2"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                d="M5 13l4 4L19 7"
              />
            </svg>
          {:else if hasFailed}
            <svg
              class="w-4 h-4"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              stroke-width="2"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          {:else if waitingForManagedRuntime || agentPairingWaiting}
            <svg
              class="w-4 h-4"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              stroke-width="2"
              aria-hidden="true"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                d="M12 8v4l3 2m6-2a9 9 0 11-18 0 9 9 0 0118 0z"
              />
            </svg>
          {:else}
            <svg
              class="w-4 h-4 animate-spin"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
            >
              <circle cx="12" cy="12" r="10" stroke-opacity="0.25" />
              <path d="M12 2a10 10 0 0 1 10 10" stroke-linecap="round" />
            </svg>
          {/if}
          <span class="text-sm font-medium">{creationHeaderLabel}</span>
        </div>
        <h3
          class="text-2xl font-semibold text-foreground mb-3 min-h-[3.5rem] leading-tight"
        >
          {#if isComplete}
            {completionTitle}
          {:else if handoffMissingFailure}
            Rollout incomplete
          {:else if hasFailed}
            Creation failed
          {:else if waitingForManagedRuntime}
            Server provisioning is still in progress
          {:else if agentPairingWaiting}
            Run the pairing command
          {:else}
            Please wait while we're
            <span class="inline-flex whitespace-nowrap">
              <MorphingText
                texts={["Completing", "Validating", "Configuring", "Deploying"]}
                interval={3000}
                typingSpeed={60}
                class="text-primary font-semibold"
              />
            </span>
          {/if}
        </h3>
        <p class="text-sm text-muted-foreground">
          {#if waitingForManagedRuntime}
            The next managed-runtime check is scheduled for this operation. If
            it remains overdue after a replica handover or restart, TechStack
            offers an exact-server resume action.
          {:else if agentPairingWaiting}
            The registration request is ready, but the server is not connected
            until a fresh Guard heartbeat appears in the server projection.
          {:else}
            This may take a few minutes. You can track the progress below.
          {/if}
        </p>
      </div>

      <!-- Progress card with task list -->
      <div
        class="bg-card/80 backdrop-blur-sm border border-border rounded-2xl p-6 shadow-2xl"
      >
        {#if initError}
          <div
            class="mb-6 p-4 rounded-xl border border-red-700/50 bg-red-900/20"
            data-testid="init-error"
          >
            <p class="text-red-200 font-medium">Setup cannot continue</p>
            <p class="text-sm text-red-300/80 mt-1">
              {initError}
            </p>
            <a
              href="/stacks/new"
              class="inline-block mt-3 text-sm text-primary hover:text-primary/80 underline"
            >
              Back to setup wizard →
            </a>
          </div>
        {/if}

        <!-- Progress bar -->
        <div class="mb-8">
          <div class="flex justify-between text-sm mb-2">
            <span class="text-muted-foreground">Progress</span>
            <span class="text-primary font-mono" aria-live="polite"
              >{overallProgress}%</span
            >
          </div>
          <div class="h-2 bg-muted rounded-full overflow-hidden">
            <div
              class="h-full bg-primary rounded-full transition-all duration-500 ease-out"
              style="width: {overallProgress}%"
            ></div>
          </div>
        </div>

        <!-- Phase-grouped task list -->
        <GroupedTaskList {tasks} />

        <!-- Footer hint for in-progress -->
        {#if !isComplete && !hasFailed}
          <p class="text-center text-muted-foreground text-xs mt-6">
            {#if waitingForManagedRuntime}
              The next check is scheduled. Dashboard, services, and server
              access remain locked until enrollment is confirmed; an overdue
              check can be resumed safely here.
            {:else if agentPairingWaiting}
              This page checks the real server projection every few seconds.
            {:else}
              This can take a few minutes. Please do not close this page.
            {/if}
          </p>
        {/if}
      </div>
    </div>

    <!-- RIGHT SIDE: Success/Error Content -->
    <div class="flex flex-col min-w-0 lg:pt-[9.75rem]">
      <!-- Completion state with worker registration -->
      {#if isComplete}
        <div
          class="bg-card/80 backdrop-blur-sm border border-border rounded-2xl p-6 shadow-2xl"
        >
          <div class="mb-6">
            <div class="flex items-center gap-3 mb-2">
              <div
                class="w-12 h-12 rounded-full bg-success/20 flex items-center justify-center"
              >
                <svg
                  class="w-6 h-6 text-success"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M5 13l4 4L19 7"
                  />
                </svg>
              </div>
              <div>
                <h2 class="text-xl font-semibold text-foreground">
                  {completionTitle}
                </h2>
                <p class="text-muted-foreground text-sm">
                  {completionSubtitle}
                </p>
              </div>
            </div>
          </div>

          {#if agentPairingRequired && connectedServer}
            <section
              class="mb-6 rounded-xl border border-success/30 bg-success/10 p-5"
              data-testid="guard-connected-summary"
            >
              <p
                class="text-xs font-semibold uppercase tracking-wide text-success"
              >
                Guard heartbeat verified
              </p>
              <h3 class="mt-1 text-lg font-semibold text-foreground">
                {connectedServer.name}
              </h3>
              <p class="mt-2 text-sm text-muted-foreground">
                TechStack now sees this server as healthy and rollout-ready in
                the real server projection. Pairing preparation alone did not
                trigger this state.
              </p>
              {#if connectedServer.connection.last_heartbeat_at}
                <p class="mt-3 text-xs text-muted-foreground">
                  Last Guard heartbeat:
                  <span class="font-mono"
                    >{connectedServer.connection.last_heartbeat_at}</span
                  >
                </p>
              {/if}
            </section>
          {/if}

          {#if stackKitHandoff}
            <section
              class="mb-6 overflow-hidden rounded-2xl border border-success/30 bg-success/10"
              data-testid="homelab-ready-card"
            >
              <div class="p-5">
                <div
                  class="flex flex-col gap-4 md:flex-row md:items-start md:justify-between"
                >
                  <div>
                    <p
                      class="text-xs font-semibold uppercase tracking-wide text-success"
                    >
                      {serverProvisioningMode === "kombify-cloud"
                        ? "Managed provisioning complete"
                        : "Rollout complete"}
                    </p>
                    <h3 class="mt-1 text-2xl font-semibold text-foreground">
                      Your Homelab is ready
                    </h3>
                    <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
                      {stackName} is verified, identity handoff is present, and the
                      first login gateway is available.
                    </p>
                  </div>
                  {#if stackKitHandoff.loginGatewayUrl}
                    <a
                      href={stackKitHandoff.loginGatewayUrl}
                      target="_blank"
                      rel="noopener noreferrer"
                      class="btn btn-primary"
                      data-testid="homelab-ready-login-link"
                    >
                      Open Homelab
                    </a>
                  {/if}
                </div>

                <div class="mt-5 grid gap-3 md:grid-cols-3">
                  <div class="rounded-lg border border-border bg-card/70 p-3">
                    <p class="text-xs text-muted-foreground">Owner</p>
                    <p
                      class="mt-1 truncate text-sm font-semibold text-foreground"
                    >
                      {stackKitHandoff.ownerDisplayName ||
                        stackKitHandoff.ownerUsername ||
                        "Owner ready"}
                    </p>
                    {#if stackKitHandoff.ownerEmail}
                      <p class="truncate text-xs text-muted-foreground">
                        {stackKitHandoff.ownerEmail}
                      </p>
                    {/if}
                  </div>
                  <div class="rounded-lg border border-border bg-card/70 p-3">
                    <p class="text-xs text-muted-foreground">Server</p>
                    <p
                      class="mt-1 truncate text-sm font-semibold text-foreground"
                    >
                      {lease?.host ||
                        lease?.publicIp ||
                        lease?.privateIp ||
                        remoteServerHost ||
                        (serverProvisioningMode === "kombify-cloud"
                          ? "Managed runtime"
                          : "Your server")}
                    </p>
                    <p class="truncate text-xs text-muted-foreground">
                      {#if serverProvisioningMode === "kombify-cloud"}
                        {lease?.provider || "kombify Cloud"} · {lease?.offering ||
                          "standard"}
                      {:else}
                        Self-hosted rollout
                      {/if}
                    </p>
                  </div>
                  <div class="rounded-lg border border-border bg-card/70 p-3">
                    <p class="text-xs text-muted-foreground">Recovery</p>
                    <p
                      class="mt-1 truncate text-sm font-semibold text-foreground"
                    >
                      {stackKitHandoff.recoveryRef
                        ? "Material linked"
                        : "Hash present"}
                    </p>
                    {#if stackKitHandoff.recoveryRef}
                      <p class="truncate text-xs text-muted-foreground">
                        {stackKitHandoff.recoveryRef}
                      </p>
                    {/if}
                  </div>
                </div>

                <div class="mt-5 flex flex-wrap gap-2">
                  <a href={dashboardHref()} class="btn btn-secondary btn-sm">
                    Dashboard
                  </a>
                  <a href={monitoringHref()} class="btn btn-secondary btn-sm">
                    Monitoring
                  </a>
                  <a href={servicesHref()} class="btn btn-secondary btn-sm">
                    Services
                  </a>
                </div>
              </div>
            </section>
          {:else if ownerSeedExpected && creationOperation === "stack" && !rolloutJobDetected}
            <!-- Create/provision finished for a self-hosted stack: the owner
                 seed is prepared, the login handoff arrives after rollout. -->
            <section
              class="mb-6 rounded-2xl border border-info/30 bg-info/10 p-5"
              data-testid="owner-prepared-card"
            >
              <p
                class="text-xs font-semibold uppercase tracking-wide text-info"
              >
                Owner prepared
              </p>
              <h3 class="mt-1 text-lg font-semibold text-foreground">
                {ownerSeedSummary?.displayName ||
                  ownerSeedSummary?.username ||
                  ownerSeedSummary?.email ||
                  "Owner seed ready"}
              </h3>
              <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
                {#if ownerSeedSummary?.source === "cloud-linked"}
                  The owner identity derives from your linked kombify Cloud
                  profile.
                {:else if ownerSeedSummary?.email}
                  The owner seed is prepared for {ownerSeedSummary.email}.
                {:else}
                  The owner seed is prepared.
                {/if}
                The first login gateway and recovery reference arrive with the StackKit
                rollout ("Review + Start").
              </p>
            </section>
          {/if}

          <!-- Requirements Info -->
          {#if requirements}
            <div
              class="bg-info/10 border border-info/30 rounded-xl p-4 mb-6"
              data-testid="requirements-card"
            >
              <div class="flex items-start gap-3">
                <svg
                  class="w-5 h-5 text-info shrink-0 mt-0.5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
                <div class="flex-1">
                  <!-- StackKit Badge -->
                  {#if requirements.stackKit}
                    <div class="flex items-center gap-2 mb-2">
                      <span
                        class="text-xs px-2 py-0.5 bg-info/30 text-info rounded"
                      >
                        {requirements.stackKit}
                      </span>
                      {#if requirements.detectedAddons && requirements.detectedAddons.length > 0}
                        {#each requirements.detectedAddons as addon}
                          <span
                            class="text-xs px-2 py-0.5 bg-primary/30 text-primary rounded"
                          >
                            +{addon}
                          </span>
                        {/each}
                      {/if}
                    </div>
                  {/if}

                  <p class="text-foreground font-medium text-sm">
                    {requirements.description}
                  </p>

                  <!-- Hardware Requirements -->
                  {#if requirements.minRAM || requirements.minCPU || requirements.specialRequirements?.length}
                    <div class="mt-3 flex flex-wrap gap-2 text-xs">
                      {#if requirements.minRAM}
                        <span
                          class="px-2 py-1 bg-muted text-foreground rounded font-medium"
                        >
                          RAM: Min. {Math.round(requirements.minRAM / 1024)}GB
                        </span>
                      {/if}
                      {#if requirements.minCPU}
                        <span
                          class="px-2 py-1 bg-muted text-foreground rounded font-medium"
                        >
                          CPU: Min. {requirements.minCPU} cores
                        </span>
                      {/if}
                    </div>
                  {/if}

                  <!-- Special Requirements -->
                  {#if requirements.specialRequirements && requirements.specialRequirements.length > 0}
                    <ul class="mt-3 text-sm text-amber-200 space-y-1">
                      {#each requirements.specialRequirements as req}
                        <li class="flex items-start gap-2">
                          <span aria-hidden="true">•</span>
                          <span>{req}</span>
                        </li>
                      {/each}
                    </ul>
                  {/if}

                  <!-- Legacy details fallback -->
                  {#if requirements.details && requirements.details.length > 0}
                    <ul class="mt-3 text-sm text-foreground space-y-1">
                      {#each requirements.details as detail}
                        <li>• {detail}</li>
                      {/each}
                    </ul>
                  {/if}
                </div>
              </div>
            </div>
          {:else if backendCompleted && creationOperation === "stack"}
            <div
              class="bg-warning/10 border border-warning/40 rounded-xl p-4 mb-6"
              data-testid="requirements-missing-card"
            >
              <p class="text-sm font-medium text-foreground">
                Backend requirements are unavailable
              </p>
              <p class="mt-1 text-sm text-muted-foreground">
                The page is not filling this with frontend estimates. Re-run
                preparation if requirements are needed for review.
              </p>
            </div>
          {/if}

          <!-- Required Credentials Section -->
          {#if requirements?.requiredCredentials && requirements.requiredCredentials.length > 0}
            <div
              class="bg-amber-900/20 border border-amber-700/30 rounded-xl p-4 mb-6"
            >
              <div class="flex items-start gap-3">
                <svg
                  class="w-5 h-5 text-amber-400 shrink-0 mt-0.5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z"
                  />
                </svg>
                <div class="flex-1">
                  <p class="text-amber-200 font-semibold text-sm mb-2">
                    Required credentials
                  </p>
                  <ul class="space-y-2">
                    {#each requirements.requiredCredentials as cred}
                      <li class="bg-muted/70 rounded p-3">
                        <div class="flex items-center justify-between">
                          <span class="text-foreground text-sm font-medium"
                            >{cred.label}</span
                          >
                          {#if cred.helpUrl}
                            <a
                              href={cred.helpUrl}
                              target="_blank"
                              rel="noopener noreferrer"
                              class="text-xs text-primary hover:text-primary/80 underline"
                            >
                              Guide →
                            </a>
                          {/if}
                        </div>
                        {#if cred.description}
                          <p class="text-sm text-foreground/85 mt-1">
                            {cred.description}
                          </p>
                        {/if}
                        {#if cred.required}
                          <span
                            class="inline-block mt-2 text-xs px-2 py-0.5 rounded bg-red-500/20 text-red-200 font-medium"
                            >Required</span
                          >
                        {/if}
                      </li>
                    {/each}
                  </ul>
                </div>
              </div>
            </div>
          {/if}

          <!-- Pre-Checks Section -->
          {#if requirements?.requiredPreChecks && requirements.requiredPreChecks.length > 0}
            <div
              class="bg-primary/10 border border-primary/40 rounded-xl p-4 mb-6"
            >
              <div class="flex items-start gap-3">
                <svg
                  class="w-5 h-5 text-primary shrink-0 mt-0.5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4"
                  />
                </svg>
                <div class="flex-1">
                  <p class="text-foreground font-semibold text-sm mb-1">
                    Requirements your servers must meet
                  </p>
                  <p class="text-xs text-muted-foreground mb-3">
                    The orchestrator blocks rollout when a `Required` item is
                    missing on the target server. Optional items only emit a log
                    warning.
                  </p>
                  <ul class="space-y-2">
                    {#each requirements.requiredPreChecks as check}
                      <li
                        class="flex items-start gap-2 text-sm text-foreground"
                      >
                        <span
                          class="shrink-0 inline-flex items-center px-2 py-0.5 rounded text-xs font-medium {check.blocking
                            ? 'bg-primary/30 text-primary'
                            : 'bg-muted text-muted-foreground'}"
                        >
                          {check.blocking ? "Required" : "Optional"}
                        </span>
                        <div class="min-w-0 flex-1">
                          <span class="font-medium">{check.type}</span>
                          {#if check.minVersion}
                            <span
                              class="ml-2 text-xs px-1.5 py-0.5 bg-muted rounded text-foreground"
                              >min: {check.minVersion}</span
                            >
                          {/if}
                          {#if check.description}
                            <p class="text-sm text-muted-foreground mt-0.5">
                              {check.description}
                            </p>
                          {/if}
                        </div>
                      </li>
                    {/each}
                  </ul>
                </div>
              </div>
            </div>
          {/if}

          {#if serverProvisioningMode === "kombify-cloud" && lease}
            <div
              class="bg-success/10 border border-success/30 rounded-xl p-5 mb-6"
              data-testid="managed-server-provisioning-card"
            >
              <div class="flex items-start justify-between gap-4 mb-3">
                <div>
                  <h3 class="text-lg font-medium text-foreground mb-1">
                    {creationOperation === "add-server"
                      ? "Managed server requested"
                      : "Managed runtime ready"}
                  </h3>
                  <p class="text-sm text-muted-foreground">
                    {#if creationOperation === "add-server"}
                      The additional subscription VM request is recorded. It
                      will appear in Operations as enrollment reports back.
                    {:else}
                      StackKit deployed on the leased subscription VM. Values
                      below are returned by the runtime job - no demo data.
                    {/if}
                  </p>
                </div>
                <span class="badge badge-success shrink-0">
                  {creationOperation === "add-server" ? "Requested" : "Live"}
                </span>
              </div>
              <dl
                class="grid gap-3 sm:grid-cols-2 text-sm"
                data-testid="managed-lease-summary"
              >
                <div>
                  <dt
                    class="text-xs uppercase tracking-wide text-muted-foreground"
                  >
                    Lease ID
                  </dt>
                  <dd class="mt-1 font-mono text-foreground break-all">
                    {lease.id}
                  </dd>
                </div>
                {#if lease.provider}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      Provider
                    </dt>
                    <dd class="mt-1 text-foreground">{lease.provider}</dd>
                  </div>
                {/if}
                {#if lease.offering}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      Offering
                    </dt>
                    <dd class="mt-1 text-foreground">{lease.offering}</dd>
                  </div>
                {/if}
                {#if lease.host || lease.publicIp}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      Host
                    </dt>
                    <dd class="mt-1 font-mono text-foreground break-all">
                      {lease.host || lease.publicIp}
                    </dd>
                  </div>
                {/if}
                {#if lease.desiredState}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      Desired state
                    </dt>
                    <dd class="mt-1 text-foreground">{lease.desiredState}</dd>
                  </div>
                {/if}
                {#if lease.billingMode}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      Billing
                    </dt>
                    <dd class="mt-1 text-foreground">{lease.billingMode}</dd>
                  </div>
                {/if}
              </dl>
            </div>
          {:else if serverProvisioningMode === "connect-remote"}
            <div
              class="bg-primary/10 border border-primary/30 rounded-xl p-5 mb-6"
              data-testid="remote-server-provisioning-card"
            >
              <h3 class="text-lg font-medium text-foreground mb-2">
                Remote server connection
              </h3>
              <p class="text-sm text-muted-foreground">
                kombify will use the captured SSH connection details for the
                existing server{#if remoteServerHost}
                  <span>
                    at {remoteServerHost}{#if remoteServerPort}:{remoteServerPort}{/if}
                  </span>{/if}{#if remoteServerUser}
                  <span> as {remoteServerUser}</span>{/if}.
              </p>
            </div>
          {/if}

          {#if creationOperation === "stack" && serverProvisioningMode === "kombify-cloud" && proofRows.length > 0}
            <div
              class="bg-card border border-border rounded-xl p-5 mb-6"
              data-testid="runtime-proof-card"
            >
              <div class="mb-4">
                <h3 class="text-lg font-medium text-foreground">
                  Runtime proof
                </h3>
                <p class="text-sm text-muted-foreground">
                  These statuses come from Runtime Action responses and the
                  final e2e proof.
                </p>
              </div>
              <div class="grid gap-3 sm:grid-cols-2">
                {#each proofRows as row}
                  <div
                    class="rounded-lg border border-border bg-background/40 p-3"
                  >
                    <p class="text-sm font-medium text-foreground">
                      {row.label}
                    </p>
                    <div class="mt-2 flex items-center gap-2">
                      <span
                        class={row.status === "verified" ||
                        row.status === "applied" ||
                        row.status === "accepted" ||
                        row.status === "passed"
                          ? "badge badge-success"
                          : "badge badge-warning"}
                      >
                        {row.status}
                      </span>
                      {#if row.action}
                        <span class="text-xs text-muted-foreground truncate">
                          {row.action}
                        </span>
                      {/if}
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          {/if}

          {#if creationOperation === "stack" && serverProvisioningMode === "kombify-cloud"}
            {#if hasStackKitIdentityHandoff && stackKitHandoff}
              <div
                class="bg-success/10 border border-success/30 rounded-xl p-5 mb-6"
                data-testid="stackkit-identity-handoff-card"
              >
                <div class="flex items-start justify-between gap-4 mb-4">
                  <div>
                    <h3 class="text-lg font-medium text-foreground mb-2">
                      First login and recovery
                    </h3>
                    <p class="text-sm text-muted-foreground">
                      StackKit returned the owner login, login gateway, and
                      recovery references for this verified Cloud Kit rollout.
                    </p>
                  </div>
                  <span class="badge badge-success shrink-0">Ready</span>
                </div>

                <div class="grid gap-3 md:grid-cols-3">
                  <div
                    class="rounded-lg bg-card/70 border border-border p-3"
                    data-testid="stackkit-owner-identity"
                  >
                    <p class="text-xs text-muted-foreground mb-1">Owner</p>
                    <p class="text-sm text-foreground font-medium truncate">
                      {stackKitHandoff.ownerDisplayName ||
                        stackKitHandoff.ownerUsername}
                    </p>
                    {#if stackKitHandoff.ownerUsername}
                      <p class="text-xs text-muted-foreground truncate">
                        {stackKitHandoff.ownerUsername}
                      </p>
                    {/if}
                    {#if stackKitHandoff.ownerEmail}
                      <p class="text-xs text-muted-foreground truncate">
                        {stackKitHandoff.ownerEmail}
                      </p>
                    {/if}
                  </div>

                  <div class="rounded-lg bg-card/70 border border-border p-3">
                    <p class="text-xs text-muted-foreground mb-2">
                      Login gateway
                    </p>
                    <a
                      href={stackKitHandoff.loginGatewayUrl || "#"}
                      target="_blank"
                      rel="noopener noreferrer"
                      class="btn btn-secondary btn-sm w-full"
                      data-testid="stackkit-login-gateway-link"
                    >
                      {stackKitHandoff.loginGatewayLabel || "Open first login"}
                    </a>
                  </div>

                  <div class="rounded-lg bg-card/70 border border-border p-3">
                    <p class="text-xs text-muted-foreground mb-2">Wallet</p>
                    <div class="flex flex-wrap gap-2">
                      <a
                        href="/wallet#access"
                        class="btn btn-secondary btn-sm"
                        data-testid="wallet-access-handoff-link"
                      >
                        Access
                      </a>
                      <a
                        href="/wallet#recovery"
                        class="btn btn-secondary btn-sm"
                        data-testid="wallet-recovery-handoff-link"
                      >
                        Recovery
                      </a>
                    </div>
                    {#if stackKitHandoff.recoveryRef}
                      <p class="mt-2 text-xs text-muted-foreground truncate">
                        {stackKitHandoff.recoveryRef}
                      </p>
                    {/if}
                  </div>
                </div>
              </div>
            {:else}
              <div
                class="bg-amber-900/30 border border-amber-600/60 rounded-xl p-5 mb-6"
                data-testid="stackkit-handoff-missing-card"
              >
                <h3 class="text-lg font-semibold text-amber-100 mb-2">
                  StackKit identity handoff is missing
                </h3>
                <p class="text-sm text-amber-50">
                  The rollout completed, but the StackKit did not return owner
                  login, login gateway, and recovery outputs. Treat this as a
                  release blocker until the runtime action response includes
                  <span class="font-mono text-foreground">stackkit_outputs</span
                  >.
                </p>
              </div>
            {/if}
          {/if}

          {#if oneLinerPreviewRequired && !simulationPreviewUrl}
            <div
              class="bg-blue-950/30 border border-blue-600/60 rounded-xl p-5 mb-6"
              data-testid="oneliner-simulation-preview-card"
            >
              <h3 class="text-lg font-semibold text-blue-100 mb-2">
                Simulate demo preview
              </h3>
              <p class="text-sm text-blue-50">
                Your install command is ready. kombify-simulate is used as a
                temporary demo preview when available and is limited to one
                hour.
              </p>
              {#if simulationPreviewStatus}
                <p class="mt-3 text-xs text-blue-100">
                  Status: <span class="font-mono"
                    >{simulationPreviewStatus}</span
                  >
                </p>
              {/if}
            </div>
          {/if}

          <!-- Worker Installation Command -->
          {#if showInstallCommand && registrationToken && installCommand && !agentPairingRequired}
            <div class="bg-muted/70 rounded-xl p-6 mb-6">
              <h3 class="text-lg font-medium text-foreground mb-2">
                Install worker
              </h3>
              {#if oneLinerPreviewRequired}
                <p class="text-sm text-muted-foreground mb-4">
                  Run this command on the server or device that should become
                  the real target for this Homelab. Simulate may also provide a
                  one-hour demo preview of the planned StackKit.
                </p>
                {#if simulationPreviewUrl}
                  <a
                    href={simulationPreviewUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    class="inline-flex items-center text-sm text-primary hover:underline mb-4"
                    data-testid="simulation-preview-link"
                  >
                    Open one-hour Simulate demo preview
                  </a>
                {/if}
                {#if simulationPreviewExpiresAt}
                  <p class="text-xs text-muted-foreground mb-4">
                    Preview expires at {simulationPreviewExpiresAt}
                  </p>
                {:else if simulationPreviewUrl}
                  <p class="text-xs text-muted-foreground mb-4">
                    Preview lifetime is limited to one hour.
                  </p>
                {/if}
              {:else}
                <p class="text-sm text-muted-foreground mb-4">
                  Run this command on all servers you want to integrate into
                  your homelab:
                </p>
              {/if}

              <!-- Server URL Override -->
              <div class="mb-4">
                <label
                  for="server-url"
                  class="block text-xs text-muted-foreground mb-1"
                >
                  kombify-TechStack server URL (reachable by workers):
                </label>
                <input
                  id="server-url"
                  type="text"
                  bind:value={serverUrl}
                  class="w-full bg-card border border-border rounded px-3 py-2 text-sm text-foreground focus:outline-none focus:border-primary transition-colors"
                  placeholder="http://192.168.x.x:5261"
                />
              </div>

              <!-- Main install command -->
              <div class="relative mb-4">
                <pre
                  class="px-4 py-3 bg-card rounded-lg text-primary text-sm font-mono overflow-x-auto pr-12">{installCommand}</pre>
                <button
                  onclick={() => copyToClipboard(installCommand, "main")}
                  class="absolute right-2 top-1/2 -translate-y-1/2 px-2 py-1 bg-secondary hover:bg-accent text-white rounded transition-colors"
                  title="Copy"
                  aria-label="Copy installation command to clipboard"
                >
                  {#if copiedCommand === "main"}
                    <svg
                      class="w-4 h-4 text-green-400"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                    >
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        stroke-width="2"
                        d="M5 13l4 4L19 7"
                      />
                    </svg>
                  {:else}
                    <svg
                      class="w-4 h-4"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                    >
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        stroke-width="2"
                        d="M8 5H6a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2v-1M8 5a2 2 0 002 2h2a2 2 0 002-2M8 5a2 2 0 012-2h2a2 2 0 012 2m0 0h2a2 2 0 012 2v3m2 4H10m0 0l3-3m-3 3l3 3"
                      />
                    </svg>
                  {/if}
                </button>
              </div>

              <!-- Alternative commands -->
              <button
                onclick={() => (showAllCommands = !showAllCommands)}
                class="text-sm text-muted-foreground hover:text-foreground/80 flex items-center gap-1"
              >
                <svg
                  class="w-4 h-4 transition-transform {showAllCommands
                    ? 'rotate-180'
                    : ''}"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M19 9l-7 7-7-7"
                  />
                </svg>
                Alternative installation methods
              </button>

              {#if showAllCommands}
                <div class="mt-4 space-y-3">
                  {#each allInstallCommands as cmd}
                    <div class="bg-card/50 rounded-lg p-3">
                      <p class="text-xs text-muted-foreground mb-2">
                        {cmd.description}
                      </p>
                      <div class="relative">
                        <pre
                          class="text-xs font-mono text-foreground/80 overflow-x-auto pr-10">{cmd.command}</pre>
                        <button
                          onclick={() =>
                            copyToClipboard(cmd.command, cmd.platform)}
                          class="absolute right-1 top-1/2 -translate-y-1/2 p-1 hover:bg-secondary rounded"
                          title="Copy"
                        >
                          {#if copiedCommand === cmd.platform}
                            <svg
                              class="w-3 h-3 text-green-400"
                              fill="none"
                              viewBox="0 0 24 24"
                              stroke="currentColor"
                            >
                              <path
                                stroke-linecap="round"
                                stroke-linejoin="round"
                                stroke-width="2"
                                d="M5 13l4 4L19 7"
                              />
                            </svg>
                          {:else}
                            <svg
                              class="w-3 h-3 text-muted-foreground"
                              fill="none"
                              viewBox="0 0 24 24"
                              stroke="currentColor"
                            >
                              <path
                                stroke-linecap="round"
                                stroke-linejoin="round"
                                stroke-width="2"
                                d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                              />
                            </svg>
                          {/if}
                        </button>
                      </div>
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
          {/if}

          <button
            onclick={goToStack}
            class="btn btn-primary w-full py-3 rounded-xl"
            data-testid="continue-to-stackkit-rollout"
          >
            {serverProvisioningMode === "kombify-cloud"
              ? "Open operations"
              : "Review and start StackKit rollout"}
          </button>
        </div>
      {:else if hasFailed}
        <div
          class="bg-card/80 backdrop-blur-sm border border-destructive/50 rounded-2xl p-6 shadow-2xl"
        >
          <div class="mb-6">
            <div class="flex items-center gap-3 mb-2">
              <div
                class="w-12 h-12 rounded-full bg-destructive/20 flex items-center justify-center"
              >
                <svg
                  class="w-6 h-6 text-destructive"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M6 18L18 6M6 6l12 12"
                  />
                </svg>
              </div>
              <div>
                <h2 class="text-xl font-semibold text-foreground">
                  {handoffMissingFailure
                    ? "Rollout incomplete"
                    : "Creation failed"}
                </h2>
                <p class="text-muted-foreground text-sm">
                  {#if failedTask?.errorMessage}
                    {failedTask.errorMessage}
                  {:else}
                    An error occurred
                  {/if}
                </p>
              </div>
            </div>
          </div>

          {#if postLeaseManagedCloudFailure && lease}
            <div
              class="bg-primary/10 border border-primary/25 rounded-xl p-4 mb-6"
              data-testid="managed-lease-failure-summary"
            >
              <p class="text-sm font-medium text-foreground mb-2">
                Existing managed VM lease referenced
              </p>
              <p class="text-sm text-muted-foreground">
                Lease {lease.id}{lease.provider
                  ? ` · ${lease.provider}`
                  : ""}{lease.host || lease.publicIp
                  ? ` · ${lease.host || lease.publicIp}`
                  : ""}
              </p>
              <p class="text-sm text-muted-foreground mt-2">
                {#if stackKitArtifactOrRoutingFailure}
                  VPS provisioning completed. Only StackKit artifact, domain
                  routing, or later rollout work failed.
                {:else}
                  The rollout reached the existing managed VM.
                {/if}
                The exact retry endpoint validates this failed job and lease before
                continuing on the existing server.
              </p>
            </div>
          {/if}

          {#if retryError}
            <div class="bg-destructive/10 rounded-xl p-3 mb-6">
              <p class="text-sm text-destructive">{retryError}</p>
            </div>
          {/if}

          <!-- Detailed error info -->
          {#if failedTask?.errorDetails}
            <div
              class="bg-destructive/10 border border-destructive/30 rounded-xl p-4 mb-6"
            >
              <div class="flex items-center justify-between gap-3">
                <p class="text-destructive text-sm font-medium">
                  Error details
                </p>
                <button
                  type="button"
                  class="btn btn-secondary btn-sm"
                  data-testid="copy-error-details-button"
                  onclick={copyErrorDetails}
                >
                  {copiedErrorDetails ? "Copied" : "Copy error details"}
                </button>
              </div>
              <pre
                data-testid="error-details-text"
                class="mt-3 max-h-48 overflow-x-auto overflow-y-auto whitespace-pre-wrap rounded border border-destructive/20 bg-background p-3 text-xs text-foreground">{failedTask.errorDetails}</pre>
            </div>
          {/if}

          <!-- Intelligent troubleshooting from failed task -->
          {#if failedTask?.troubleshooting && failedTask.troubleshooting.length > 0}
            <div
              class="rounded-xl border border-warning/30 bg-warning/10 p-4 mb-6"
              data-testid="creation-troubleshooting"
            >
              <p
                class="text-foreground font-medium text-sm mb-3 flex items-center gap-2"
              >
                <svg
                  class="w-5 h-5 text-warning"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0014 18.469V19a2 2 0 11-4 0v-.531c0-.895-.356-1.754-.988-2.386l-.548-.547z"
                  />
                </svg>
                Troubleshooting
              </p>
              <ol
                class="text-sm text-foreground space-y-2 list-decimal list-inside"
              >
                {#each failedTask.troubleshooting as step}
                  <li class="leading-relaxed text-foreground/90">{step}</li>
                {/each}
              </ol>
            </div>
          {:else}
            <!-- Fallback suggestions if no specific troubleshooting available -->
            <div class="bg-muted/50 rounded-xl p-4 mb-6">
              <p class="text-sm text-muted-foreground mb-3">
                Possible solutions:
              </p>
              <ul class="text-sm text-foreground/80 space-y-2">
                <li class="flex items-start gap-2">
                  <span class="text-primary">•</span>
                  <span>Check your network connection</span>
                </li>
                <li class="flex items-start gap-2">
                  <span class="text-primary">•</span>
                  <span>Make sure the backend server is running</span>
                </li>
                <li class="flex items-start gap-2">
                  <span class="text-primary">•</span>
                  <span>Review your configuration settings</span>
                </li>
              </ul>
            </div>
          {/if}

          <div class="flex gap-3">
            <button
              onclick={handleFailurePrimaryAction}
              disabled={failurePrimaryActionDisabled}
              class="flex-1 py-3 px-6 bg-primary hover:bg-primary/80 text-primary-foreground font-medium rounded-xl transition-colors"
            >
              {failurePrimaryActionLabel}
            </button>
            <button
              onclick={goToStack}
              class="py-3 px-6 bg-secondary hover:bg-accent text-white font-medium rounded-xl transition-colors"
            >
              Operations
            </button>
          </div>
        </div>
      {:else if !isComplete}
        <!-- Progressive step details during creation -->
        <div class="space-y-4">
          {#if waitingForManagedRuntime}
            <section
              class="rounded-2xl border border-warning/30 bg-card/80 p-6 shadow-2xl"
              data-testid="waiting-enrollment-card"
              role="status"
              aria-live="polite"
            >
              <div
                class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between"
              >
                <div>
                  <p
                    class="text-xs font-semibold uppercase tracking-wide text-warning"
                  >
                    Provisioning in progress
                  </p>
                  <h2 class="mt-1 text-xl font-semibold text-foreground">
                    Server provisioning is still in progress
                  </h2>
                  <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
                    {#if waitingForProviderProvision}
                      The provider operation has not yet handed the existing
                      Managed Runtime over to the StackKit rollout. TechStack
                      scheduled the next exact-lease check.
                    {:else}
                      The Managed Runtime is not fully reachable for rollout
                      yet. The pending signal may be its address, credentials,
                      or enrollment. TechStack scheduled the next check.
                    {/if}
                  </p>
                  {#if nextResumeLabel}
                    <p
                      class="mt-3 text-xs text-muted-foreground"
                      data-testid="waiting-enrollment-next-resume"
                    >
                      Next scheduled check:
                      <time datetime={jobNextResumeAt}>{nextResumeLabel}</time>
                    </p>
                  {/if}
                  <p class="mt-3 max-w-2xl text-xs text-muted-foreground">
                    {#if jobResumeAvailableAt}
                      Starting at {formatNextResumeAt(jobResumeAvailableAt)},
                      the server can authorize a safe resume on the same VM.
                    {:else}
                      A manual resume is enabled only after server-side
                      validation.
                    {/if}
                    This does not create another VM.
                  </p>
                </div>
                <span
                  class="inline-flex shrink-0 items-center gap-2 rounded-full border border-warning/30 bg-warning/10 px-3 py-1 text-xs font-medium text-warning"
                  data-testid="waiting-enrollment-status"
                >
                  <span class="h-2 w-2 animate-pulse rounded-full bg-warning"
                  ></span>
                  Not reachable yet
                </span>
              </div>

              {#if currentStepMessage}
                <div
                  class="mt-5 rounded-lg border border-warning/20 bg-warning/10 px-3 py-2"
                >
                  <p class="text-xs font-medium text-warning">Current status</p>
                  <p class="mt-1 text-sm text-foreground">
                    {currentStepMessage}
                  </p>
                </div>
              {/if}

              <div
                class="mt-5 grid gap-2 sm:grid-cols-3"
                data-testid="waiting-enrollment-access"
              >
                <button
                  type="button"
                  disabled
                  aria-disabled="true"
                  class="btn btn-secondary btn-sm cursor-not-allowed opacity-50"
                  data-testid="waiting-dashboard-disabled"
                >
                  Dashboard
                </button>
                <button
                  type="button"
                  disabled
                  aria-disabled="true"
                  class="btn btn-secondary btn-sm cursor-not-allowed opacity-50"
                  data-testid="waiting-services-disabled"
                >
                  Services
                </button>
                <button
                  type="button"
                  disabled
                  aria-disabled="true"
                  class="btn btn-secondary btn-sm cursor-not-allowed opacity-50"
                  data-testid="waiting-access-disabled"
                >
                  Server access
                </button>
              </div>
              <p class="mt-3 text-xs text-muted-foreground">
                These access paths are enabled only after a real enrollment
                signal.
              </p>
              {#if waitingRecoveryAvailable}
                <div
                  class="mt-4 rounded-lg border border-info/30 bg-info/10 p-3"
                  data-testid="waiting-enrollment-recovery"
                >
                  <p class="text-xs text-muted-foreground">
                    The scheduled check is overdue. TechStack validates the
                    stack, source job, and exact lease together, then continues
                    only the rollout on the existing VM.
                  </p>
                  <button
                    type="button"
                    class="btn btn-primary btn-sm mt-3"
                    disabled={retryingRollout}
                    onclick={resumeOverdueManagedRuntime}
                    data-testid="waiting-enrollment-retry"
                  >
                    {retryingRollout
                      ? "Validating the existing VM..."
                      : waitingForProviderProvision
                        ? "Continue rollout on the existing VM"
                        : "Resume enrollment on the existing VM"}
                  </button>
                  {#if retryError}
                    <p
                      class="mt-2 text-xs text-destructive"
                      data-testid="waiting-enrollment-retry-error"
                    >
                      {retryError}
                    </p>
                  {/if}
                </div>
              {/if}
            </section>
          {:else if agentPairingWaiting}
            <section
              class="rounded-2xl border border-primary/30 bg-card/80 p-6 shadow-2xl"
              data-testid="guard-pairing-card"
            >
              <div
                class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between"
              >
                <div>
                  <p
                    class="text-xs font-semibold uppercase tracking-wide text-primary"
                  >
                    Waiting for real connection
                  </p>
                  <h2 class="mt-1 text-xl font-semibold text-foreground">
                    Pairing command ready
                  </h2>
                  <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
                    Run this one-liner on the additional server. The completed
                    registration job only created the pairing token; TechStack
                    will show “Server connected” only after the outbound Guard
                    reports a fresh heartbeat and the server projection is
                    healthy.
                  </p>
                </div>
                <span
                  class="inline-flex shrink-0 items-center gap-2 rounded-full border border-warning/30 bg-warning/10 px-3 py-1 text-xs font-medium text-warning"
                  data-testid="guard-pairing-status"
                >
                  <span class="h-2 w-2 rounded-full bg-warning"></span>
                  Not connected yet
                </span>
              </div>

              {#if remoteServerHost}
                <div
                  class="mt-5 rounded-lg border border-border bg-muted/40 p-3"
                >
                  <p class="text-xs text-muted-foreground">
                    Planned remote target
                  </p>
                  <p class="mt-1 font-mono text-sm text-foreground">
                    {remoteServerUser
                      ? `${remoteServerUser}@`
                      : ""}{remoteServerHost}{remoteServerPort
                      ? `:${remoteServerPort}`
                      : ""}
                  </p>
                  <p class="mt-1 text-xs text-muted-foreground">
                    The SSH details are planning metadata. The current Guard
                    connection is outbound HTTPS and starts with the command
                    below.
                  </p>
                </div>
              {/if}

              {#if registrationToken && installCommand}
                <div class="mt-5">
                  <label
                    for="pairing-server-url"
                    class="block text-xs font-medium text-muted-foreground"
                  >
                    TechStack URL reachable from the server
                  </label>
                  <input
                    id="pairing-server-url"
                    type="text"
                    bind:value={serverUrl}
                    class="mt-1 w-full rounded-lg border border-border bg-background px-3 py-2 text-sm text-foreground focus:border-primary focus:outline-none"
                  />
                </div>
                <pre
                  class="mt-4 max-w-full overflow-x-auto whitespace-pre-wrap break-all rounded-lg border border-border bg-background p-4 pr-4 font-mono text-sm leading-relaxed text-primary"
                  data-testid="server-registration-command">{installCommand}</pre>
                <button
                  type="button"
                  onclick={() => copyToClipboard(installCommand, "pairing")}
                  disabled={pairingTokenExpired}
                  class="btn btn-primary mt-3 w-full sm:w-auto"
                  data-testid="copy-pairing-command"
                >
                  {#if copiedCommand === "pairing"}
                    Pairing command copied
                  {:else if copiedCommand === "error:pairing"}
                    Copy failed — select the command above
                  {:else}
                    Copy pairing command
                  {/if}
                </button>
                {#if pairingTokenExpiresAt}
                  <p class="mt-3 text-xs text-muted-foreground">
                    Pairing token expires at
                    <span class="font-mono">{pairingTokenExpiresAt}</span>.
                  </p>
                {/if}
                {#if pairingTokenExpired}
                  <div
                    class="mt-4 rounded-lg border border-destructive/30 bg-destructive/10 p-3"
                    role="alert"
                  >
                    <p class="text-sm font-medium text-destructive">
                      This pairing token has expired.
                    </p>
                    <p class="mt-1 text-xs text-muted-foreground">
                      Return to Add Server to generate a new one-liner. This
                      page will not claim a connection from an expired command.
                    </p>
                    <button
                      type="button"
                      class="btn btn-secondary btn-sm mt-3"
                      onclick={retryFromStart}
                    >
                      Generate new command
                    </button>
                  </div>
                {/if}
              {:else}
                <div
                  class="mt-5 rounded-lg border border-destructive/30 bg-destructive/10 p-4"
                  role="alert"
                >
                  <p class="text-sm font-medium text-destructive">
                    Pairing command unavailable
                  </p>
                  <p class="mt-1 text-xs text-muted-foreground">
                    The registration job did not return a pairing token. Return
                    to Add Server and prepare the connection again.
                  </p>
                </div>
              {/if}

              {#if connectionPollError}
                <p
                  class="mt-4 rounded-lg border border-warning/30 bg-warning/10 p-3 text-xs text-muted-foreground"
                  role="status"
                >
                  {connectionPollError}
                </p>
              {/if}
            </section>
          {/if}

          <!-- Current step detail card -->
          {#if currentStepInfo && !agentPairingWaiting && !waitingForManagedRuntime}
            {#key currentTask?.id}
              <div
                class="bg-card/80 backdrop-blur-sm border border-primary/30 rounded-2xl p-6 shadow-2xl"
              >
                <div class="flex items-start gap-4">
                  <div
                    class="w-10 h-10 rounded-full bg-primary/20 flex items-center justify-center shrink-0"
                  >
                    <svg
                      class="w-5 h-5 text-primary animate-spin"
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="2"
                    >
                      <circle cx="12" cy="12" r="10" stroke-opacity="0.25" />
                      <path
                        d="M12 2a10 10 0 0 1 10 10"
                        stroke-linecap="round"
                      />
                    </svg>
                  </div>
                  <div class="flex-1 min-w-0">
                    <p class="text-xs text-primary font-medium mb-1">
                      Step {completedCount + 1} of {tasks.length}
                    </p>
                    <h3 class="text-lg font-semibold text-foreground mb-2">
                      {currentStepInfo.title}
                    </h3>
                    <p class="text-sm text-muted-foreground leading-relaxed">
                      {currentStepInfo.description}
                    </p>
                    {#if currentStepMessage}
                      <div
                        class="mt-4 rounded-lg border border-primary/20 bg-primary/10 px-3 py-2"
                        aria-live="polite"
                      >
                        <p class="text-xs font-medium text-primary">
                          Current status
                        </p>
                        <p class="mt-1 text-sm text-foreground">
                          {currentStepMessage}
                        </p>
                      </div>
                    {/if}
                    <p
                      class="text-xs text-muted-foreground/70 mt-3 leading-relaxed"
                    >
                      {currentStepInfo.detail}
                    </p>
                  </div>
                </div>
              </div>
            {/key}
          {/if}

          <!-- Completed steps summary -->
          {#if completedCount > 0}
            <div
              class="bg-card/80 backdrop-blur-sm border border-border rounded-2xl p-5 shadow-2xl"
            >
              <p class="text-xs text-muted-foreground font-medium mb-3">
                Completed
              </p>
              <div class="space-y-2">
                {#each tasks.filter((t) => t.status === "completed") as done}
                  {@const info = STEP_DETAILS[done.id]}
                  <div class="flex items-center gap-3">
                    <svg
                      class="w-4 h-4 text-green-500 shrink-0"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                      stroke-width="2"
                    >
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        d="M5 13l4 4L19 7"
                      />
                    </svg>
                    <span class="text-sm text-muted-foreground">
                      {info?.title ?? done.label}
                    </span>
                  </div>
                {/each}
              </div>
            </div>
          {/if}

          <!-- What happens next -->
          <div class="bg-card/60 border border-border/50 rounded-2xl p-5">
            <p class="text-xs text-muted-foreground font-medium mb-2">
              After creation
            </p>
            <p class="text-sm text-muted-foreground leading-relaxed">
              {#if creationOperation === "add-server" && serverProvisioningMode === "kombify-cloud"}
                The additional managed server is added to this TechStack and
                will appear in the server dashboard once enrollment reports
                back.
              {:else if creationOperation === "add-server" && agentPairingRequired}
                After you run the one-liner, the outbound Guard enrolls with
                this TechStack. The dashboard only marks it connected after a
                fresh heartbeat is present in the server projection.
              {:else if creationOperation === "add-server"}
                The additional server is registered against this TechStack and
                can then receive StackKit services.
              {:else if serverProvisioningMode === "kombify-cloud"}
                kombify will provision the subscription server and then continue
                with the StackKit rollout.
              {:else if serverProvisioningMode === "connect-remote"}
                kombify will use the remote SSH configuration captured in the
                wizard.
              {:else}
                You will receive a worker installation command to connect your
                servers to this stack.
              {/if}
            </p>
          </div>
        </div>
      {/if}
    </div>
  </div>
</div>
