<script lang="ts">
  import { onMount } from "svelte";
  import {
    attachCatalogService,
    importObservedService,
    listServiceRegistry,
    migrateRegistryService,
    verifyRegistryService,
    deleteRegistryService,
    type RegistryCatalogService,
    type RegistryServer,
    type RegistryService,
    type ServiceRegistryPayload,
  } from "$lib/api/registry";
  import { getMonitoringCockpit } from "$lib/api/monitoring";
  import {
    applyCockpitRefreshError,
    applyCockpitRefreshSuccess,
    beginCockpitRefresh,
    type CockpitRefreshState,
  } from "$lib/monitoring/cockpit-snapshot";
  import { getJob, type JobDetail } from "$lib/api/jobs";
  import type { StackOperationServer } from "$lib/api/stacks";
  import {
    getManagedServiceLogs,
    listCanonicalServices,
    runManagedServiceAction,
    type CanonicalService,
    type ManagedServiceAction,
    type ManagedServiceLogEntry,
  } from "$lib/api/services";
  import {
    openServiceUrl,
    serviceCardMetrics as resolveServiceCardMetrics,
    serviceCardPlacement as resolveServiceCardPlacement,
    serviceCardStatus as resolveServiceCardStatus,
    serviceCardStatusMessage as resolveServiceCardStatusMessage,
    serviceHasActiveMigration,
    serviceManagementState,
  } from "$lib/service-card-adapter";
  import ServiceList, {
    type ServiceListGroup,
    type ServiceListItem,
  } from "$lib/components/inventory/ServiceList.svelte";
  import Modal from "$lib/components/Modal.svelte";
  import { confirmInApp } from "$lib/dialogs/in-app-dialog";
  import {
    ServiceCard,
    ServiceCardCompact,
    type ServiceMetric,
    type ServiceMigration,
    type ServicePlacement,
    type ServiceStatusKind,
  } from "$lib/components/open-core";
  import {
    AlertTriangle,
    Server,
    HardDrive,
    Cpu,
    Check,
    Trash2,
    ArrowRight,
    Loader2,
  } from "@lucide/svelte";

  const emptyRegistry: ServiceRegistryPayload = {
    catalog: [],
    stacks: [],
    servers: [],
    services: [],
    migration_available: false,
    migration_unavailable_reason:
      "Runtime service migration is not enabled on this deployment.",
  };

  let registry = $state<ServiceRegistryPayload>(emptyRegistry);
  let canonicalServices = $state<CanonicalService[]>([]);
  let loading = $state(true);
  let inventoryLoading = $state(true);
  let saving = $state(false);
  let registryError = $state<string | null>(null);
  let inventoryError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  interface ManagedServiceActionState {
    action: ManagedServiceAction;
    jobId: string;
    state: string;
    detail?: string;
    logCursor?: string;
    expectedInventoryRevision: number;
    converging?: boolean;
    convergenceDeadline?: number;
    reasonCode?: string;
    retryable?: boolean;
  }
  interface ManagedServiceLogState {
    loading: boolean;
    entries: ManagedServiceLogEntry[];
    nextCursor?: string;
    error?: string;
  }
  let serviceActionJobs = $state<Record<string, ManagedServiceActionState>>({});
  let serviceLogs = $state<Record<string, ManagedServiceLogState>>({});
  let managedServiceActionsPolling = false;
  let servicesPageActive = false;
  let statusFilter = $state<string>("all");
  let showNewService = $state(false);
  let serviceMode = $state<"catalog" | "import">("catalog");
  let selectedStackId = $state("");
  let selectedServerId = $state("");
  let selectedCatalogId = $state("");
  let unmanagedName = $state("");
  let unmanagedDisplayName = $state("");
  let unmanagedType = $state("custom");
  let unmanagedPort = $state("");
  let unmanagedUrl = $state("");

  // Sub-tabs
  let activeTab = $state<"services" | "servers">("services");

  // Drag & drop state
  let activeDragService = $state<RegistryService | null>(null);
  let activeDragServerId = $state<string | null>(null);

  // Migration state
  let showMigrationConfirmModal = $state(false);
  let migrationSourceService = $state<RegistryService | null>(null);
  let migrationTargetServer = $state<RegistryServer | null>(null);
  let selectedMigrationTargetId = $state("");
  let activeMigrationJobIds = $state<string[]>([]);
  // The migration presentation is bound to explicit job receipts.  A generic
  // service status string alone must never turn a service into "Migrating".
  let migrationServiceIdsByJob = $state<Record<string, string[]>>({});
  let migrationJobs = $state<Record<string, JobDetail>>({});
  let cockpitState = $state<CockpitRefreshState>({
    snapshot: null,
    stackId: "",
    stale: false,
    error: null,
    generation: 0,
  });
  const cockpitStale = $derived(cockpitState.stale);
  const cockpitError = $derived(cockpitState.error);
  const activeCockpit = $derived(
    cockpitState.snapshot?.techstack_id === selectedStackId
      ? cockpitState.snapshot
      : null,
  );

  onMount(() => {
    servicesPageActive = true;
    const params = new URLSearchParams(window.location.search);
    const stackId = params.get("stack_id") || params.get("stack");
    const tab = params.get("tab");
    if (stackId) selectedStackId = stackId;
    if (tab === "servers") activeTab = "servers";
    void loadServicesPage();
    const interval = setInterval(() => {
      if (hasActiveMigrations() || activeMigrationJobIds.length > 0) {
        void pollMigrationJobs();
        void refreshRegistryQuietly();
      }
      if (hasActiveManagedServiceActions()) {
        void pollManagedServiceActionJobs();
      }
      if (hasPendingManagedServiceConvergence()) {
        void refreshManagedServiceConvergence();
      }
    }, 3000);
    return () => {
      servicesPageActive = false;
      clearInterval(interval);
    };
  });

  const targetServers = $derived(
    selectedStackId
      ? registry.servers.filter((server) => server.stack_id === selectedStackId)
      : registry.servers,
  );

  const selectedCatalogService = $derived(
    registry.catalog.find((service) => service.id === selectedCatalogId),
  );

  interface RuntimeServiceRow extends ServiceListItem {
    runtime: CanonicalService;
    registry?: RegistryService;
    runtimeTargetName: string;
  }

  function label(value?: string): string {
    return (value || "unknown").replace(/[-_]/g, " ");
  }

  function registryServiceForRuntime(
    runtime: CanonicalService,
  ): RegistryService | undefined {
    return registry.services.find((service) => service.id === runtime.id);
  }

  function runtimeServicePlacement(
    runtime: CanonicalService,
    service?: RegistryService,
  ): "local" | "cloud" | "managed" | "unknown" {
    if (runtime.target_kind === "managed_workload") return "managed";
    const server = registry.servers.find(
      (candidate) => candidate.id === runtime.server_id,
    );
    const stack = registry.stacks.find(
      (candidate) => candidate.id === runtime.techstack_id,
    );
    const resolved = resolveServiceCardPlacement(
      {
        ...service,
        server_id: runtime.server_id,
        server_name: server?.name || service?.server_name,
        provider:
          stack?.provider_id || stack?.lease_provider || stack?.runtime_lane,
      },
      [
        stack?.server_provisioning_mode,
        stack?.runtime_offering_id,
        stack?.provider_id,
        stack?.lease_provider,
      ]
        .filter(Boolean)
        .join(" "),
    );
    const evidence = [
      stack?.server_mode,
      stack?.server_provisioning_mode,
      stack?.runtime_offering_id,
      stack?.provider_id,
      stack?.lease_provider,
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    if (resolved === "cloud" || /cloud|hostinger|vps|provider/.test(evidence)) {
      return "cloud";
    }
    if (resolved === "local" && /local|device|homelab/.test(evidence)) {
      return "local";
    }
    return "unknown";
  }

  function hasPersistedMigration(service?: RegistryService): boolean {
    return serviceHasActiveMigration({
      migration_status: service?.migration_status,
      migration_available: registry.migration_available,
    });
  }

  function hasTrackedMigration(serviceId: string): boolean {
    return Object.values(migrationServiceIdsByJob).some((ids) =>
      ids.includes(serviceId),
    );
  }

  const runtimeServiceRows = $derived.by<RuntimeServiceRow[]>(() =>
    canonicalServices.map((runtime) => {
      const service = registryServiceForRuntime(runtime);
      const server = registry.servers.find(
        (candidate) => candidate.id === runtime.server_id,
      );
      const runtimeState = runtime.health.state || runtime.observed_state;
      const placement = runtimeServicePlacement(runtime, service);
      const cardPlacement: ServicePlacement =
        placement === "managed" ? "serverless" : placement;
      const management = runtime.management_state;
      const migration =
        hasPersistedMigration(service) || hasTrackedMigration(runtime.id);
      const runtimeTargetId =
        runtime.target_kind === "managed_workload"
          ? runtime.placement.managed_target_ref || "managed-workload:unknown"
          : runtime.server_id || "runtime-target:unknown";
      const needsAttention = [
        "error",
        "failed",
        "unhealthy",
        "offline",
        "unknown",
      ].includes(runtimeState.toLowerCase());
      const cardSource = {
        ...service,
        name: runtime.name,
        display_name: service?.display_name || runtime.name,
        type: service?.type || runtime.service_key,
        status: runtimeState,
        management_state: runtime.management_state,
        migration_available: registry.migration_available,
        server_id: runtime.server_id,
        server_name: server?.name || service?.server_name,
        placement: cardPlacement,
        operation_state: migration ? "running" : undefined,
      };
      const accessUrl =
        service?.url && serviceAccessEnabled(service)
          ? service.url
          : typeof runtime.access.url === "string"
            ? runtime.access.url
            : undefined;
      return {
        id: runtime.id,
        name: service?.display_name || runtime.name,
        meta: `${service?.type || runtime.service_key || "service"} · ${placement === "cloud" ? "Cloud" : placement === "managed" ? "Managed workload" : placement === "local" ? "Local" : "Placement unknown"}`,
        placement: cardPlacement,
        status: resolveServiceCardStatus(cardSource),
        statusLabel: label(runtimeState),
        statusMessage: resolveServiceCardStatusMessage(cardSource),
        metrics: resolveServiceCardMetrics(cardSource),
        runtimeTargetId,
        targetKind: runtime.target_kind,
        targetLabel:
          runtime.target_kind === "managed_workload"
            ? runtime.placement.provider_id || "Managed provider target"
            : server?.name || runtime.server_id || "Placement unknown",
        managementLabel:
          management === "managed"
            ? "Configured"
            : management === "observed"
              ? "Observed"
              : "Ownership unknown",
        workflowLabel: migration
          ? label(service?.migration_status || "operation active")
          : undefined,
        freshnessLabel: label(runtime.placement.freshness.state),
        sourceLabel: runtime.source || undefined,
        details: `Desired ${runtime.desired_state || "unknown"} · Observed ${runtime.observed_state || "unknown"}`,
        attention: needsAttention || migration,
        onOpen: accessUrl
          ? () => openServiceUrl({ url: accessUrl })
          : undefined,
        runtime,
        registry: service,
        runtimeTargetName:
          server?.name || service?.server_name || runtimeTargetId,
      };
    }),
  );

  const runtimeSummary = $derived(() => {
    const running = runtimeServiceRows.filter((service) => {
      const state =
        service.runtime.health.state || service.runtime.observed_state;
      return (
        state === "running" || state === "healthy" || state === "reachable"
      );
    }).length;
    const observed = runtimeServiceRows.filter(
      (service) => service.runtime.management_state === "observed",
    ).length;
    const pending = runtimeServiceRows.filter((service) =>
      ["pending", "adopting", "starting"].includes(
        (service.runtime.observed_state || "").toLowerCase(),
      ),
    ).length;
    const error = runtimeServiceRows.filter((service) =>
      ["error", "failed", "unhealthy", "offline"].includes(
        (
          service.runtime.health.state ||
          service.runtime.observed_state ||
          ""
        ).toLowerCase(),
      ),
    ).length;
    return {
      running,
      observed,
      pending,
      error,
      total: runtimeServiceRows.length,
    };
  });

  const filteredRuntimeServiceRows = $derived(
    statusFilter === "all"
      ? runtimeServiceRows
      : statusFilter === "observed"
        ? runtimeServiceRows.filter(
            (service) => service.runtime.management_state === "observed",
          )
        : statusFilter === "running"
          ? runtimeServiceRows.filter((service) =>
              ["running", "healthy", "reachable"].includes(
                (
                  service.runtime.health.state ||
                  service.runtime.observed_state ||
                  ""
                ).toLowerCase(),
              ),
            )
          : statusFilter === "pending"
            ? runtimeServiceRows.filter((service) =>
                ["pending", "adopting", "starting"].includes(
                  (service.runtime.observed_state || "").toLowerCase(),
                ),
              )
            : runtimeServiceRows.filter((service) =>
                ["error", "failed", "unhealthy", "offline"].includes(
                  (
                    service.runtime.health.state ||
                    service.runtime.observed_state ||
                    ""
                  ).toLowerCase(),
                ),
              ),
  );

  const runtimeServiceGroups = $derived.by<ServiceListGroup[]>(() => {
    const groups = new Map<string, ServiceListGroup>();
    for (const service of filteredRuntimeServiceRows) {
      const existing = groups.get(service.runtimeTargetId);
      if (existing) {
        existing.items.push(service);
        continue;
      }
      const server = registry.servers.find(
        (candidate) => candidate.id === service.runtimeTargetId,
      );
      groups.set(service.runtimeTargetId, {
        id: service.runtimeTargetId,
        name: server?.name || service.runtimeTargetName,
        targetKind: service.targetKind,
        meta: server
          ? `${server.role_label} · ${server.hostname || server.id}`
          : service.targetKind === "managed_workload"
            ? "Managed provider target"
            : "Runtime target",
        items: [service],
      });
    }
    return Array.from(groups.values())
      .map((group) => ({
        ...group,
        items: group.items.sort(
          (left, right) =>
            Number(Boolean(right.attention)) -
              Number(Boolean(left.attention)) ||
            left.name.localeCompare(right.name),
        ),
      }))
      .sort((left, right) => left.name.localeCompare(right.name));
  });

  const runtimeServiceById = $derived.by(
    () => new Map(runtimeServiceRows.map((service) => [service.id, service])),
  );

  // Kept only for the Placement Board's independently loaded registry
  // projection.  The Applications tab below deliberately renders the
  // canonical runtime list, not these management rows.
  const summary = $derived(() => {
    const running = registry.services.filter(
      (service) => service.status === "running" || service.status === "healthy",
    ).length;
    const observed = registry.services.filter(
      (service) => serviceManagementState(service) === "observed",
    ).length;
    const pending = registry.services.filter((service) =>
      ["pending", "adopting", "starting"].includes(service.status),
    ).length;
    const error = registry.services.filter((service) =>
      ["error", "unhealthy"].includes(service.status),
    ).length;
    return {
      running,
      observed,
      pending,
      error,
      total: registry.services.length,
    };
  });

  const filteredServices = $derived(
    statusFilter === "all"
      ? registry.services
      : statusFilter === "observed"
        ? registry.services.filter(
            (service) => serviceManagementState(service) === "observed",
          )
        : statusFilter === "running"
          ? registry.services.filter((service) =>
              ["running", "healthy"].includes(service.status),
            )
          : registry.services.filter(
              (service) => service.status === statusFilter,
            ),
  );

  const servicesByServer = $derived(() =>
    registry.servers.map((server) => {
      const services = filteredServices.filter(
        (service) => service.server_id === server.id,
      );
      return {
        server,
        managed: services.filter(
          (service) => serviceManagementState(service) === "managed",
        ),
        unmanaged: services.filter(
          (service) => serviceManagementState(service) === "observed",
        ),
      };
    }),
  );

  $effect(() => {
    if (!selectedStackId && registry.stacks.length > 0) {
      selectedStackId = registry.stacks[0].id;
    }
    if (
      selectedStackId &&
      (!selectedServerId ||
        !registry.servers.some(
          (server) =>
            server.id === selectedServerId &&
            server.stack_id === selectedStackId,
        ))
    ) {
      selectedServerId = targetServers[0]?.id ?? "";
    }
    if (!selectedCatalogId && registry.catalog.length > 0) {
      selectedCatalogId = registry.catalog[0].id;
    }
  });

  $effect(() => {
    if (selectedStackId && selectedStackId !== cockpitState.stackId) {
      void loadCockpit(selectedStackId);
    }
  });

  async function loadRegistryManagement() {
    loading = true;
    registryError = null;
    try {
      registry = await listServiceRegistry();
    } catch (err) {
      registryError =
        err instanceof Error
          ? err.message
          : "Service Registry could not be loaded.";
    } finally {
      loading = false;
    }
  }

  async function loadInventory() {
    inventoryLoading = true;
    inventoryError = null;
    try {
      canonicalServices = await listCanonicalServices();
    } catch (err) {
      inventoryError =
        err instanceof Error
          ? err.message
          : "Runtime inventory could not be loaded.";
    } finally {
      inventoryLoading = false;
    }
  }

  async function loadServicesPage() {
    await Promise.allSettled([loadRegistryManagement(), loadInventory()]);
  }

  async function refreshRegistryQuietly() {
    const registryRefresh = listServiceRegistry()
      .then(async (nextRegistry) => {
        if (!servicesPageActive) return;
        registry = nextRegistry;
        if (selectedStackId) {
          await loadCockpit(selectedStackId);
        }
      })
      .catch(() => undefined);
    const inventoryRefresh = listCanonicalServices()
      .then((nextServices) => {
        if (!servicesPageActive) return;
        canonicalServices = nextServices;
      })
      .catch(() => undefined);
    await Promise.allSettled([registryRefresh, inventoryRefresh]);
    // Keep each last-known-good surface independently; the next manual load can
    // surface an error without letting a legacy registry failure hide Inventory.
  }

  async function loadCockpit(stackId: string) {
    const started = beginCockpitRefresh(cockpitState, stackId);
    cockpitState = started.state;
    try {
      const nextCockpit = await getMonitoringCockpit(stackId);
      const activeServerIDs = new Set(
        registry.servers
          .filter((server) => server.stack_id === stackId)
          .map((server) => server.id),
      );
      cockpitState = applyCockpitRefreshSuccess(
        cockpitState,
        started.request,
        nextCockpit,
        activeServerIDs,
      );
    } catch (err) {
      cockpitState = applyCockpitRefreshError(
        cockpitState,
        started.request,
        err,
      );
    }
  }

  function openNewService(mode: "catalog" | "import" = "catalog") {
    serviceMode = mode;
    actionError = null;
    showNewService = true;
  }

  function closeNewService() {
    if (saving) return;
    showNewService = false;
    actionError = null;
  }

  async function submitCatalogService() {
    if (!selectedStackId || !selectedServerId || !selectedCatalogId) {
      actionError = "Select a stack, server, and catalog service.";
      return;
    }
    saving = true;
    actionError = null;
    try {
      const service = await attachCatalogService({
        stack_id: selectedStackId,
        server_id: selectedServerId,
        service_id: selectedCatalogId,
      });
      upsertService(service);
      showNewService = false;
    } catch (err) {
      actionError =
        err instanceof Error
          ? err.message
          : "Catalog service could not be added.";
    } finally {
      saving = false;
    }
  }

  async function submitObservedService() {
    if (!selectedStackId || !selectedServerId || !unmanagedName.trim()) {
      actionError = "Select a stack and server, then enter a service name.";
      return;
    }
    saving = true;
    actionError = null;
    try {
      const service = await importObservedService({
        stack_id: selectedStackId,
        server_id: selectedServerId,
        name: unmanagedName,
        display_name: unmanagedDisplayName,
        type: unmanagedType,
        port: unmanagedPort ? Number(unmanagedPort) : undefined,
        url: unmanagedUrl,
      });
      upsertService(service);
      unmanagedName = "";
      unmanagedDisplayName = "";
      unmanagedType = "custom";
      unmanagedPort = "";
      unmanagedUrl = "";
      showNewService = false;
    } catch (err) {
      actionError =
        err instanceof Error
          ? err.message
          : "Unmanaged service could not be imported.";
    } finally {
      saving = false;
    }
  }

  function upsertService(service: RegistryService) {
    const existing = registry.services.findIndex(
      (item) => item.id === service.id,
    );
    registry = {
      ...registry,
      services:
        existing >= 0
          ? registry.services.map((item, index) =>
              index === existing ? service : item,
            )
          : [service, ...registry.services],
    };
  }

  function serviceTypeLabel(service: RegistryCatalogService | RegistryService) {
    return service.type.replace(/[-_]/g, " ");
  }

  async function controlInventoryService(
    service: CanonicalService,
    action: ManagedServiceAction,
    logCursor?: string,
  ) {
    actionError = null;
    serviceActionJobs = {
      ...serviceActionJobs,
      [service.id]: {
        action,
        jobId: "",
        state: "queueing",
        logCursor,
        expectedInventoryRevision: service.inventory_revision,
      },
    };
    if (action === "logs") {
      const previous = serviceLogs[service.id];
      serviceLogs = {
        ...serviceLogs,
        [service.id]: {
          loading: true,
          entries: previous?.entries ?? [],
          nextCursor: previous?.nextCursor,
        },
      };
    }
    try {
      const result = await runManagedServiceAction(
        service.id,
        action,
        service.inventory_revision,
        crypto.randomUUID(),
        action === "logs" ? { cursor: logCursor } : undefined,
      );
      if (!servicesPageActive) return;
      if (!result.job_id)
        throw new Error("Service action did not return a job ID.");
      serviceActionJobs = {
        ...serviceActionJobs,
        [service.id]: {
          action,
          jobId: result.job_id,
          // The POST is only an acknowledgement; the job receipt is the result.
          state: "queued",
          logCursor,
          expectedInventoryRevision: service.inventory_revision,
        },
      };
      void pollManagedServiceActionJobs();
    } catch (err) {
      if (!servicesPageActive) return;
      const error =
        err instanceof Error ? err.message : "Service action failed.";
      serviceActionJobs = {
        ...serviceActionJobs,
        [service.id]: {
          action,
          jobId: "",
          state: "failed",
          detail: error,
          logCursor,
          expectedInventoryRevision: service.inventory_revision,
        },
      };
      if (action === "logs") {
        const previous = serviceLogs[service.id];
        serviceLogs = {
          ...serviceLogs,
          [service.id]: {
            loading: false,
            entries: previous?.entries ?? [],
            nextCursor: previous?.nextCursor,
            error,
          },
        };
      }
      actionError = error;
    }
  }

  function normalizeManagedServiceJobState(state: string): string {
    return state.trim().toLowerCase() || "queued";
  }

  function managedServiceJobOutcome(result: JobDetail["result"]): {
    reasonCode?: string;
    retryable?: boolean;
  } {
    if (!result || typeof result !== "object") return {};
    const root = result as Record<string, unknown>;
    const records: Record<string, unknown>[] = [root];
    for (const value of [root.service_action_receipt, root.command_result]) {
      if (value && typeof value === "object") {
        records.push(value as Record<string, unknown>);
      }
    }
    const commandResult = root.command_result;
    if (commandResult && typeof commandResult === "object") {
      const data = (commandResult as Record<string, unknown>).data;
      if (data && typeof data === "object") {
        const dataRecord = data as Record<string, unknown>;
        records.push(dataRecord);
        if (typeof dataRecord.output === "string") {
          try {
            const output = JSON.parse(dataRecord.output);
            if (output && typeof output === "object") {
              records.push(output as Record<string, unknown>);
            }
          } catch {
            // Human output is valid but has no structured outcome metadata.
          }
        }
      }
    }
    for (const record of records) {
      const reasonCode =
        typeof record.reason_code === "string"
          ? record.reason_code
          : typeof record.reasonCode === "string"
            ? record.reasonCode
            : undefined;
      const retryable =
        typeof record.retryable === "boolean" ? record.retryable : undefined;
      if (reasonCode || retryable !== undefined)
        return { reasonCode, retryable };
    }
    return {};
  }

  function isManagedServiceActionTerminal(state: string): boolean {
    return ["completed", "failed", "error", "cancelled", "canceled"].includes(
      normalizeManagedServiceJobState(state),
    );
  }

  function hasActiveManagedServiceActions(): boolean {
    return Object.values(serviceActionJobs).some(
      (job) => Boolean(job.jobId) && !isManagedServiceActionTerminal(job.state),
    );
  }

  function isManagedServiceActionActive(
    job: ManagedServiceActionState | undefined,
  ): boolean {
    if (!job) return false;
    return job.converging || !isManagedServiceActionTerminal(job.state);
  }

  function hasPendingManagedServiceConvergence(): boolean {
    return Object.values(serviceActionJobs).some((job) => job.converging);
  }

  async function loadManagedServiceLogs(
    serviceId: string,
    cursor: string | undefined,
  ) {
    const previous = serviceLogs[serviceId];
    try {
      const page = await getManagedServiceLogs(serviceId, { cursor });
      if (!servicesPageActive) return;
      serviceLogs = {
        ...serviceLogs,
        [serviceId]: {
          loading: false,
          entries: cursor
            ? [...(previous?.entries ?? []), ...page.entries]
            : page.entries,
          nextCursor: page.next_cursor,
        },
      };
    } catch (err) {
      if (!servicesPageActive) return;
      const error =
        err instanceof Error
          ? err.message
          : "Service logs could not be loaded.";
      serviceLogs = {
        ...serviceLogs,
        [serviceId]: {
          loading: false,
          entries: previous?.entries ?? [],
          nextCursor: previous?.nextCursor,
          error,
        },
      };
    }
  }

  async function pollManagedServiceActionJobs() {
    if (managedServiceActionsPolling) return;
    const activeActions = Object.entries(serviceActionJobs).filter(
      ([, job]) =>
        Boolean(job.jobId) && !isManagedServiceActionTerminal(job.state),
    );
    if (activeActions.length === 0) return;
    managedServiceActionsPolling = true;
    try {
      const updates = await Promise.all(
        activeActions.map(async ([serviceId, action]) => {
          try {
            return { serviceId, action, job: await getJob(action.jobId) };
          } catch {
            // Retain the job as pollable after a transient read failure.
            return null;
          }
        }),
      );
      if (!servicesPageActive) return;
      const nextActions = { ...serviceActionJobs };
      const completedLogs: Array<{ serviceId: string; cursor?: string }> = [];
      let refreshInventory = false;
      for (const update of updates) {
        if (!update) continue;
        const current = nextActions[update.serviceId];
        if (!current || current.jobId !== update.action.jobId) continue;
        const state = normalizeManagedServiceJobState(update.job.state);
        const terminal = isManagedServiceActionTerminal(state);
        const outcome = managedServiceJobOutcome(update.job.result);
        const observedService = canonicalServices.find(
          (service) => service.id === update.serviceId,
        );
        const convergenceWindowSeconds = observedService ? 90 : 60;
        nextActions[update.serviceId] = {
          ...current,
          state,
          converging: state === "completed" && current.action !== "logs",
          convergenceDeadline:
            state === "completed" && current.action !== "logs"
              ? Date.now() + convergenceWindowSeconds * 1000
              : undefined,
          reasonCode: outcome.reasonCode,
          retryable: outcome.retryable,
          detail:
            state === "completed"
              ? update.job.message ||
                "Action completed; inventory is refreshing."
              : terminal
                ? update.job.error_details ||
                  update.job.error ||
                  update.job.message ||
                  "Service action did not complete."
                : update.job.message || update.job.current_step,
        };
        if (state === "completed") {
          if (current.action === "logs") {
            completedLogs.push({
              serviceId: update.serviceId,
              cursor: current.logCursor,
            });
          } else {
            refreshInventory = true;
          }
        } else if (terminal && current.action === "logs") {
          const previousLogs = serviceLogs[update.serviceId];
          serviceLogs = {
            ...serviceLogs,
            [update.serviceId]: {
              loading: false,
              entries: previousLogs?.entries ?? [],
              nextCursor: previousLogs?.nextCursor,
              error:
                nextActions[update.serviceId].detail ??
                "Service log collection did not complete.",
            },
          };
        }
      }
      serviceActionJobs = nextActions;
      if (refreshInventory) void refreshRegistryQuietly();
      for (const log of completedLogs) {
        void loadManagedServiceLogs(log.serviceId, log.cursor);
      }
    } finally {
      managedServiceActionsPolling = false;
    }
  }

  async function refreshManagedServiceConvergence() {
    await refreshRegistryQuietly();
    if (!servicesPageActive) return;
    const nextActions = { ...serviceActionJobs };
    for (const [serviceId, action] of Object.entries(nextActions)) {
      if (!action.converging) continue;
      if (
        action.convergenceDeadline !== undefined &&
        Date.now() >= action.convergenceDeadline
      ) {
        nextActions[serviceId] = {
          ...action,
          converging: false,
          reasonCode: "inventory_observation_timeout",
          retryable: true,
          detail:
            "Action completed, but no newer inventory observation arrived within the service freshness window.",
        };
        continue;
      }
      const observed = canonicalServices.find(
        (service) => service.id === serviceId,
      );
      if (
        observed &&
        observed.inventory_revision > action.expectedInventoryRevision
      ) {
        nextActions[serviceId] = {
          ...action,
          converging: false,
          detail: "Action completed and fresh inventory was observed.",
        };
      }
    }
    serviceActionJobs = nextActions;
  }

  function loadMoreManagedServiceLogs(service: CanonicalService) {
    const nextCursor = serviceLogs[service.id]?.nextCursor;
    if (
      !nextCursor ||
      isManagedServiceActionActive(serviceActionJobs[service.id])
    )
      return;
    void controlInventoryService(service, "logs", nextCursor);
  }

  // Capacity calculations for servers
  interface ServerCapacity {
    storageFree?: number;
    storageTotal?: number;
    storagePercent?: number;
    ramUsed?: number;
    ramTotal?: number;
    ramPercent?: number;
    source: "telemetry" | "capabilities" | "unknown";
  }

  function cockpitServerFor(
    server: RegistryServer,
  ): StackOperationServer | undefined {
    const servers = activeCockpit?.servers ?? [];
    return servers.find(
      (candidate) =>
        candidate.id === server.id ||
        candidate.id === server.worker_id ||
        candidate.hostname === server.hostname ||
        candidate.hostname === server.name,
    );
  }

  function getServerCapacity(server: RegistryServer): ServerCapacity {
    const opsServer = cockpitServerFor(server);
    if (!opsServer) return { source: "unknown" };

    const diskPercent = opsServer.health?.disk_percent?.value;
    const ramPercent = opsServer.health?.memory_percent?.value;
    const diskTotal = Number(opsServer.capabilities?.disk_gb ?? 0);
    const ramTotalGb = Number(opsServer.capabilities?.ram_mb ?? 0) / 1024;

    if (diskTotal > 0 || ramTotalGb > 0) {
      const storagePercent =
        typeof diskPercent === "number" ? Math.round(diskPercent) : undefined;
      const ramPercentRounded =
        typeof ramPercent === "number" ? Math.round(ramPercent) : undefined;
      return {
        storageTotal: diskTotal > 0 ? Math.round(diskTotal) : undefined,
        storageFree:
          diskTotal > 0 && typeof diskPercent === "number"
            ? Number((diskTotal * (1 - diskPercent / 100)).toFixed(1))
            : undefined,
        storagePercent,
        ramTotal: ramTotalGb > 0 ? Number(ramTotalGb.toFixed(1)) : undefined,
        ramUsed:
          ramTotalGb > 0 && typeof ramPercent === "number"
            ? Number((ramTotalGb * (ramPercent / 100)).toFixed(1))
            : undefined,
        ramPercent: ramPercentRounded,
        source:
          typeof diskPercent === "number" || typeof ramPercent === "number"
            ? "telemetry"
            : "capabilities",
      };
    }

    return { source: "unknown" };
  }

  function applicationLabel(service: RegistryService | null): string {
    return service?.application_name || service?.display_name || "Application";
  }

  function serviceStack(service: RegistryService) {
    return registry.stacks.find((stack) => stack.id === service.stack_id);
  }

  function servicePlacement(service: RegistryService): ServicePlacement {
    const stack = serviceStack(service);
    return resolveServiceCardPlacement(
      {
        ...service,
        provider:
          stack?.provider_id || stack?.lease_provider || stack?.runtime_lane,
      },
      [
        stack?.server_provisioning_mode,
        stack?.runtime_offering_id,
        stack?.provider_id,
        stack?.lease_provider,
      ]
        .filter(Boolean)
        .join(" "),
    );
  }

  function serviceCardStatus(service: RegistryService): ServiceStatusKind {
    return resolveServiceCardStatus({
      ...service,
      migration_available: registry.migration_available,
      operation_state: isServiceMigrating(service.id)
        ? service.migration_status || "queued"
        : undefined,
    });
  }

  function serviceStatusMessage(service: RegistryService): string | undefined {
    if (serviceManagementState(service) === "observed") {
      return "Observed unmanaged service. Adopt it before moving.";
    }
    if (service.status === "archived") {
      return "Archived source service after migration.";
    }
    if (service.status === "error" || service.status === "unhealthy") {
      return service.move_blocked_reason || "Service reported an error.";
    }
    if (service.status === "starting") {
      return "Waiting for a Docker health or endpoint probe.";
    }
    if (service.status === "unknown") {
      return "No current runtime observation is available.";
    }
    if (service.status === "reachable") {
      return "Endpoint is reachable, but protected service health is not verified.";
    }
    return undefined;
  }

  function serviceMigration(
    service: RegistryService,
  ): ServiceMigration | undefined {
    if (!isServiceMigrating(service.id)) {
      return undefined;
    }
    const phase = service.migration_status || "queued";
    const progress =
      phase === "pending_verification" ? 95 : phase === "deploying" ? 75 : 45;
    return {
      to: servicePlacement(service),
      progress,
      message: getMigrationMessage(service.id),
    };
  }

  function serviceMetrics(service: RegistryService): ServiceMetric[] {
    const metrics: ServiceMetric[] = [
      { label: "Type", value: serviceTypeLabel(service) },
      { label: "Server", value: service.server_name },
    ];
    if (service.port) {
      metrics.push({ label: "Port", value: String(service.port) });
    }
    return metrics;
  }

  function serviceMeta(service: RegistryService): string {
    const meta = [serviceTypeLabel(service)];
    if (service.port) meta.push(`:${service.port}`);
    return meta.join(" · ");
  }

  function openService(service: RegistryService) {
    const url = service.url;
    if (!url || !serviceAccessEnabled(service)) return;
    window.open(url, "_blank", "noopener,noreferrer");
  }

  function serviceAccessEnabled(service: RegistryService): boolean {
    if (!service.url || isServiceMigrating(service.id)) return false;
    return ![
      "unknown",
      "migrating",
      "deploying",
      "pending_verification",
      "archived",
      "pending",
      "adopting",
      "stopped",
      "offline",
    ].includes(service.status.toLowerCase());
  }

  function canMoveApplication(service: RegistryService): boolean {
    if (!registry.migration_available) return false;
    if (isServiceMigrating(service.id)) return false;
    if (typeof service.move_allowed === "boolean") {
      return service.move_allowed;
    }
    return (
      serviceManagementState(service) === "managed" &&
      (service.status === "running" || service.status === "stopped")
    );
  }

  function moveBlockedReason(service: RegistryService): string {
    if (!registry.migration_available) {
      return (
        registry.migration_unavailable_reason ||
        "Runtime service migration is not enabled on this deployment."
      );
    }
    if (isServiceMigrating(service.id)) {
      return "Application is already moving or waiting for verification.";
    }
    return (
      service.move_blocked_reason ||
      (serviceManagementState(service) === "observed"
        ? "Observed unmanaged applications must be adopted before they can be moved."
        : "Application needs a stable running or stopped state before it can be moved.")
    );
  }

  function moveTargetsFor(service: RegistryService): RegistryServer[] {
    return registry.servers.filter(
      (server) =>
        server.stack_id === service.stack_id &&
        server.id !== service.server_id &&
        server.rollout_ready,
    );
  }

  function selectedMigrationTarget(): RegistryServer | null {
    return (
      registry.servers.find(
        (server) => server.id === selectedMigrationTargetId,
      ) || migrationTargetServer
    );
  }

  function openMigration(
    service: RegistryService,
    targetServer?: RegistryServer,
  ) {
    if (!canMoveApplication(service)) {
      actionError = moveBlockedReason(service);
      return;
    }
    const target = targetServer || moveTargetsFor(service)[0];
    if (!target) {
      actionError = "No available target server for this application.";
      return;
    }
    migrationSourceService = service;
    migrationTargetServer = target;
    selectedMigrationTargetId = target.id;
    showMigrationConfirmModal = true;
    actionError = null;
  }

  // Drag and drop handlers
  function handleDragStart(event: DragEvent, service: RegistryService) {
    if (!canMoveApplication(service)) {
      event.preventDefault();
      return;
    }
    activeDragService = service;
    if (event.dataTransfer) {
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", service.id || "");
    }
  }

  function handleDragOver(event: DragEvent, serverId: string) {
    event.preventDefault();
    if (activeDragService && activeDragService.server_id !== serverId) {
      activeDragServerId = serverId;
    }
  }

  function handleDragLeave() {
    activeDragServerId = null;
  }

  function handleDrop(event: DragEvent, targetServerId: string) {
    event.preventDefault();
    activeDragServerId = null;

    if (!activeDragService) return;
    if (activeDragService.server_id === targetServerId) return;

    const targetServer = registry.servers.find((s) => s.id === targetServerId);
    if (!targetServer) return;

    openMigration(activeDragService, targetServer);
    activeDragService = null;
  }

  function cancelMigration() {
    showMigrationConfirmModal = false;
    migrationSourceService = null;
    migrationTargetServer = null;
    selectedMigrationTargetId = "";
  }

  async function confirmMigration() {
    const targetServer = selectedMigrationTarget();
    if (!migrationSourceService || !targetServer) return;

    showMigrationConfirmModal = false;
    const sourceSvc = migrationSourceService;

    migrationSourceService = null;
    migrationTargetServer = null;
    selectedMigrationTargetId = "";

    try {
      saving = true;
      actionError = null;

      const res = await migrateRegistryService(sourceSvc.id!, targetServer.id);
      if (res.job_id) {
        activeMigrationJobIds = Array.from(
          new Set([...activeMigrationJobIds, res.job_id]),
        );
        migrationServiceIdsByJob = {
          ...migrationServiceIdsByJob,
          [res.job_id]: [sourceSvc.id, res.target_service.id].filter(
            (id): id is string => Boolean(id),
          ),
        };
        await pollMigrationJobs();
      }
      upsertService(res.source_service);
      upsertService(res.target_service);
      await refreshRegistryQuietly();
    } catch (err) {
      actionError =
        err instanceof Error ? err.message : "Failed to initiate migration.";
    } finally {
      saving = false;
    }
  }

  function isServiceMigrating(serviceId: string | undefined): boolean {
    if (!serviceId) return false;
    const service = registry.services.find((s) => s.id === serviceId);
    return hasTrackedMigration(serviceId) || hasPersistedMigration(service);
  }

  function hasActiveMigrations(): boolean {
    return (
      Object.keys(migrationServiceIdsByJob).length > 0 ||
      registry.services.some((service) => hasPersistedMigration(service))
    );
  }

  async function pollMigrationJobs() {
    if (activeMigrationJobIds.length === 0) return;
    const hadActiveJobs = activeMigrationJobIds.length > 0;
    const nextJobs: Record<string, JobDetail> = { ...migrationJobs };
    const remaining: string[] = [];
    for (const jobId of activeMigrationJobIds) {
      try {
        const job = await getJob(jobId);
        nextJobs[jobId] = job;
        if (
          !["completed", "failed", "error", "cancelled"].includes(job.state)
        ) {
          remaining.push(jobId);
        }
      } catch {
        remaining.push(jobId);
      }
    }
    migrationJobs = nextJobs;
    activeMigrationJobIds = remaining;
    if (remaining.length === 0 && hadActiveJobs) {
      migrationServiceIdsByJob = {};
    }
  }

  function getMigrationMessage(serviceId: string | undefined): string {
    if (!serviceId) return "";
    const service = registry.services.find((s) => s.id === serviceId);
    const phase = service?.migration_status;
    if (phase === "migrating") {
      return "Relocating on backend...";
    }
    if (phase === "deploying") {
      return "Waiting for runtime deployment...";
    }
    if (phase === "pending_verification") {
      return "Target is ready for verification.";
    }
    return "Migration job is active.";
  }

  async function handleVerifyService(serviceId: string) {
    try {
      saving = true;
      actionError = null;
      const res = await verifyRegistryService(serviceId);
      upsertService(res.service);
      if (res.archived_service) {
        upsertService(res.archived_service);
      }
    } catch (err) {
      actionError =
        err instanceof Error ? err.message : "Failed to verify service.";
    } finally {
      saving = false;
    }
  }

  async function handleRemoveOld(serviceId: string) {
    if (
      !(await confirmInApp({
        title: "Delete archived service?",
        message:
          "This permanently deletes the archived service from its source server.",
        confirmText: "Delete",
        tone: "danger",
      }))
    ) {
      return;
    }
    try {
      saving = true;
      actionError = null;
      await deleteRegistryService(serviceId);
      registry = {
        ...registry,
        services: registry.services.filter((s) => s.id !== serviceId),
      };
    } catch (err) {
      actionError =
        err instanceof Error
          ? err.message
          : "Failed to delete archived service.";
    } finally {
      saving = false;
    }
  }
</script>

<div
  class="p-4 md:p-6 animate-in fade-in duration-300"
  data-testid="services-page"
>
  <div
    class="mb-4 flex flex-col gap-3 md:flex-row md:items-center md:justify-end"
  >
    <div
      class="flex flex-wrap items-center gap-3 animate-in slide-in-from-right-4 duration-300"
    >
      <!-- Sub-tab Selector -->
      <div
        role="tablist"
        aria-label="Services mode"
        class="flex rounded-lg border border-border bg-card/60 p-1 text-sm shrink-0 shadow-sm"
      >
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === "services"}
          class="rounded-md px-4 py-2 font-medium transition-all cursor-pointer {activeTab ===
          'services'
            ? 'bg-muted text-foreground shadow-sm font-semibold'
            : 'text-muted-foreground hover:text-foreground'}"
          onclick={() => (activeTab = "services")}
        >
          Applications
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === "servers"}
          class="rounded-md px-4 py-2 font-medium transition-all cursor-pointer {activeTab ===
          'servers'
            ? 'bg-muted text-foreground shadow-sm font-semibold'
            : 'text-muted-foreground hover:text-foreground'}"
          onclick={() => (activeTab = "servers")}
          data-testid="server-management-tab"
        >
          Placement Board
        </button>
      </div>

      {#if activeTab === "services"}
        <button
          type="button"
          class="btn btn-primary cursor-pointer disabled:cursor-not-allowed disabled:opacity-50"
          onclick={() => openNewService("catalog")}
          disabled={loading || registryError !== null}
          title={registryError
            ? "Application management is unavailable until the service registry recovers."
            : "Add a new application"}
          data-testid="new-service-button"
        >
          New Application
        </button>
      {/if}
    </div>
  </div>

  {#if activeTab === "services"}
    <!-- Applications View -->
    {#if inventoryLoading}
      <section
        class="mb-6 h-24 animate-pulse rounded-lg border border-border bg-muted/20"
        data-testid="inventory-services-loading"
        aria-label="Loading canonical service inventory"
      ></section>
    {:else if inventoryError}
      <section
        class="mb-6 rounded-lg border border-red-700 bg-red-950/40 p-4 text-red-100"
        role="alert"
        data-testid="inventory-services-error-panel"
      >
        {inventoryError}
      </section>
    {/if}

    {#if !loading && !registryError}
      {@const s = runtimeSummary()}
      <div
        class="mb-6 grid grid-cols-2 gap-4 md:grid-cols-5 animate-in fade-in duration-200"
      >
        <button
          type="button"
          onclick={() => (statusFilter = "all")}
          class="rounded-lg border bg-card/70 p-3 text-center transition-colors cursor-pointer {statusFilter ===
          'all'
            ? 'border-primary'
            : 'border-border hover:border-border/80'}"
        >
          <div class="text-xl font-bold text-foreground">{s.total}</div>
          <div class="text-xs text-muted-foreground">All</div>
        </button>
        <button
          type="button"
          onclick={() => (statusFilter = "running")}
          class="rounded-lg border bg-card/70 p-3 text-center transition-colors cursor-pointer {statusFilter ===
          'running'
            ? 'border-green-500 bg-green-950/5'
            : 'border-border hover:border-border/80'}"
        >
          <div class="text-xl font-bold text-green-300">{s.running}</div>
          <div class="text-xs text-muted-foreground">Running</div>
        </button>
        <button
          type="button"
          onclick={() => (statusFilter = "pending")}
          class="rounded-lg border bg-card/70 p-3 text-center transition-colors cursor-pointer {statusFilter ===
          'pending'
            ? 'border-yellow-500 bg-yellow-950/5'
            : 'border-border hover:border-border/80'}"
        >
          <div class="text-xl font-bold text-yellow-300">{s.pending}</div>
          <div class="text-xs text-muted-foreground">Pending</div>
        </button>
        <button
          type="button"
          onclick={() => (statusFilter = "observed")}
          class="rounded-lg border bg-card/70 p-3 text-center transition-colors cursor-pointer {statusFilter ===
          'observed'
            ? 'border-blue-500 bg-blue-950/5'
            : 'border-border hover:border-border/80'}"
        >
          <div class="text-xl font-bold text-blue-300">{s.observed}</div>
          <div class="text-xs text-muted-foreground">Observed</div>
        </button>
        <button
          type="button"
          onclick={() => (statusFilter = "error")}
          class="rounded-lg border bg-card/70 p-3 text-center transition-colors cursor-pointer {statusFilter ===
          'error'
            ? 'border-red-500 bg-red-950/5'
            : 'border-border hover:border-border/80'}"
        >
          <div class="text-xl font-bold text-red-300">{s.error}</div>
          <div class="text-xs text-muted-foreground">Error</div>
        </button>
      </div>
    {/if}

    {#if !inventoryLoading && !inventoryError}
      <ServiceList
        groups={runtimeServiceGroups}
        countLabel={`${filteredRuntimeServiceRows.length} shown · ${runtimeSummary().total} runtime service${runtimeSummary().total === 1 ? "" : "s"}`}
        emptyTitle={canonicalServices.length === 0
          ? "No runtime services reported"
          : `No services matching ${statusFilter}`}
        emptyBody={canonicalServices.length === 0
          ? "Services appear here after a connected runtime target reports them."
          : "Clear the filter to show all runtime services."}
        testId="runtime-service-list"
        cardTestId="runtime-service-card"
      >
        {#snippet children(item)}
          {@const row = runtimeServiceById.get(item.id)}
          {#if row}
            {@const service = row.runtime}
            {@const registryService = row.registry}
            {@const serviceActionJob = serviceActionJobs[service.id]}
            {@const serviceLogState = serviceLogs[service.id]}
            <div class="flex flex-wrap gap-2">
              {#if registryError === null}
                {#each service.allowed_actions.filter((action) => action === "start" || action === "stop" || action === "restart" || action === "logs") as action}
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm disabled:opacity-50"
                    disabled={isManagedServiceActionActive(serviceActionJob)}
                    onclick={() =>
                      controlInventoryService(
                        service,
                        action as ManagedServiceAction,
                      )}
                  >
                    {action}
                  </button>
                {/each}
                {#if registryService?.status === "pending_verification"}
                  <button
                    type="button"
                    class="btn btn-primary btn-sm"
                    onclick={() => handleVerifyService(registryService.id!)}
                    disabled={saving}>Verify & Finish</button
                  >
                {/if}
                {#if registryService && canMoveApplication(registryService)}
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm"
                    onclick={() => openMigration(registryService)}
                    disabled={saving ||
                      moveTargetsFor(registryService).length === 0}>Move</button
                  >
                {/if}
              {/if}
            </div>
            {#if serviceActionJob}
              <div
                class="mt-3 rounded border border-border/70 bg-muted/25 p-2 text-xs"
                data-testid="managed-service-action-status"
                data-service-id={service.id}
                role={isManagedServiceActionTerminal(serviceActionJob.state) &&
                serviceActionJob.state !== "completed"
                  ? "alert"
                  : "status"}
              >
                <p class="font-medium text-foreground">
                  {serviceActionJob.action}: {serviceActionJob.state}
                </p>
                {#if serviceActionJob.detail}<p
                    class="mt-1 text-muted-foreground"
                  >
                    {serviceActionJob.detail}
                  </p>{/if}
              </div>
            {/if}
            {#if serviceLogState}
              <div
                class="mt-3 rounded border border-border/70 bg-muted/20 p-2"
                data-testid="managed-service-logs"
                data-service-id={service.id}
              >
                <p class="font-medium text-foreground">
                  Service logs{serviceLogState.loading ? " · Loading…" : ""}
                </p>
                {#if serviceLogState.error}<p
                    class="mt-2 text-xs text-destructive"
                    role="alert"
                  >
                    {serviceLogState.error}
                  </p>{/if}
                {#if serviceLogState.entries.length > 0}
                  <ol
                    class="mt-2 max-h-56 space-y-1 overflow-auto font-mono text-[11px]"
                  >
                    {#each serviceLogState.entries as entry (`${entry.timestamp}-${entry.message}`)}
                      <li class="rounded bg-background/60 p-1.5">
                        <time
                          class="text-muted-foreground"
                          datetime={entry.timestamp}>{entry.timestamp}</time
                        >
                        <pre
                          class="whitespace-pre-wrap break-words text-foreground">{entry.message}</pre>
                      </li>
                    {/each}
                  </ol>
                {/if}
                {#if serviceLogState.nextCursor}
                  <button
                    type="button"
                    class="mt-2 btn btn-secondary btn-sm"
                    disabled={isManagedServiceActionActive(serviceActionJob)}
                    onclick={() => loadMoreManagedServiceLogs(service)}
                    >Load older logs</button
                  >
                {/if}
              </div>
            {/if}
          {/if}
        {/snippet}
      </ServiceList>
    {/if}

    {#if registryError}
      <div
        class="flex gap-3 rounded-lg border border-amber-500/30 bg-amber-500/10 p-4 text-sm text-amber-100 animate-in fade-in"
        role="status"
        data-testid="registry-services-warning"
      >
        <AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-amber-400" />
        <div class="min-w-0">
          <p class="font-semibold">
            Application management is temporarily unavailable
          </p>
          <p class="mt-1 text-amber-100/80">
            Any runtime inventory shown above remains read-only. Catalog,
            import, verification, and migration actions stay disabled until the
            service registry recovers.
          </p>
          <p class="mt-2 break-words font-mono text-xs text-amber-100/70">
            {registryError}
          </p>
          <button
            type="button"
            class="btn btn-secondary btn-sm mt-3 cursor-pointer"
            onclick={() => void loadRegistryManagement()}
          >
            Retry management data
          </button>
        </div>
      </div>
    {/if}
  {:else}
    <!-- Application Placement Board -->
    {#if !loading && !registryError && !registry.migration_available}
      <div
        class="mb-5 flex gap-3 rounded-lg border border-amber-500/30 bg-amber-500/10 p-4 text-sm text-amber-100"
        role="status"
        data-testid="migration-unavailable"
      >
        <AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-amber-400" />
        <div>
          <p class="font-semibold">Runtime migration is not enabled yet</p>
          <p class="mt-1 text-amber-100/80">
            {registry.migration_unavailable_reason ||
              "Drag-and-drop stays disabled until deployment, health verification, cutover, and source drain are handled by a real runtime executor."}
          </p>
        </div>
      </div>
    {/if}
    {#if cockpitStale || cockpitError}
      <div
        class="mb-5 flex gap-3 rounded-lg border border-amber-500/30 bg-amber-500/10 p-4 text-sm text-amber-100"
        role={cockpitError ? "alert" : "status"}
        data-testid="placement-cockpit-refresh-state"
      >
        <AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-amber-400" />
        <div class="min-w-0">
          <p class="font-semibold">
            {cockpitError
              ? "Capacity telemetry refresh failed"
              : "Showing last verified capacity telemetry"}
          </p>
          <p class="mt-1 text-amber-100/80">
            The last successful snapshot remains visible until this stack
            reports replacement or explicit down evidence.
          </p>
          {#if cockpitError}
            <p
              class="mt-2 break-words font-mono text-xs text-amber-100/70"
              data-testid="placement-cockpit-refresh-error"
            >
              {cockpitError.message}
            </p>
          {/if}
        </div>
      </div>
    {/if}
    {#if loading}
      <div class="flex gap-6 overflow-x-auto pb-4">
        {#each Array(3) as _, i (i)}
          <div
            class="w-80 shrink-0 h-[450px] border border-border bg-card/30 rounded-xl animate-pulse p-4"
          ></div>
        {/each}
      </div>
    {:else if registryError}
      <div
        class="flex gap-3 rounded-lg border border-amber-500/30 bg-amber-500/10 p-4 text-sm text-amber-100"
        role="status"
        data-testid="registry-placement-warning"
      >
        <AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-amber-400" />
        <div class="min-w-0">
          <p class="font-semibold">
            Application placement is temporarily unavailable
          </p>
          <p class="mt-1 text-amber-100/80">
            Placement and migration controls stay disabled until the service
            registry recovers.
          </p>
          <p class="mt-2 break-words font-mono text-xs text-amber-100/70">
            {registryError}
          </p>
          <button
            type="button"
            class="btn btn-secondary btn-sm mt-3 cursor-pointer"
            onclick={() => void loadRegistryManagement()}
          >
            Retry management data
          </button>
        </div>
      </div>
    {:else if registry.servers.length === 0}
      <div class="rounded-lg border border-border bg-card/60 p-12 text-center">
        <p class="text-lg text-foreground">No servers registered</p>
        <p class="mt-1 text-sm text-muted-foreground">
          Connect nodes to your stack to manage application placement.
        </p>
      </div>
    {:else}
      <div
        class="flex gap-6 overflow-x-auto pb-6 scrollbar-thin select-none animate-in fade-in duration-300"
      >
        {#each registry.servers as server (server.id)}
          {@const serverServices = registry.services.filter(
            (s) => s.server_id === server.id,
          )}
          {@const stack = registry.stacks.find(
            (st) => st.id === server.stack_id,
          )}
          {@const cap = getServerCapacity(server)}

          <!-- Server Column -->
          <div
            class="w-80 shrink-0 flex flex-col bg-card/35 backdrop-blur-sm border rounded-xl p-4 min-h-[550px] transition-all duration-300
                   {activeDragServerId === server.id
              ? 'border-primary/60 bg-primary/5 shadow-md shadow-primary/5'
              : 'border-border/80'}"
            ondragover={(e) => handleDragOver(e, server.id)}
            ondragleave={handleDragLeave}
            ondrop={(e) => handleDrop(e, server.id)}
            role="list"
            aria-label={`Server ${server.name} column`}
          >
            <!-- Server Headline Card -->
            <div
              class="bg-muted/30 border border-border/75 rounded-lg p-3.5 mb-4 shadow-sm hover:border-primary/30 transition-colors duration-300"
            >
              <div class="flex items-start justify-between gap-2">
                <h3 class="font-bold text-sm text-foreground truncate">
                  {server.name}
                </h3>
                <span
                  class="text-[9px] px-1.5 py-0.5 rounded-md bg-muted border border-border font-medium text-muted-foreground shrink-0 uppercase tracking-wider"
                >
                  {server.role_label}
                </span>
              </div>
              <p
                class="text-[10px] text-muted-foreground/80 truncate font-mono mt-1"
              >
                {server.hostname || "No hostname"}
              </p>

              <div
                class="mt-3 pt-2.5 border-t border-border/50 flex items-center justify-between text-xs"
              >
                <span class="text-muted-foreground font-medium">Type:</span>
                <span class="font-semibold text-foreground capitalize">
                  {stack?.server_mode === "cloud" ||
                  stack?.provider_id ||
                  stack?.lease_provider
                    ? "Cloud VPS"
                    : "Local Node"}
                </span>
              </div>

              <!-- Capacity Meters -->
              <div class="mt-4 space-y-3">
                <div>
                  <div
                    class="flex justify-between text-[10px] text-muted-foreground mb-1"
                  >
                    <span class="flex items-center gap-1 font-medium"
                      ><HardDrive class="w-3.5 h-3.5 text-primary/80" /> Storage</span
                    >
                    <span class="font-medium">
                      {#if cap.storageTotal}
                        {cap.storageFree ?? "?"} GB free / {cap.storageTotal} GB
                      {:else}
                        telemetry pending
                      {/if}
                    </span>
                  </div>
                  <div
                    class="w-full bg-muted/65 rounded-full h-1.5 overflow-hidden"
                  >
                    <div
                      class="bg-primary h-1.5 rounded-full transition-all duration-300"
                      style="width: {cap.storagePercent ?? 0}%"
                    ></div>
                  </div>
                </div>
                <div>
                  <div
                    class="flex justify-between text-[10px] text-muted-foreground mb-1"
                  >
                    <span class="flex items-center gap-1 font-medium"
                      ><Cpu class="w-3.5 h-3.5 text-info/80" /> RAM</span
                    >
                    <span class="font-medium">
                      {#if cap.ramTotal}
                        {cap.ramUsed ?? "?"} GB / {cap.ramTotal} GB
                      {:else}
                        telemetry pending
                      {/if}
                    </span>
                  </div>
                  <div
                    class="w-full bg-muted/65 rounded-full h-1.5 overflow-hidden"
                  >
                    <div
                      class="bg-info h-1.5 rounded-full transition-all duration-300"
                      style="width: {cap.ramPercent ?? 0}%"
                    ></div>
                  </div>
                </div>
                <p
                  class="text-[9px] uppercase tracking-wide text-muted-foreground/70"
                >
                  {cap.source === "telemetry"
                    ? cockpitStale
                      ? "last verified telemetry"
                      : "live telemetry"
                    : cap.source === "capabilities"
                      ? "server capabilities"
                      : "no capacity data"}
                </p>
              </div>
            </div>

            <!-- Application Cards Stack -->
            <div class="flex-1 space-y-3 flex flex-col">
              {#each serverServices as service (service.id || `${service.stack_id}-${service.name}`)}
                {@const isMigrating = isServiceMigrating(service.id)}
                {@const canMove = canMoveApplication(service)}

                <!-- Application Card -->
                <div
                  draggable={canMove}
                  ondragstart={(e) => handleDragStart(e, service)}
                  ondragend={() => (activeDragService = null)}
                  role="listitem"
                  class="space-y-2 {canMove
                    ? 'cursor-grab active:cursor-grabbing'
                    : 'opacity-75'} {service.status === 'migrating' ||
                  service.status === 'deploying'
                    ? 'animate-pulse'
                    : ''} {service.status === 'archived' ? 'opacity-55' : ''}"
                >
                  <ServiceCardCompact
                    name={applicationLabel(service)}
                    meta={serviceMeta(service)}
                    placement={servicePlacement(service)}
                    status={serviceCardStatus(service)}
                    migration={serviceMigration(service)}
                    showGrip={canMove}
                    onOpen={serviceAccessEnabled(service)
                      ? () => openService(service)
                      : undefined}
                  />

                  <!-- Relocating status messages -->
                  {#if isMigrating}
                    <div
                      class="mt-2.5 pt-2 border-t border-border/40 flex items-center gap-1.5 text-[11px] text-amber-400 font-medium"
                    >
                      <Loader2 class="w-3 h-3 animate-spin shrink-0" />
                      <span class="truncate"
                        >{getMigrationMessage(service.id)}</span
                      >
                    </div>
                  {/if}

                  <!-- Actions -->
                  {#if service.status === "pending_verification"}
                    <div class="mt-3 flex gap-2">
                      <button
                        type="button"
                        class="w-full btn btn-primary py-1.5 px-2.5 text-[10px] h-auto flex items-center justify-center gap-1 cursor-pointer"
                        onclick={() => handleVerifyService(service.id!)}
                        disabled={saving}
                      >
                        {#if saving}
                          <Loader2 class="w-3 h-3 animate-spin" />
                        {:else}
                          <Check class="w-3.5 h-3.5" />
                        {/if}
                        Verify & Finish
                      </button>
                    </div>
                  {/if}

                  {#if canMove}
                    <div class="mt-3 flex gap-2">
                      <button
                        type="button"
                        class="w-full btn btn-secondary py-1.5 px-2.5 text-[10px] h-auto flex items-center justify-center gap-1 cursor-pointer"
                        onclick={() => openMigration(service)}
                        disabled={saving ||
                          moveTargetsFor(service).length === 0}
                        title={moveTargetsFor(service).length === 0
                          ? "No available target server for this application"
                          : "Move application"}
                      >
                        <ArrowRight class="w-3.5 h-3.5" />
                        Move
                      </button>
                    </div>
                  {/if}

                  {#if service.status === "archived"}
                    <div class="mt-3 flex gap-2">
                      <button
                        type="button"
                        class="w-full btn btn-secondary border-red-900/40 hover:bg-red-950/20 hover:text-red-200 hover:border-red-900 py-1.5 px-2.5 text-[10px] h-auto flex items-center justify-center gap-1 cursor-pointer"
                        onclick={() => handleRemoveOld(service.id!)}
                        disabled={saving}
                      >
                        {#if saving}
                          <Loader2 class="w-3 h-3 animate-spin" />
                        {:else}
                          <Trash2 class="w-3.5 h-3.5 text-red-400" />
                        {/if}
                        Remove Old
                      </button>
                    </div>
                  {/if}
                </div>
              {/each}

              {#if activeDragServerId === server.id && activeDragService && activeDragService.server_id !== server.id}
                <div
                  class="border-2 border-dashed border-primary/50 bg-primary/5 rounded-lg p-4 h-24 flex flex-col items-center justify-center text-xs text-muted-foreground transition-all gap-1.5"
                >
                  <ArrowRight class="w-5 h-5 text-primary animate-pulse" />
                  <span class="font-medium"
                    >Move {applicationLabel(activeDragService)} here</span
                  >
                </div>
              {/if}

              {#if serverServices.length === 0}
                <div
                  class="flex-1 border border-dashed border-border/40 rounded-lg p-6 flex flex-col items-center justify-center text-center text-xs text-muted-foreground min-h-[140px] bg-card/20"
                >
                  <Server class="w-5 h-5 text-muted-foreground/50 mb-1.5" />
                  <span class="font-medium text-foreground/80"
                    >No applications deployed</span
                  >
                  <span class="text-[10px] mt-1 text-muted-foreground/80"
                    >{registry.migration_available
                      ? "Drop managed applications here"
                      : "Runtime migration unavailable"}</span
                  >
                </div>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

<!-- Application Move Confirmation Modal -->
{#if showMigrationConfirmModal && migrationSourceService && migrationTargetServer}
  {@const currentMigrationTarget = selectedMigrationTarget()}
  <Modal title="Move Application" onClose={cancelMigration} maxWidth="md">
    <div class="space-y-4">
      <div
        class="flex gap-3 p-3.5 bg-amber-500/10 border border-amber-500/20 text-amber-200 rounded-lg text-sm"
      >
        <AlertTriangle
          class="w-5 h-5 shrink-0 text-amber-400 mt-0.5 animate-pulse"
        />
        <div>
          <span class="font-semibold block">Application Move</span>
          This starts a controlled move of
          <strong class="text-foreground"
            >{applicationLabel(migrationSourceService)}</strong
          >.
        </div>
      </div>

      <p class="text-sm text-muted-foreground leading-relaxed">
        You are about to move this application from <strong
          class="text-foreground">{migrationSourceService.server_name}</strong
        >
        to
        <strong class="text-foreground"
          >{currentMigrationTarget?.name || migrationTargetServer.name}</strong
        >.
      </p>

      <label class="block space-y-1.5 text-sm">
        <span class="font-medium text-foreground">Target server</span>
        <select
          class="input w-full"
          bind:value={selectedMigrationTargetId}
          onchange={(event) => {
            const id = (event.currentTarget as HTMLSelectElement).value;
            selectedMigrationTargetId = id;
            migrationTargetServer =
              registry.servers.find((server) => server.id === id) ||
              migrationTargetServer;
          }}
        >
          {#each moveTargetsFor(migrationSourceService) as server (server.id)}
            <option value={server.id}>
              {server.name} · {server.role_label}
            </option>
          {/each}
        </select>
      </label>

      <div
        class="text-xs bg-muted/45 border border-border p-3.5 rounded-lg space-y-2"
      >
        <div class="font-semibold text-foreground">Move Steps:</div>
        <ol class="list-decimal list-inside space-y-1.5 text-muted-foreground">
          <li>
            The application config will be duplicated on the target server.
          </li>
          <li>
            A temporary instance will be deployed on <span
              class="text-foreground font-medium"
              >{currentMigrationTarget?.name ||
                migrationTargetServer.name}</span
            >.
          </li>
          <li>
            You can test the new instance while the old one remains active.
          </li>
          <li>
            Upon your manual verification, the old instance is deactivated and
            can be permanently removed.
          </li>
        </ol>
      </div>
    </div>

    {#snippet footer()}
      <button
        type="button"
        class="px-4 py-2 rounded-lg bg-secondary text-secondary-foreground hover:bg-secondary/80 transition-colors disabled:opacity-50 cursor-pointer font-medium"
        onclick={cancelMigration}
        disabled={saving}
      >
        Cancel
      </button>
      <button
        type="button"
        class="px-4 py-2 rounded-lg bg-primary hover:bg-primary/90 text-primary-foreground transition-colors disabled:opacity-50 flex items-center gap-1.5 cursor-pointer font-semibold"
        onclick={confirmMigration}
        disabled={saving}
      >
        {#if saving}
          <Loader2 class="w-4 h-4 animate-spin" />
        {/if}
        Start Move
      </button>
    {/snippet}
  </Modal>
{/if}

<!-- Add New Application Modal -->
{#if showNewService}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4 animate-in fade-in"
    role="presentation"
    onclick={(event) => {
      if (event.target === event.currentTarget) closeNewService();
    }}
  >
    <div
      class="w-full max-w-3xl rounded-lg border border-border bg-background p-6 shadow-xl"
      role="dialog"
      aria-modal="true"
      aria-labelledby="new-service-title"
      data-testid="new-service-dialog"
    >
      <div class="mb-5 flex items-start justify-between gap-4">
        <div>
          <h2
            id="new-service-title"
            class="text-xl font-semibold text-foreground"
          >
            New Application
          </h2>
          <p class="mt-1 text-sm text-muted-foreground">
            Add a catalog application or import unmanaged server inventory.
          </p>
        </div>
        <button
          type="button"
          class="btn btn-secondary cursor-pointer"
          onclick={closeNewService}
          disabled={saving}
        >
          Close
        </button>
      </div>

      <div class="mb-5 flex gap-2" role="tablist" aria-label="Service mode">
        <button
          type="button"
          class="btn {serviceMode === 'catalog'
            ? 'btn-primary'
            : 'btn-secondary'} cursor-pointer"
          aria-pressed={serviceMode === "catalog"}
          onclick={() => (serviceMode = "catalog")}
        >
          Catalog
        </button>
        <button
          type="button"
          class="btn {serviceMode === 'import'
            ? 'btn-primary'
            : 'btn-secondary'} cursor-pointer"
          aria-pressed={serviceMode === "import"}
          onclick={() => (serviceMode = "import")}
        >
          Import unmanaged
        </button>
      </div>

      <div class="grid gap-4 md:grid-cols-2">
        <label class="block">
          <span class="mb-1 block text-sm text-muted-foreground">Stack</span>
          <select
            bind:value={selectedStackId}
            class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
            data-testid="service-target-stack"
          >
            {#each registry.stacks as stack (stack.id)}
              <option value={stack.id}>{stack.name}</option>
            {/each}
          </select>
        </label>
        <label class="block">
          <span class="mb-1 block text-sm text-muted-foreground">Server</span>
          <select
            bind:value={selectedServerId}
            class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
            data-testid="service-target-server"
          >
            {#each targetServers as server (server.id)}
              <option value={server.id}
                >{server.name} · {server.role_label}</option
              >
            {/each}
          </select>
        </label>
      </div>

      {#if registry.stacks.length === 0 || registry.servers.length === 0}
        <div
          class="mt-5 rounded-lg border border-warning/40 bg-warning/10 p-4 text-sm text-warning animate-in slide-in-from-top-2"
        >
          A stack with at least one registered server is required before
          services can be attached.
        </div>
      {:else if serviceMode === "catalog"}
        <div class="mt-5 grid gap-3 md:grid-cols-2">
          {#each registry.catalog as service (service.id)}
            <button
              type="button"
              class="rounded-lg border border-border bg-card/70 p-4 text-left transition-colors hover:border-primary/60 cursor-pointer {selectedCatalogId ===
              service.id
                ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
                : ''}"
              aria-pressed={selectedCatalogId === service.id}
              onclick={() => (selectedCatalogId = service.id)}
              data-testid={`catalog-service-${service.id}`}
            >
              <span class="font-medium text-foreground">
                {service.display_name}
              </span>
              <span class="mt-1 block text-sm capitalize text-muted-foreground">
                {serviceTypeLabel(service)}
              </span>
              <span
                class="mt-2 block text-sm text-muted-foreground font-medium"
              >
                {service.description}
              </span>
            </button>
          {/each}
        </div>
        {#if selectedCatalogService}
          <p class="mt-4 text-sm text-muted-foreground animate-in fade-in">
            Selected: {selectedCatalogService.display_name}
          </p>
        {/if}
      {:else}
        <div class="mt-5 grid gap-4 md:grid-cols-2">
          <label class="block">
            <span class="mb-1 block text-sm text-muted-foreground">Name</span>
            <input
              bind:value={unmanagedName}
              class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
              placeholder="custom-dashboard"
              data-testid="unmanaged-service-name"
            />
          </label>
          <label class="block">
            <span class="mb-1 block text-sm text-muted-foreground"
              >Display name</span
            >
            <input
              bind:value={unmanagedDisplayName}
              class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
              placeholder="Custom Dashboard"
            />
          </label>
          <label class="block">
            <span class="mb-1 block text-sm text-muted-foreground">Type</span>
            <input
              bind:value={unmanagedType}
              class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
              placeholder="custom"
            />
          </label>
          <label class="block">
            <span class="mb-1 block text-sm text-muted-foreground">Port</span>
            <input
              type="number"
              min="1"
              max="65535"
              bind:value={unmanagedPort}
              class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
              placeholder="8080"
            />
          </label>
          <label class="block md:col-span-2">
            <span class="mb-1 block text-sm text-muted-foreground">URL</span>
            <input
              type="url"
              bind:value={unmanagedUrl}
              class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
              placeholder="http://server.local:8080"
            />
          </label>
        </div>
      {/if}

      {#if actionError}
        <div
          class="mt-5 rounded-lg border border-red-700 bg-red-950/40 p-3 text-sm text-red-100"
          role="alert"
        >
          {actionError}
        </div>
      {/if}

      <div class="mt-6 flex justify-end gap-3">
        <button
          type="button"
          class="btn btn-secondary cursor-pointer"
          onclick={closeNewService}
          disabled={saving}
        >
          Cancel
        </button>
        {#if serviceMode === "catalog"}
          <button
            type="button"
            class="btn btn-primary cursor-pointer"
            onclick={submitCatalogService}
            disabled={saving || !selectedStackId || !selectedServerId}
            data-testid="attach-catalog-service"
          >
            {saving ? "Adding..." : "Add Service"}
          </button>
        {:else}
          <button
            type="button"
            class="btn btn-primary cursor-pointer"
            onclick={submitObservedService}
            disabled={saving || !selectedStackId || !selectedServerId}
            data-testid="import-observed-service"
          >
            {saving ? "Importing..." : "Import as Observed"}
          </button>
        {/if}
      </div>
    </div>
  </div>
{/if}
