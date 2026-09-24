import { tr } from "#lib/i18n.svelte.js";
import { goto } from "$app/navigation";
import { page } from "$app/state";
import { getClientBootstrap } from "#lib/client/bootstrap.js";
import * as Sentry from "@sentry/sveltekit";
import { sanitizeSensitiveText } from "#lib/security/sensitive-data.js";
import { getJob, streamJobProgress } from "#lib/api/jobs.js";
import { renewNodePairing } from "#lib/api/pairing.js";
import { getBestPublicServerUrl } from "#lib/api/client.js";
import {
  deployStack,
  provisionStack,
  retryStackRollout,
  resumeStackEnrollment,
  resumeRemoteEnrollment,
  type StackJobAcceptedResponse,
} from "#lib/api/stacks.js";
import {
  createWizardRun,
  getActiveWizardRun,
  wizardRunNeedsAttention,
  type WizardRunRequest,
  type WizardRunResponse,
  type WizardApplianceReceipt,
} from "#lib/api/wizardRuns.js";
import { ApiRequestError } from "#lib/api/client.js";
import { parseApiError } from "#lib/api/errors.js";
import {
  listCanonicalServers,
  getCanonicalServer,
  type CanonicalServer,
} from "#lib/api/registry.js";
import { getNetworks } from "#lib/discovery/api.js";
import { getWorkerRegistryUrl } from "#lib/api/tunnel.js";
import { retryDispatchFor } from "#lib/support/server-outcome.js";
import {
  requiresGuardConnection,
  readApplianceReceipt,
} from "#lib/wizard/rollout-continuation.js";
import {
  restoreJoinServerBaseline,
  selectGuardConnectedServer,
  authoritativeConnectRemoteReady,
  resolvePlannedServerTarget,
  serverHasFreshGuardHeartbeat,
} from "#lib/wizard/guard-connection.js";
import { appVersion } from "#lib/config.js";
import { authStore } from "#lib/stores/auth.svelte.js";
import {
  buildTechstackCreationEventProperties,
  createPostHogClient,
  toTechstackAnalyticsUser,
  type TechstackCreationEventInput,
} from "#lib/analytics/posthog.js";
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
} from "#lib/wizard/index.js";
import { clearJoinWizardIdempotencyKeys } from "#lib/wizard/idempotency-keys.js";
import {
  isWizardIdempotencyConflict,
  parseWizardConflictRecovery,
} from "#lib/wizard/wizard-conflict-recovery.js";
import {
  asRecord,
  buildFailureDetails,
  buildFailureFallback,
  extractLeaseSummary,
  extractStackKitIdentityHandoff,
  formatNextResumeAt,
  getBoolean,
  getString,
  hasVerifiedRuntimeResult,
  isLocalhostHost,
  isRunningJobStatus,
  isServerProvisioningMode,
  normalizeCreationOperation,
  normalizeJobResult,
  sanitizeSentrySummary,
} from "./creation-results.js";
import type {
  LeaseSummary,
  RuntimeProofEntry,
  StackKitIdentityHandoff,
} from "./creation-results.js";

type WizardRunSubmission = {
  request: WizardRunRequest;
  idempotencyKey: string;
};

const runtimeTaskIds = new Set(RUNTIME_TASKS.map((task) => task.id));

let stackName = $state("");
let stackId = $state("");
let jobId = $state("");
let applianceReceipt = $state<WizardApplianceReceipt>();
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
let resumeAvailableLabel = $derived(formatNextResumeAt(jobResumeAvailableAt));
let pairingTokenExpiresAt = $state("");
let pairingStartedAt = $state("");
let expectedDeviceName = $state("");
let plannedServerId = $state("");
let guardConnectionWaitStartedAt = $state(0);
let existingServerIds = new Set<string>();
let existingServerBaselineKnown = false;
let connectedServer = $state<CanonicalServer | null>(null);
let connectionPollError = $state("");
let connectionPollingInProgress = false;
let connectionPollingInterval: ReturnType<typeof setInterval> | null = null;
let remoteEnrollmentPollingInterval: ReturnType<typeof setInterval> | null =
  null;
let connectionClock = $state(Date.now());
const CONNECTION_POLL_INTERVAL = 3000;
const GUARD_HEARTBEAT_FRESH_MS = 90_000;
const GUARD_HEARTBEAT_FUTURE_SKEW_MS = 30_000;
const CONNECT_REMOTE_GUARD_WAIT_MS = 5 * 60_000;
let agentPairingRequired = $derived(
  requiresGuardConnection(creationOperation, serverProvisioningMode),
);
let agentPairingWaiting = $derived(
  agentPairingRequired && backendCompleted && !connectedServer,
);
let pairingJobId = $state("");
let remoteEnrollmentState = $state("");
let remoteEnrollmentFailed = $state(false);
let remoteEnrollmentActive = $derived(
  serverProvisioningMode === "connect-remote" &&
    agentPairingRequired &&
    !connectedServer &&
    Boolean(pairingJobId) &&
    !remoteEnrollmentFailed &&
    remoteEnrollmentState !== "completed" &&
    remoteEnrollmentState !== "success" &&
    remoteEnrollmentState !== "failed" &&
    remoteEnrollmentState !== "error",
);
let remoteEnrollmentSucceeded = $derived(
  remoteEnrollmentState === "completed" || remoteEnrollmentState === "success",
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
// Transient poll failures back off up to a minute instead of stopping the
// loop: the page stays resumable and a recovery on the backend is observed.
const MAX_POLL_BACKOFF_MS = 60_000;
let nextPollAttemptAt = 0;
// A dropped SSE stream is re-armed with bounded backoff; polling keeps the
// page current while the stream is down.
const SSE_RETRY_BASE_MS = 5_000;
const SSE_RETRY_MAX_MS = 60_000;
let streamRetryAttempt = 0;
let streamRetryTimer: ReturnType<typeof setTimeout> | null = null;
// While the SSE stream delivers change notifications, polling drops to a
// slow safety net; a stream failure restores the fast interval.
const SSE_SAFETY_POLL_INTERVAL = 15_000;
// Job IDs are durable references; issued credentials stay in component memory.
let pairingTokenIssued = false;
let pairingRecoveryBusy = $state(false);
let pairingRecoveryError = $state("");
let remoteEnrollmentMessage = $state("");
let remoteEnrollmentStep = $state("");
let remoteEnrollmentProgress = $state(0);
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
let wizardIdempotencyConflictFailed = $state(false);
let jobRetryable = $state<boolean | undefined>(undefined);
let overallProgress = $derived(
  agentPairingRequired
    ? Math.round(
        ((tasks.filter((t) => t.status === "completed").length +
          (connectedServer ? 1 : 0)) /
          (tasks.length + 1)) *
          100,
      )
    : Math.round(
        (tasks.filter((t) => t.status === "completed").length / tasks.length) *
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
// A create/provision job can finish before a StackKit rollout has begun.
let rolloutJobDetected = $derived(
  jobType === "deploy" ||
    runtimePhase === "deployed" ||
    runtimePhase === "verified",
);
// Installation and identity activation are separate real outcomes. The
// completion screen reads the current identity status from its owner API;
// legacy metadata is not a substitute for an enrolled passkey.
let handoffMissingFailure = $derived(
  backendCompleted &&
    creationOperation === "stack" &&
    serverProvisioningMode === "kombify-cloud" &&
    !hasVerifiedRuntimeProof,
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
    ? "Additional Node"
    : stackName || "Creating StackKit deployment",
);
let postLeaseManagedCloudFailure = $derived(
  creationOperation === "stack" &&
    serverProvisioningMode === "kombify-cloud" &&
    Boolean(stackId) &&
    hasManagedLeaseReference &&
    isPostLeaseRuntimeTask(failedTaskId),
);
let stackKitArtifactOrRoutingFailure = $derived(
  postLeaseManagedCloudFailure && isStackKitArtifactOrRoutingTask(failedTaskId),
);
let failureRetryDispatch = $derived(
  retryDispatchFor({
    job_id: jobId,
    type: jobType,
    lease_id: lease?.id,
    retryable: jobRetryable,
  }),
);
let connectRemoteConnectionReady = $derived(
  serverProvisioningMode === "connect-remote" &&
    (Boolean(connectedServer) || remoteEnrollmentSucceeded),
);
let connectRemoteStackKitFailed = $derived(
  serverProvisioningMode === "connect-remote" &&
    hasFailed &&
    connectRemoteConnectionReady &&
    !remoteEnrollmentFailed,
);
let failurePrimaryActionLabel = $derived(
  retryingRollout
    ? "Retrying..."
    : wizardIdempotencyConflictFailed
      ? "Start fresh attempt"
      : connectRemoteStackKitFailed && failureRetryDispatch
        ? failureRetryDispatch.kind === "rollout"
          ? "Continue StackKit rollout on connected Node"
          : failureRetryDispatch.kind === "deploy"
            ? "Continue StackKit deployment on connected Node"
            : "Continue StackKit on connected Node"
        : failureRetryDispatch?.kind === "rollout"
          ? "Retry rollout"
          : failureRetryDispatch?.kind === "deploy"
            ? "Retry deployment"
            : failureRetryDispatch?.kind === "provision"
              ? "Retry server request"
              : "Try again",
);
let failurePrimaryActionDisabled = $derived(retryingRollout);
let failurePrimaryActionAvailable = $derived(
  wizardIdempotencyConflictFailed || jobRetryable !== false,
);
let completionTitle = $derived(
  creationOperation === "add-server"
    ? agentPairingRequired
      ? "Node connected"
      : serverProvisioningMode === "kombify-cloud"
        ? "Managed Node requested"
        : "Node registration ready"
    : serverProvisioningMode === "hypervisor"
      ? tr("wizard.server.hypervisor.connected")
      : serverProvisioningMode === "kombify-cloud"
        ? tr("wizard.identity.installationComplete")
        : serverProvisioningMode === "connect-remote"
          ? "Configuration prepared"
          : "Node connection ready",
);
let completionSubtitle = $derived(
  creationOperation === "add-server"
    ? agentPairingRequired
      ? `${connectedServer?.name || "The additional Node"} reported a fresh Guard heartbeat and is visible in the Node projection.`
      : serverProvisioningMode === "kombify-cloud"
        ? "kombify requested the additional managed Node. You can continue from the dashboard while enrollment finishes."
        : "The additional Node registration is ready for the existing Homelab."
    : serverProvisioningMode === "kombify-cloud"
      ? tr("wizard.identity.installationDescription")
      : serverProvisioningMode === "connect-remote"
        ? "Your remote Node configuration is ready for rollout"
        : serverProvisioningMode === "hypervisor"
          ? tr("wizard.server.hypervisor.connectedDetail")
          : "Connect your Node to continue the rollout",
);
let dashboardHref = $derived(() => {
  const params = ["phase=review"];
  if (stackId) params.unshift(`stack=${encodeURIComponent(stackId)}`);
  return `/dashboard?${params.join("&")}`;
});
let monitoringHref = $derived(() => {
  const params = ["focus=servers"];
  if (stackId) params.unshift(`stack_id=${encodeURIComponent(stackId)}`);
  return `/monitoring?${params.join("&")}`;
});
let servicesHref = $derived(() => {
  return stackId
    ? `/services?kit_deployment_id=${encodeURIComponent(stackId)}`
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

function applyJoinServerBaseline(existingServerIdsValue: unknown) {
  const baseline = restoreJoinServerBaseline(existingServerIdsValue);
  if (!baseline.known) return;
  existingServerBaselineKnown = true;
  existingServerIds = baseline.ids;
}

function applyPlannedServerTarget(source: Record<string, unknown>) {
  const next = resolvePlannedServerTarget(source);
  if (next) plannedServerId = next;
}

function usesAuthoritativeConnectRemoteTarget(): boolean {
  return (
    serverProvisioningMode === "connect-remote" &&
    Boolean(plannedServerId.trim())
  );
}

function guardHeartbeatFreshness() {
  return {
    connectionClock,
    freshMs: GUARD_HEARTBEAT_FRESH_MS,
    futureSkewMs: GUARD_HEARTBEAT_FUTURE_SKEW_MS,
    pairingStartedAt,
  };
}

function hasFreshGuardHeartbeat(server: CanonicalServer): boolean {
  return serverHasFreshGuardHeartbeat(
    server,
    stackId,
    guardHeartbeatFreshness(),
  );
}

function selectConnectedServer(
  servers: CanonicalServer[],
): CanonicalServer | null {
  return selectGuardConnectedServer({
    servers,
    stackId,
    creationOperation,
    existingServerBaselineKnown,
    existingServerIds,
    expectedDeviceName,
    remoteServerHost,
    ...guardHeartbeatFreshness(),
  });
}

function stopRemoteEnrollmentPolling() {
  if (remoteEnrollmentPollingInterval) {
    clearInterval(remoteEnrollmentPollingInterval);
    remoteEnrollmentPollingInterval = null;
  }
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

function maybeExpireConnectRemoteGuardWait() {
  if (
    !usesAuthoritativeConnectRemoteTarget() ||
    !remoteEnrollmentSucceeded ||
    connectedServer ||
    !guardConnectionWaitStartedAt
  ) {
    return;
  }
  if (
    Date.now() - guardConnectionWaitStartedAt <
    CONNECT_REMOTE_GUARD_WAIT_MS
  ) {
    return;
  }
  connectionPollError =
    "Guard has not reported a fresh heartbeat for this Node yet. Retry SSH enrollment on the saved connection, or verify the agent is running on the server.";
}

function guardConnectionPollingReady(): boolean {
  if (backendCompleted) return true;
  return (
    serverProvisioningMode === "connect-remote" && remoteEnrollmentSucceeded
  );
}

async function pollAuthoritativeConnectRemoteServer() {
  const server = await getCanonicalServer(plannedServerId);
  connectionPollError = "";
  if (
    authoritativeConnectRemoteReady(
      server,
      plannedServerId,
      stackId,
      guardHeartbeatFreshness(),
    )
  ) {
    connectedServer = server;
    registrationToken = "";
    return;
  }
  if (connectedServer) {
    expireConnectedServerWithoutFreshHeartbeat();
  }
  maybeExpireConnectRemoteGuardWait();
}

async function pollGuardConnection() {
  // Advance and apply the local heartbeat TTL even while an earlier registry
  // request is still in flight. A hung or failing projection must never keep
  // a cached connection claim alive beyond the canonical freshness window.
  connectionClock = Date.now();
  expireConnectedServerWithoutFreshHeartbeat();
  if (
    !agentPairingRequired ||
    !guardConnectionPollingReady() ||
    !stackId ||
    connectionPollingInProgress
  ) {
    return;
  }
  connectionPollingInProgress = true;
  try {
    if (usesAuthoritativeConnectRemoteTarget()) {
      await pollAuthoritativeConnectRemoteServer();
      return;
    }
    const servers = await listCanonicalServers(stackId || undefined);
    connectionPollError = "";
    const nextConnectedServer = selectConnectedServer(servers);
    if (nextConnectedServer) {
      connectedServer = nextConnectedServer;
      registrationToken = "";
    } else if (connectedServer) {
      expireConnectedServerWithoutFreshHeartbeat();
    }
  } catch {
    connectionPollError = usesAuthoritativeConnectRemoteTarget()
      ? "The reserved Node could not be checked. Retry SSH enrollment on the saved connection once the server inventory is available again."
      : "The Node projection could not be checked. The pairing command remains available, but this page will not claim a connection without a fresh Guard heartbeat.";
  } finally {
    connectionPollingInProgress = false;
  }
}

function startConnectionPolling() {
  if (
    !agentPairingRequired ||
    !guardConnectionPollingReady() ||
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

function startRemoteEnrollmentPolling() {
  if (
    serverProvisioningMode !== "connect-remote" ||
    !pairingJobId ||
    remoteEnrollmentSucceeded ||
    remoteEnrollmentFailed ||
    remoteEnrollmentPollingInterval
  ) {
    return;
  }
  void pollRemoteEnrollmentJob();
  remoteEnrollmentPollingInterval = setInterval(
    pollRemoteEnrollmentJob,
    CONNECTION_POLL_INTERVAL,
  );
}

// Poll job status from API with exponential backoff on errors
async function pollJobStatus() {
  if (!jobId || pollingInProgress) return;
  if (Date.now() < nextPollAttemptAt) return;
  pollingInProgress = true;
  connectionClock = Date.now();

  try {
    const job = await getJob(jobId);
    pollErrorCount = 0; // Reset on success
    nextPollAttemptAt = 0;

    if (job) {
      jobType = job.type || jobType;
      const jobStatus = job.state || "pending";
      jobState = jobStatus;
      jobRetryable = job.retryable;
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
          error: "Managed Node request is still running",
          error_details:
            "The Add Node request has been running for over 2 minutes. This path should only request or prepare the additional Node, not run the full StackKit rollout. Open Operations to check whether the Node request was created, then retry Add Node if no new Node appears.",
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
          string | undefined;
        const legacyErrorMessage = (job as any)?.error_message as
          string | undefined;
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
          stackId,
        );

        captureCreationFailure(job, failureError, failureDetails);

        tasks = updateTasksWithError(tasks, {
          step: job.step,
          error: failureError,
          error_details: failureDetails,
          state: job.state,
          user_guidance: job.user_guidance,
        });
        stopPolling();
        return;
      }

      // Reflect the backend's reported step transitions on the visible task
      // list - never fabricate progress past what the backend has emitted.
      tasks = updateTasksFromJob(tasks, job);

      await pollRemoteEnrollmentJob();

      if (jobStatus === "completed" || jobStatus === "success") {
        backendCompleted = true;
        const resultRecord = result || {};
        const verifiedRuntime = hasVerifiedRuntimeResult(resultRecord);
        const identityHandoff = extractStackKitIdentityHandoff(resultRecord);
        const missingCloudHandoff =
          creationOperation === "stack" &&
          serverProvisioningMode === "kombify-cloud" &&
          !verifiedRuntime;
        if (missingCloudHandoff) {
          tasks = updateTasksWithError(tasks, {
            step: "verify_rollout",
            error: "StackKit runtime verification missing",
            error_details:
              "The completed runtime job did not return verified runtime evidence. Retry the rollout to verify the installation.",
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
              errorCode: "runtime_verification_missing",
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
        startRemoteEnrollmentPolling();
        if (serverProvisioningMode !== "connect-remote") {
          ensurePairingCommandPrepared();
        }
        return;
      }

      // A canceled job must not leave the page in an endless spinner: surface
      // it as a failure with the normal retry path.
      if (job.state === "canceled") {
        tasks = updateTasksWithError(tasks, {
          step: job.step || tasks[0]?.id,
          error: "Job was canceled",
          error_details:
            "The backend canceled this job before it completed. Retry to continue from the last safe checkpoint.",
          state: job.state,
          user_guidance: job.user_guidance,
        });
        stopPolling();
        return;
      }
    }
  } catch (e) {
    console.error("Failed to poll job status:", e);

    // Session expiry is not a network problem: stop and say so instead of
    // telling the user to check their connection.
    const authFailure =
      e instanceof ApiRequestError && (e.status === 401 || e.status === 403);
    if (authFailure) {
      tasks = updateTasksWithError(tasks, {
        error: "Session expired",
        error_details:
          "Your session is no longer valid, so the creation progress cannot be checked. Sign in again and reopen this creation to continue monitoring it.",
      });
      stopPolling();
      return;
    }

    pollErrorCount += 1;
    // Keep trying with bounded exponential backoff; only tell the user after
    // repeated failures, and keep the background retries running.
    nextPollAttemptAt =
      Date.now() +
      Math.min(
        BASE_POLL_INTERVAL * 2 ** (pollErrorCount - 1),
        MAX_POLL_BACKOFF_MS,
      );
    if (pollErrorCount >= MAX_POLL_ERRORS) {
      tasks = updateTasksWithError(tasks, {
        error: "Connection to server lost",
        error_details: `After ${MAX_POLL_ERRORS} failed attempts, the backend could not be reached. The page keeps retrying with backoff; you can also check your network connection and reload to resume.`,
      });
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
  if (streamRetryTimer) {
    clearTimeout(streamRetryTimer);
    streamRetryTimer = null;
  }
  streamRetryAttempt = 0;
  nextPollAttemptAt = 0;
  startRemoteEnrollmentPolling();
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
      streamRetryAttempt = 0;
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
      scheduleStreamRetry();
    },
  });
  // The handlers may fire synchronously (constructor failure); do not
  // resurrect a stream reference the fallback already cleared.
  if (!fellBack) {
    jobStreamClose = close;
  }
}

// A dropped stream falls back to fast polling immediately; the stream itself
// is re-armed with bounded backoff so a long creation keeps its accelerator.
function scheduleStreamRetry() {
  if (pageDestroyed || streamRetryTimer || !jobId) return;
  if (backendCompleted || hasFailed) return;
  const delay = Math.min(
    SSE_RETRY_BASE_MS * 2 ** streamRetryAttempt,
    SSE_RETRY_MAX_MS,
  );
  streamRetryAttempt += 1;
  streamRetryTimer = setTimeout(() => {
    streamRetryTimer = null;
    if (pageDestroyed || backendCompleted || hasFailed) return;
    startJobStream();
  }, delay);
}

async function recoverPairingCommand() {
  if (pairingRecoveryBusy || !stackId || !showInstallCommand) return;
  pairingRecoveryBusy = true;
  pairingRecoveryError = "";
  try {
    const job = await getJob(pairingJobId || jobId);
    const metadata = normalizeJobResult(job.result);
    const pairing = await renewNodePairing(stackId, metadata || {});
    if (pageDestroyed) return;
    registrationToken = pairing.token;
    pairingTokenExpiresAt = pairing.expires_at;
    pairingTokenIssued = true;
    if (!pairingStartedAt) pairingStartedAt = new Date().toISOString();
  } catch (error) {
    pairingRecoveryError = sanitizeSensitiveText(
      error instanceof Error
        ? error.message
        : "Could not prepare the connection command.",
    );
  } finally {
    pairingRecoveryBusy = false;
  }
}

function ensurePairingCommandPrepared() {
  if (
    pairingRecoveryBusy ||
    registrationToken ||
    !showInstallCommand ||
    serverProvisioningMode === "connect-remote" ||
    !stackId ||
    !backendCompleted ||
    !agentPairingRequired ||
    connectedServer
  ) {
    return;
  }
  void recoverPairingCommand();
}

async function pollRemoteEnrollmentJob() {
  if (serverProvisioningMode !== "connect-remote" || !pairingJobId) {
    return;
  }
  try {
    const job = await getJob(pairingJobId);
    if (!job) return;
    remoteEnrollmentState = job.state || remoteEnrollmentState;
    remoteEnrollmentStep = job.step || job.current_step || remoteEnrollmentStep;
    remoteEnrollmentMessage =
      job.message ||
      remoteEnrollmentMessage ||
      "Connecting to your Node over SSH…";
    remoteEnrollmentProgress =
      typeof job.progress === "number"
        ? job.progress
        : remoteEnrollmentProgress;
    if (job.state === "failed" || job.state === "error") {
      remoteEnrollmentFailed = true;
      pairingRecoveryError = sanitizeSensitiveText(
        job.error || job.message || "Remote SSH enrollment failed.",
      );
      return;
    }
    if (job.state === "completed" || job.state === "success") {
      remoteEnrollmentState = job.state;
      remoteEnrollmentFailed = false;
      const resultRecord = asRecord(job.result) ?? {};
      applyPlannedServerTarget(resultRecord);
      stopRemoteEnrollmentPolling();
      if (creationOperation === "add-server" && pairingJobId === jobId) {
        backendCompleted = true;
      }
      if (!pairingStartedAt) {
        pairingStartedAt = new Date().toISOString();
      }
      if (!guardConnectionWaitStartedAt) {
        guardConnectionWaitStartedAt = Date.now();
      }
      startConnectionPolling();
    }
  } catch (error) {
    pairingRecoveryError = sanitizeSensitiveText(
      error instanceof Error
        ? error.message
        : "Could not check remote SSH enrollment progress.",
    );
  }
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
      (authStore.deploymentMode === "saas" ? "saas-embedded" : "selfhost-oss"),
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

function applyJobResult(result: Record<string, unknown>) {
  // A directly issued token keeps its own expiry; jobs carry only metadata.
  if (!pairingTokenIssued && (!pairingJobId || pairingJobId === jobId)) {
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
  applyJoinServerBaseline(result.existing_server_ids);
  applyPlannedServerTarget(result);
}

// The owner-scoped run ledger is the Wizard resume authority. Explicit URL
// parameters identify an immediate handoff; a dashboard resume without
// parameters adopts only a run that still needs attention.
async function adoptActiveWizardRun(expectedJobId = ""): Promise<boolean> {
  try {
    const run = await getActiveWizardRun();
    if (!run || (!expectedJobId && !wizardRunNeedsAttention(run))) {
      return false;
    }
    const result = (run.result ?? {}) as Record<string, unknown>;
    applianceReceipt = readApplianceReceipt(result.appliance);
    const resume = asRecord(result.resume_context);
    const ledgerJobId = run.job_id || run.pairing_job_id || "";
    if (!ledgerJobId) return false;
    if (
      expectedJobId &&
      expectedJobId !== run.job_id &&
      expectedJobId !== run.pairing_job_id
    ) {
      return false;
    }
    jobId = ledgerJobId;
    stackId = stackId || run.kit_deployment_id || "";
    const resultName = typeof result.name === "string" ? result.name : "";
    if (resultName) stackName = resultName;
    if (result.kit_assignment_mode === "join") {
      creationOperation = "add-server";
    }
    const mode = getString(resume, ["server_provisioning_mode"]);
    if (isServerProvisioningMode(mode)) {
      serverProvisioningMode = mode;
    }
    remoteServerHost =
      getString(resume, ["remote_server_host"]) || remoteServerHost;
    remoteServerUser =
      getString(resume, ["remote_server_user"]) || remoteServerUser;
    const remotePort = Number(resume?.remote_server_port ?? 0);
    if (Number.isInteger(remotePort) && remotePort > 0) {
      remoteServerPort = remotePort;
    }
    expectedDeviceName =
      getString(resume, ["expected_device_name"]) || expectedDeviceName;
    ownerSeedExpected = getBoolean(resume, ["owner_seed_expected"]);
    const ownerSummary = asRecord(resume?.owner_seed_summary);
    if (ownerSeedExpected && ownerSummary) {
      ownerSeedSummary = {
        email: getString(ownerSummary, ["email"]) || undefined,
        username: getString(ownerSummary, ["username"]) || undefined,
        displayName: getString(ownerSummary, ["display_name"]) || undefined,
        source: getString(ownerSummary, ["source"]) || undefined,
      };
    }
    if (Array.isArray(result.existing_server_ids)) {
      applyJoinServerBaseline(result.existing_server_ids);
    }
    applyPlannedServerTarget(result);
    if (run.pairing_job_id && run.pairing_job_id !== jobId) {
      pairingJobId = run.pairing_job_id;
    }
    if (run.pairing_job_id && !pairingStartedAt) {
      pairingStartedAt = run.updated_at || run.created_at || "";
    }
    return true;
  } catch (error) {
    console.warn("Active wizard-run lookup failed:", error);
    return false;
  }
}

function pendingWizardRunSubmission(): WizardRunSubmission | null {
  const candidate = (page.state as { wizardRunSubmission?: unknown })
    .wizardRunSubmission;
  if (!candidate || typeof candidate !== "object") return null;
  const submission = candidate as Partial<WizardRunSubmission>;
  if (
    !submission.request ||
    typeof submission.request !== "object" ||
    typeof submission.idempotencyKey !== "string" ||
    !submission.idempotencyKey
  ) {
    return null;
  }
  return submission as WizardRunSubmission;
}

function markWizardSubmissionRunning() {
  latestJobMessage =
    "Validating the selected StackKit and preparing the Node...";
  jobState = "running";
  tasks = tasks.map((task, index) =>
    index === 0
      ? { ...task, status: "running", message: latestJobMessage }
      : task,
  );
}

function applyCreationHandoffToSearchParams(params: URLSearchParams) {
  if (stackId) params.set("stack_id", stackId);
  if (stackName) params.set("name", stackName);
  if (creationOperation === "add-server") {
    params.set("operation", "add-server");
  }
  if (jobId) params.set("job_id", jobId);
  if (pairingJobId) params.set("pairing_job_id", pairingJobId);
}

async function clearWizardSubmissionState(run?: WizardRunResponse) {
  const params = new URLSearchParams(window.location.search);
  if (run) {
    applianceReceipt = readApplianceReceipt(run.appliance);
    stackId = run.kit_deployment_id || stackId;
    stackName = run.name || stackName;
    jobId = run.job_id || run.pairing_job_id || "";
    pairingJobId = run.pairing_job_id || "";
    applyPlannedServerTarget(run as unknown as Record<string, unknown>);
  }
  applyCreationHandoffToSearchParams(params);
  await goto(`/stacks/creating?${params.toString()}`, {
    replace: true,
    shallow: true,
    state: {},
  });
}

function beginJobTracking() {
  if (!jobId) return;
  stopPolling();
  pollingStartedAt = Date.now();
  pollErrorCount = 0;
  nextPollAttemptAt = 0;
  streamRetryAttempt = 0;
  pollingInProgress = false;
  void pollJobStatus();
  pollingInterval = setInterval(pollJobStatus, BASE_POLL_INTERVAL);
  startJobStream();
}

async function resumeWizardRunFromConflict(error: unknown): Promise<boolean> {
  clearJoinWizardIdempotencyKeys(stackId);
  if (await adoptActiveWizardRun()) {
    wizardIdempotencyConflictFailed = false;
    initError = null;
    beginJobTracking();
    return Boolean(jobId);
  }
  if (error == null) return false;
  const recovery = parseWizardConflictRecovery(parseApiError(error).details);
  const nextStackId = recovery.stackId || stackId;
  const nextJobId = recovery.jobId || recovery.pairingJobId;
  if (!nextStackId || !nextJobId) return false;
  stackId = nextStackId;
  jobId = recovery.jobId || nextJobId;
  pairingJobId = recovery.pairingJobId || pairingJobId;
  creationOperation = "add-server";
  wizardIdempotencyConflictFailed = false;
  initError = null;
  jobState = "running";
  latestJobMessage = "Resuming the earlier Node registration attempt…";
  tasks = tasks.map((task, index) =>
    index === 0
      ? { ...task, status: "running", message: latestJobMessage }
      : task,
  );
  syncTaskListForOperation();
  beginJobTracking();
  return true;
}

function wizardSubmissionErrorDetails(details: unknown): string {
  if (details == null) return "";
  try {
    return sanitizeSensitiveText(JSON.stringify(details, null, 2));
  } catch {
    return sanitizeSensitiveText(details);
  }
}

async function submitPendingWizardRun(
  submission: WizardRunSubmission,
): Promise<boolean> {
  const transport = submission.request.intent.server.transport;
  if (isServerProvisioningMode(transport)) {
    serverProvisioningMode = transport;
    syncTaskListForOperation();
  }
  markWizardSubmissionRunning();
  try {
    const run = await createWizardRun(
      submission.request,
      submission.idempotencyKey,
    );
    if (!run.job_id && !run.pairing_job_id) {
      throw new Error("Node registration did not return a creation job.");
    }
    clearJoinWizardIdempotencyKeys(stackId);
    await clearWizardSubmissionState(run);
    await adoptActiveWizardRun(jobId);
    return true;
  } catch (error) {
    if (isWizardIdempotencyConflict(error)) {
      if (await resumeWizardRunFromConflict(error)) {
        await clearWizardSubmissionState();
        return true;
      }
      wizardIdempotencyConflictFailed = true;
    }
    const parsed = parseApiError(error);
    jobState = "failed";
    tasks = updateTasksWithError(tasks, {
      step: tasks[0]?.id,
      error: parsed.message || "Failed to prepare Node registration.",
      error_details: wizardSubmissionErrorDetails(parsed.details),
      state: "failed",
    });
    await clearWizardSubmissionState();
    return false;
  }
}

function resetCreationState() {
  stopPolling();
  stopConnectionPolling();
  stackName = "";
  stackId = "";
  jobId = "";
  applianceReceipt = undefined;
  creationOperation = "stack";
  registrationToken = "";
  stackKitHandoff = null;
  lease = null;
  runtimePhase = "";
  verificationStatus = "";
  runtimeProof = {};
  simulationPreviewStatus = "";
  simulationPreviewUrl = "";
  simulationPreviewExpiresAt = "";
  e2eProof = null;
  tasks = createTaskList();
  latestJobMessage = "";
  jobState = "";
  jobWaitReason = "";
  jobNextResumeAt = "";
  jobResumeAvailableAt = "";
  jobResumeAvailable = false;
  pollingInterval = null;
  requirements = null;
  serverProvisioningMode = "legacy";
  remoteServerHost = "";
  remoteServerUser = "";
  remoteServerPort = null;
  serverUrl = "";
  copiedCommand = null;
  copiedErrorDetails = false;
  showAllCommands = false;
  backendCompleted = false;
  jobType = "";
  pairingTokenExpiresAt = "";
  pairingStartedAt = "";
  expectedDeviceName = "";
  plannedServerId = "";
  guardConnectionWaitStartedAt = 0;
  existingServerIds = new Set<string>();
  existingServerBaselineKnown = false;
  connectedServer = null;
  connectionPollError = "";
  connectionPollingInProgress = false;
  connectionPollingInterval = null;
  remoteEnrollmentPollingInterval = null;
  connectionClock = Date.now();
  ownerSeedExpected = false;
  ownerSeedSummary = null;
  pollErrorCount = 0;
  pairingJobId = "";
  pairingTokenIssued = false;
  pairingRecoveryBusy = false;
  pairingRecoveryError = "";
  jobStreamClose = null;
  sseActive = false;
  pageDestroyed = false;
  pollingStartedAt = 0;
  pollingInProgress = false;
  retryingRollout = false;
  retryError = "";
  jobRetryable = undefined;
  wizardIdempotencyConflictFailed = false;
  initError = null;
  capturedCreationFailureJobIds.clear();
  capturedRolloutResultKeys.clear();
}

async function mount() {
  resetCreationState();

  // URL identifies the immediate handoff; the ledger supplies durable
  // context and is also sufficient for dashboard/cross-device resume.
  const params = new URLSearchParams(window.location.search);
  jobId = params.get("job_id") || params.get("job") || "";
  stackName = params.get("name") || "New StackKit deployment";
  stackId = params.get("stack_id") || params.get("stack") || "";
  creationOperation =
    normalizeCreationOperation(params.get("operation")) || "stack";
  syncTaskListForOperation();
  const pendingSubmission = pendingWizardRunSubmission();
  if (pendingSubmission) {
    await submitPendingWizardRun(pendingSubmission);
  } else {
    await adoptActiveWizardRun(jobId);
  }
  pairingJobId = pairingJobId || params.get("pairing_job_id") || "";
  syncTaskListForOperation();

  if (!jobId && !hasFailed) {
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

  if (jobId) {
    beginJobTracking();
    startRemoteEnrollmentPolling();
    if (
      remoteEnrollmentSucceeded ||
      serverProvisioningMode === "connect-remote"
    ) {
      void pollRemoteEnrollmentJob();
      startConnectionPolling();
    }
  }
}

function unmount() {
  pageDestroyed = true;
  stopPolling();
  stopRemoteEnrollmentPolling();
  stopConnectionPolling();
}

function goToStack() {
  const params = new URLSearchParams();
  if (stackId) params.set("stack", stackId);
  params.set("phase", "review");
  goto(`/dashboard?${params.toString()}`);
}

function retryFromStart() {
  if (stackId) {
    clearJoinWizardIdempotencyKeys(stackId);
  }
  goto(
    creationOperation === "add-server" && stackId
      ? `/stacks/${encodeURIComponent(stackId)}/servers/new`
      : "/stacks/new",
  );
}

async function resumePreviousWizardAttempt() {
  if (retryingRollout) return;
  retryError = "";
  retryingRollout = true;
  try {
    if (await resumeWizardRunFromConflict(null)) {
      await clearWizardSubmissionState();
      return;
    }
    if (await adoptActiveWizardRun()) {
      wizardIdempotencyConflictFailed = false;
      initError = null;
      beginJobTracking();
      return;
    }
    retryError =
      "Could not resume the earlier registration. Start a fresh attempt instead.";
  } finally {
    retryingRollout = false;
  }
}

async function handleFailurePrimaryAction() {
  if (wizardIdempotencyConflictFailed) {
    retryFromStart();
    return;
  }
  if (failureRetryDispatch?.kind === "rollout") {
    await retryStackKitRollout();
    return;
  }
  if (
    failureRetryDispatch?.kind === "deploy" ||
    failureRetryDispatch?.kind === "provision"
  ) {
    await retryStackLifecycle(failureRetryDispatch.kind);
    return;
  }
  retryFromStart();
}

async function retryStackLifecycle(kind: "deploy" | "provision") {
  if (!stackId || retryingRollout) return;
  retryError = "";
  retryingRollout = true;
  try {
    const response =
      kind === "deploy"
        ? await deployStack(stackId)
        : await provisionStack(stackId);
    await continueWithAcceptedRollout(
      response,
      kind === "deploy"
        ? "Retrying deployment..."
        : "Retrying server provisioning...",
    );
  } catch (error) {
    retryError =
      error instanceof Error
        ? error.message
        : "Could not start lifecycle retry.";
  } finally {
    retryingRollout = false;
  }
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
      error instanceof Error ? error.message : "Could not start rollout retry.";
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

async function retryRemoteSSHEnrollment() {
  if (!stackId || retryingRollout) return;
  retryError = "";
  pairingRecoveryError = "";
  retryingRollout = true;
  remoteEnrollmentFailed = false;
  remoteEnrollmentActive = true;
  remoteEnrollmentMessage = "Retrying SSH enrollment on the saved connection…";
  try {
    const response = await resumeRemoteEnrollment(stackId, {
      pairing_job_id: pairingJobId || undefined,
    });
    const nextPairingJobId = response.pairing_job_id?.trim() || "";
    if (nextPairingJobId) {
      pairingJobId = nextPairingJobId;
      if (creationOperation === "add-server" && !jobId) {
        jobId = nextPairingJobId;
      }
    }
    if (response.server_remote_host) {
      remoteServerHost = response.server_remote_host;
    }
    if (response.server_remote_user) {
      remoteServerUser = response.server_remote_user;
    }
    if (response.server_remote_port) {
      remoteServerPort = response.server_remote_port;
    }
    applyPlannedServerTarget(response as unknown as Record<string, unknown>);
    guardConnectionWaitStartedAt = Date.now();
    if (!pollingInterval && pairingJobId) {
      pollingInterval = setInterval(pollJobStatus, BASE_POLL_INTERVAL);
      void pollJobStatus();
    }
  } catch (error) {
    remoteEnrollmentFailed = true;
    remoteEnrollmentActive = false;
    pairingRecoveryError = sanitizeSensitiveText(
      error instanceof Error
        ? error.message
        : "Could not retry remote SSH enrollment.",
    );
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
  jobRetryable = undefined;
  jobWaitReason = "";
  jobNextResumeAt = "";
  jobResumeAvailableAt = "";
  jobResumeAvailable = false;
  latestJobMessage = message;
  tasks = resetTasksForRolloutRetry();

  const params = new URLSearchParams();
  params.set("job_id", jobId);
  params.set("stack_id", stackId);
  if (stackName) params.set("name", stackName);
  params.set("phase", "rollout");
  // SvelteKit 3 merges noScroll/keepFocus into `reset`; `noScroll: true`
  // is `reset: false`.
  await goto(`/stacks/creating?${params.toString()}`, {
    replaceState: true,
    reset: false,
  });

  pollingStartedAt = Date.now();
  pollJobStatus();
  pollingInterval = setInterval(pollJobStatus, BASE_POLL_INTERVAL);
}

function copyToken() {
  navigator.clipboard.writeText(registrationToken);
}

function toggleAllCommands() {
  showAllCommands = !showAllCommands;
}

export const creation = {
  get stackName() {
    return stackName;
  },
  set stackName(value: typeof stackName) {
    stackName = value;
  },
  get stackId() {
    return stackId;
  },
  set stackId(value: typeof stackId) {
    stackId = value;
  },
  get jobId() {
    return jobId;
  },
  set jobId(value: typeof jobId) {
    jobId = value;
  },
  get applianceReceipt() {
    return applianceReceipt;
  },
  set applianceReceipt(value: typeof applianceReceipt) {
    applianceReceipt = value;
  },
  get creationOperation() {
    return creationOperation;
  },
  set creationOperation(value: typeof creationOperation) {
    creationOperation = value;
  },
  get registrationToken() {
    return registrationToken;
  },
  set registrationToken(value: typeof registrationToken) {
    registrationToken = value;
  },
  get stackKitHandoff() {
    return stackKitHandoff;
  },
  set stackKitHandoff(value: typeof stackKitHandoff) {
    stackKitHandoff = value;
  },
  get lease() {
    return lease;
  },
  set lease(value: typeof lease) {
    lease = value;
  },
  get runtimePhase() {
    return runtimePhase;
  },
  set runtimePhase(value: typeof runtimePhase) {
    runtimePhase = value;
  },
  get verificationStatus() {
    return verificationStatus;
  },
  set verificationStatus(value: typeof verificationStatus) {
    verificationStatus = value;
  },
  get runtimeProof() {
    return runtimeProof;
  },
  set runtimeProof(value: typeof runtimeProof) {
    runtimeProof = value;
  },
  get simulationPreviewStatus() {
    return simulationPreviewStatus;
  },
  set simulationPreviewStatus(value: typeof simulationPreviewStatus) {
    simulationPreviewStatus = value;
  },
  get simulationPreviewUrl() {
    return simulationPreviewUrl;
  },
  set simulationPreviewUrl(value: typeof simulationPreviewUrl) {
    simulationPreviewUrl = value;
  },
  get simulationPreviewExpiresAt() {
    return simulationPreviewExpiresAt;
  },
  set simulationPreviewExpiresAt(value: typeof simulationPreviewExpiresAt) {
    simulationPreviewExpiresAt = value;
  },
  get e2eProof() {
    return e2eProof;
  },
  set e2eProof(value: typeof e2eProof) {
    e2eProof = value;
  },
  get tasks() {
    return tasks;
  },
  set tasks(value: typeof tasks) {
    tasks = value;
  },
  get latestJobMessage() {
    return latestJobMessage;
  },
  set latestJobMessage(value: typeof latestJobMessage) {
    latestJobMessage = value;
  },
  get jobState() {
    return jobState;
  },
  set jobState(value: typeof jobState) {
    jobState = value;
  },
  get jobWaitReason() {
    return jobWaitReason;
  },
  set jobWaitReason(value: typeof jobWaitReason) {
    jobWaitReason = value;
  },
  get jobNextResumeAt() {
    return jobNextResumeAt;
  },
  set jobNextResumeAt(value: typeof jobNextResumeAt) {
    jobNextResumeAt = value;
  },
  get jobResumeAvailableAt() {
    return jobResumeAvailableAt;
  },
  set jobResumeAvailableAt(value: typeof jobResumeAvailableAt) {
    jobResumeAvailableAt = value;
  },
  get jobResumeAvailable() {
    return jobResumeAvailable;
  },
  set jobResumeAvailable(value: typeof jobResumeAvailable) {
    jobResumeAvailable = value;
  },
  get pollingInterval() {
    return pollingInterval;
  },
  set pollingInterval(value: typeof pollingInterval) {
    pollingInterval = value;
  },
  get requirements() {
    return requirements;
  },
  set requirements(value: typeof requirements) {
    requirements = value;
  },
  get serverProvisioningMode() {
    return serverProvisioningMode;
  },
  set serverProvisioningMode(value: typeof serverProvisioningMode) {
    serverProvisioningMode = value;
  },
  get remoteServerHost() {
    return remoteServerHost;
  },
  set remoteServerHost(value: typeof remoteServerHost) {
    remoteServerHost = value;
  },
  get remoteServerUser() {
    return remoteServerUser;
  },
  set remoteServerUser(value: typeof remoteServerUser) {
    remoteServerUser = value;
  },
  get remoteServerPort() {
    return remoteServerPort;
  },
  set remoteServerPort(value: typeof remoteServerPort) {
    remoteServerPort = value;
  },
  get serverUrl() {
    return serverUrl;
  },
  set serverUrl(value: typeof serverUrl) {
    serverUrl = value;
  },
  get copiedCommand() {
    return copiedCommand;
  },
  set copiedCommand(value: typeof copiedCommand) {
    copiedCommand = value;
  },
  get copiedErrorDetails() {
    return copiedErrorDetails;
  },
  set copiedErrorDetails(value: typeof copiedErrorDetails) {
    copiedErrorDetails = value;
  },
  get showAllCommands() {
    return showAllCommands;
  },
  set showAllCommands(value: typeof showAllCommands) {
    showAllCommands = value;
  },
  get backendCompleted() {
    return backendCompleted;
  },
  set backendCompleted(value: typeof backendCompleted) {
    backendCompleted = value;
  },
  get jobType() {
    return jobType;
  },
  set jobType(value: typeof jobType) {
    jobType = value;
  },
  get pairingTokenExpiresAt() {
    return pairingTokenExpiresAt;
  },
  set pairingTokenExpiresAt(value: typeof pairingTokenExpiresAt) {
    pairingTokenExpiresAt = value;
  },
  get pairingStartedAt() {
    return pairingStartedAt;
  },
  set pairingStartedAt(value: typeof pairingStartedAt) {
    pairingStartedAt = value;
  },
  get expectedDeviceName() {
    return expectedDeviceName;
  },
  set expectedDeviceName(value: typeof expectedDeviceName) {
    expectedDeviceName = value;
  },
  get existingServerIds() {
    return existingServerIds;
  },
  set existingServerIds(value: typeof existingServerIds) {
    existingServerIds = value;
  },
  get existingServerBaselineKnown() {
    return existingServerBaselineKnown;
  },
  set existingServerBaselineKnown(value: typeof existingServerBaselineKnown) {
    existingServerBaselineKnown = value;
  },
  get connectedServer() {
    return connectedServer;
  },
  set connectedServer(value: typeof connectedServer) {
    connectedServer = value;
  },
  get connectionPollError() {
    return connectionPollError;
  },
  set connectionPollError(value: typeof connectionPollError) {
    connectionPollError = value;
  },
  get connectionPollingInProgress() {
    return connectionPollingInProgress;
  },
  set connectionPollingInProgress(value: typeof connectionPollingInProgress) {
    connectionPollingInProgress = value;
  },
  get connectionPollingInterval() {
    return connectionPollingInterval;
  },
  set connectionPollingInterval(value: typeof connectionPollingInterval) {
    connectionPollingInterval = value;
  },
  get connectionClock() {
    return connectionClock;
  },
  set connectionClock(value: typeof connectionClock) {
    connectionClock = value;
  },
  get ownerSeedExpected() {
    return ownerSeedExpected;
  },
  set ownerSeedExpected(value: typeof ownerSeedExpected) {
    ownerSeedExpected = value;
  },
  get ownerSeedSummary() {
    return ownerSeedSummary;
  },
  set ownerSeedSummary(value: typeof ownerSeedSummary) {
    ownerSeedSummary = value;
  },
  get pollErrorCount() {
    return pollErrorCount;
  },
  set pollErrorCount(value: typeof pollErrorCount) {
    pollErrorCount = value;
  },
  get pairingJobId() {
    return pairingJobId;
  },
  set pairingJobId(value: typeof pairingJobId) {
    pairingJobId = value;
  },
  get pairingTokenIssued() {
    return pairingTokenIssued;
  },
  set pairingTokenIssued(value: typeof pairingTokenIssued) {
    pairingTokenIssued = value;
  },
  get pairingRecoveryBusy() {
    return pairingRecoveryBusy;
  },
  set pairingRecoveryBusy(value: typeof pairingRecoveryBusy) {
    pairingRecoveryBusy = value;
  },
  get pairingRecoveryError() {
    return pairingRecoveryError;
  },
  set pairingRecoveryError(value: typeof pairingRecoveryError) {
    pairingRecoveryError = value;
  },
  get jobStreamClose() {
    return jobStreamClose;
  },
  set jobStreamClose(value: typeof jobStreamClose) {
    jobStreamClose = value;
  },
  get sseActive() {
    return sseActive;
  },
  set sseActive(value: typeof sseActive) {
    sseActive = value;
  },
  get pageDestroyed() {
    return pageDestroyed;
  },
  set pageDestroyed(value: typeof pageDestroyed) {
    pageDestroyed = value;
  },
  get pollingStartedAt() {
    return pollingStartedAt;
  },
  set pollingStartedAt(value: typeof pollingStartedAt) {
    pollingStartedAt = value;
  },
  get pollingInProgress() {
    return pollingInProgress;
  },
  set pollingInProgress(value: typeof pollingInProgress) {
    pollingInProgress = value;
  },
  get retryingRollout() {
    return retryingRollout;
  },
  set retryingRollout(value: typeof retryingRollout) {
    retryingRollout = value;
  },
  get retryError() {
    return retryError;
  },
  set retryError(value: typeof retryError) {
    retryError = value;
  },
  get jobRetryable() {
    return jobRetryable;
  },
  set jobRetryable(value: typeof jobRetryable) {
    jobRetryable = value;
  },
  get initError() {
    return initError;
  },
  set initError(value: typeof initError) {
    initError = value;
  },
  get showInstallCommand() {
    return showInstallCommand;
  },
  get oneLinerPreviewRequired() {
    return oneLinerPreviewRequired;
  },
  get waitingForProviderProvision() {
    return waitingForProviderProvision;
  },
  get waitingForManagedRuntime() {
    return waitingForManagedRuntime;
  },
  get nextResumeLabel() {
    return nextResumeLabel;
  },
  get agentPairingRequired() {
    return agentPairingRequired;
  },
  get agentPairingWaiting() {
    return agentPairingWaiting;
  },
  get remoteEnrollmentActive() {
    return remoteEnrollmentActive;
  },
  get remoteEnrollmentFailed() {
    return remoteEnrollmentFailed;
  },
  get remoteEnrollmentMessage() {
    return remoteEnrollmentMessage;
  },
  get remoteEnrollmentStep() {
    return remoteEnrollmentStep;
  },
  get remoteEnrollmentProgress() {
    return remoteEnrollmentProgress;
  },
  get pairingTokenExpired() {
    return pairingTokenExpired;
  },
  get waitingRecoveryAvailable() {
    return waitingRecoveryAvailable;
  },
  get overallProgress() {
    return overallProgress;
  },
  get hasStackKitIdentityHandoff() {
    return hasStackKitIdentityHandoff;
  },
  get hasVerifiedRuntimeProof() {
    return hasVerifiedRuntimeProof;
  },
  get rolloutJobDetected() {
    return rolloutJobDetected;
  },
  get handoffMissingFailure() {
    return handoffMissingFailure;
  },
  get isComplete() {
    return isComplete;
  },
  get failedTask() {
    return failedTask;
  },
  get hasFailed() {
    return hasFailed;
  },
  get failedTaskId() {
    return failedTaskId;
  },
  get hasManagedLeaseReference() {
    return hasManagedLeaseReference;
  },
  get creationHeaderLabel() {
    return creationHeaderLabel;
  },
  get postLeaseManagedCloudFailure() {
    return postLeaseManagedCloudFailure;
  },
  get stackKitArtifactOrRoutingFailure() {
    return stackKitArtifactOrRoutingFailure;
  },
  get failureRetryDispatch() {
    return failureRetryDispatch;
  },
  get failurePrimaryActionLabel() {
    return failurePrimaryActionLabel;
  },
  get connectRemoteConnectionReady() {
    return connectRemoteConnectionReady;
  },
  get connectRemoteStackKitFailed() {
    return connectRemoteStackKitFailed;
  },
  get wizardIdempotencyConflictFailed() {
    return wizardIdempotencyConflictFailed;
  },
  get failurePrimaryActionDisabled() {
    return failurePrimaryActionDisabled;
  },
  get failurePrimaryActionAvailable() {
    return failurePrimaryActionAvailable;
  },
  get completionTitle() {
    return completionTitle;
  },
  get completionSubtitle() {
    return completionSubtitle;
  },
  get dashboardHref() {
    return dashboardHref;
  },
  get monitoringHref() {
    return monitoringHref;
  },
  get servicesHref() {
    return servicesHref;
  },
  get proofRows() {
    return proofRows;
  },
  get currentTask() {
    return currentTask;
  },
  get currentStepInfo() {
    return currentStepInfo;
  },
  get currentStepMessage() {
    return currentStepMessage;
  },
  get completedCount() {
    return completedCount;
  },
  get installCommand() {
    return installCommand;
  },
  get allInstallCommands() {
    return allInstallCommands;
  },
  get resumeAvailableLabel() {
    return resumeAvailableLabel;
  },
  ensureRuntimeTaskList,
  replaceTaskListIfShapeChanged,
  syncTaskListForOperation,
  addServerRequestHasExceededGuard,
  hasFreshGuardHeartbeat,
  selectConnectedServer,
  stopConnectionPolling,
  expireConnectedServerWithoutFreshHeartbeat,
  pollGuardConnection,
  startConnectionPolling,
  pollJobStatus,
  stopPolling,
  restoreFastPolling,
  startJobStream,
  recoverPairingCommand,
  rolloutAnalyticsInput,
  captureRolloutResultOnce,
  captureRolloutEvent,
  captureCreationFailure,
  copyToClipboard,
  copyErrorDetails,
  applyJobResult,
  adoptActiveWizardRun,
  pendingWizardRunSubmission,
  markWizardSubmissionRunning,
  isWizardIdempotencyConflict,
  wizardSubmissionErrorDetails,
  clearWizardSubmissionState,
  submitPendingWizardRun,
  resetCreationState,
  mount,
  unmount,
  goToStack,
  retryFromStart,
  resumePreviousWizardAttempt,
  handleFailurePrimaryAction,
  retryStackLifecycle,
  resetTasksForRolloutRetry,
  retryStackKitRollout,
  resumeOverdueManagedRuntime,
  retryRemoteSSHEnrollment,
  continueWithAcceptedRollout,
  copyToken,
  toggleAllCommands,
  runtimeTaskIds,
  CONNECTION_POLL_INTERVAL,
  GUARD_HEARTBEAT_FRESH_MS,
  GUARD_HEARTBEAT_FUTURE_SKEW_MS,
  MAX_POLL_ERRORS,
  BASE_POLL_INTERVAL,
  SSE_SAFETY_POLL_INTERVAL,
  MAX_PENDING_DURATION_MS,
  MAX_ADD_SERVER_RUNNING_DURATION_MS,
  capturedCreationFailureJobIds,
  capturedRolloutResultKeys,
};
