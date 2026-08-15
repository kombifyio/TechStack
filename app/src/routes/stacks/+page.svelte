<script lang="ts">
  import { onMount } from "svelte";
  import { getClientBootstrap } from "$lib/client/bootstrap";
  import { isCancelledRequestError, parseApiError } from "$lib/api/errors";
  import { getBestPublicServerUrl } from "$lib/api/client";
  import { getWorkerRegistryUrl } from "$lib/api/tunnel";
  import { listWorkers, type Worker } from "$lib/api/workers";
  import { goto } from "$app/navigation";
  import {
    type DeploymentRequirements,
    generateInstallCommand,
  } from "$lib/wizard";
  import { authHandler } from "$lib/stores/authHandler.svelte";
  import { authStore } from "$lib/stores/auth.svelte";
  import SessionRenewalPanel from "$lib/components/SessionRenewalPanel.svelte";
  import { appVersion } from "$lib/config";
  import {
    createPostHogClient,
    toTechstackAnalyticsUser,
  } from "$lib/analytics/posthog";
  import StackImportExportModal from "$lib/components/StackImportExportModal.svelte";
  import {
    assignStackWorker,
    decommissionMonthlyRuntime,
    deployStack,
    getStackOperations,
    provisionStack,
    reconnectMonthlyRuntime,
    resolveMonthlyRuntimeCustody,
    retryStackRollout,
    type MonthlyRuntimeStatus,
    type Stack,
    type StackOperationServer,
    type StackCustodyLease,
    type StackOperationsPayload,
  } from "$lib/api/stacks";
  import { getLatestStackProvisionJob } from "$lib/api/jobs";
  import {
    chosenHomelabName,
    getHomelab,
    type HomelabView,
  } from "$lib/api/homelab";
  import { stackIdentity } from "$lib/stores/stackIdentity";
  import { StackIdentityBadge } from "$lib/components/open-core";
  import {
    getActiveWizardRun,
    wizardRunNeedsAttention,
    type ActiveWizardRun,
  } from "$lib/api/wizardRuns";
  import WizardRunBanner from "$lib/components/WizardRunBanner.svelte";
  import { confirmInApp } from "$lib/dialogs/in-app-dialog";
  import { cleanupActionForFailure } from "$lib/custody/cleanup-action";
  import {
    serverCardKit,
    serverCardMeta,
    serverCardMetrics,
    serverCardStatus,
    serverDomains,
    serverPrimaryAddress,
  } from "$lib/server-card-adapter";
  import { ServerCard, type ServerStatusKind } from "$lib/components/open-core";
  import Collapsible from "$lib/components/ui/Collapsible.svelte";
  import {
    AlertTriangle,
    HardDrive,
    HeartPulse,
    Play,
    Plus,
    RefreshCw,
    Server,
    Waypoints,
  } from "@lucide/svelte";
  import GuidancePanel from "$lib/components/hub/GuidancePanel.svelte";
  import {
    outcomeFromLatestFailure,
    retryDispatchFor,
    type ServerOutcome,
  } from "$lib/support/server-outcome";
  import { isManagedRuntimeServer } from "$lib/managed-runtime-server";
  import {
    listCanonicalServers,
    isCurrentCanonicalServer,
    type CanonicalServer,
  } from "$lib/api/registry";
  import {
    listCanonicalServices,
    type CanonicalService,
  } from "$lib/api/services";

  let loading = $state(true);
  // `loading` also covers later refreshes. This separate latch prevents the
  // first partially-loaded stack/registration state from ever becoming a
  // user-visible dashboard before canonical operations have resolved.
  let initialDashboardResolved = $state(false);
  let error = $state<string | null>(null);
  let sessionRenewalRequired = $state(false);
  let sessionRenewalBodyKey = $state<string | undefined>(undefined);
  let copiedInstallCommand = $state(false);
  let copiedRegistryUrl = $state(false);
  let showImportExport = $state(false);
  let importExportMode = $state<"import" | "export">("import");
  type DashboardServer = StackOperationServer & {
    /** The StackKit deployment that owns this node. */
    stack_id: string;
  };
  type DashboardService = StackOperationsPayload["services"][number] & {
    /** The StackKit deployment that owns this service. */
    stack_id: string;
  };
  type DashboardFailure = NonNullable<
    StackOperationsPayload["latestFailure"]
  > & {
    stack_id: string;
    stack_name: string;
  };
  type DashboardCustodyLease = StackCustodyLease & {
    stack_id: string;
  };
  type DashboardOperations = Omit<
    StackOperationsPayload,
    "servers" | "services" | "latestFailure" | "custodyLeases"
  > & {
    servers: DashboardServer[];
    services: DashboardService[];
    latestFailure?: DashboardFailure | null;
    custodyLeases?: DashboardCustodyLease[] | null;
  };

  let operations = $state<DashboardOperations | null>(null);
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

  type StackDashboardItem = Stack & {
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

  // The homelab's user-facing title. A name the owner typed in Settings always
  // wins - otherwise renaming would be a silent no-op for every account that
  // has a Stack Identity. While the row still carries its generated name, the
  // kombify Cloud Stack Identity is the closest thing to a chosen name (D9).
  let homelabTitle = $derived(
    initialDashboardResolved
      ? chosenHomelabName(homelab?.homelab) ||
          $stackIdentity?.name?.trim() ||
          "Your homelab"
      : "",
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
  let operationsEvidenceFresh = $derived(
    Boolean(operations && !operationsError),
  );
  let canRollout = $derived(
    operationsEvidenceFresh && operations?.readiness?.can_start === true,
  );

  let approvedWorkers = $derived(
    workers.filter(
      (w) => w.approved && (!w.stack_id || deploymentIds.has(w.stack_id)),
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
      ? canonicalServices.filter((service) =>
          ["healthy", "running", "reachable"].includes(
            (service.health.state || service.observed_state).toLowerCase(),
          ),
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
    return canonicalServers.filter((server) => !telemetryIDs.has(server.id));
  });

  function canonicalServerFor(
    telemetry: StackOperationServer,
  ): CanonicalServer | undefined {
    return canonicalServers.find(
      (server) =>
        server.id === telemetry.id || server.id === telemetry.agent_id,
    );
  }

  function dashboardServerMeta(telemetry: StackOperationServer): string {
    const canonical = canonicalServerFor(telemetry);
    if (!canonical) return serverCardMeta(telemetry);
    return [
      statusLabel(canonical.environment_class || "unknown"),
      statusLabel(canonical.offering || "unknown_offering"),
      canonical.provider_id || canonical.provider.ref,
    ]
      .filter(Boolean)
      .join(" · ");
  }

  function canonicalServerStatus(server: CanonicalServer): ServerStatusKind {
    switch (server.health.state.toLowerCase()) {
      case "healthy":
        return "healthy";
      case "degraded":
        return "degraded";
      case "unhealthy":
        return "degraded";
      default:
        return "pending";
    }
  }
  // An empty service list on a healthy, connected server is a fact about the
  // host, and the operator can only act on it once the dashboard says which
  // of the two service sources produced nothing. "No runtime services
  // reported" alone reads like a broken dashboard.
  let servicesEmptyReason = $derived.by<string>(() => {
    if (dashboardServers.length === 0) {
      return "No server has reported an inventory yet.";
    }
    const manifestSeen = dashboardServers.some(
      (server) => server.capabilities?.stackkit_manifest_observed === true,
    );
    const discoveryRan = dashboardServers.some(
      (server) => server.capabilities?.service_discovery_observed === true,
    );
    if (manifestSeen || discoveryRan) {
      return "The kombify Agent reported an inventory without services: it found neither a StackKit service nor a running container or unit on the connected servers.";
    }
    const agentVersions = [
      ...new Set(
        dashboardServers
          .map((server) => server.capabilities?.agent_version)
          .filter((version): version is string => Boolean(version)),
      ),
    ];
    const agentSuffix =
      agentVersions.length > 0 ? ` (agent ${agentVersions.join(", ")})` : "";
    return (
      "Services come from two sources: a completed StackKit rollout, and the containers and units the Agent discovers on the host. " +
      `This server reported neither — no StackKit manifest was found and the Agent${agentSuffix} ran no service discovery. ` +
      "Open the server details to check the Agent, and the failure panel below for the last rollout."
    );
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

  // A custody lease is not a machine. The label explains why it has no server,
  // so an operator can tell an expired lease from one whose VM was deleted at
  // the provider.
  const custodyReasonLabels: Record<string, string> = {
    provider_reports_absent: "VM no longer exists at the provider",
    lease_cancelled: "Lease cancelled",
    lease_archived: "Lease archived",
    enrollment_failed: "Server never finished enrolling",
    no_execution_authority: "No execution authority (legacy or unbound lease)",
    never_observed: "Never observed",
  };
  function custodyReasonLabel(reason: string): string {
    return custodyReasonLabels[reason] ?? statusLabel(reason);
  }
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
  const supportShelf = [
    {
      title: "Open monitoring",
      description: "Review health, alerts, jobs, and audit history.",
      href: "/monitoring",
      label: "Monitoring",
    },
    {
      title: "Manage services",
      description: "View installed and observed services for each server.",
      href: "/services",
      label: "Services",
    },
    {
      title: "Open wallet",
      description: "Manage credentials and recovery material.",
      href: "/wallet",
      label: "Wallet",
    },
    {
      title: "Add another server",
      description: "Expand compute or storage through the Creation Wizard.",
      href: "/docs/getting-started/add-server",
      label: "Guide",
    },
    {
      title: "Review backups",
      description: "Run restore-oriented checks for production data.",
      href: "/docs/getting-started/backups",
      label: "Guide",
    },
    {
      title: "Share services securely",
      description: "Gateway und Identity-Defaults statt roher Ports nutzen.",
      href: "/docs/getting-started/service-access",
      label: "Guide",
    },
  ];
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

  function normalizeStackDashboardItem(item: Stack): StackDashboardItem {
    const raw = item as Stack & {
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
        const job = await getLatestStackProvisionJob(currentStack.id);
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

  function aggregateStatus(values: string[]): string {
    const normalized = values
      .map((value) => value.trim().toLowerCase())
      .filter(Boolean);
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
  ): StackDashboardItem {
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
      name: chosenHomelabName(homelab?.homelab) || "Your homelab",
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
      payload.servers.map((server) => ({ ...server, stack_id: deployment.id })),
    );
    const services = snapshots.flatMap(({ deployment, payload }) =>
      payload.services.map((service) => ({
        ...service,
        stack_id: deployment.id,
      })),
    );
    const failures = snapshots
      .flatMap(({ deployment, payload }) =>
        payload.latestFailure
          ? [
              {
                ...payload.latestFailure,
                stack_id: deployment.id,
                stack_name: deployment.name,
              },
            ]
          : [],
      )
      .sort((left, right) =>
        (right.updated_at || right.created_at || "").localeCompare(
          left.updated_at || left.created_at || "",
        ),
      );
    const custodyLeases = snapshots.flatMap(({ deployment, payload }) =>
      (payload.custodyLeases ?? []).map((lease) => ({
        ...lease,
        stack_id: deployment.id,
      })),
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
      currentJob:
        snapshots.length === 1 ? snapshots[0]!.payload.currentJob : null,
      runtimeLifecycle:
        snapshots.length === 1 ? snapshots[0]!.payload.runtimeLifecycle : null,
      latestFailure: failures[0] ?? null,
      custodyLeases,
    };
  }

  async function loadOperationsContext(
    currentDeployments: StackDashboardItem[],
  ) {
    if (currentDeployments.length === 0) {
      operations = null;
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
      operations = null;
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
        "Operations data is not available for this homelab yet.";
      return;
    }
    operations = aggregateOperations(snapshots);
    const rejected = results.filter(
      (result): result is PromiseRejectedResult => result.status === "rejected",
    );
    operationsError =
      rejected.length > 0
        ? `${rejected.length} StackKit deployment operation${rejected.length === 1 ? "" : "s"} could not be loaded.`
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
  let latestFailureOutcome = $derived(
    operations?.latestFailure
      ? outcomeFromLatestFailure(operations.latestFailure)
      : null,
  );

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
    return `Latest ${statusLabel(failure.type || "rollout")} failed — ${detail}`;
  });

  let operationsUnavailableOutcome = $derived.by<ServerOutcome | null>(() => {
    if (!operationsError) return null;
    return {
      status: "degraded",
      reasonCode: "operations_unavailable",
      capability: "techstack.server.operations",
      retryable: true,
      userGuidance: {
        title: "Operations data is not available yet",
        body: operationsError,
        nextSteps: [{ id: "ops-retry", label: "Erneut laden", kind: "retry" }],
      },
    };
  });

  async function retryOperations() {
    if (retryingOperations) return;
    retryingOperations = true;
    error = null;
    try {
      const failure = operations?.latestFailure;
      if (failure) {
        const targetDeployment = deployments.find(
          (deployment) => deployment.id === failure.stack_id,
        );
        if (!targetDeployment) {
          throw new Error(
            "The failed StackKit deployment is no longer part of this homelab. Refresh the homelab and retry the exact deployment.",
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
              ? await provisionStack(targetDeployment.id)
              : dispatch?.kind === "deploy"
                ? await deployStack(targetDeployment.id)
                : null;
        if (!result) {
          throw new Error(
            "This failed run cannot be retried automatically and safely.",
          );
        }
        const params = new URLSearchParams();
        params.set("stack_id", targetDeployment.id);
        params.set("job_id", result.job_id);
        params.set("operation", "stack");
        await goto(`/stacks/creating?${params.toString()}`);
        return;
      }
      await loadOperationsContext(deployments);
    } catch (err) {
      const parsed = parseApiError(err);
      error = `Rollout could not be retried: ${parsed.message}`;
    } finally {
      retryingOperations = false;
    }
  }

  $effect(() => {
    // Keep operations fresh even for managed-runtime stacks without worker
    // rows. An active wizard run keeps the timer alive too: the banner must
    // update while the first deployment has no stack row yet.
    if (workers.length > 0 || deployments.length > 0 || activeWizardRun) {
      refreshInterval = setInterval(async () => {
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
      }, 15_000);
    }

    return () => {
      if (refreshInterval) clearInterval(refreshInterval);
    };
  });

  function isTenantContextDenial(reason: unknown): boolean {
    const parsed = parseApiError(reason);
    if (!parsed.isForbidden) return false;
    const details = parsed.details as
      | { error_code?: unknown; reason_code?: unknown }
      | undefined;
    return (
      details?.error_code === "tenant_context_required" ||
      details?.reason_code === "tenant_context_missing"
    );
  }

  async function load() {
    loading = true;
    error = null;
    sessionRenewalRequired = false;
    sessionRenewalBodyKey = undefined;

    try {
      const timeoutMs = 8_000;
      const [workersRes, homelabRes, activeRunRes] = await Promise.allSettled([
        withTimeout(listWorkers(), timeoutMs, "workers list"),
        withTimeout(getHomelab(), timeoutMs, "homelab"),
        withTimeout(getActiveWizardRun(), timeoutMs, "active wizard run"),
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
        if (!error) error = parsed.message;
        workers = [];
      } else {
        workers = workersRes.value;
      }

      if (homelabRes.status === "rejected") {
        const parsed = parseApiError(homelabRes.reason);
        if (!error) error = parsed.message;
        // Keep the last owner-scoped homelab and its deployment IDs while a
        // refresh is unavailable. Clearing them would erase the aggregated
        // telemetry and falsely render the first-run empty state on a 429.
        if (!homelabResolved) {
          homelab = null;
          deployments = [];
        }
      } else {
        homelabResolved = true;
        homelab = homelabRes.value;
        deployments = (homelab?.kit_deployments ?? []).map(
          normalizeStackDashboardItem,
        );
      }
      // Same for the run banner: absent or unreadable simply means no banner.
      activeWizardRun =
        activeRunRes.status === "fulfilled" ? activeRunRes.value : null;

      await loadStackContext(singleDeployment);
      await Promise.all([
        loadOperationsContext(deployments),
        loadCanonicalInventoryContext(),
      ]);
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
      initialDashboardResolved = true;
    }
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

    load();
  });

  async function assignWorkerToCurrentStack(
    stackId: string | undefined,
    workerId: string,
  ) {
    if (!stackId || !operationsEvidenceFresh) return;
    assigningWorkerId = workerId;
    error = null;
    try {
      await assignStackWorker(stackId, workerId);
      await load();
    } catch (err) {
      const parsed = parseApiError(err);
      error = parsed.message;
    } finally {
      assigningWorkerId = null;
    }
  }

  function copyInstallCommand() {
    navigator.clipboard.writeText(installCommand);
    copiedInstallCommand = true;
    setTimeout(() => (copiedInstallCommand = false), 2000);
  }

  function copyRegistryUrl() {
    if (!serverUrl) return;
    navigator.clipboard.writeText(serverUrl);
    copiedRegistryUrl = true;
    setTimeout(() => (copiedRegistryUrl = false), 2000);
  }

  let rolloutLoading = $state(false);

  async function startRollout(deploymentId = singleDeployment?.id) {
    const deployment = deployments.find((item) => item.id === deploymentId);
    if (!deployment) return;
    if (!canRollout) {
      error =
        operationsError ||
        operations?.readiness?.message ||
        "Rollout readiness is unavailable. Refresh the operations data before starting.";
      return;
    }

    rolloutLoading = true;
    error = null;

    try {
      const result = await deployStack(deployment.id);

      // Success - navigate to rollout status page
      goto(
        `/stacks/creating?name=${encodeURIComponent(deployment.name)}&stack_id=${encodeURIComponent(
          deployment.id,
        )}${result.job_id ? `&job_id=${encodeURIComponent(result.job_id)}` : ""}&phase=rollout`,
      );
    } catch (err) {
      const parsed = parseApiError(err);
      error = `Rollout failed: ${parsed.message}`;
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
        title: "Reconnect failed",
        body:
          parsed.message ||
          "The runtime probe returned no reason. Retry, or open the server details for the full runtime record.",
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
        title: "Server is reporting again",
        body: `The runtime answered the probe and the Guard heartbeat is current (${statusLabel(server.health.state)}).`,
      };
    }
    const machineState =
      status.status?.state?.trim() ||
      status.observed_state?.trim() ||
      "not reported";
    const enrollment = status.enrollment_status?.trim() || "not reported";
    return {
      leaseId,
      tone: "warning",
      title: "Runtime answered, but the server is still offline",
      body:
        `The provider reports the machine as "${machineState}" and enrollment as "${enrollment}". ` +
        "Reconnect can only re-run that probe — it cannot restart the kombify Agent on the machine. " +
        "The server stays offline until the Agent sends a heartbeat again, so continue in the server details, " +
        "where the enrollment command and the last contact are shown.",
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
      decommissionError = parsed.message || "Decommission failed";
    } finally {
      cleaningCustodyLeaseId = null;
    }
  }

  function custodyAllows(
    lease: StackCustodyLease,
    action: "decommission" | "resolve_custody",
  ): boolean {
    return lease.allowed_actions?.includes(action) === true;
  }

  async function resolveCustodyLease(lease: StackCustodyLease) {
    if (resolvingCustodyLeaseId) return;
    const confirmed = await confirmInApp({
      title: "Resolve stale custody record?",
      message:
        "Confirm that the provider resource has already been removed. Techstack will archive only its stale custody record and will not delete a provider resource.",
      confirmText: "Resolve record",
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
          ? "Custody record unchanged: the Techstack gateway could not reach the backend. Retry this exact record when the service is available."
          : parsed.message || "Custody resolution failed";
    } finally {
      resolvingCustodyLeaseId = null;
    }
  }

  async function retryDestroyCleanup() {
    const selection = failedCleanupAction;
    if (!selection) {
      decommissionError =
        "The failed cleanup is not bound to an actionable lease. Refresh the lifecycle evidence and use the action on the exact custody record.";
      return;
    }
    if (selection.action === "resolve_custody") {
      await resolveCustodyLease(selection.lease);
      return;
    }
    await decommissionCustodyLease(selection.lease.lease_id);
  }

  function statusBadgeClass(status: string): string {
    switch (status) {
      case "active":
      case "completed":
      case "connected":
      case "healthy":
      case "running":
      case "ok":
        return "badge badge-success";
      case "current":
      case "ready":
      case "stale":
      case "pending":
        return "badge badge-warning";
      case "offline":
      case "failed":
      case "degraded":
      case "error":
        return "badge badge-destructive";
      default:
        return "badge badge-secondary";
    }
  }

  function serverDetailsHref(
    serverId: string,
    stackId = singleDeployment?.id,
  ): string {
    return stackId
      ? `/stacks/${encodeURIComponent(stackId)}/servers/${encodeURIComponent(
          serverId,
        )}`
      : "#";
  }

  function monitoringHref(focus: string): string {
    return `/monitoring?focus=${encodeURIComponent(focus)}${focus === "history" ? "#history" : ""}`;
  }

  function servicesHref(tab = "services"): string {
    return `/services?tab=${encodeURIComponent(tab)}`;
  }

  function shelfHref(href: string): string {
    if (href === "/monitoring") return monitoringHref("overview");
    if (href === "/services") return servicesHref("services");
    return href;
  }

  function addServerHref(): string {
    return "/stacks/new";
  }

  function statusLabel(status: string): string {
    return status.replace(/_/g, " ");
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
</script>

<div class="mx-auto max-w-6xl p-4 md:p-6" data-testid="stacks-dashboard">
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

  <header class="mb-6" data-testid="homelab-header">
    <div
      class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between"
    >
      <div class="flex min-w-0 items-center gap-3">
        {#if $stackIdentity}
          <StackIdentityBadge identity={$stackIdentity} />
        {/if}
        <div class="min-w-0">
          <h1
            class="truncate text-2xl font-semibold text-foreground"
            data-testid="homelab-title"
          >
            {homelabTitle}
          </h1>
        </div>
      </div>

      {#if deployments.length > 0 && initialDashboardResolved}
        <div
          class="flex flex-wrap items-center gap-2 lg:shrink-0 lg:justify-end"
          data-testid="stack-action-bar"
        >
          <a
            class="btn btn-primary"
            href={addServerHref()}
            data-testid="add-server-button"
          >
            <Plus class="h-4 w-4" />
            Register additional servers
          </a>
          {#if showReviewStart}
            <button
              class="btn btn-primary"
              data-testid="review-start-button"
              onclick={() => startRollout()}
              disabled={!canRollout || rolloutLoading}
              title="Start rollout after review"
            >
              <Play class="h-4 w-4" />
              {rolloutLoading ? "Starting..." : "Review + Start"}
            </button>
          {/if}
          {#if singleDeployment}
            <button
              onclick={() => {
                importExportMode = "export";
                showImportExport = true;
              }}
              class="btn btn-secondary"
              title="Import or export this deployment's configuration"
            >
              Import / Export
            </button>
          {/if}
          <button
            class="btn btn-secondary"
            data-testid="dashboard-refresh-button"
            onclick={load}
            disabled={loading}
            title="Refresh servers, services, and operations"
          >
            <RefreshCw class="h-4 w-4" />
            Refresh
          </button>
        </div>
      {/if}
    </div>
  </header>

  {#if activeWizardRun && wizardRunNeedsAttention(activeWizardRun)}
    <div class="mb-6">
      <WizardRunBanner run={activeWizardRun} />
    </div>
  {/if}

  {#if sessionRenewalRequired}
    <div class="mb-6">
      <SessionRenewalPanel
        onretry={() => load()}
        busy={loading}
        bodyKey={sessionRenewalBodyKey}
      />
    </div>
  {:else if error}
    <div
      class="card mb-6 border-destructive/30 bg-destructive/5"
      data-testid="stacks-error-panel"
    >
      <div class="card-content">
        <p class="text-foreground">{error}</p>
      </div>
    </div>
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

  {#if !initialDashboardResolved}
    <section
      class="mb-8 space-y-4"
      data-testid="dashboard-loading-state"
      aria-label="Loading homelab operations"
      aria-busy="true"
    >
      <div class="h-10 animate-pulse rounded-lg bg-muted"></div>
      <div class="grid gap-3 lg:grid-cols-2">
        <div
          class="h-44 animate-pulse rounded-lg border border-border bg-card"
        ></div>
        <div
          class="h-44 animate-pulse rounded-lg border border-border bg-card"
        ></div>
      </div>
    </section>
  {/if}

  {#if initialDashboardResolved && deployments.length > 0 && operations}
    <section
      class="mb-8 space-y-6"
      data-testid="stack-operations-dashboard"
      aria-label="Homelab operations"
    >
      {#if operationsError}
        <div
          class="rounded-lg border border-warning/40 bg-warning/10 p-4"
          data-testid="operations-evidence-stale"
          role="status"
        >
          <p class="font-medium text-foreground">
            Operations evidence is stale
          </p>
          <p class="mt-1 text-sm text-muted-foreground">
            The last verified snapshot remains visible, but server mutations are
            disabled until Refresh succeeds. {operationsError}
          </p>
        </div>
      {/if}

      {#if showRuntimeProgress}
        <section
          class="rounded-lg border border-border bg-card p-5"
          data-testid="runtime-lifecycle-progress"
          aria-label="Runtime rollout progress"
        >
          <div class="flex items-start justify-between gap-4">
            <div>
              <p class="text-sm font-medium text-foreground">Runtime rollout</p>
              <p class="mt-1 text-sm text-muted-foreground">
                {currentRuntimePhase?.message ||
                  operations.currentJob?.message ||
                  "The persisted rollout is waiting for its next checkpoint."}
              </p>
            </div>
            <span
              class={statusBadgeClass(
                operations.currentJob?.state || "pending",
              )}
            >
              {statusLabel(operations.currentJob?.state || "pending")}
            </span>
          </div>
          <div class="mt-4 h-2 overflow-hidden rounded-full bg-muted">
            <div
              class="h-full rounded-full bg-primary transition-[width]"
              style={`width: ${Math.max(0, Math.min(100, operations.currentJob?.progress ?? 0))}%`}
            ></div>
          </div>
          <div
            class="mt-2 flex flex-wrap justify-between gap-2 text-xs text-muted-foreground"
          >
            <span>
              Phase: {statusLabel(
                operations.runtimeLifecycle?.current_phase ||
                  operations.currentJob?.step ||
                  "pending",
              )}
            </span>
            <span>{operations.currentJob?.progress ?? 0}%</span>
          </div>
        </section>
      {/if}

      {#if showReviewStart && canRollout}
        <section
          class="rounded-lg border border-primary/40 bg-primary/10 p-4"
          data-testid="stackkit-rollout-guidance"
          role="status"
        >
          <p class="font-medium text-foreground">
            Your server is connected. Continue with the StackKit rollout.
          </p>
          <p class="mt-1 text-sm text-muted-foreground">
            Review the selected StackKit, services, and server placement, then
            use Review + Start. A failed application does not block the rest of
            this deployment from being reviewed or started again.
          </p>
        </section>
      {/if}

      <!-- The collapsible failure panel below the servers is the single
           consolidated notice surface; the readiness line only speaks when no
           failure panel is present, so the same error never renders twice. -->
      {#if !latestFailureOutcome}
        <div
          class="flex min-w-0 flex-wrap items-center gap-2 text-sm"
          data-testid="stack-readiness-line"
        >
          <span class={statusBadgeClass(operations.readiness.status)}>
            {reviewPhase ? "Review" : statusLabel(operations.readiness.status)}
          </span>
          {#if operations.readiness.message}
            <span class="text-muted-foreground"
              >{operations.readiness.message}</span
            >
          {:else if !hasServerInventory}
            <span class="text-muted-foreground">
              No manageable server or service inventory has been reported yet.
            </span>
          {/if}
        </div>
      {/if}

      {#if hasServerInventory}
        <div
          class="flex divide-x divide-border overflow-hidden rounded-lg border border-border bg-card text-sm"
          data-testid="stack-kpi-strip"
        >
          <a
            class="flex min-w-0 flex-1 items-center gap-1.5 px-3 py-2 transition-colors hover:bg-muted/30"
            href={monitoringHref("servers")}
          >
            <Server class="h-4 w-4 shrink-0 text-primary" />
            <span class="truncate text-xs text-muted-foreground">Server</span>
            <span class="font-semibold text-foreground">
              {canonicalInventoryUnavailable
                ? operations.kpis.registered_servers
                : canonicalServers.length}
            </span>
            <span
              class="truncate text-xs text-muted-foreground"
              data-testid="worker-connected-count"
              >{connectedWorkers}/{operations.readiness.required_servers}
              connected</span
            >
          </a>
          <a
            class="flex min-w-0 flex-1 items-center gap-1.5 px-3 py-2 transition-colors hover:bg-muted/30"
            href={monitoringHref("health")}
            title="Heartbeat and metrics"
          >
            <HeartPulse class="h-4 w-4 shrink-0 text-success" />
            <span class="truncate text-xs text-muted-foreground">Healthy</span>
            <span class="font-semibold text-foreground">
              {canonicalInventoryUnavailable
                ? operations.kpis.healthy_servers
                : canonicalServers.filter(
                    (server) => server.health.state === "healthy",
                  ).length}
            </span>
          </a>
          <a
            class="flex min-w-0 flex-1 items-center gap-1.5 px-3 py-2 transition-colors hover:bg-muted/30"
            href={monitoringHref("services")}
            data-testid="stack-running-services-kpi"
          >
            <Waypoints class="h-4 w-4 shrink-0 text-info" />
            <span class="truncate text-xs text-muted-foreground">Services</span>
            <span
              class="font-semibold text-foreground"
              data-testid="metric-card-value"
            >
              {dashboardRunningServiceCount}
            </span>
            <span class="truncate text-xs text-muted-foreground">
              {dashboardServiceCount} recorded
            </span>
          </a>
          <a
            class="flex min-w-0 flex-1 items-center gap-1.5 px-3 py-2 transition-colors hover:bg-muted/30"
            href={monitoringHref("alerts")}
            title={statusLabel(operations.monitoring.status)}
          >
            <AlertTriangle class="h-4 w-4 shrink-0 text-warning" />
            <span class="truncate text-xs text-muted-foreground">Alerts</span>
            <span class="font-semibold text-foreground">
              {operations.kpis.active_alerts}
            </span>
          </a>
        </div>
      {/if}

      {#if hasServerInventory}
        <div class="rounded-lg border border-border bg-card p-5">
          <div class="mb-4 flex items-center justify-between gap-3">
            <div class="flex items-center gap-2">
              <HardDrive class="h-5 w-5 text-primary" />
              <h3 class="text-lg font-semibold text-foreground">Servers</h3>
            </div>
            <span class="text-sm text-muted-foreground">
              {dashboardServers.length + canonicalOnlyServers.length} server{dashboardServers.length +
                canonicalOnlyServers.length ===
              1
                ? ""
                : "s"}
            </span>
          </div>

          {#if canonicalInventoryUnavailable}
            <p
              class="mb-3 rounded-lg border border-dashed border-warning/40 bg-warning/10 p-3 text-sm text-warning"
              role="status"
              data-testid="dashboard-inventory-unavailable"
            >
              Canonical server and service state are unavailable. The retained
              telemetry below is diagnostic and is not treated as a zero
              inventory.
            </p>
          {/if}

          {#if dashboardServers.length === 0 && canonicalOnlyServers.length === 0}
            <p class="text-sm text-muted-foreground">
              No workers are registered for this stack yet.
            </p>
          {:else}
            <div class="grid gap-3 lg:grid-cols-2">
              {#each dashboardServers as server (`${server.stack_id}:${server.id}`)}
                {@const domains = serverDomains(server)}
                {@const showAssign =
                  server.assignment === "unassigned" &&
                  server.approved &&
                  server.assignable !== false}
                {@const cardProps = {
                  "data-testid": "server-card",
                  hostname: server.hostname,
                  meta: dashboardServerMeta(server),
                  status: serverCardStatus(server),
                  metrics: serverCardMetrics(server),
                  address: serverPrimaryAddress(server),
                  domain: domains[0],
                  domainExtraCount:
                    domains.length > 1 ? domains.length - 1 : undefined,
                  kit: serverCardKit(server),
                  note:
                    server.precheck_state === "failed"
                      ? "Server prechecks failed"
                      : undefined,
                  noteTone: "error" as const,
                  detailsHref: serverDetailsHref(server.id, server.stack_id),
                }}
                {#if showAssign || isManagedRuntimeServer(server)}
                  <ServerCard {...cardProps}>
                    {#snippet actions()}
                      <div class="flex w-full flex-wrap items-center gap-2">
                        {#if showAssign}
                          <button
                            class="btn btn-secondary btn-sm w-full"
                            data-testid="assign-server-button"
                            disabled={!operationsEvidenceFresh ||
                              assigningWorkerId === server.id}
                            onclick={() =>
                              assignWorkerToCurrentStack(
                                server.stack_id,
                                server.id,
                              )}
                          >
                            {assigningWorkerId === server.id
                              ? "Assigning..."
                              : "Assign to this stack"}
                          </button>
                        {/if}
                        {#if isManagedRuntimeServer(server)}
                          <div
                            class="flex flex-wrap gap-2"
                            data-testid="managed-runtime-action-row"
                          >
                            <button
                              class="btn btn-primary btn-sm"
                              data-testid="deploy-stackkit-button"
                              onclick={() => startRollout(server.stack_id)}
                              disabled={!canRollout || rolloutLoading}
                            >
                              <Play class="h-4 w-4" />
                              {rolloutLoading
                                ? "Starting..."
                                : "Deploy StackKit"}
                            </button>
                            <a
                              class="btn btn-secondary btn-sm"
                              data-testid="open-server-details-link"
                              href={serverDetailsHref(
                                server.id,
                                server.stack_id,
                              )}
                            >
                              Open server details
                            </a>
                            {#if server.lease_id && canReconnectManagedRuntime(server)}
                              <button
                                class="btn btn-secondary btn-sm"
                                data-testid="reconnect-server-button"
                                onclick={() =>
                                  reconnectManagedRuntime(server.lease_id)}
                                disabled={!operationsEvidenceFresh ||
                                  reconnectingLeaseId === server.lease_id}
                              >
                                <RefreshCw class="h-4 w-4" />
                                {reconnectingLeaseId === server.lease_id
                                  ? "Reconnecting..."
                                  : "Reconnect"}
                              </button>
                            {/if}
                          </div>
                        {/if}
                      </div>
                    {/snippet}
                  </ServerCard>
                {:else}
                  <ServerCard {...cardProps} />
                {/if}
              {/each}
              {#each canonicalOnlyServers as server (server.id)}
                <ServerCard
                  data-testid="server-card"
                  hostname={server.name}
                  meta={[
                    statusLabel(server.environment_class || "unknown"),
                    statusLabel(server.offering || "unknown_offering"),
                    server.provider_id || server.provider.ref,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                  status={canonicalServerStatus(server)}
                  statusLabel={statusLabel(server.health.state)}
                  note="Current telemetry is not available for this canonical server."
                  noteTone="warning"
                  detailsHref={serverDetailsHref(server.id)}
                />
              {/each}
            </div>
          {/if}
          {@render reconnectOutcomeBanner()}
          {#if decommissionError}
            <div
              class="mt-4 rounded-lg border border-destructive/30 bg-destructive/10 p-3"
            >
              <p class="text-sm text-destructive">{decommissionError}</p>
            </div>
          {/if}
        </div>
      {/if}

      <!-- Leases without a machine. They stay visible because they can still
           cost money, but they are never presented as servers. -->
      {#if custodyLeases.length > 0}
        <div
          class="rounded-lg border border-warning/40 bg-warning/5 p-4"
          data-testid="custody-leases"
        >
          <div class="mb-3 flex items-center gap-2">
            <AlertTriangle class="h-4 w-4 shrink-0 text-warning" />
            <h3 class="text-sm font-semibold text-foreground">
              {custodyLeases.length} lease{custodyLeases.length === 1
                ? ""
                : "s"}
              without a server
            </h3>
          </div>
          <p class="mb-3 text-xs text-muted-foreground">
            These leases still hold custody and may still be billed, but no
            machine backs them. They are not shown as servers.
          </p>
          <ul class="space-y-2">
            {#each custodyLeases as lease (lease.lease_id)}
              <li
                class="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border border-border bg-card px-3 py-2 text-sm"
                data-testid="custody-lease"
              >
                <span class="font-medium text-foreground">{lease.label}</span>
                {#if lease.provider}
                  <span class="font-mono text-xs text-muted-foreground"
                    >{lease.provider}</span
                  >
                {/if}
                <span class="badge badge-warning"
                  >{custodyReasonLabel(lease.reason)}</span
                >
                {#if lease.last_known_ip}
                  <span
                    class="font-mono text-xs text-muted-foreground"
                    title="Last known address; the machine is gone"
                    >was {lease.last_known_ip}</span
                  >
                {/if}
                <span
                  class="ml-auto font-mono text-[11px] text-muted-foreground"
                  >{lease.lease_id}</span
                >
                {#if custodyAllows(lease, "decommission")}
                  <button
                    class="btn btn-outline btn-sm text-destructive hover:bg-destructive/10 hover:text-destructive"
                    data-testid="decommission-custody-lease"
                    onclick={() => decommissionCustodyLease(lease.lease_id)}
                    disabled={cleaningCustodyLeaseId === lease.lease_id}
                  >
                    {cleaningCustodyLeaseId === lease.lease_id
                      ? "Decommissioning..."
                      : "Decommission"}
                  </button>
                {/if}
                {#if custodyAllows(lease, "resolve_custody")}
                  <button
                    class="btn btn-outline btn-sm"
                    data-testid="resolve-custody-lease"
                    onclick={() => resolveCustodyLease(lease)}
                    disabled={resolvingCustodyLeaseId === lease.lease_id}
                  >
                    {resolvingCustodyLeaseId === lease.lease_id
                      ? "Resolving..."
                      : "Resolve record"}
                  </button>
                {/if}
              </li>
            {/each}
          </ul>
          {#if decommissionError}
            <p class="mt-3 text-sm text-destructive">{decommissionError}</p>
          {/if}
        </div>
      {/if}

      {#if latestFailureOutcome}
        {#if failedCleanupAction && ["destroy", "decommission"].includes(operations.latestFailure?.type?.toLowerCase() ?? "")}
          <button
            class="btn btn-outline btn-sm mb-2 text-destructive hover:bg-destructive/10 hover:text-destructive"
            data-testid="retry-destroy-cleanup"
            onclick={retryDestroyCleanup}
            disabled={cleaningCustodyLeaseId !== null}
          >
            {failedCleanupAction.action === "resolve_custody"
              ? "Resolve exact record"
              : "Retry exact cleanup"}
          </button>
        {/if}
        <Collapsible
          summary={latestFailureSummary}
          tone="error"
          badge={statusLabel(operations.latestFailure?.state || "failed")}
          testId="latest-failure-collapsible"
        >
          <GuidancePanel
            outcome={latestFailureOutcome}
            surface="stacks.hub"
            resourceId={operations.latestFailure?.stack_id ||
              homelab?.homelab?.id}
            resourceName={operations.latestFailure?.stack_name || homelabTitle}
            onRetry={retryOperations}
            retrying={retryingOperations}
          />
        </Collapsible>
      {/if}

      {#if hasServerInventory}
        <section class="rounded-lg border border-border bg-card p-4">
          <div class="mb-3 flex items-center justify-between gap-3">
            <p class="font-semibold text-foreground">Help & resources</p>
            <a
              class="text-sm text-primary hover:underline"
              href={monitoringHref("history")}
            >
              Open history
            </a>
          </div>
          <div class="grid gap-2 md:grid-cols-3">
            {#each supportShelf as item (item.title)}
              <a
                class="rounded-lg border border-border bg-background/40 px-3 py-2 transition-colors hover:border-primary/50"
                href={shelfHref(item.href)}
              >
                <span class="text-xs font-semibold uppercase text-primary">
                  {item.label}
                </span>
                <p class="mt-1 text-sm font-medium text-foreground">
                  {item.title}
                </p>
                <p class="mt-1 text-xs text-muted-foreground">
                  {item.description}
                </p>
              </a>
            {/each}
          </div>
        </section>
      {/if}

      {#if hasServerInventory}
        <section
          class="rounded-lg border border-border bg-card p-4"
          data-testid="dashboard-services-summary"
        >
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <p class="font-semibold text-foreground">Services</p>
              <p class="mt-1 text-sm text-muted-foreground">
                {dashboardServiceCount} runtime service{dashboardServiceCount ===
                1
                  ? ""
                  : "s"} recorded across {dashboardServers.length +
                  canonicalOnlyServers.length} server{dashboardServers.length +
                  canonicalOnlyServers.length ===
                1
                  ? ""
                  : "s"}.
              </p>
              {#if dashboardServiceCount === 0}
                <p
                  class="mt-2 text-sm text-muted-foreground"
                  data-testid="services-empty-reason"
                >
                  {servicesEmptyReason}
                </p>
              {/if}
            </div>
            <a
              class="btn btn-secondary btn-sm"
              href={servicesHref("services")}
              data-testid="dashboard-services-link">Manage services</a
            >
          </div>
        </section>
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
          class="rounded-lg border border-border bg-card p-5"
          data-testid="partial-rollout-dashboard"
          aria-label="Partial rollout managed runtime"
        >
          <div class="mb-4 flex items-start justify-between gap-3">
            <div>
              <p class="text-lg font-semibold text-foreground">
                {singleDeployment.name}
              </p>
              <p class="mt-1 text-sm text-muted-foreground">
                Managed-runtime lease/allocation metadata exists, but the
                current provider and Guard state could not be verified.
              </p>
              <p
                class="mt-2 text-xs text-warning"
                data-testid="worker-connected-count"
              >
                0 verified connected · operations evidence unavailable
              </p>
            </div>
            <span class="badge badge-warning">partial rollout</span>
          </div>

          <ServerCard
            data-testid="server-card"
            hostname="Managed runtime"
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
                <button
                  class="btn btn-primary btn-sm"
                  data-testid="deploy-stackkit-button"
                  onclick={() => startRollout()}
                  disabled={!canRollout || rolloutLoading}
                >
                  <Play class="h-4 w-4" />
                  {rolloutLoading ? "Starting..." : "Deploy StackKit"}
                </button>
                {#if singleDeployment.lease_id}
                  <a
                    class="btn btn-secondary btn-sm"
                    data-testid="open-server-details-link"
                    href={managedRuntimeServerHref(singleDeployment)}
                  >
                    Open server details
                  </a>
                  <button
                    class="btn btn-secondary btn-sm"
                    data-testid="reconnect-server-button"
                    onclick={() =>
                      reconnectManagedRuntime(singleDeployment.lease_id)}
                    disabled={reconnectingLeaseId === singleDeployment.lease_id}
                  >
                    <RefreshCw class="h-4 w-4" />
                    {reconnectingLeaseId === singleDeployment.lease_id
                      ? "Reconnecting..."
                      : "Reconnect"}
                  </button>
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

  <!-- Worker Registration Section (shown for deployed stacks) -->
  {#if initialDashboardResolved && singleDeployment && !operations && !operationsError && registrationToken && singleDeployment.state === "running"}
    <div
      class="card mb-6 border-success/30"
      data-testid="worker-management-card"
    >
      <div class="card-content">
        <div class="flex items-start justify-between mb-4">
          <div class="flex items-center gap-3">
            <div
              class="w-10 h-10 rounded-lg bg-success/10 flex items-center justify-center"
            >
              <svg
                class="w-5 h-5 text-success"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
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
              <h2 class="text-lg font-semibold text-foreground">
                Connection status unavailable
              </h2>
              <p class="text-sm text-muted-foreground">
                Operations evidence is required before connected servers can be
                confirmed.
              </p>
            </div>
          </div>
          <span class="badge badge-warning" data-testid="worker-connected-count"
            >{connectedWorkers} verified connected</span
          >
        </div>

        <!-- Worker registry URL for running stack -->
        {#if serverUrl}
          <div class="rounded-lg bg-muted/50 p-4 mb-4">
            <div class="flex items-center justify-between gap-3 mb-2">
              <span class="text-sm text-muted-foreground">
                Worker-Registry Link{registryMode ? ` (${registryMode})` : ""}:
              </span>
              <button onclick={copyRegistryUrl} class="btn btn-ghost btn-sm">
                {copiedRegistryUrl ? "✓ Copied" : "Copy"}
              </button>
            </div>
            <a
              href={serverUrl}
              target="_blank"
              rel="noopener noreferrer"
              data-testid="worker-registry-url"
              class="text-sm font-mono text-primary break-all hover:underline"
            >
              {serverUrl}
            </a>
            {#if registryUrlError}
              <div class="mt-2 text-xs text-warning">
                Could not automatically determine registry URL: {registryUrlError}
              </div>
            {/if}
          </div>
        {/if}

        <!-- Install command for running stack -->
        {#if installCommand}
          <div class="rounded-lg bg-muted/50 p-4">
            <div class="flex items-center justify-between mb-2">
              <span class="text-sm text-muted-foreground"
                >Install command for new workers:</span
              >
              <button onclick={copyInstallCommand} class="btn btn-ghost btn-sm">
                {copiedInstallCommand ? "✓ Copied" : "Copy"}
              </button>
            </div>
            <pre
              class="text-sm font-mono text-primary overflow-x-auto whitespace-pre-wrap break-all">{installCommand}</pre>
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Worker Registration & Rollout Section (shown when stack exists but not fully deployed) -->
  {#if initialDashboardResolved && singleDeployment && !operations && !operationsError && registrationToken && singleDeployment.state !== "running"}
    <div class="card mb-8" data-testid="worker-management-card">
      <div class="card-content">
        <div class="flex items-start justify-between mb-4">
          <div class="flex items-center gap-3">
            <div
              class="w-10 h-10 rounded-lg bg-primary/10 flex items-center justify-center"
            >
              <svg
                class="w-5 h-5 text-primary"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M5 12h14M12 5l7 7-7 7"
                />
              </svg>
            </div>
            <div>
              <h2 class="text-lg font-semibold text-foreground">
                Connect worker
              </h2>
              <p class="text-sm text-muted-foreground">
                {#if requirements}
                  {requirements.description}
                {:else}
                  Connect your servers to this homelab
                {/if}
              </p>
            </div>
          </div>
          <div class="text-right">
            <div class="text-2xl font-bold text-primary">
              <span data-testid="worker-connected-count"
                >{connectedWorkers}/{requirements?.minTotalServers || 1}</span
              >
            </div>
            <div class="text-xs text-muted-foreground">
              Workers connected (verified)
            </div>
          </div>
        </div>

        <!-- Progress bar -->
        {#if requirements}
          <div class="mb-4">
            <div class="h-2 bg-muted rounded-full overflow-hidden">
              <div
                class="h-full rounded-full transition-all duration-500 {canRollout
                  ? 'bg-success'
                  : 'bg-primary'}"
                style="width: {Math.min(
                  100,
                  (connectedWorkers / requirements.minTotalServers) * 100,
                )}%"
              ></div>
            </div>
          </div>
        {/if}

        <!-- Approved workers remain visible, but approval is not connection evidence. -->
        {#if approvedWorkers.length > 0}
          <div class="mb-4 space-y-2">
            <div class="text-sm text-muted-foreground mb-2">
              Approved workers:
            </div>
            {#each approvedWorkers as w}
              <div
                data-testid="approved-worker-row"
                class="flex items-center justify-between rounded-lg bg-muted/50 px-3 py-2"
              >
                <div class="flex items-center gap-3">
                  <div class="h-2.5 w-2.5 rounded-full bg-gray-500"></div>
                  <div class="min-w-0">
                    <div class="text-sm text-foreground truncate">
                      {w.hostname || w.id}
                    </div>
                    <div class="text-xs text-muted-foreground truncate">
                      {w.ip || "-"}
                    </div>
                  </div>
                </div>
                <span class="badge badge-secondary">
                  Connection unverified
                </span>
              </div>
            {/each}
          </div>
        {/if}

        <!-- Worker registry URL (must be reachable from worker machines) -->
        {#if serverUrl}
          <div class="rounded-lg bg-muted/50 p-4 mb-4">
            <div class="flex items-center justify-between gap-3 mb-2">
              <span class="text-sm text-muted-foreground">
                Worker-Registry Link{registryMode ? ` (${registryMode})` : ""}:
              </span>
              <button onclick={copyRegistryUrl} class="btn btn-ghost btn-sm">
                {copiedRegistryUrl ? "✓ Copied" : "Copy"}
              </button>
            </div>
            <a
              href={serverUrl}
              target="_blank"
              rel="noopener noreferrer"
              data-testid="worker-registry-url"
              class="text-sm font-mono text-primary break-all hover:underline"
            >
              {serverUrl}
            </a>
            {#if registryUrlError}
              <div class="mt-2 text-xs text-warning">
                Could not automatically determine registry URL: {registryUrlError}
              </div>
            {/if}
          </div>
        {/if}

        <!-- Install command -->
        {#if installCommand}
          <div class="rounded-lg bg-muted/50 p-4 mb-4">
            <div class="flex items-center justify-between mb-2">
              <span class="text-sm text-muted-foreground">Install command:</span
              >
              <button onclick={copyInstallCommand} class="btn btn-ghost btn-sm">
                {copiedInstallCommand ? "✓ Copied" : "Copy"}
              </button>
            </div>
            <pre
              class="text-sm font-mono text-primary overflow-x-auto whitespace-pre-wrap break-all">{installCommand}</pre>
          </div>
        {/if}

        <!-- Actions -->
        <div class="flex flex-wrap gap-3">
          <button
            data-testid="deploy-homelab-button"
            onclick={() => startRollout()}
            class="btn {canRollout && !rolloutLoading
              ? 'btn-primary'
              : 'btn-secondary opacity-50 cursor-not-allowed'}"
            disabled={!canRollout || rolloutLoading}
          >
            {#if rolloutLoading}
              <span class="animate-spin inline-block mr-2">⏳</span> Starting rollout...
            {:else if canRollout}
              Deploy homelab
            {:else}
              Waiting for verified workers ({connectedWorkers}/{requirements?.minTotalServers ||
                1})
            {/if}
          </button>
        </div>

        <!-- Requirements details - prominently displayed -->
        {#if requirements?.details?.length && requirements.details.length > 0}
          <div class="mt-6 pt-4 border-t border-border">
            <h3
              class="text-sm font-medium text-foreground mb-3 flex items-center gap-2"
            >
              <svg
                class="w-4 h-4 text-info"
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
              Requirements for your setup
            </h3>
            <div class="grid gap-2 sm:grid-cols-2">
              {#each requirements.details as detail, i}
                <div
                  class="flex items-start gap-2 text-sm text-muted-foreground bg-muted/30 rounded-lg px-3 py-2"
                >
                  <svg
                    class="w-4 h-4 text-info shrink-0 mt-0.5"
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
                  <span>{detail}</span>
                </div>
              {/each}
            </div>

            <!-- Next steps hint -->
            {#if !canRollout}
              <div
                class="mt-4 p-3 bg-warning/10 border border-warning/30 rounded-lg"
              >
                <p class="text-sm text-foreground flex items-start gap-2">
                  <svg
                    class="w-4 h-4 text-warning shrink-0 mt-0.5"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                  >
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      stroke-width="2"
                      d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
                    />
                  </svg>
                  <span>
                    <strong>Next step:</strong> Run the install command above on
                    {requirements.minTotalServers > 1
                      ? `at least ${requirements.minTotalServers} servers`
                      : "your server"}, then wait for operations evidence to
                    confirm the connection.
                  </span>
                </p>
              </div>
            {:else}
              <div
                class="mt-4 p-3 bg-success/10 border border-success/30 rounded-lg"
              >
                <p class="text-sm text-foreground flex items-start gap-2">
                  <svg
                    class="w-4 h-4 text-success shrink-0 mt-0.5"
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
                  <span>
                    <strong>Ready!</strong> All required workers are connected. You
                    can now deploy the homelab.
                  </span>
                </p>
              </div>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}

  {#if !loading && homelabResolved && deployments.length === 0}
    <!-- First Start Section -->
    <div class="card mb-8" data-testid="legacy-stacks-empty-state">
      <div class="card-content">
        <div
          class="flex flex-col gap-4 md:flex-row md:items-center md:justify-between"
        >
          <div>
            <h2 class="text-lg font-semibold text-foreground">
              Create your TechStack
            </h2>
            <p class="text-sm text-muted-foreground">
              Start with the wizard or import an existing StackKit spec.
            </p>
          </div>
          <div class="flex flex-wrap gap-2">
            <a href="/stacks/new" class="btn btn-primary"> Get started </a>
            <button
              class="btn btn-secondary"
              onclick={() => {
                importExportMode = "import";
                showImportExport = true;
              }}
            >
              Import / Export
            </button>
          </div>
        </div>
      </div>
    </div>
  {/if}
</div>

<!-- Import/Export Modal -->
{#if showImportExport}
  <StackImportExportModal
    mode={importExportMode}
    stackId={singleDeployment?.id}
    onClose={() => (showImportExport = false)}
    onSuccess={(result) => {
      showImportExport = false;
      goto(
        `/stacks/creating?job_id=${encodeURIComponent(result.jobId)}&stack_id=${encodeURIComponent(result.stackId)}`,
      );
    }}
  />
{/if}
