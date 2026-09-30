<script lang="ts">
  import { tr, trn } from "#lib/i18n.svelte.js";
  import { onMount } from "svelte";
  import {
    readSectionCache,
    writeSectionCache,
  } from "#lib/data/sectionCache.js";

  const CACHE_WORKERS = "stacks:workers";
  const CACHE_HOMELAB = "stacks:homelab";
  const CACHE_OPERATIONS = "stacks:operations";
  import { getClientBootstrap } from "#lib/client/bootstrap.js";
  import { isCancelledRequestError, parseApiError } from "#lib/api/errors.js";
  import { getBestPublicServerUrl } from "#lib/api/client.js";
  import { getWorkerRegistryUrl } from "#lib/api/tunnel.js";
  import { listWorkers, type Worker } from "#lib/api/workers.js";
  import { goto, pushState } from "$app/navigation";
  import { page } from "$app/state";
  import {
    type DeploymentRequirements,
    generateInstallCommand,
  } from "#lib/wizard/index.js";
  import { authHandler } from "#lib/stores/authHandler.svelte.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import SessionRenewalPanel from "#lib/components/SessionRenewalPanel.svelte";
  import { appVersion } from "#lib/config.js";
  import {
    createPostHogClient,
    toTechstackAnalyticsUser,
  } from "#lib/analytics/posthog.js";
  import StackImportExportModal from "#lib/components/StackImportExportModal.svelte";
  import {
    assignStackWorker,
    decommissionMonthlyRuntime,
    deployStack,
    getStackOperations,
    provisionStack,
    reconnectMonthlyRuntime,
    resolveMonthlyRuntimeCustody,
    resumeRemoteEnrollment,
    retryStackRollout,
    type MonthlyRuntimeStatus,
    type KitDeployment,
    type StackOperationServer,
    type StackCustodyLease,
    type StackOperationsPayload,
  } from "#lib/api/stacks.js";
  import { getLatestKitDeploymentProvisionJob } from "#lib/api/jobs.js";
  import {
    getHomelab,
    chosenHomelabName,
    type HomelabView,
  } from "#lib/api/homelab.js";
  import { stackIdentity } from "#lib/stores/stackIdentity.js";
  import HomelabTitle from "#lib/components/dashboard/HomelabTitle.svelte";
  import {
    getActiveWizardRun,
    wizardRunNeedsAttention,
    type ActiveWizardRun,
  } from "#lib/api/wizardRuns.js";
  import WizardRunBanner from "#lib/components/WizardRunBanner.svelte";
  import { confirmInApp } from "#lib/dialogs/in-app-dialog.js";
  import { cleanupActionForFailure } from "#lib/custody/cleanup-action.js";
  import {
    actionableServerOutcome,
    canonicalServerFor,
    serverForOperationTarget,
    statusLabel,
    type DashboardServer,
  } from "#lib/server-card-adapter.js";
  import { ServerCard } from "@kombiverselabs/ui/server";
  import CompleteDashboard from "#lib/components/dashboard/CompleteDashboard.svelte";
  import StrataDashboard from "#lib/components/dashboard/StrataDashboard.svelte";
  import NodeActions from "#lib/components/dashboard/NodeActions.svelte";
  import DashboardServiceSheet from "#lib/components/dashboard/DashboardServiceSheet.svelte";
  import WorkerRegistrationCard from "#lib/components/dashboard/WorkerRegistrationCard.svelte";
  import CustodyLeasesPanel from "#lib/components/dashboard/CustodyLeasesPanel.svelte";
  import {
    buildNodeViews,
    primaryWorkloadKeys,
    serviceState,
    type NodeView,
  } from "#lib/dashboard/homelab-model.js";
  import {
    loadDevices,
    loadPeople,
    type DeviceEntry,
    type PersonEntry,
    type SectionState,
  } from "#lib/dashboard/people.js";
  import type { StatusFacts } from "#lib/dashboard/strata.js";
  import { dashboardPreset } from "#lib/stores/dashboardPreset.svelte.js";
  import { Surface } from "@kombiverselabs/ui/primitives";
  import Button from "#lib/components/ui/Button.svelte";
  import {
    AlertTriangle,
    HeartPulse,
    MonitorSmartphone,
    Play,
    Plus,
    RefreshCw,
    Server,
    Waypoints,
  } from "@lucide/svelte";
  import GuidancePanel from "#lib/components/hub/GuidancePanel.svelte";
  import {
    outcomeFromLatestFailure,
    retryDispatchFor,
    type ServerOutcome,
  } from "#lib/support/server-outcome.js";
  import { isManagedRuntimeServer } from "#lib/managed-runtime-server.js";
  import {
    getOrCreateStackActionIdempotency,
    settleStackActionIdempotency,
    type StackIdempotentAction,
  } from "#lib/idempotency/managed-runtime.js";
  import {
    listCanonicalServers,
    isCurrentCanonicalServer,
    type CanonicalServer,
  } from "#lib/api/registry.js";
  import {
    listCanonicalServices,
    type CanonicalService,
  } from "#lib/api/services.js";

  let loading = $state(true);
  // Section-level latches, deliberately NOT one dashboard-wide gate.
  //
  // A single latch, flipped once at the very end of load(), meant the fastest
  // section still waited for the slowest before it could appear at all: the
  // requests ran in parallel but the PAINT did not, so splitting the fetches
  // changed nothing on screen. Each section now reveals itself when its own
  // evidence lands. `homelabResolved` further down is the third of these.
  //
  // What the old latch protected is still protected: the registration
  // fallback panels must not flash before operations have been consulted, so
  // they wait on these two rather than on the page as a whole.
  let operationsResolved = $state(false);
  let stackContextResolved = $state(false);
  let error = $state<string | null>(null);
  /** Last verified data is on screen but the refresh behind it failed. */
  let dataStale = $state(false);
  let loadRetryAttempt = 0;
  let loadRetryTimer: ReturnType<typeof setTimeout> | null = null;
  let sessionRenewalRequired = $state(false);
  let sessionRenewalBodyKey = $state<string | undefined>(undefined);
  let showImportExport = $state(false);
  let importExportMode = $state<"import" | "export">("import");
  type DashboardFailure = NonNullable<
    StackOperationsPayload["latestFailure"]
  > & {
    kit_deployment_id: string;
    kit_deployment_name: string;
  };
  type DashboardOperations = Omit<
    StackOperationsPayload,
    "stack" | "servers" | "latestFailure" | "currentJob"
  > & {
    stack: Omit<StackOperationsPayload["stack"], "kit_deployment_id">;
    servers: DashboardServer[];
    latestFailure?: DashboardFailure | null;
    currentJob?:
      | (NonNullable<StackOperationsPayload["currentJob"]> & {
          kit_deployment_id?: string;
        })
      | null;
  };

  let operations = $state<DashboardOperations | null>(null);
  // True while `operations` is a cached snapshot that this load has not yet
  // reconfirmed. It is shown, because an instant approximate dashboard beats
  // an empty one, but it is NOT evidence: see `operationsEvidenceFresh`.
  let operationsFromCache = $state(false);
  const operationsByDeployment = new Map<string, StackOperationsPayload>();
  let operationsDeploymentKey = "";
  let canonicalServers = $state<CanonicalServer[]>([]);
  let canonicalServices = $state<CanonicalService[]>([]);
  let canonicalInventoryUnavailable = $state(false);
  let canonicalInventoryResolved = $state(false);
  let operationsError = $state<string | null>(null);
  let reviewPhase = $state(false);
  let assigningWorkerId = $state<string | null>(null);
  let cleaningCustodyLeaseId = $state<string | null>(null);
  let reconnectingLeaseId = $state<string | null>(null);
  // The stated result of the last Reconnect. A silent action is unusable: the
  // operator must be able to tell "probe succeeded, machine still offline"
  // apart from "nothing happened".
  type ReconnectOutcome = {
    leaseId: string;
    tone: "success" | "warning" | "error";
    title: string;
    body: string;
  };
  let reconnectOutcome = $state<ReconnectOutcome | null>(null);
  let resolvingCustodyLeaseId = $state<string | null>(null);
  let decommissionError = $state<string | null>(null);
  let retryingOperations = $state(false);
  let capturedConnectedHomelab = false;

  type StackDashboardItem = KitDeployment & {
    nodes: number;
    servicesCount: number;
    created?: string;
    updated?: string;
  };

  let deployments = $state<StackDashboardItem[]>([]);
  // The owner-scoped homelab is the only dashboard identity. Its kit
  // deployments remain internal facts used to aggregate node evidence; they
  // are never a second Techstack selector.
  let homelab = $state<HomelabView | null>(null);
  // A transient homelab read failure must not turn a populated dashboard into
  // the first-run empty state. This becomes true for both a resolved homelab
  // and an authoritative 404/null response.
  let homelabResolved = $state(false);
  // Active wizard run (ledger-backed): drives the resume banner (plan D6).
  let activeWizardRun = $state<ActiveWizardRun | null>(null);
  // Stack-scoped mutation flows are available only when there is exactly one
  // deployment. Read-only inventory below always aggregates every deployment.
  let singleDeployment = $derived<StackDashboardItem | null>(
    deployments.length === 1 ? deployments[0] : null,
  );
  let deploymentIds = $derived(new Set(deployments.map((item) => item.id)));
  // The homelab's user-facing name: the saved Stack Identity is the one
  // naming authority (owner direction 2026-09-26); a legacy chosen homelab
  // name only fills in before an identity exists. Without either, "Your
  // homelab" is the title.
  let homelabName = $derived(
    ($stackIdentity?.savedAt && $stackIdentity.name?.trim()) ||
      chosenHomelabName(homelab?.homelab),
  );
  let homelabTitle = $derived(
    homelabResolved ? homelabName || tr("ui.homelabDashboardPage.yourHomelab") : "",
  );

  // Worker registration state
  let workers = $state<Worker[]>([]);
  let requirements = $state<DeploymentRequirements | null>(null);
  let registrationToken = $state("");
  let serverUrl = $state("");
  let registryMode = $state<string>("");
  let registryUrlError = $state<string | null>(null);
  let installCommand = $derived(
    registrationToken && serverUrl
      ? generateInstallCommand(serverUrl, registrationToken)
      : "",
  );
  // Connection and rollout authority comes exclusively from the canonical
  // operations readiness projection. Approval is admission, not a heartbeat.
  let connectedWorkers = $derived(
    operations?.readiness?.connected_servers ?? 0,
  );
  // Mutation authority. A cached snapshot is explicitly not fresh: it may
  // describe a rollout that has since finished or failed, so every control
  // that changes server state stays disabled until this load reconfirms it.
  let operationsEvidenceFresh = $derived(
    Boolean(operations && !operationsError && !operationsFromCache),
  );
  let canRollout = $derived(
    operationsEvidenceFresh && operations?.readiness?.can_start === true,
  );

  let approvedWorkers = $derived(
    workers.filter(
      (worker) =>
        worker.approved &&
        (!worker.kit_deployment_id ||
          deploymentIds.has(worker.kit_deployment_id)),
    ),
  );
  let operationServers = $derived<DashboardServer[]>(operations?.servers ?? []);
  let operationDashboardServers = $derived(
    operationServers.filter(isDashboardServer),
  );
  let dashboardServers = $derived.by<DashboardServer[]>(() => {
    if (canonicalInventoryUnavailable || !canonicalInventoryResolved) {
      return operationDashboardServers;
    }
    const ids = new Set(canonicalServers.map((server) => server.id));
    return operationDashboardServers.filter(
      (server) => ids.has(server.id) || ids.has(server.agent_id || ""),
    );
  });
  let dashboardServiceCount = $derived(
    !canonicalInventoryUnavailable && canonicalInventoryResolved
      ? canonicalServices.length
      : (operations?.services.length ?? 0),
  );
  let dashboardRunningServiceCount = $derived(
    !canonicalInventoryUnavailable && canonicalInventoryResolved
      ? canonicalServices.filter(
          (service) => serviceState(service).tone === "ok",
        ).length
      : (operations?.kpis.running_services ?? 0),
  );
  let canonicalOnlyServers = $derived.by<CanonicalServer[]>(() => {
    if (canonicalInventoryUnavailable || !canonicalInventoryResolved) {
      return [];
    }
    const telemetryIDs = new Set(
      operationDashboardServers.flatMap((server) => [
        server.id,
        server.agent_id || "",
      ]),
    );
    return canonicalServers.filter(
      (server) =>
        !telemetryIDs.has(server.id) &&
        (!server.kit_deployment_id ||
          deploymentIds.has(server.kit_deployment_id)),
    );
  });
  let canonicalInventoryAuthoritative = $derived(
    canonicalInventoryResolved && !canonicalInventoryUnavailable,
  );
  let dashboardServerCount = $derived(
    canonicalInventoryAuthoritative
      ? canonicalServers.length
      : (operations?.kpis.registered_servers ?? 0),
  );
  let dashboardConnectedServerCount = $derived(
    canonicalInventoryAuthoritative
      ? canonicalServers.filter(
          (server) =>
            server.connection.state.trim().toLowerCase() === "connected",
        ).length
      : (operations?.readiness.connected_servers ?? 0),
  );
  let dashboardHealthyServerCount = $derived(
    canonicalInventoryAuthoritative
      ? canonicalServers.filter(
          (server) => server.health.state.trim().toLowerCase() === "healthy",
        ).length
      : (operations?.kpis.healthy_servers ?? 0),
  );

  // An empty service list on a healthy, connected server is a fact about the
  // host, and the operator can only act on it once the dashboard says which
  // of the two service sources produced nothing. "No runtime services
  // reported" alone reads like a broken dashboard.
  let servicesEmptyReason = $derived.by<string>(() => {
    if (dashboardServers.length === 0) {
      return tr("ui.homelabDashboardPage.noNodeHasReportedAn");
    }
    const agentVersions = [
      ...new Set(
        dashboardServers
          .map((server) => server.capabilities?.agent_version?.trim())
          .filter((version): version is string => Boolean(version)),
      ),
    ];
    const agentEvidence =
      agentVersions.length > 0
        ? trn("ui.homelab.agentVersions", agentVersions.length, {
            versions: agentVersions.join(", "),
          })
        : tr("ui.homelabDashboardPage.theAgentVersionWasNot");
    const manifestSeen = dashboardServers.some(
      (server) => server.capabilities?.stackkit_manifest_observed === true,
    );
    const discoveryRan = dashboardServers.some(
      (server) => server.capabilities?.service_discovery_observed === true,
    );
    if (manifestSeen && discoveryRan) {
      return [
        tr("ui.homelabDashboardPage.bothServiceSourcesReportedAn"),
        agentEvidence,
      ].join(" ");
    }
    if (manifestSeen) {
      return [
        tr("ui.homelabDashboardPage.theStackkitManifestSourceReported"),
        agentEvidence,
      ].join(" ");
    }
    if (discoveryRan) {
      return [
        tr("ui.homelabDashboardPage.agentDiscoveryReportedNoRunning"),
        agentEvidence,
      ].join(" ");
    }
    return [
      tr("ui.homelab.noSourceObserved"),
      agentEvidence,
      tr("ui.homelabDashboardPage.openTheNodeDetailsTo"),
    ].join(" ");
  });
  let custodyLeases = $derived<StackCustodyLease[]>(
    operations?.custodyLeases ?? [],
  );
  let failedCleanupAction = $derived(
    cleanupActionForFailure(custodyLeases, operations?.latestFailure?.lease_id),
  );
  let hasServerInventory = $derived(
    Boolean(
      operations &&
      (dashboardServers.length > 0 ||
        canonicalOnlyServers.length > 0 ||
        dashboardServiceCount > 0),
    ),
  );

  let currentRuntimePhase = $derived(
    operations?.runtimeLifecycle?.phases.find(
      (phase) => phase.id === operations?.runtimeLifecycle?.current_phase,
    ),
  );
  // Only render the rollout progress card while a rollout is genuinely
  // active. A failed or completed job must not leave a dead progress card
  // above the server inventory.
  let showRuntimeProgress = $derived.by(() => {
    const jobState = operations?.currentJob?.state;
    if (jobState) return ["pending", "running"].includes(jobState);
    return ["pending", "running"].includes(currentRuntimePhase?.status ?? "");
  });
  let isManagedOperationsStack = $derived(
    deployments.some(
      (deployment) =>
        deployment.server_provisioning_mode === "kombify-cloud" ||
        deployment.server_mode === "monthly-runtime" ||
        deployment.runtime_lane === "monthly-runtime" ||
        Boolean(deployment.lease_id),
    ),
  );
  let showReviewStart = $derived(
    Boolean(
      singleDeployment &&
      operations?.readiness?.review_required &&
      !isManagedOperationsStack,
    ),
  );

  // Auto-refresh worker status every 15s (graceful degradation)
  let refreshInterval: ReturnType<typeof setInterval> | null = null;
  let refreshInFlight = false;

  function withTimeout<T>(
    promise: Promise<T>,
    ms: number,
    label: string,
  ): Promise<T> {
    let timeout: ReturnType<typeof setTimeout> | null = null;
    const timeoutPromise = new Promise<T>((_, reject) => {
      timeout = setTimeout(() => {
        reject(new Error(`${label} timed out after ${ms}ms`));
      }, ms);
    });

    return Promise.race([promise, timeoutPromise]).finally(() => {
      if (timeout) clearTimeout(timeout);
    });
  }

  function normalizeStackDashboardItem(
    item: KitDeployment,
  ): StackDashboardItem {
    const raw = item as KitDeployment & {
      status?: string;
      created?: string;
      updated?: string;
    };

    return {
      ...item,
      state: item.state || raw.status || "pending",
      services: Array.isArray(item.services) ? item.services : [],
      nodes: 0,
      servicesCount: Array.isArray(item.services) ? item.services.length : 0,
      created_at: item.created_at || raw.created || "",
      updated_at: item.updated_at || raw.updated || "",
      created: raw.created || item.created_at || "",
      updated: raw.updated || item.updated_at || "",
    };
  }

  async function loadStackContext(currentStack: StackDashboardItem | null) {
    if (!currentStack) {
      registrationToken = "";
      requirements = null;
      return;
    }

    // Compute into locals and assign only after the async job lookup resolves.
    // Resetting registrationToken/requirements up front made the worker
    // registration card blink out on every manual refresh.
    let nextToken = "";
    let nextRequirements: DeploymentRequirements | null = null;

    // Registration token can come from:
    // - latest provision job result (current backend)
    // - stack.techstack.options.registration_token (unified output)
    // - legacy stack.metadata.options.registration_token (older schema)
    const unified = (currentStack as any).techstack;
    if (unified?.options?.registration_token) {
      nextToken = String(unified.options.registration_token);
    }

    if (!nextToken) {
      const legacyMeta = (currentStack as any).metadata;
      const legacyToken = legacyMeta?.options?.registration_token;
      if (legacyToken) nextToken = String(legacyToken);
    }

    if (!nextToken) {
      try {
        const job = await getLatestKitDeploymentProvisionJob(currentStack.id);
        const token = (job as any)?.result?.registration_token;
        if (token) nextToken = String(token);

        // If the job provides requirements, prefer them.
        const req = (job as any)?.result?.requirements;
        if (req) {
          nextRequirements = req as DeploymentRequirements;
        }
      } catch {
        // ignore (no job yet, or API unavailable)
      }
    }

    // Do not synthesize frontend requirements here. The operations screen must
    // show only persisted backend/job data.
    registrationToken = nextToken;
    requirements = nextRequirements;
  }

  // Callers pass fields straight off the wire, so an absent status has to be
  // an "unknown" rather than a thrown TypeError: trimming before filtering
  // meant one missing field aborted the whole operations aggregate and took
  // the dashboard down with an opaque "reading 'trim'". `uniqueMessages`
  // below already guards the same way.
  function aggregateStatus(values: Array<string | undefined | null>): string {
    const normalized = values
      .map((value) => value?.trim().toLowerCase())
      .filter(Boolean) as string[];
    if (normalized.length === 0) return "unknown";
    if (
      normalized.some((value) =>
        ["failed", "error", "offline", "degraded", "unhealthy"].includes(value),
      )
    ) {
      return "degraded";
    }
    return normalized[0];
  }

  function uniqueMessages(values: Array<string | undefined>): string {
    return Array.from(
      new Set(values.map((value) => value?.trim()).filter(Boolean)),
    ).join(" · ");
  }

  function homelabStackProjection(
    currentDeployments: StackDashboardItem[],
  ): Omit<StackDashboardItem, "kit_deployment_id"> {
    const created = currentDeployments
      .map((deployment) => deployment.created_at)
      .filter(Boolean)
      .sort()[0];
    const updated = currentDeployments
      .map((deployment) => deployment.updated_at)
      .filter(Boolean)
      .sort()
      .at(-1);
    return {
      id: homelab?.homelab?.id || "homelab",
      name: homelabName || tr("ui.homelabDashboardPage.yourHomelab"),
      provider: "homelab",
      state: aggregateStatus(currentDeployments.map((item) => item.state)),
      services: Array.from(
        new Set(currentDeployments.flatMap((item) => item.services)),
      ),
      created_at: created || "",
      updated_at: updated || "",
      nodes: 0,
      servicesCount: currentDeployments.reduce(
        (count, item) => count + item.servicesCount,
        0,
      ),
      created,
      updated,
    };
  }

  function aggregateOperations(
    snapshots: Array<{
      deployment: StackDashboardItem;
      payload: StackOperationsPayload;
    }>,
  ): DashboardOperations {
    const servers = snapshots.flatMap(({ deployment, payload }) =>
      payload.servers.map((server) => ({
        ...server,
        kit_deployment_id: deployment.id,
      })),
    );
    const services = snapshots.flatMap(({ payload }) => payload.services);
    const failures = snapshots
      .flatMap(({ deployment, payload }) =>
        payload.latestFailure
          ? [
              {
                ...payload.latestFailure,
                kit_deployment_id: deployment.id,
                kit_deployment_name: deployment.name,
              },
            ]
          : [],
      )
      .sort((left, right) =>
        (right.updated_at || right.created_at || "").localeCompare(
          left.updated_at || left.created_at || "",
        ),
      );
    const custodyLeases = snapshots.flatMap(
      ({ payload }) => payload.custodyLeases ?? [],
    );
    const readinessValues = snapshots.map(({ payload }) => payload.readiness);
    const firstMonitoring = snapshots[0]!.payload.monitoring;
    return {
      stack: homelabStackProjection(deployments),
      readiness: {
        status: aggregateStatus(readinessValues.map((value) => value.status)),
        can_start:
          snapshots.length === 1 && readinessValues[0]?.can_start === true,
        required_servers: readinessValues.reduce(
          (total, value) => total + value.required_servers,
          0,
        ),
        approved_servers: readinessValues.reduce(
          (total, value) => total + value.approved_servers,
          0,
        ),
        connected_servers: readinessValues.reduce(
          (total, value) => total + value.connected_servers,
          0,
        ),
        pending_servers: readinessValues.reduce(
          (total, value) => total + value.pending_servers,
          0,
        ),
        assigned_servers: readinessValues.reduce(
          (total, value) => total + value.assigned_servers,
          0,
        ),
        available_servers: readinessValues.reduce(
          (total, value) => total + value.available_servers,
          0,
        ),
        unassigned_servers: readinessValues.reduce(
          (total, value) => total + value.unassigned_servers,
          0,
        ),
        message: uniqueMessages(readinessValues.map((value) => value.message)),
        review_required:
          snapshots.length === 1 &&
          readinessValues[0]?.review_required === true,
      },
      nextSteps: snapshots.flatMap(({ deployment, payload }) =>
        payload.nextSteps.map((step) => ({
          ...step,
          id: `${deployment.id}:${step.id}`,
        })),
      ),
      kpis: {
        registered_servers: snapshots.reduce(
          (total, { payload }) => total + payload.kpis.registered_servers,
          0,
        ),
        healthy_servers: snapshots.reduce(
          (total, { payload }) => total + payload.kpis.healthy_servers,
          0,
        ),
        running_services: snapshots.reduce(
          (total, { payload }) => total + payload.kpis.running_services,
          0,
        ),
        active_alerts: snapshots.reduce(
          (total, { payload }) => total + payload.kpis.active_alerts,
          0,
        ),
      },
      servers,
      services,
      monitoring: {
        ...firstMonitoring,
        status: aggregateStatus(
          snapshots.map(({ payload }) => payload.monitoring.status),
        ),
        message: uniqueMessages(
          snapshots.map(({ payload }) => payload.monitoring.message),
        ),
      },
      alerts: snapshots.flatMap(({ payload }) => payload.alerts),
      // Each rollout runs on one Node, so with several deployments the one
      // active job is still attributable: it carries its deployment along.
      currentJob: activeJobAcross(snapshots),
      runtimeLifecycle:
        snapshots.length === 1 ? snapshots[0]!.payload.runtimeLifecycle : null,
      latestFailure: failures[0] ?? null,
      custodyLeases,
    };
  }

  function activeJobAcross(
    snapshots: Array<{
      deployment: StackDashboardItem;
      payload: StackOperationsPayload;
    }>,
  ): DashboardOperations["currentJob"] {
    if (snapshots.length === 1) {
      const job = snapshots[0]!.payload.currentJob;
      return job ? { ...job, kit_deployment_id: snapshots[0]!.deployment.id } : null;
    }
    for (const { deployment, payload } of snapshots) {
      const job = payload.currentJob;
      if (job && ["pending", "running"].includes(job.state)) {
        return { ...job, kit_deployment_id: deployment.id };
      }
    }
    return null;
  }

  async function loadOperationsContext(
    currentDeployments: StackDashboardItem[],
  ) {
    if (currentDeployments.length === 0) {
      operations = null;
      operationsFromCache = false;
      operationsByDeployment.clear();
      operationsDeploymentKey = "";
      operationsError = null;
      return;
    }
    const nextDeploymentKey = currentDeployments
      .map((deployment) => deployment.id)
      .sort()
      .join("\u0000");
    if (operationsDeploymentKey !== nextDeploymentKey) {
      // A different set of deployments invalidates the aggregate, cached or
      // not: keeping it would attribute one homelab's servers to another.
      // This is also what discards a cached snapshot whose deployment set no
      // longer matches, since the seed restores that key alongside it.
      operations = null;
      operationsFromCache = false;
      operationsDeploymentKey = nextDeploymentKey;
    }
    const currentDeploymentIds = new Set(
      currentDeployments.map((deployment) => deployment.id),
    );
    for (const deploymentId of operationsByDeployment.keys()) {
      if (!currentDeploymentIds.has(deploymentId)) {
        operationsByDeployment.delete(deploymentId);
      }
    }
    const results = await Promise.allSettled(
      currentDeployments.map(async (deployment) => ({
        deployment,
        payload: await getStackOperations(deployment.id),
      })),
    );
    for (const result of results) {
      if (result.status === "fulfilled") {
        operationsByDeployment.set(
          result.value.deployment.id,
          result.value.payload,
        );
      }
    }
    const snapshots = currentDeployments.flatMap((deployment) => {
      const payload = operationsByDeployment.get(deployment.id);
      return payload ? [{ deployment, payload }] : [];
    });
    if (snapshots.length === 0) {
      const messages = results.flatMap((result) =>
        result.status === "rejected"
          ? [parseApiError(result.reason).message]
          : [],
      );
      operationsError =
        uniqueMessages(messages) ||
        tr("ui.homelabDashboardPage.operationsDataIsNotAvailable");
      return;
    }
    operations = aggregateOperations(snapshots);
    operationsFromCache = false;
    writeSectionCache(CACHE_OPERATIONS, {
      deploymentKey: nextDeploymentKey,
      operations,
    });
    const rejected = results.filter(
      (result): result is PromiseRejectedResult => result.status === "rejected",
    );
    operationsError =
      rejected.length > 0
        ? trn("ui.homelab.operationsNotLoaded", rejected.length)
        : null;
    captureConnectedOnce(operations);
  }

  async function loadCanonicalInventoryContext() {
    if (deployments.length === 0) {
      canonicalServers = [];
      canonicalServices = [];
      canonicalInventoryUnavailable = false;
      canonicalInventoryResolved = false;
      return;
    }
    const [serverResult, serviceResult] = await Promise.allSettled([
      listCanonicalServers(),
      listCanonicalServices(),
    ]);
    canonicalInventoryUnavailable =
      serverResult.status === "rejected" || serviceResult.status === "rejected";
    canonicalInventoryResolved = !canonicalInventoryUnavailable;
    if (serverResult.status === "fulfilled") {
      canonicalServers = serverResult.value.filter(isCurrentCanonicalServer);
    }
    if (serviceResult.status === "fulfilled") {
      canonicalServices = serviceResult.value;
    }
  }

  function captureConnectedOnce(snapshot: DashboardOperations) {
    if (typeof window === "undefined") return;
    if (capturedConnectedHomelab) return;
    if ((snapshot.readiness.connected_servers ?? 0) < 1) return;
    const user = toTechstackAnalyticsUser(authStore.cloudUser);
    if (!user?.authSubject) return;
    capturedConnectedHomelab = true;
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
    }).capture("techstack:connected", {
      user,
      properties: {
        connection_class: isManagedOperationsStack
          ? "managed_vps"
          : "self_hosted",
      },
    });
  }

  // Structured guidance for the operations surface: prefer the already-plumbed
  // per-stack failure the backend surfaces; otherwise a degraded "not yet
  // available" state. Both render through GuidancePanel with a retry action.
  let latestFailureDeployment = $derived.by(() => {
    const failure = operations?.latestFailure;
    if (!failure) return null;
    return (
      deployments.find(
        (deployment) => deployment.id === failure.kit_deployment_id,
      ) ?? null
    );
  });
  let latestFailureOutcome = $derived(
    operations?.latestFailure
      ? outcomeFromLatestFailure(operations.latestFailure, {
          serverProvisioningMode:
            latestFailureDeployment?.server_provisioning_mode,
          connectedServers: operations?.readiness?.connected_servers,
        })
      : null,
  );

  // Destroy/decommission history is not the default dashboard state. Keep the
  // panel only while cleanup is still possible (matching custody) or the
  // fleet is empty of Nodes and still holding leases.
  let showLatestFailurePanel = $derived.by(() => {
    const failure = operations?.latestFailure;
    if (!failure || !latestFailureOutcome) return false;
    const type = (failure.type || "").trim().toLowerCase();
    if (type !== "destroy" && type !== "decommission") {
      return true;
    }
    if (failedCleanupAction) return true;
    const liveNodes = dashboardServers.length + canonicalOnlyServers.length;
    return liveNodes === 0 && custodyLeases.length > 0;
  });

  // A StackKit rollout runs on exactly one server, so its failure and its
  // progress belong on that Node's card. Null keeps them as a page-level
  // notice under the health strip (e.g. before any Node is registered).
  // The backend already resolved the target (T1-T4 in stack_operations.go);
  // this only maps it onto the rendered cards. While any Node exists the
  // failure is never a page-level banner: an unmatched target falls back to
  // its deployment's Node, then to the first Node. The compact page notice
  // remains only when there is no Node at all.
  let failureServer = $derived.by(() => {
    if (!showLatestFailurePanel || !latestFailureOutcome) return null;
    const failure = operations?.latestFailure;
    const direct = serverForOperationTarget(failure, dashboardServers);
    if (direct) return direct;
    return (
      dashboardServers.find(
        (server) => server.kit_deployment_id === failure?.kit_deployment_id,
      ) ??
      dashboardServers[0] ??
      null
    );
  });
  let runtimeJobServer = $derived(
    showRuntimeProgress
      ? serverForOperationTarget(
          {
            ...operations?.currentJob,
            kit_deployment_id:
              operations?.currentJob?.kit_deployment_id ||
              (deployments.length === 1 ? deployments[0]!.id : undefined),
          },
          dashboardServers,
        )
      : null,
  );
  function isSameServer(
    left: DashboardServer | null,
    right: DashboardServer,
  ): boolean {
    return Boolean(
      left &&
      left.id === right.id &&
      left.kit_deployment_id === right.kit_deployment_id,
    );
  }
  // The card already shows its own last outcome; do not repeat the same
  // failure under it.
  function nodeShowsFailure(server: DashboardServer): boolean {
    if (!isSameServer(failureServer, server)) return false;
    const own = actionableServerOutcome(
      server,
      canonicalServerFor(server, canonicalServers),
    );
    return !own || own.reasonCode !== latestFailureOutcome?.reasonCode;
  }

  // Last-operation failures belong in job history and on the Node/service
  // cards that actually failed. The homepage must not keep a degraded badge
  // after the failure panel is hidden (stale destroy still leaves stack
  // readiness as error/degraded).
  let showHomepageReadinessLine = $derived.by(() => {
    if (!operations?.readiness || showLatestFailurePanel) return false;
    if (operations.latestFailure) return false;
    const status = (operations.readiness.status || "").trim().toLowerCase();
    return !["error", "failed", "degraded"].includes(status);
  });

  // One-line header for the collapsed failure panel below the server list.
  let latestFailureSummary = $derived.by(() => {
    const failure = operations?.latestFailure;
    if (!failure) return "";
    const detail =
      failure.message ||
      failure.error ||
      failure.reason ||
      failure.step ||
      failure.job_id;
    const type = (failure.type || "").trim().toLowerCase();
    if (type === "remote_enrollment") {
      return tr("ui.homelab.sshFailed", { detail });
    }
    if (
      latestFailureDeployment?.server_provisioning_mode === "connect-remote" &&
      (operations?.readiness?.connected_servers ?? 0) > 0 &&
      (type === "provision" || type === "deploy")
    ) {
      return tr("ui.homelab.preparationFailed", { detail });
    }
    return tr("ui.homelab.latestFailed", {
      type: statusLabel(failure.type || "rollout"),
      detail,
    });
  });

  let operationsUnavailableOutcome = $derived.by<ServerOutcome | null>(() => {
    if (!operationsError) return null;
    return {
      status: "degraded",
      reasonCode: "operations_unavailable",
      capability: "techstack.server.operations",
      retryable: true,
      userGuidance: {
        title: tr("ui.homelabDashboardPage.operationsDataIsNotAvailable2"),
        body: operationsError,
        nextSteps: [{ id: "ops-retry", label: tr("ui.homelabDashboardPage.retry"), kind: "retry" }],
      },
    };
  });

  async function runKeyedStackAction<Result>(
    stackId: string,
    action: StackIdempotentAction,
    dispatch: (key: string) => Promise<Result>,
  ) {
    const attempt = getOrCreateStackActionIdempotency(
      window.sessionStorage,
      stackId,
      action,
    );
    try {
      const result = await dispatch(attempt.key);
      settleStackActionIdempotency(window.sessionStorage, attempt, {
        status: 202,
      });
      return result;
    } catch (cause) {
      const parsed = parseApiError(cause);
      settleStackActionIdempotency(window.sessionStorage, attempt, parsed);
      throw cause;
    }
  }

  async function retryOperations() {
    if (retryingOperations) return;
    retryingOperations = true;
    error = null;
    try {
      const failure = operations?.latestFailure;
      if (failure) {
        const targetDeployment = deployments.find(
          (deployment) => deployment.id === failure.kit_deployment_id,
        );
        if (!targetDeployment) {
          throw new Error(
            tr("ui.homelabDashboardPage.theFailedStackkitDeploymentIs"),
          );
        }
        // Same authority the guidance panel uses to decide whether to offer
        // the retry step at all, so a rendered button always has a call.
        const dispatch = retryDispatchFor(failure);
        const result =
          dispatch?.kind === "rollout"
            ? await retryStackRollout(targetDeployment.id, {
                source_job_id: dispatch.sourceJobId,
                lease_id: dispatch.leaseId,
              })
            : dispatch?.kind === "provision"
              ? await runKeyedStackAction(
                  targetDeployment.id,
                  "provision",
                  (key) => provisionStack(targetDeployment.id, key),
                )
              : dispatch?.kind === "deploy"
                ? await runKeyedStackAction(
                    targetDeployment.id,
                    "deploy",
                    (key) => deployStack(targetDeployment.id, key),
                  )
                : dispatch?.kind === "remote_enrollment"
                  ? await resumeRemoteEnrollment(targetDeployment.id, {
                      pairing_job_id: failure.job_id,
                    })
                  : null;
        if (!result) {
          throw new Error(
            tr("ui.homelabDashboardPage.thisFailedRunCannotBe"),
          );
        }
        const params = new URLSearchParams();
        params.set("stack_id", targetDeployment.id);
        if (dispatch?.kind === "remote_enrollment") {
          const pairingJobId =
            ("pairing_job_id" in result &&
              typeof result.pairing_job_id === "string" &&
              result.pairing_job_id.trim()) ||
            failure.job_id;
          if (pairingJobId) params.set("pairing_job_id", pairingJobId);
        } else if ("job_id" in result && result.job_id) {
          params.set("job_id", result.job_id);
        }
        params.set("operation", "stack");
        await goto(`/stacks/creating?${params.toString()}`);
        return;
      }
      await loadOperationsContext(deployments);
    } catch (err) {
      const parsed = parseApiError(err);
      error = tr("ui.homelab.retryFailed", { message: parsed.message });
    } finally {
      retryingOperations = false;
    }
  }

  async function refreshDashboardContext() {
    if (refreshInFlight) return;
    refreshInFlight = true;
    try {
      // The canonical operations projection is mutation authority. Refresh it
      // independently from the legacy worker list so an unrelated worker-list
      // outage cannot leave an old can_start snapshot trusted indefinitely.
      await Promise.allSettled([
        listWorkers().then((workersList) => {
          workers = workersList;
        }),
        loadOperationsContext(deployments),
        loadCanonicalInventoryContext(),
        getActiveWizardRun()
          .then((run) => {
            activeWizardRun = run;
          })
          .catch(() => {}),
      ]);
    } finally {
      refreshInFlight = false;
    }
  }

  $effect(() => {
    // Keep operations fresh even for managed-runtime stacks without worker
    // rows. An active wizard run keeps the timer alive too: the banner must
    // update while the first deployment has no stack row yet.
    if (workers.length > 0 || deployments.length > 0 || activeWizardRun) {
      refreshInterval = setInterval(() => {
        void refreshDashboardContext();
      }, 15_000);
    }

    return () => {
      if (refreshInterval) {
        clearInterval(refreshInterval);
        refreshInterval = null;
      }
    };
  });

  function isTenantContextDenial(reason: unknown): boolean {
    const parsed = parseApiError(reason);
    if (!parsed.isForbidden) return false;
    const details = parsed.details as
      { error_code?: unknown; reason_code?: unknown } | undefined;
    return (
      details?.error_code === "tenant_context_required" ||
      details?.reason_code === "tenant_context_missing"
    );
  }

  async function load() {
    if (loadRetryTimer) {
      clearTimeout(loadRetryTimer);
      loadRetryTimer = null;
    }
    loading = true;
    error = null;
    sessionRenewalRequired = false;
    sessionRenewalBodyKey = undefined;

    try {
      const timeoutMs = 8_000;
      const workersPromise = withTimeout(
        listWorkers(),
        timeoutMs,
        "workers list",
      );
      const homelabPromise = withTimeout(getHomelab(), timeoutMs, "homelab");
      const activeRunPromise = withTimeout(
        getActiveWizardRun(),
        timeoutMs,
        "active wizard run",
      );

      // Publish each section the moment ITS request settles, so a fast list
      // is on screen while a slow one is still travelling. The awaited
      // allSettled below only drives the SHARED auth decision.
      void workersPromise.then(
        (value) => {
          workers = value;
          writeSectionCache(CACHE_WORKERS, value);
        },
        () => {},
      );
      void homelabPromise.then(
        (value) => {
          homelabResolved = true;
          homelab = value;
          deployments = (value?.kit_deployments ?? []).map(
            normalizeStackDashboardItem,
          );
          writeSectionCache(CACHE_HOMELAB, value);
        },
        () => {},
      );

      const [workersRes, homelabRes, activeRunRes] = await Promise.allSettled([
        workersPromise,
        homelabPromise,
        activeRunPromise,
      ]);

      // The loaders share one credential path; recover the session once
      // instead of surfacing several copies of the same auth failure.
      const rejections = [workersRes, homelabRes, activeRunRes].filter(
        (result): result is PromiseRejectedResult =>
          result.status === "rejected",
      );
      const authFailure = rejections.find(
        (result) => parseApiError(result.reason).isAuthError,
      );
      if (authFailure) {
        const outcome = await authHandler.handleUnauthorized(
          () => load(),
          undefined,
          authFailure.reason,
        );
        if (outcome === "reauth_required") sessionRenewalRequired = true;
        return;
      }
      const tenantDenied = rejections.find((result) =>
        isTenantContextDenial(result.reason),
      );
      if (tenantDenied) {
        sessionRenewalRequired = true;
        sessionRenewalBodyKey = "auth.session.tenantContextRequired";
        return;
      }

      if (workersRes.status === "rejected") {
        const parsed = parseApiError(workersRes.reason);
        // Keep the last verified list (cache-hydrated or from a previous
        // load) and say it is stale. Emptying it made every deploy-cutover
        // 502 look like a wiped fleet.
        if (workers.length === 0) {
          if (!error) error = parsed.message;
        } else {
          dataStale = true;
        }
      } else {
        dataStale = false;
      }

      if (homelabRes.status === "rejected") {
        const parsed = parseApiError(homelabRes.reason);
        if (homelabResolved) {
          dataStale = true;
        } else if (!error) {
          error = parsed.message;
        }
        // Keep the last owner-scoped homelab and its deployment IDs while a
        // refresh is unavailable. Clearing them would erase the aggregated
        // telemetry and falsely render the first-run empty state on a 429.
        if (!homelabResolved) {
          homelab = null;
          deployments = [];
        }
      }
      // success already published above
      // Same for the run banner: absent or unreadable simply means no banner.
      activeWizardRun =
        activeRunRes.status === "fulfilled" ? activeRunRes.value : null;

      // Three independent reads. loadStackContext used to run sequentially in
      // front of the other two, which put a whole extra round trip between the
      // homelab landing and the server cards appearing — it needs the homelab,
      // nothing else needs it.
      await Promise.all([
        loadStackContext(singleDeployment).finally(() => {
          stackContextResolved = true;
        }),
        loadOperationsContext(deployments).finally(() => {
          operationsResolved = true;
        }),
        loadCanonicalInventoryContext(),
      ]);
      if (deployments.length > 0) void loadPeopleAndDevices();
    } catch (err) {
      if (isCancelledRequestError(err)) return;
      const parsed = parseApiError(err);

      // Handle auth errors with the global handler
      if (parsed.isAuthError) {
        const outcome = await authHandler.handleUnauthorized(
          () => load(),
          undefined,
          err,
        );
        if (outcome === "reauth_required") sessionRenewalRequired = true;
        return;
      }

      error = parsed.message;
    } finally {
      loading = false;
      // A failed or stale load retries itself with backoff. Without this a
      // first load that lands in a deploy-cutover window left an empty page
      // forever: the 15 s refresh timer only arms once data exists.
      if (error || dataStale) {
        scheduleLoadRetry();
      } else {
        loadRetryAttempt = 0;
      }
    }
  }

  function scheduleLoadRetry() {
    if (loadRetryTimer) return;
    const delayMs = Math.min(5_000 * 2 ** loadRetryAttempt, 30_000);
    loadRetryAttempt += 1;
    loadRetryTimer = setTimeout(() => {
      loadRetryTimer = null;
      void load();
    }, delayMs);
  }

  async function refreshWorkerRegistryUrl() {
    registryUrlError = null;
    try {
      const res = await getWorkerRegistryUrl();
      if (res?.url) {
        serverUrl = res.url;
        registryMode = String(res.mode || "");
      }
    } catch (err) {
      // Non-blocking: fall back to best-effort origin-based URL.
      const parsed = parseApiError(err);
      registryUrlError = parsed.message;
    }
  }

  onMount(() => {
    const params = new URLSearchParams(window.location.search);
    reviewPhase = params.get("phase") === "review";

    // Best-effort fallback (must be in onMount for SSR)
    serverUrl = getBestPublicServerUrl() || window.location.origin;

    // Try to resolve a worker-reachable URL (LAN IP / tunnel / tailscale / custom).
    void refreshWorkerRegistryUrl();

    // Paint what we last saw before the network is even asked. Cached values
    // are a render accelerator, never an authority: load() revalidates them
    // immediately and overwrites whatever came back.
    const cachedWorkers = readSectionCache<typeof workers>(CACHE_WORKERS);
    if (cachedWorkers) workers = cachedWorkers;
    const cachedHomelab = readSectionCache<typeof homelab>(CACHE_HOMELAB);
    if (cachedHomelab) {
      homelab = cachedHomelab;
      deployments = (cachedHomelab?.kit_deployments ?? []).map(
        normalizeStackDashboardItem,
      );
      // Without this the cache was invisible: the sections stayed behind
      // their "not resolved yet" gate and the skeleton covered the very
      // content that had just been restored.
      homelabResolved = true;
    }
    const cachedOperations = readSectionCache<{
      deploymentKey: string;
      operations: DashboardOperations;
    }>(CACHE_OPERATIONS);
    if (cachedOperations?.operations && deployments.length > 0) {
      operations = cachedOperations.operations;
      operationsDeploymentKey = cachedOperations.deploymentKey;
      operationsFromCache = true;
    }

    load();
    syncServiceSheetFromLocation();
    window.addEventListener("popstate", syncServiceSheetFromLocation);

    return () => {
      window.removeEventListener("popstate", syncServiceSheetFromLocation);
      if (loadRetryTimer) {
        clearTimeout(loadRetryTimer);
        loadRetryTimer = null;
      }
    };
  });

  async function assignWorkerToDeployment(
    kitDeploymentId: string | undefined,
    workerId: string,
  ) {
    if (!kitDeploymentId || !operationsEvidenceFresh) return;
    assigningWorkerId = workerId;
    error = null;
    try {
      await assignStackWorker(kitDeploymentId, workerId);
      await load();
    } catch (err) {
      const parsed = parseApiError(err);
      error = parsed.message;
    } finally {
      assigningWorkerId = null;
    }
  }

  let rolloutLoading = $state(false);

  async function startRollout(deploymentId = singleDeployment?.id) {
    const deployment = deployments.find((item) => item.id === deploymentId);
    if (!deployment) return;
    if (!canRollout) {
      error =
        operationsError ||
        operations?.readiness?.message ||
        tr("ui.homelabDashboardPage.rolloutReadinessIsUnavailableRefresh");
      return;
    }

    rolloutLoading = true;
    error = null;

    try {
      const result = await runKeyedStackAction(deployment.id, "deploy", (key) =>
        deployStack(deployment.id, key),
      );

      // Success - navigate to rollout status page
      goto(
        `/stacks/creating?name=${encodeURIComponent(deployment.name)}&stack_id=${encodeURIComponent(
          deployment.id,
        )}${result.job_id ? `&job_id=${encodeURIComponent(result.job_id)}` : ""}&phase=rollout`,
      );
    } catch (err) {
      const parsed = parseApiError(err);
      error = tr("ui.homelab.rolloutFailed", { message: parsed.message });
    } finally {
      rolloutLoading = false;
    }
  }

  function canReconnectManagedRuntime(server: StackOperationServer): boolean {
    if (!server.lease_id || !isManagedRuntimeServer(server)) return false;
    return ["stalled", "pending", "stale", "offline", "error"].includes(
      server.health.state,
    );
  }

  // Reconnect re-runs the provider enrollment probe for a managed runtime. It
  // cannot restart the Guard agent on the machine, so a probe that succeeds
  // while the server stays offline is a normal — and previously invisible —
  // result: the button spun, the page reloaded, and the card looked identical.
  // Every path below therefore ends in a stated outcome.
  async function reconnectManagedRuntime(leaseId: string | undefined) {
    const normalizedLeaseId = leaseId?.trim();
    if (!normalizedLeaseId) return;

    reconnectingLeaseId = normalizedLeaseId;
    reconnectOutcome = null;
    decommissionError = null;
    error = null;
    try {
      const status = await reconnectMonthlyRuntime(normalizedLeaseId);
      await load();
      reconnectOutcome = describeReconnectResult(normalizedLeaseId, status);
    } catch (err) {
      const parsed = parseApiError(err);
      reconnectOutcome = {
        leaseId: normalizedLeaseId,
        tone: "error",
        title: tr("ui.homelabDashboardPage.reconnectFailed"),
        body:
          parsed.message ||
          tr("ui.homelabDashboardPage.theRuntimeProbeReturnedNo"),
      };
    } finally {
      reconnectingLeaseId = null;
    }
  }

  // describeReconnectResult reports what the probe actually established. It
  // never claims the server is back: that claim belongs to the reloaded
  // readiness projection, which is the only heartbeat evidence there is.
  function describeReconnectResult(
    leaseId: string,
    status: MonthlyRuntimeStatus,
  ): ReconnectOutcome {
    const server = (operations?.servers ?? []).find(
      (candidate) => candidate.lease_id === leaseId,
    );
    const online =
      server !== undefined &&
      !["offline", "stalled", "stale", "error", "pending"].includes(
        server.health.state,
      );
    if (online) {
      return {
        leaseId,
        tone: "success",
        title: tr("ui.homelabDashboardPage.nodeIsReportingAgain"),
        body: tr("ui.homelab.probeAnswered", {
          state: statusLabel(server.health.state),
        }),
      };
    }
    const machineState =
      status.status?.state?.trim() ||
      status.observed_state?.trim() ||
      tr("ui.serverDetail.sourceNotReported");
    const enrollment =
      status.enrollment_status?.trim() || tr("ui.serverDetail.sourceNotReported");
    return {
      leaseId,
      tone: "warning",
      title: tr("ui.homelabDashboardPage.runtimeAnsweredButTheNode"),
      body: tr("ui.homelab.probeOffline", { machineState, enrollment }),
    };
  }

  async function decommissionCustodyLease(leaseId: string | undefined) {
    const normalizedLeaseId = leaseId?.trim();
    if (!normalizedLeaseId) return;

    cleaningCustodyLeaseId = normalizedLeaseId;
    decommissionError = null;
    error = null;
    try {
      await decommissionMonthlyRuntime(normalizedLeaseId);
      await load();
    } catch (err) {
      const parsed = parseApiError(err);
      decommissionError = parsed.message || tr("ui.homelabDashboardPage.decommissionFailed");
    } finally {
      cleaningCustodyLeaseId = null;
    }
  }

  async function resolveCustodyLease(lease: StackCustodyLease) {
    if (resolvingCustodyLeaseId) return;
    const confirmed = await confirmInApp({
      title: tr("ui.homelabDashboardPage.resolveStaleCustodyRecord"),
      message:
        tr("ui.homelabDashboardPage.confirmThatTheProviderResource"),
      confirmText: tr("ui.homelabDashboardPage.resolveRecord"),
      tone: "warning",
    });
    if (!confirmed) return;

    resolvingCustodyLeaseId = lease.lease_id;
    decommissionError = null;
    error = null;
    try {
      await resolveMonthlyRuntimeCustody(lease.lease_id);
      await load();
    } catch (err) {
      const parsed = parseApiError(err);
      decommissionError =
        parsed.code === "upstream_unavailable"
          ? tr("ui.homelabDashboardPage.custodyRecordUnchangedTheTechstack")
          : parsed.message || tr("ui.homelabDashboardPage.custodyResolutionFailed");
    } finally {
      resolvingCustodyLeaseId = null;
    }
  }

  async function retryDestroyCleanup() {
    const selection = failedCleanupAction;
    if (!selection) {
      decommissionError =
        tr("ui.homelabDashboardPage.theFailedCleanupIsNot");
      return;
    }
    if (selection.action === "resolve_custody") {
      await resolveCustodyLease(selection.lease);
      return;
    }
    await decommissionCustodyLease(selection.lease.lease_id);
  }

  function statusKind(status: string): string {
    switch (status) {
      case "active":
      case "completed":
      case "connected":
      case "healthy":
      case "running":
      case "ok":
        return "ok";
      case "current":
      case "ready":
      case "stale":
      case "pending":
        return "warn";
      case "offline":
      case "failed":
      case "degraded":
      case "error":
        return "error";
      default:
        return "off";
    }
  }

  function serverDetailsHref(
    serverId: string,
    kitDeploymentId = singleDeployment?.id,
  ): string {
    return kitDeploymentId
      ? `/stacks/${encodeURIComponent(
          kitDeploymentId,
        )}/servers/${encodeURIComponent(serverId)}`
      : "#";
  }

  /**
   * The monitoring page answers node state and availability history; services
   * live on their own page. `?focus=` is gone with the blocks it scrolled to,
   * so each tile links to the surface that actually reports its number.
   */
  function monitoringHref(focus: string): string {
    if (focus === "services") return servicesHref("services");
    return focus === "history" ? "/monitoring#history" : "/monitoring";
  }

  function servicesHref(tab = "services"): string {
    return `/services?tab=${encodeURIComponent(tab)}`;
  }

  function addServerHref(): string {
    return singleDeployment
      ? `/stacks/${encodeURIComponent(singleDeployment.id)}/servers/new`
      : "/stacks/new";
  }

  function hasManagedRuntimeLease(item: StackDashboardItem | null): boolean {
    if (!item) return false;
    return (
      item.server_provisioning_mode === "kombify-cloud" ||
      item.server_mode === "monthly-runtime" ||
      item.runtime_lane === "monthly-runtime" ||
      Boolean(item.lease_id?.trim())
    );
  }

  function managedRuntimeProviderLabel(item: StackDashboardItem): string {
    return (
      item.provider_id ||
      item.lease_provider ||
      item.simulate_provider_id ||
      item.provider ||
      "managed-runtime"
    );
  }

  function managedRuntimeServerHref(item: StackDashboardItem): string {
    return item.lease_id ? serverDetailsHref(`lease:${item.lease_id}`) : "#";
  }

  function isDashboardServer(server: StackOperationServer): boolean {
    if (isManagedRuntimeServer(server)) return true;
    if (!server.approved) return false;
    if (server.assignment === "stack") return true;
    return server.assignment === "unassigned" && server.assignable !== false;
  }
  // ── Dashboard presets ────────────────────────────────────────────────
  // One data load above feeds whichever preset the owner picked in Settings.
  let peopleState = $state<SectionState<PersonEntry>>({ state: "loading" });
  let devicesState = $state<SectionState<DeviceEntry>>({ state: "loading" });
  let peopleLoadKey = "";

  let showStrata = $derived(
    dashboardPreset.current === "strata" &&
      deployments.length > 0 &&
      Boolean(operations) &&
      hasServerInventory,
  );
  let nodeViews = $derived<NodeView[]>(
    buildNodeViews(
      dashboardServers,
      canonicalOnlyServers,
      canonicalServers,
      canonicalServices,
      primaryWorkloadKeys(deployments),
    ),
  );

  // The wizard run's notice belongs to the one Node its rollout targets: the
  // job's server, else a Node of the run's deployment. Only a homelab with no
  // Node at all shows it at the top.
  let wizardRunNotice = $derived(
    activeWizardRun && wizardRunNeedsAttention(activeWizardRun)
      ? activeWizardRun
      : null,
  );
  let wizardRunNodeKey = $derived.by(() => {
    const run = wizardRunNotice;
    if (!run || !hasServerInventory || nodeViews.length === 0) return null;
    const target = run.target_server_id || run.node_id || "";
    const node =
      (target &&
        nodeViews.find(
          (candidate) =>
            candidate.server?.id === target ||
            candidate.nodeId === target ||
            candidate.canonical?.id === target,
        )) ||
      nodeViews.find(
        (candidate) => candidate.deploymentId === run.kit_deployment_id,
      ) ||
      nodeViews[0];
    return node.key;
  });

  // Top placement is only for a homelab whose Node list is settled and empty:
  // while inventory loads the notice waits instead of flashing above Nodes.
  let wizardRunTopNotice = $derived(
    Boolean(wizardRunNotice) &&
      !wizardRunNodeKey &&
      homelabResolved &&
      (deployments.length === 0 ||
        (operationsResolved &&
          (canonicalInventoryResolved || canonicalInventoryUnavailable) &&
          !hasServerInventory)),
  );

  async function refreshWizardRun() {
    try {
      activeWizardRun = await getActiveWizardRun();
    } catch {
      // The next dashboard refresh retries; an unreadable run shows nothing.
    }
  }

  function deploymentName(deploymentId: string): string {
    return (
      deployments.find((deployment) => deployment.id === deploymentId)?.name ||
      tr("ui.homelabDashboardPage.stackkitDeployment")
    );
  }

  function deploymentHref(deploymentId: string): string {
    return deploymentId ? `/stacks/${encodeURIComponent(deploymentId)}` : "#";
  }

  function nodeHref(node: NodeView): string {
    return node.server
      ? serverDetailsHref(node.server.id, node.server.kit_deployment_id)
      : serverDetailsHref(node.nodeId, node.deploymentId || undefined);
  }

  function nodeNameFor(server: DashboardServer | null): string | null {
    if (!server) return null;
    return (
      nodeViews.find(
        (node) =>
          node.server?.id === server.id &&
          node.server.kit_deployment_id === server.kit_deployment_id,
      )?.name ?? null
    );
  }

  const unhealthyConnections = new Set(["offline", "stale", "degraded", "revoked"]);
  let strataFacts = $derived<StatusFacts>({
    nodes: nodeViews.length,
    unhealthyNodes: nodeViews.filter(
      (node) =>
        node.axes.health === "degraded" ||
        node.axes.health === "unhealthy" ||
        unhealthyConnections.has(node.axes.connection),
    ).length,
    alerts: operations?.kpis.active_alerts ?? 0,
    rolloutNode: nodeNameFor(runtimeJobServer),
    failureNode: nodeNameFor(failureServer),
  });
  let strataRollout = $derived(
    runtimeJobServer
      ? {
          nodeKey: `${runtimeJobServer.kit_deployment_id}:${runtimeJobServer.id}`,
          label:
            currentRuntimePhase?.message ||
            operations?.currentJob?.message ||
            `StackKit rollout · ${statusLabel(
              operations?.runtimeLifecycle?.current_phase ||
                operations?.currentJob?.step ||
                "pending",
            )}`,
          progress: Math.max(
            0,
            Math.min(100, operations?.currentJob?.progress ?? 0),
          ),
        }
      : null,
  );

  /**
   * People and devices load beside the fleet, never in front of it: each
   * source settles into its own honest state.
   */
  async function loadPeopleAndDevices() {
    const ids = deployments.map((deployment) => deployment.id);
    const key = ids.slice().sort().join("\u0000");
    const ownerName =
      authStore.cloudUser?.name || authStore.cloudUser?.email || tr("ui.homelabDashboardPage.you");
    if (key !== peopleLoadKey) {
      peopleLoadKey = key;
      peopleState = { state: "loading" };
      devicesState = { state: "loading" };
    }
    const [people, devices] = await Promise.all([
      loadPeople(ids, ownerName),
      loadDevices(),
    ]);
    peopleState = people;
    devicesState = devices;
  }

  // Service detail sheet, deep-linked as `?service=<id>` like the Services
  // page: a click pushes the address, back closes it.
  let detailServiceId = $state<string | null>(null);
  let detailService = $derived(
    detailServiceId
      ? (canonicalServices.find((service) => service.id === detailServiceId) ??
          null)
      : null,
  );
  let detailNodeName = $derived(
    detailService
      ? nodeViews.find(
          (node) =>
            node.apps.includes(detailService!) ||
            node.system.includes(detailService!),
        )?.name
      : undefined,
  );

  function openServiceSheet(serviceId: string) {
    detailServiceId = serviceId;
    const url = new URL(page.url.href);
    url.searchParams.set("service", serviceId);
    pushState(url, {});
  }

  function closeServiceSheet() {
    if (!detailServiceId) return;
    detailServiceId = null;
    const url = new URL(page.url.href);
    url.searchParams.delete("service");
    pushState(url, {});
  }

  function syncServiceSheetFromLocation() {
    detailServiceId = new URL(window.location.href).searchParams.get("service");
  }
</script>

<!-- With a Node inventory the dashboard is bound to the viewport: header,
     KPI strip and footer stay put and only the Node and device lists scroll.
     `contain: size` keeps the content from stretching the shell's content
     slot, so `h-full` resolves to the height left beside the footer; on a
     screen too short for even the bounded lists the page area scrolls itself. -->
<div
  class="mx-auto max-w-[90rem] p-4 md:p-6 {hasServerInventory && operations
    ? 'flex h-full min-h-0 flex-col overflow-y-auto [contain:size] md:py-4'
    : ''}"
  data-testid="stacks-dashboard"
>
  <!-- Declared once at the root so both server surfaces report the same
       Reconnect result instead of one of them staying silent. -->
  {#snippet reconnectOutcomeBanner()}
    {#if reconnectOutcome}
      <div
        class="mt-4 rounded-lg border p-3 {reconnectOutcome.tone === 'error'
          ? 'border-destructive/30 bg-destructive/10'
          : reconnectOutcome.tone === 'warning'
            ? 'border-warning/40 bg-warning/10'
            : 'border-success/40 bg-success/10'}"
        data-testid="reconnect-outcome"
        data-tone={reconnectOutcome.tone}
        role="status"
      >
        <p class="text-sm font-semibold text-foreground">
          {reconnectOutcome.title}
        </p>
        <p class="mt-1 text-sm text-muted-foreground">
          {reconnectOutcome.body}
        </p>
      </div>
    {/if}
  {/snippet}

  {#snippet runtimeProgressCard()}
    <section
      data-kx="plate"
      class="p-5"
      data-testid="runtime-lifecycle-progress"
      aria-label={tr("ui.homelab.rolloutProgress")}
    >
      <div class="flex items-start justify-between gap-4">
        <div>
          <p class="text-sm font-medium text-foreground">{tr("ui.homelab.runtimeRollout")}</p>
          <p class="mt-1 text-sm text-muted-foreground">
            {currentRuntimePhase?.message ||
              operations?.currentJob?.message ||
              tr("ui.homelabDashboardPage.thePersistedRolloutIsWaiting")}
          </p>
        </div>
        <span
          data-kx="status"
          data-status={statusKind(operations?.currentJob?.state || "pending")}
          class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
        >
          {statusLabel(operations?.currentJob?.state || "pending")}
        </span>
      </div>
      <div class="mt-4 h-2 overflow-hidden rounded-full bg-muted">
        <div
          class="h-full rounded-full bg-primary transition-[width]"
          style={`width: ${Math.max(0, Math.min(100, operations?.currentJob?.progress ?? 0))}%`}
        ></div>
      </div>
      <div
        class="mt-2 flex flex-wrap justify-between gap-2 text-xs text-muted-foreground"
      >
        <span>
          {tr("ui.homelab.phase", {
            phase: statusLabel(
              operations?.runtimeLifecycle?.current_phase ||
              operations?.currentJob?.step ||
              "pending",
            ),
          })}
        </span>
        <span>{operations?.currentJob?.progress ?? 0}%</span>
      </div>
    </section>
  {/snippet}

  <!-- The latest failed operation with its guidance and, for a failed
       cleanup, the exact cleanup action. -->
  {#snippet latestFailureGuidance(surface: string)}
    {#if latestFailureOutcome}
      <p class="mb-2 text-sm font-medium text-foreground">
        {latestFailureSummary}
      </p>
      {#if failedCleanupAction && ["destroy", "decommission"].includes(operations?.latestFailure?.type?.toLowerCase() ?? "")}
        <Button
          variant="destructive"
          size="sm"
          class="mb-2"
          testId="retry-destroy-cleanup"
          onclick={retryDestroyCleanup}
          disabled={cleaningCustodyLeaseId !== null}
        >
          {failedCleanupAction.action === "resolve_custody"
            ? tr("ui.homelabDashboardPage.resolveExactRecord")
            : tr("ui.homelabDashboardPage.retryExactCleanup")}
        </Button>
      {/if}
      <GuidancePanel
        outcome={latestFailureOutcome}
        {surface}
        resourceId={operations?.latestFailure?.kit_deployment_id ||
          homelab?.homelab?.id}
        resourceName={operations?.latestFailure?.kit_deployment_name ||
          homelabTitle}
        onRetry={retryOperations}
        retrying={retryingOperations}
        compact
      />
    {/if}
  {/snippet}

  {#snippet wizardRunNodeNotice(node: NodeView)}
    {#if wizardRunNotice && node.key === wizardRunNodeKey}
      <WizardRunBanner run={wizardRunNotice} onChanged={refreshWizardRun} />
    {/if}
  {/snippet}

  {#snippet serverNotice(server: DashboardServer)}
    {#if isSameServer(runtimeJobServer, server)}
      {@render runtimeProgressCard()}
    {/if}
    {#if nodeShowsFailure(server)}
      <div data-testid="node-latest-failure">
        {@render latestFailureGuidance("stacks.hub.server")}
      </div>
    {/if}
  {/snippet}

  {#snippet headerActions()}
    {#if deployments.length > 0 && homelabResolved}
      <div
        class="flex flex-wrap items-center gap-2"
        data-testid="stack-action-bar"
      >
        <Button
          variant="primary"
          testId="add-server-button"
          anchor="techstack-add-server"
          onclick={() => goto(addServerHref())}
        >
          <Plus class="h-4 w-4" />
          {tr("ui.homelab.newNode")}
        </Button>
        {#if showReviewStart}
          <Button
            variant="primary"
            testId="review-start-button"
            onclick={() => startRollout()}
            disabled={!canRollout || rolloutLoading}
          >
            <Play class="h-4 w-4" />
            {rolloutLoading ? tr("ui.homelabDashboardPage.starting") : tr("ui.homelabDashboardPage.reviewStart")}
          </Button>
        {/if}
        {#if singleDeployment}
          <Button
            variant="secondary"
            onclick={() => {
              importExportMode = "export";
              showImportExport = true;
            }}
          >
            {tr("ui.common.importExport")}
          </Button>
        {/if}
        <Button
          variant="secondary"
          testId="dashboard-refresh-button"
          onclick={load}
          disabled={loading}
          ariaLabel={tr("ui.homelabDashboardPage.refresh")}
        >
          <RefreshCw class="h-4 w-4 {loading ? 'animate-spin' : ''}" />
        </Button>
      </div>
    {/if}
  {/snippet}

  {#snippet nodeControls(node: NodeView)}
    <NodeActions
      {node}
      {operationsEvidenceFresh}
      assigning={Boolean(node.server && assigningWorkerId === node.server.id)}
      reconnecting={Boolean(
        node.server?.lease_id && reconnectingLeaseId === node.server.lease_id,
      )}
      {canRollout}
      {rolloutLoading}
      canReconnect={node.server ? canReconnectManagedRuntime(node.server) : false}
      onAssign={() =>
        node.server &&
        assignWorkerToDeployment(node.server.kit_deployment_id, node.server.id)}
      onDeploy={() => node.server && startRollout(node.server.kit_deployment_id)}
      onReconnect={() =>
        node.server?.lease_id && reconnectManagedRuntime(node.server.lease_id)}
      onOpenDetails={() => goto(nodeHref(node))}
    />
  {/snippet}

  {#if !showStrata}
    <div
      class={hasServerInventory ? "mb-3" : "mb-6"}
      data-testid="homelab-header"
    >
      <!-- The Stack Identity is the title; no second badge beside the actions. -->
      <header
        class="flex flex-col items-start justify-between gap-4 py-4 sm:flex-row"
      >
        <!-- Below 48rem the fixed menu toggle sits top-left: the title moves
             beside it and the eyebrow is dropped. -->
        <div class="min-w-0 pl-14 min-[48rem]:pl-0">
          {#if homelabName}
            <p
              class="mb-1 hidden text-[0.72rem] font-bold tracking-[0.08em] text-primary uppercase min-[48rem]:block"
            >
              {tr("ui.dashboard.yourHomelab")}
            </p>
          {/if}
          <HomelabTitle
            identity={$stackIdentity}
            fallback={homelabTitle}
            class="text-[clamp(1.35rem,2.5vw,2rem)] leading-tight font-bold text-foreground"
          />
        </div>
        <div class="flex flex-wrap gap-2">
          {@render headerActions()}
        </div>
      </header>
    </div>
  {/if}

  <!-- Health first: the KPI strip sits directly under the title, with any
       rollout or failure that no Node card can hold. -->
  {#if deployments.length > 0 && operations && (hasServerInventory || (showRuntimeProgress && !runtimeJobServer) || (showLatestFailurePanel && latestFailureOutcome && !failureServer))}
    <div
      class="{hasServerInventory ? 'mb-3' : 'mb-6'} space-y-3"
      data-testid="homelab-health"
    >
      {#if hasServerInventory && !showStrata}
        <div
          data-kx="plate"
          class="grid grid-cols-2 overflow-hidden text-sm sm:flex sm:divide-x sm:divide-border"
          data-testid="stack-kpi-strip"
        >
          <a
            class="flex min-w-0 flex-1 items-center gap-1.5 px-3 py-2 transition-colors hover:bg-muted/30"
            href={monitoringHref("servers")}
          >
            <Server class="h-4 w-4 shrink-0 text-primary" />
            <span class="truncate text-xs text-muted-foreground">{tr("ui.dashboard.nodes")}</span>
            <span class="font-semibold text-foreground">
              {dashboardServerCount}
            </span>
            <span
              class="truncate text-xs text-muted-foreground"
              data-testid="worker-connected-count"
              >{tr("ui.homelab.connectedOfTotal", { connected: dashboardConnectedServerCount, total: dashboardServerCount })}</span
            >
          </a>
          <a
            class="flex min-w-0 flex-1 items-center gap-1.5 px-3 py-2 transition-colors hover:bg-muted/30"
            href={monitoringHref("health")}
            title={tr("ui.homelab.heartbeat")}
          >
            <HeartPulse class="h-4 w-4 shrink-0 text-success" />
            <span class="truncate text-xs text-muted-foreground">{tr("ui.dashboard.healthy")}</span>
            <span class="font-semibold text-foreground">
              {dashboardHealthyServerCount}
            </span>
          </a>
          <a
            class="flex min-w-0 flex-1 items-center gap-1.5 px-3 py-2 transition-colors hover:bg-muted/30"
            href={monitoringHref("services")}
            data-testid="stack-running-services-kpi"
          >
            <Waypoints class="h-4 w-4 shrink-0 text-info" />
            <span class="truncate text-xs text-muted-foreground">{tr("ui.dashboard.services")}</span>
            <span
              class="font-semibold text-foreground"
              data-testid="metric-card-value"
            >
              {dashboardRunningServiceCount}
            </span>
            <span class="truncate text-xs text-muted-foreground">
              {tr("ui.homelab.recorded", { count: dashboardServiceCount })}
            </span>
          </a>
          {#if devicesState.state === "ready"}
            <a
              class="flex min-w-0 flex-1 items-center gap-1.5 px-3 py-2 transition-colors hover:bg-muted/30"
              href="#dashboard-people-title"
              data-testid="stack-devices-kpi"
            >
              <MonitorSmartphone class="h-4 w-4 shrink-0 text-primary" />
              <span class="truncate text-xs text-muted-foreground">{tr("ui.dashboard.devices")}</span>
              <span class="font-semibold text-foreground">
                {devicesState.items.length}
              </span>
              <span class="truncate text-xs text-muted-foreground">
                {tr("ui.homelab.onlineCount", { count: devicesState.items.filter((item) => item.online).length })}
              </span>
            </a>
          {/if}
          <a
            class="flex min-w-0 flex-1 items-center gap-1.5 px-3 py-2 transition-colors hover:bg-muted/30"
            href={monitoringHref("alerts")}
            title={statusLabel(operations.monitoring.status)}
          >
            <AlertTriangle class="h-4 w-4 shrink-0 text-warning" />
            <span class="truncate text-xs text-muted-foreground">{tr("ui.dashboard.alerts")}</span>
            <span class="font-semibold text-foreground">
              {operations.kpis.active_alerts}
            </span>
          </a>
        </div>
      {/if}
      {#if showRuntimeProgress && !runtimeJobServer}
        {@render runtimeProgressCard()}
      {/if}
      {#if showLatestFailurePanel && latestFailureOutcome && !failureServer}
        <div
          class="space-y-2 rounded-lg border border-destructive/30 bg-destructive/5 p-3"
          data-testid="latest-failure-notice"
          role="status"
        >
          {@render latestFailureGuidance("stacks.hub")}
        </div>
      {/if}
    </div>
  {/if}

  {#if wizardRunNotice && wizardRunTopNotice}
    <div class="mb-6">
      <WizardRunBanner run={wizardRunNotice} onChanged={refreshWizardRun} />
    </div>
  {/if}

  {#if sessionRenewalRequired}
    <div class="mb-6">
      <SessionRenewalPanel bodyKey={sessionRenewalBodyKey} />
    </div>
  {:else if error}
    <Surface class="mb-6 p-4">
      <div data-testid="stacks-error-panel">
        <p class="text-foreground">{error}</p>
      </div>
    </Surface>
  {:else if dataStale}
    <Surface class="mb-6 p-3">
      <p
        class="text-sm text-muted-foreground"
        data-testid="stacks-stale-notice"
      >
        {tr("ui.homelabDashboardPage.lastVerifiedStateRetainedThe")}
      </p>
    </Surface>
  {/if}

  {#if deployments.length > 0 && operationsUnavailableOutcome}
    <div class="mb-6">
      <GuidancePanel
        outcome={operationsUnavailableOutcome}
        surface="stacks.hub"
        onRetry={retryOperations}
        retrying={retryingOperations}
      />
    </div>
  {/if}

  <!-- Two skeletons, not one, because the two sections arrive separately.
       The homelab one clears as soon as the homelab lands; only the operations
       area keeps waiting for the per-deployment reads behind it. -->
  {#if loading && !homelabResolved}
    <section
      class="mb-8 space-y-4"
      data-testid="dashboard-loading-state"
      aria-label={tr("ui.homelab.loading")}
      aria-busy="true"
    >
      <div class="h-10 animate-pulse rounded-lg bg-muted"></div>
    </section>
  {/if}

  {#if loading && homelabResolved && deployments.length > 0 && !operations && !operationsError}
    <section
      class="mb-8 space-y-4"
      data-testid="dashboard-operations-loading-state"
      aria-label={tr("ui.homelab.loadingOperations")}
      aria-busy="true"
    >
      <div class="grid gap-3 lg:grid-cols-2">
        <div data-kx="plate" class="h-44 animate-pulse"></div>
        <div data-kx="plate" class="h-44 animate-pulse"></div>
      </div>
    </section>
  {/if}

  {#if deployments.length > 0 && operations}
    <section
      class={hasServerInventory
        ? "flex flex-1 flex-col gap-3"
        : "mb-8 space-y-6"}
      data-testid="stack-operations-dashboard"
      aria-label={tr("ui.homelab.operations")}
    >
      {#if operationsFromCache && !operationsError}
        <!-- The cached snapshot is on screen so the dashboard is readable at
             once. Say plainly that it is not yet reconfirmed, because the
             mutation controls are disabled for exactly that reason. -->
        <p
          class="text-sm text-muted-foreground"
          data-testid="operations-evidence-reconfirming"
          role="status"
        >
          {tr("ui.homelab.reconfirming")}
        </p>
      {/if}

      {#if operationsError}
        <div
          class="rounded-lg border border-warning/40 bg-warning/10 p-4"
          data-testid="operations-evidence-stale"
          role="status"
        >
          <p class="font-medium text-foreground">
            {tr("ui.homelab.evidenceStale")}
          </p>
          <p class="mt-1 text-sm text-muted-foreground">
            {tr("ui.homelab.evidenceStaleBody")} {operationsError}
          </p>
        </div>
      {/if}

      {#if showReviewStart && canRollout}
        <section
          class="rounded-lg border border-primary/40 bg-primary/10 p-4"
          data-testid="stackkit-rollout-guidance"
          role="status"
        >
          <p class="font-medium text-foreground">
            {tr("ui.homelab.nodeConnected")}
          </p>
          <p class="mt-1 text-sm text-muted-foreground">
            {tr("ui.homelab.reviewHint")}
          </p>
        </section>
      {/if}

      <!-- Review/waiting guidance can sit under the title. A failed job must
           not: that belongs in history and on the Node or service card. -->
      {#if !showStrata && showHomepageReadinessLine}
        <div
          class="flex min-w-0 flex-wrap items-center gap-2 text-sm"
          data-testid="stack-readiness-line"
        >
          <span
            data-kx="status"
            data-status={statusKind(operations.readiness.status)}
            class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
          >
            {reviewPhase ? tr("ui.homelabDashboardPage.review") : statusLabel(operations.readiness.status)}
          </span>
          {#if operations.readiness.message}
            <span class="text-muted-foreground"
              >{operations.readiness.message}</span
            >
          {:else if !hasServerInventory}
            <span class="text-muted-foreground">
              {tr("ui.homelab.noInventory")}
            </span>
          {/if}
        </div>
      {/if}

      {#if hasServerInventory}
        {#if showStrata}
          <StrataDashboard
            title={homelabTitle}
            identity={$stackIdentity}
            nodes={nodeViews}
            facts={strataFacts}
            stats={{
              apps: dashboardServiceCount,
              appsNote: showRuntimeProgress
                ? "1 rolling out"
                : dashboardRunningServiceCount > 0 ||
                    canonicalServices.some(
                      (service) => serviceState(service).label,
                    )
                  ? `${dashboardRunningServiceCount} running`
                  : "no health reported",
              alerts: operations.kpis.active_alerts,
              connected: dashboardConnectedServerCount,
            }}
            rollout={strataRollout}
            {deploymentName}
            {deploymentHref}
            {nodeHref}
            onOpenService={openServiceSheet}
            people={peopleState}
            devices={devicesState}
          >
            {#snippet actions()}
              {@render headerActions()}
            {/snippet}
            {#snippet nodeNotice(node)}
              {@render wizardRunNodeNotice(node)}
            {/snippet}
          </StrataDashboard>
        {:else}
          <CompleteDashboard
            nodes={nodeViews}
            {deploymentName}
            {deploymentHref}
            {nodeHref}
            onOpenService={openServiceSheet}
            people={peopleState}
            devices={devicesState}
            {canonicalInventoryUnavailable}
          >
            {#snippet nodeNotice(node)}
              {@render wizardRunNodeNotice(node)}
              {#if node.server}
                {@render serverNotice(node.server)}
              {/if}
            {/snippet}
            {#snippet nodeActions(node)}
              {@render nodeControls(node)}
            {/snippet}
          </CompleteDashboard>
        {/if}
        {@render reconnectOutcomeBanner()}
        {#if decommissionError}
          <div
            class="mt-4 rounded-lg border border-destructive/30 bg-destructive/10 p-3"
          >
            <p class="text-sm text-destructive">{decommissionError}</p>
          </div>
        {/if}
      {/if}

      {#if custodyLeases.length > 0}
        <CustodyLeasesPanel
          {custodyLeases}
          {cleaningCustodyLeaseId}
          {resolvingCustodyLeaseId}
          {decommissionError}
          decommissionCustodyLease={(leaseId) =>
            decommissionCustodyLease(leaseId)}
          {resolveCustodyLease}
        />
      {/if}

      {#if hasServerInventory && dashboardServiceCount === 0}
        <!-- The counts live in the stats above; only an empty inventory says
             which source came back empty (owner direction 2026-09-26). -->
        <p
          class="shrink-0 pt-2 text-sm text-muted-foreground"
          data-testid="services-empty-reason"
        >
          {servicesEmptyReason}
        </p>
      {/if}
    </section>
  {:else if deployments.length > 0 && operationsError}
    <div class="mb-6 space-y-4">
      {#if operationsUnavailableOutcome}
        <GuidancePanel
          outcome={operationsUnavailableOutcome}
          surface="stacks.hub"
          resourceId={homelab?.homelab?.id}
          resourceName={homelabTitle}
          onRetry={retryOperations}
          retrying={retryingOperations}
        />
      {/if}

      {#if singleDeployment && hasManagedRuntimeLease(singleDeployment)}
        <section
          data-kx="plate"
          class="p-5"
          data-testid="partial-rollout-dashboard"
          aria-label={tr("ui.homelab.partialRollout")}
        >
          <div class="mb-4 flex items-start justify-between gap-3">
            <div>
              <p class="text-lg font-semibold text-foreground">
                {singleDeployment.name}
              </p>
              <p class="mt-1 text-sm text-muted-foreground">
                {tr("ui.homelabDashboardPage.managedRuntimeLeaseAllocationMetadata")}
              </p>
              <p
                class="mt-2 text-xs text-warning"
                data-testid="worker-connected-count"
              >
                {tr("ui.homelabDashboardPage.0VerifiedConnectedOperationsEvidence")}
              </p>
            </div>
            <span
              data-kx="status"
              data-status="warn"
              class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
              >{tr("ui.homelab.partialRolloutBadge")}</span
            >
          </div>

          <ServerCard
            data-testid="server-card"
            hostname={tr("ui.homelabDashboardPage.managedRuntime")}
            meta={[
              managedRuntimeProviderLabel(singleDeployment),
              "managed runtime",
              singleDeployment.lease_id
                ? `lease ${singleDeployment.lease_id}`
                : "",
            ]
              .filter(Boolean)
              .join(" · ")}
            status="pending"
            statusLabel={statusLabel(singleDeployment.state || "pending")}
            address={singleDeployment.server_ip}
            detailsHref={singleDeployment.lease_id
              ? managedRuntimeServerHref(singleDeployment)
              : undefined}
          >
            {#snippet actions()}
              <div
                class="flex w-full flex-wrap items-center gap-2"
                data-testid="managed-runtime-action-row"
              >
                <Button
                  variant="primary"
                  size="sm"
                  testId="deploy-stackkit-button"
                  onclick={() => startRollout()}
                  disabled={!canRollout || rolloutLoading}
                >
                  <Play class="h-4 w-4" />
                  {rolloutLoading ? tr("ui.homelabDashboardPage.starting") : tr("ui.homelabDashboardPage.deployStackkit")}
                </Button>
                {#if singleDeployment.lease_id}
                  <Button
                    variant="secondary"
                    size="sm"
                    testId="open-server-details-link"
                    onclick={() =>
                      goto(managedRuntimeServerHref(singleDeployment))}
                  >
                    {tr("ui.node.openDetails")}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    testId="reconnect-server-button"
                    onclick={() =>
                      reconnectManagedRuntime(singleDeployment.lease_id)}
                    disabled={reconnectingLeaseId === singleDeployment.lease_id}
                  >
                    <RefreshCw class="h-4 w-4" />
                    {reconnectingLeaseId === singleDeployment.lease_id
                      ? tr("ui.homelabDashboardPage.reconnecting")
                      : tr("ui.homelabDashboardPage.reconnect")}
                  </Button>
                {/if}
              </div>
            {/snippet}
          </ServerCard>
          {@render reconnectOutcomeBanner()}
          {#if decommissionError}
            <div
              class="mt-4 rounded-lg border border-destructive/30 bg-destructive/10 p-3"
            >
              <p class="text-sm text-destructive">{decommissionError}</p>
            </div>
          {/if}
        </section>
      {/if}
    </div>
  {/if}

  <!-- Legacy registration: a single deployment with a registration token
       and no operations evidence yet. -->
  {#if operationsResolved && stackContextResolved && singleDeployment && !operations && !operationsError && registrationToken}
    <div class={singleDeployment.state === "running" ? "mb-6" : "mb-8"}>
      <WorkerRegistrationCard
        variant={singleDeployment.state === "running" ? "running" : "connect"}
        {connectedWorkers}
        {requirements}
        {approvedWorkers}
        {serverUrl}
        {registryMode}
        {registryUrlError}
        {installCommand}
        {canRollout}
        {rolloutLoading}
        onDeploy={() => startRollout()}
      />
    </div>
  {/if}

  {#if !loading && homelabResolved && deployments.length === 0}
    <!-- First Start Section -->
    <Surface class="mb-8 p-6" ariaLabel={tr("ui.homelab.start")}>
      <div
        class="flex flex-col gap-4 md:flex-row md:items-center md:justify-between"
        data-testid="legacy-stacks-empty-state"
      >
        <div>
          <h2 class="text-lg font-semibold text-foreground">
            {tr("ui.homelab.start")}
          </h2>
          <p class="text-sm text-muted-foreground">
            {tr("ui.homelab.startHint")}
          </p>
        </div>
        <div class="flex flex-wrap gap-2">
          <Button
            variant="primary"
            onclick={() => goto("/stacks/new")}
            anchor="techstack-wizard-entry"
          >
            {tr("ui.homelab.getStarted")}
          </Button>
          <Button
            variant="secondary"
            onclick={() => {
              importExportMode = "import";
              showImportExport = true;
            }}
          >
            {tr("ui.common.importExport")}
          </Button>
        </div>
      </div>
    </Surface>
  {/if}
</div>

<DashboardServiceSheet
  service={detailService}
  nodeName={detailNodeName}
  onclose={closeServiceSheet}
/>

<!-- Import/Export Modal -->
{#if showImportExport}
  <StackImportExportModal
    mode={importExportMode}
    kitDeploymentId={singleDeployment?.id}
    onClose={() => (showImportExport = false)}
    onSuccess={(result) => {
      showImportExport = false;
      goto(
        `/stacks/creating?job_id=${encodeURIComponent(result.jobId)}&stack_id=${encodeURIComponent(result.kitDeploymentId)}`,
      );
    }}
  />
{/if}
