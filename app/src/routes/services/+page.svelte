<script lang="ts">
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import { tr, trn, stateLabel } from "#lib/i18n.svelte.js";
  import { onMount } from "svelte";
  import { goto, pushState, replaceState } from "$app/navigation";
  import { page } from "$app/state";
  import {
    listServiceRegistry,
    migrateRegistryService,
    verifyRegistryService,
    deleteRegistryService,
    type RegistryServer,
    type RegistryService,
    type ServiceRegistryPayload,
  } from "#lib/api/registry.js";
  import { getMonitoringCockpit } from "#lib/api/monitoring.js";
  import {
    applyCockpitRefreshError,
    applyCockpitRefreshSuccess,
    beginCockpitRefresh,
    type CockpitRefreshState,
  } from "#lib/monitoring/cockpit-snapshot.js";
  import { getJob, type JobDetail } from "#lib/api/jobs.js";
  import {
    cockpitServerFor,
    migrationProgress,
    moveTargetsFor as moveTargetsAmong,
    serverCapacity,
    serviceAccessEnabled as serviceAccessEnabledFor,
    serviceMeta,
    type PlacementColumn,
    type ServerCapacity,
  } from "#lib/services/placement.js";
  import {
    listCanonicalServicesReport,
    listServiceApplicationsReport,
    type CanonicalService,
    type ServiceApplication,
    type ServiceRowRejection,
  } from "#lib/api/services.js";
  import {
    openServiceUrl,
    serviceCardLogo,
    serviceCardLogoDomain,
    serviceCardLogoSrc,
    serviceCardUpdate,
    serviceCardVersion,
    serviceIsLocked,
    serviceLockReason,
    serviceCardPlacement as resolveServiceCardPlacement,
    serviceCardStatus as resolveServiceCardStatus,
    serviceCardStatusMessage as resolveServiceCardStatusMessage,
    serviceHasActiveMigration,
    serviceManagementState,
    type GovernedServiceAction,
  } from "#lib/service-card-adapter.js";
  import {
    ServiceActionTracker,
    actionFailed,
    isActionActive,
  } from "#lib/services/service-action-tracker.svelte.js";
  import {
    actionLabel,
    adoptionUnavailableReason,
    applicationCardActions,
    componentCardActions,
    runApplicationAction,
    runComponentAction,
    type ApplicationAction,
  } from "#lib/services/application-actions.js";
  import {
    applicationLocked,
    applicationOwnership,
    applicationPlacement,
    applicationVersion,
    runtimePlacement,
  } from "#lib/services/application-card.js";
  import ServiceList, {
    type ServiceListGroup,
    type ServiceListItem,
  } from "#lib/components/inventory/ServiceList.svelte";
  import ServiceMoveDialog from "#lib/components/inventory/ServiceMoveDialog.svelte";
  import TrademarkNotice from "#lib/components/TrademarkNotice.svelte";
  import PlacementBoard from "#lib/components/inventory/PlacementBoard.svelte";
  import { placeUseCasesHref } from "#lib/server-links.js";
  import ServiceAdoptionDialog from "#lib/components/inventory/ServiceAdoptionDialog.svelte";
  import { confirmInApp } from "#lib/dialogs/in-app-dialog.js";
  import {
    ServiceDetailSheet,
    type ServiceCardAction,
    type ServiceDetailTab,
    type ServiceMigration,
    type ServicePlacement,
    type ServiceStatusKind,
  } from "@kombiverselabs/ui/service";
  import { PageHeader } from "@kombiverselabs/ui/shell";
  import Button from "#lib/components/ui/Button.svelte";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import { SegmentedControl } from "@kombiverselabs/ui/primitives";
  import {
    AlertTriangle,
    Check,
    Trash2,
    ArrowRight,
    Loader2,
  } from "@lucide/svelte";

  const emptyRegistry: ServiceRegistryPayload = {
    catalog: [],
    kit_deployments: [],
    servers: [],
    services: [],
    migration_available: false,
    migration_unavailable_reason: tr(
      "ui.services.runtimeServiceMigrationIsNot",
    ),
  };

  let registry = $state<ServiceRegistryPayload>(emptyRegistry);
  let canonicalServices = $state<CanonicalService[]>([]);
  let serviceApplications = $state<ServiceApplication[]>([]);
  let loading = $state(true);
  let inventoryLoading = $state(true);
  let saving = $state(false);
  let registryError = $state<string | null>(null);
  let inventoryError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  /** Rows the client could not read; the rest of the list still renders. */
  let inventoryRejected = $state<ServiceRowRejection[]>([]);
  const tracker = new ServiceActionTracker({
    refresh: () => refreshRegistryQuietly(),
    observedRevision: (serviceId) =>
      canonicalServices.find((service) => service.id === serviceId)
        ?.inventory_revision,
  });
  let servicesPageActive = false;
  let statusFilter = $state<string>("all");
  let adoptionService = $state<CanonicalService | null>(null);
  let releaseAdoption = $state(false);
  let selectedKitDeploymentId = $state("");
  let selectedServerId = $state("");

  // Sub-tabs
  let activeTab = $state<"services" | "servers">("services");

  // Service detail sheet — driven by ?service={id} shallow routing so it
  // deep-links, the back button closes it, and board state survives.
  let detailTab = $state<ServiceDetailTab>("overview");
  /**
   * The sheet's own state, kept in step with the URL rather than derived from
   * it: `page.url` does not follow a shallow `pushState` in this SvelteKit
   * version, so a derivation off it renders a deep link correctly and then
   * never reacts to a click. Open/close set this directly and popstate re-reads
   * the address bar, so deep link, click and back all agree.
   */
  let detailServiceId = $state<string | null>(null);

  function serviceIdFromLocation(): string | null {
    if (typeof location === "undefined") return null;
    return new URL(location.href).searchParams.get("service");
  }

  /** `?service=<id>&sheet=logs` deep-links straight into one sheet tab. */
  function sheetTabFromLocation(): ServiceDetailTab {
    if (typeof location === "undefined") return "overview";
    const sheet = new URL(location.href).searchParams.get("sheet");
    return sheet === "activity" || sheet === "logs" || sheet === "operations"
      ? sheet
      : "overview";
  }

  /** `?service=<id>&adopt=1` opens "Stack it" for an observed component. */
  function openAdoptionFromLocation() {
    if (!servicesPageActive) return;
    const url = new URL(location.href);
    if (url.searchParams.get("adopt") !== "1") return;
    const target = canonicalServices.find(
      (service) => service.id === url.searchParams.get("service"),
    );
    url.searchParams.delete("adopt");
    replaceState(url, {});
    if (target?.allowed_actions.includes("adopt")) {
      openAdoptionDialog(target, false);
    }
  }

  function syncServiceSheetFromLocation() {
    detailServiceId = serviceIdFromLocation();
    detailTab = sheetTabFromLocation();
  }

  function openServiceSheet(
    serviceId: string,
    tab: ServiceDetailTab = "overview",
  ) {
    detailTab = tab;
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
    url.searchParams.delete("sheet");
    pushState(url, {});
  }

  function openAdoptionDialog(service: CanonicalService, release: boolean) {
    closeServiceSheet();
    adoptionService = service;
    releaseAdoption = release;
  }

  function closeAdoptionDialog() {
    const service = adoptionService;
    adoptionService = null;
    if (service) openServiceSheet(service.id, "operations");
  }

  /**
   * Owner approval is a real gate, not an implied checkbox: every governed
   * mutation asks before the POST sends owner_approved.
   */
  async function requestServiceAction(
    service: CanonicalService,
    action: GovernedServiceAction,
  ) {
    actionError = null;
    if (action === "logs") openServiceSheet(service.id, "logs");
    const error = await runComponentAction(tracker, service, action);
    if (error) actionError = error;
  }

  function cardActionsForRuntime(
    runtime: CanonicalService,
    accessUrl: string | undefined,
  ): ServiceCardAction[] {
    return componentCardActions(runtime, tracker, {
      onAction: (action) => void requestServiceAction(runtime, action),
      onAdopt: () => openAdoptionDialog(runtime, false),
      onOpen: accessUrl ? () => openServiceUrl({ url: accessUrl }) : undefined,
      onDetails: () => openServiceSheet(runtime.id),
    });
  }

  async function requestApplicationAction(
    application: ServiceApplication,
    action: ApplicationAction,
  ) {
    actionError = null;
    const error = await runApplicationAction(tracker, application, action);
    if (error) actionError = error;
  }

  function cardActionsForApplication(
    application: ServiceApplication,
  ): ServiceCardAction[] {
    return applicationCardActions(application, tracker, {
      onLogs: (target) => void requestServiceAction(target, "logs"),
      onRun: (action) => void requestApplicationAction(application, action),
      onAdopt: (target) => openAdoptionDialog(target, false),
      onOpen: (url) => openServiceUrl({ url }),
      onDetails: () =>
        void goto(
          `/services/${encodeURIComponent(application.application_id)}`,
        ),
    });
  }

  // Migration state
  let showMigrationConfirmModal = $state(false);
  let migrationSourceService = $state<RegistryService | null>(null);
  let migrationTargetServer = $state<RegistryServer | null>(null);
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
    cockpitState.snapshot?.kit_deployment_id === selectedKitDeploymentId
      ? cockpitState.snapshot
      : null,
  );

  onMount(() => {
    servicesPageActive = true;
    tracker.start();
    const params = new URLSearchParams(window.location.search);
    const kitDeploymentId = params.get("kit_deployment_id");
    const tab = params.get("tab");
    if (kitDeploymentId) selectedKitDeploymentId = kitDeploymentId;
    if (tab === "servers") activeTab = "servers";
    syncServiceSheetFromLocation();
    window.addEventListener("popstate", syncServiceSheetFromLocation);
    void loadServicesPage().then(openAdoptionFromLocation);
    const interval = setInterval(() => {
      if (hasActiveMigrations() || activeMigrationJobIds.length > 0) {
        void pollMigrationJobs();
        void refreshRegistryQuietly();
      }
      if (tracker.hasActiveJobs() || tracker.hasConverging()) {
        void tracker.tick();
      }
    }, 3000);
    return () => {
      servicesPageActive = false;
      tracker.stop();
      window.removeEventListener("popstate", syncServiceSheetFromLocation);
      clearInterval(interval);
    };
  });

  interface RuntimeServiceRow extends ServiceListItem {
    runtime: CanonicalService;
    registry?: RegistryService;
    runtimeTargetName: string;
  }

  function label(value?: string): string {
    return stateLabel(value);
  }

  function registryServiceForRuntime(
    runtime: CanonicalService,
  ): RegistryService | undefined {
    return registry.services.find((service) => service.id === runtime.id);
  }

  const placementContext = $derived({
    servers: registry.servers,
    kitDeployments: registry.kit_deployments,
  });

  function runtimeServicePlacement(
    runtime: CanonicalService,
    service?: RegistryService,
  ): ServicePlacement {
    return runtimePlacement(runtime, service, placementContext);
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
      const cardPlacement: ServicePlacement = placement;
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
      // A component keeps its own recognition: the viewer's logo override
      // belongs to the application, not to each of its parts.
      const runtimeLogoDomain = serviceCardLogoDomain(
        runtime.catalog,
        runtime.application_key,
        runtime.service_key,
        runtime.application_display_name,
        service?.display_name || runtime.name,
      );
      const accessUrl =
        service?.url && serviceAccessEnabled(service)
          ? service.url
          : typeof runtime.access.url === "string"
            ? runtime.access.url
            : undefined;
      return {
        id: runtime.id,
        name: service?.display_name || runtime.name,
        meta:
          management === "observed"
            ? tr("ui.services.discoveredOn", {
                name:
                  server?.name ||
                  runtime.server_id ||
                  tr("ui.services.thisHomelab"),
              })
            : `${service?.type || runtime.service_key || tr("ui.services.serviceFallback")} · ${tr(`ui.services.placement.${placement}`)}`,
        placement: cardPlacement,
        status: resolveServiceCardStatus(cardSource),
        statusLabel: label(runtimeState),
        statusMessage: resolveServiceCardStatusMessage(cardSource),
        runtimeTargetId,
        targetKind: runtime.target_kind,
        targetLabel:
          runtime.target_kind === "managed_workload"
            ? runtime.placement.provider_id ||
              tr("ui.services.managedProviderTarget")
            : server?.name ||
              runtime.server_id ||
              tr("ui.services.placementUnknown"),
        managementLabel:
          management === "managed"
            ? tr("ui.services.stacked")
            : management === "observed"
              ? tr("ui.services.unstacked")
              : tr("ui.services.ownershipUnknown"),
        workflowLabel: migration
          ? label(service?.migration_status || "operation active")
          : undefined,
        freshnessLabel: label(runtime.placement.freshness.state),
        sourceLabel: runtime.source || undefined,
        details: tr("ui.services.desiredObserved", {
          desired: stateLabel(runtime.desired_state),
          observed: stateLabel(runtime.observed_state),
        }),
        logoDomain: runtimeLogoDomain,
        logoSrc: serviceCardLogoSrc(runtimeLogoDomain, {
          catalog: runtime.catalog,
        }),
        version: serviceCardVersion(runtime.version),
        update: serviceCardUpdate(runtime.version),
        locked: serviceIsLocked(runtime.mutation_lock),
        lockedReason: serviceLockReason(runtime.mutation_lock),
        attention: needsAttention || migration,
        actions: cardActionsForRuntime(runtime, accessUrl),
        desiredState: runtime.desired_state || undefined,
        observedState: runtime.observed_state || undefined,
        runtime,
        registry: service,
        runtimeTargetName:
          server?.name || service?.server_name || runtimeTargetId,
      };
    }),
  );

  const detailRow = $derived(
    detailServiceId
      ? runtimeServiceRows.find((row) => row.id === detailServiceId)
      : undefined,
  );
  const detailApplication = $derived(
    detailRow
      ? serviceApplications.find((application) =>
          application.components.some(
            (component) => component.id === detailRow.runtime.id,
          ),
        )
      : undefined,
  );
  /** Board rows may reference registry services without a canonical row yet. */
  const detailRegistryService = $derived(
    detailServiceId
      ? (detailRow?.registry ??
          registry.services.find((service) => service.id === detailServiceId))
      : undefined,
  );
  /** The canonical runtime row, which is where the owner guardrail lives. */
  const detailRuntimeService = $derived(detailRow?.runtime);
  const detailOpen = $derived(
    Boolean(detailServiceId && (detailRow || detailRegistryService)),
  );
  const detailJob = $derived(
    detailServiceId ? tracker.jobs[detailServiceId] : undefined,
  );
  const detailLogState = $derived(
    detailServiceId ? tracker.logs[detailServiceId] : undefined,
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
            ? tr("ui.services.managedProviderTarget")
            : tr("ui.services.runtimeTarget"),
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

  const RUNNING_STATES = ["healthy", "running", "reachable"];
  const PENDING_STATES = ["pending", "unknown", "adopting", "starting"];
  const ERROR_STATES = ["degraded", "error", "failed", "unhealthy", "offline"];

  function applicationMatchesFilter(application: ServiceApplication): boolean {
    const status = application.status.toLowerCase();
    switch (statusFilter) {
      case "all":
        return true;
      case "running":
        return RUNNING_STATES.includes(status);
      case "pending":
        return PENDING_STATES.includes(status);
      case "observed":
        return ["observed", "mixed"].includes(
          applicationOwnership(application),
        );
      case "error":
        return ERROR_STATES.includes(status);
      default:
        return status === statusFilter;
    }
  }

  const applicationRows = $derived.by<ServiceListItem[]>(() =>
    serviceApplications
      .filter((application) => !application.system)
      .filter(applicationMatchesFilter)
      .map((application) => {
        const server = registry.servers.find(
          (candidate) => candidate.id === application.server_id,
        );
        const version = applicationVersion(application);
        const ownership = applicationOwnership(application);
        const logo = serviceCardLogo(
          application.catalog ?? application.components[0]?.catalog,
          application.application_key,
          [application.application_key, application.display_name],
        );
        const status = resolveServiceCardStatus({ status: application.status });
        const actions = cardActionsForApplication(application);
        const stackable = actions.some((action) => action.id === "adopt");
        const lockedComponent = application.components.find((component) =>
          serviceIsLocked(component.mutation_lock),
        );
        return {
          id: application.application_id,
          name: application.display_name,
          meta: `${trn("ui.services.componentCount", application.components.length)} · ${server?.name || application.server_id}`,
          address: application.access.address,
          addressUnavailableReason: application.access.address
            ? undefined
            : label(application.access.reason || "address unavailable"),
          detailsHref: `/services/${encodeURIComponent(application.application_id)}`,
          placement: applicationPlacement(
            application,
            placementContext,
            registryServiceForRuntime,
          ),
          status,
          statusLabel: label(application.status),
          // The Stack-it strip carries its own hint, and an error strip must
          // not wear an address note.
          statusMessage:
            application.access.kind === "internal" &&
            !stackable &&
            status !== "error"
              ? tr("ui.services.internalAddressOnlyOpenIt")
              : undefined,
          managementLabel:
            ownership === "managed"
              ? tr("ui.services.stacked")
              : ownership === "observed"
                ? tr("ui.services.unstacked")
                : ownership === "mixed"
                  ? tr("ui.serviceCards.ownershipMixed")
                  : undefined,
          runtimeTargetId: application.server_id,
          targetKind: "server",
          targetLabel: server?.name || application.server_id,
          freshnessLabel:
            application.observed_at || tr("ui.serviceCards.notObserved"),
          sourceLabel: tr("ui.services.stackkitsApplicationModel"),
          logoDomain: logo.domain,
          logoSrc: logo.src,
          version: serviceCardVersion(version),
          update: serviceCardUpdate(version),
          locked: applicationLocked(application),
          lockedReason: serviceLockReason(lockedComponent?.mutation_lock),
          actions,
        } satisfies ServiceListItem;
      }),
  );

  const applicationById = $derived.by(
    () =>
      new Map(
        serviceApplications.map((application) => [
          application.application_id,
          application,
        ]),
      ),
  );

  const applicationGroups = $derived.by<ServiceListGroup[]>(() => {
    const groups = new Map<string, ServiceListGroup>();
    for (const application of applicationRows) {
      const server = registry.servers.find(
        (candidate) => candidate.id === application.runtimeTargetId,
      );
      const group = groups.get(application.runtimeTargetId) || {
        id: application.runtimeTargetId,
        name: server?.name || application.runtimeTargetId,
        meta: server?.hostname || tr("ui.services.nodeBoundApplications"),
        targetKind: "server",
        items: [],
      };
      group.items.push(application);
      groups.set(application.runtimeTargetId, group);
    }
    return Array.from(groups.values());
  });

  const systemServiceGroups = $derived.by<ServiceListGroup[]>(() =>
    (() => {
      // A component of a listed application belongs to that application card,
      // never to the system list; only unclassified technical rows land here.
      const applicationComponentIds = new Set(
        serviceApplications
          .filter((application) => !application.system)
          .flatMap((application) =>
            application.components.map((component) => component.id),
          ),
      );
      const systemIDs = new Set(
        runtimeServiceRows
          .filter(
            (item) =>
              !applicationComponentIds.has(item.id) &&
              (item.runtime.application_key === "system" ||
                item.runtime.management_state === "observed"),
          )
          .map((item) => item.id),
      );
      return runtimeServiceGroups
        .map((group) => ({
          ...group,
          items: group.items.filter((item) => systemIDs.has(item.id)),
        }))
        .filter((group) => group.items.length > 0);
    })(),
  );

  const applicationSummary = $derived(() => {
    const applications = serviceApplications.filter((item) => !item.system);
    const statusOf = (item: ServiceApplication) => item.status.toLowerCase();
    return {
      total: applications.length,
      running: applications.filter((item) =>
        RUNNING_STATES.includes(statusOf(item)),
      ).length,
      pending: applications.filter((item) =>
        PENDING_STATES.includes(statusOf(item)),
      ).length,
      observed: applications.filter((item) =>
        ["observed", "mixed"].includes(applicationOwnership(item)),
      ).length,
      error: applications.filter((item) =>
        ERROR_STATES.includes(statusOf(item)),
      ).length,
    };
  });

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
    const selectedServer = registry.servers.find(
      (server) => server.id === selectedServerId,
    );
    const targetServer =
      selectedServer ??
      registry.servers.find(
        (server) => server.kit_deployment_id === selectedKitDeploymentId,
      ) ??
      registry.servers[0];
    selectedServerId = targetServer?.id ?? "";
    selectedKitDeploymentId =
      targetServer?.kit_deployment_id ?? registry.kit_deployments[0]?.id ?? "";
  });

  $effect(() => {
    if (
      selectedKitDeploymentId &&
      selectedKitDeploymentId !== cockpitState.stackId
    ) {
      void loadCockpit(selectedKitDeploymentId);
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
          : tr("ui.services.serviceRegistryCouldNotBe");
    } finally {
      loading = false;
    }
  }

  async function loadInventory() {
    inventoryLoading = true;
    inventoryError = null;
    try {
      const [services, applications] = await Promise.all([
        listCanonicalServicesReport(),
        listServiceApplicationsReport(),
      ]);
      canonicalServices = services.items;
      serviceApplications = applications.items;
      inventoryRejected = [...services.rejected, ...applications.rejected];
    } catch (err) {
      inventoryError =
        err instanceof Error
          ? err.message
          : tr("ui.services.runtimeInventoryCouldNotBe");
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
        if (selectedKitDeploymentId) {
          await loadCockpit(selectedKitDeploymentId);
        }
      })
      .catch(() => undefined);
    const inventoryRefresh = Promise.all([
      listCanonicalServicesReport(),
      listServiceApplicationsReport(),
    ])
      .then(([nextServices, nextApplications]) => {
        if (!servicesPageActive) return;
        canonicalServices = nextServices.items;
        serviceApplications = nextApplications.items;
        inventoryRejected = [
          ...nextServices.rejected,
          ...nextApplications.rejected,
        ];
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
          .filter((server) => server.kit_deployment_id === stackId)
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

  // New applications arrive as use cases on a target the owner picks: one
  // of the existing StackKits or servers first, a new server last.
  function openNewService() {
    actionError = null;
    void goto(placeUseCasesHref());
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

  function getServerCapacity(server: RegistryServer): ServerCapacity {
    return serverCapacity(
      cockpitServerFor(activeCockpit?.servers ?? [], server),
    );
  }

  function applicationLabel(service: RegistryService | null): string {
    return (
      service?.application_name ||
      service?.display_name ||
      tr("ui.services.application")
    );
  }

  function serviceDeployment(service: RegistryService) {
    return registry.kit_deployments.find(
      (deployment) => deployment.id === service.kit_deployment_id,
    );
  }

  function servicePlacement(service: RegistryService): ServicePlacement {
    const deployment = serviceDeployment(service);
    return resolveServiceCardPlacement(
      {
        ...service,
        provider:
          deployment?.provider_id ||
          deployment?.lease_provider ||
          deployment?.runtime_lane,
      },
      [
        deployment?.server_provisioning_mode,
        deployment?.runtime_offering_id,
        deployment?.provider_id,
        deployment?.lease_provider,
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

  function serviceMigration(
    service: RegistryService,
  ): ServiceMigration | undefined {
    if (!isServiceMigrating(service.id)) {
      return undefined;
    }
    return {
      to: servicePlacement(service),
      progress: migrationProgress(service.migration_status || "queued"),
      message: getMigrationMessage(service.id),
    };
  }

  function openService(service: RegistryService) {
    const url = service.url;
    if (!url || !serviceAccessEnabled(service)) return;
    window.open(url, "_blank", "noopener,noreferrer");
  }

  function serviceAccessEnabled(service: RegistryService): boolean {
    return serviceAccessEnabledFor(service, isServiceMigrating(service.id));
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
        tr("ui.services.runtimeServiceMigrationIsNot")
      );
    }
    if (isServiceMigrating(service.id)) {
      return tr("ui.services.applicationIsAlreadyMovingOr");
    }
    return (
      service.move_blocked_reason ||
      (serviceManagementState(service) === "observed"
        ? tr("ui.services.observedUnmanagedApplicationsMustBe")
        : tr("ui.services.applicationNeedsAStableRunning"))
    );
  }

  function moveTargetsFor(service: RegistryService): RegistryServer[] {
    return moveTargetsAmong(registry.servers, service);
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
      actionError = tr("ui.services.noAvailableTargetNodeFor");
      return;
    }
    migrationSourceService = service;
    migrationTargetServer = target;
    showMigrationConfirmModal = true;
    actionError = null;
  }

  const placementColumns = $derived.by((): PlacementColumn[] =>
    registry.servers.map((server) => {
      const deployment = registry.kit_deployments.find(
        (candidate) => candidate.id === server.kit_deployment_id,
      );
      return {
        server,
        cloud: Boolean(
          deployment?.server_mode === "cloud" ||
            deployment?.provider_id ||
            deployment?.lease_provider,
        ),
        capacity: getServerCapacity(server),
        cards: registry.services
          .filter((service) => service.server_id === server.id)
          .map((service) => ({
            key: service.id || `${service.kit_deployment_id}-${service.name}`,
            service,
            name: applicationLabel(service),
            meta: serviceMeta(service),
            placement: servicePlacement(service),
            status: serviceCardStatus(service),
            migration: serviceMigration(service),
            canMove: canMoveApplication(service),
            migrating: isServiceMigrating(service.id),
            migrationMessage: getMigrationMessage(service.id),
            actions: [
              ...(serviceAccessEnabled(service)
                ? [
                    {
                      id: "open" as const,
                      onSelect: () => openService(service),
                    },
                  ]
                : []),
              {
                id: "details" as const,
                onSelect: () => openServiceSheet(service.id!),
              },
            ],
          })),
      };
    }),
  );

  function cancelMigration() {
    showMigrationConfirmModal = false;
    migrationSourceService = null;
    migrationTargetServer = null;
  }

  async function confirmMigration(targetServer: RegistryServer) {
    if (!migrationSourceService) return;

    showMigrationConfirmModal = false;
    const sourceSvc = migrationSourceService;

    migrationSourceService = null;
    migrationTargetServer = null;

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
        err instanceof Error
          ? err.message
          : tr("ui.services.failedToInitiateMigration");
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
      return tr("ui.services.relocatingOnBackend");
    }
    if (phase === "deploying") {
      return tr("ui.services.waitingForRuntimeDeployment");
    }
    if (phase === "pending_verification") {
      return tr("ui.services.targetIsReadyForVerification");
    }
    return tr("ui.services.migrationJobIsActive");
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
        err instanceof Error
          ? err.message
          : tr("ui.services.failedToVerifyService");
    } finally {
      saving = false;
    }
  }

  async function handleRemoveOld(serviceId: string) {
    if (
      !(await confirmInApp({
        title: tr("ui.services.deleteArchivedService"),
        message: tr("ui.services.thisPermanentlyDeletesTheArchived"),
        confirmText: tr("ui.settings.delete"),
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
          : tr("ui.services.failedToDeleteArchivedService");
    } finally {
      saving = false;
    }
  }
</script>

{#if adoptionService}{#key `${adoptionService.id}:${releaseAdoption}`}<ServiceAdoptionDialog
      service={adoptionService}
      release={releaseAdoption}
      onClose={closeAdoptionDialog}
      onComplete={refreshRegistryQuietly}
    />{/key}{/if}

<div
  class="p-4 md:p-6 animate-in fade-in duration-300"
  data-testid="services-page"
>
  <PageHeader title={tr("ui.services.services")}>
    {#snippet actions()}
      <div
        class="flex flex-wrap items-center gap-3 animate-in slide-in-from-right-4 duration-300"
      >
        <!-- One view-switcher pattern across the app: a group of toggle
             buttons, like the server list and Apps. -->
        <div data-testid="services-mode-switch">
          <SegmentedControl
            items={[
              { value: "services", label: tr("ui.services.applications") },
              { value: "servers", label: tr("ui.services.placementBoard") },
            ]}
            value={activeTab}
            label={tr("ui.services.servicesMode")}
            onchange={(next) => (activeTab = next as "services" | "servers")}
          />
        </div>

        {#if activeTab === "services"}
          <Button
            variant="primary"
            testId="new-service-button"
            onclick={openNewService}
            disabled={loading || registryError !== null}
            ariaLabel={registryError
              ? tr("ui.services.applicationManagementIsUnavailableUntil")
              : tr("layout.addUseCases.entry")}
          >
            {tr("layout.addUseCases.entry")}
          </Button>
        {/if}
      </div>
    {/snippet}
  </PageHeader>

  {#if actionError}
    <ErrorCallout
      class="mb-4"
      testId="services-action-error"
      message={actionError}
    />
  {/if}

  {#if activeTab === "services"}
    <!-- Applications View -->
    {#if inventoryLoading}
      <LoadingNote
        class="mb-6"
        label={tr("ui.services.loadingCanonicalServiceInventory")}
        testid="inventory-services-loading"
      />
    {:else if inventoryError}
      <ErrorCallout
        class="mb-6"
        testId="inventory-services-error-panel"
        message={inventoryError}
      />
    {/if}

    {#if inventoryRejected.length > 0}
      <section
        class="mb-6 flex gap-3 rounded-lg border border-warning/30 bg-warning/10 p-4 text-sm text-warning"
        role="status"
        data-testid="inventory-rows-rejected"
      >
        <AlertTriangle class="mt-0.5 h-5 w-5 shrink-0" />
        <div class="min-w-0">
          <p class="font-semibold">
            {trn("ui.serviceCards.rowsRejected", inventoryRejected.length)}
          </p>
          <p class="mt-1 text-warning/80">
            {tr("ui.serviceCards.rowsRejectedHint")}
          </p>
        </div>
      </section>
    {/if}

    {#if !loading && !registryError}
      {@const s = applicationSummary()}
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
          <div class="text-xs text-muted-foreground">
            {tr("ui.services.all")}
          </div>
        </button>
        <button
          type="button"
          onclick={() => (statusFilter = "running")}
          class="rounded-lg border bg-card/70 p-3 text-center transition-colors cursor-pointer {statusFilter ===
          'running'
            ? 'border-success bg-success/5'
            : 'border-border hover:border-border/80'}"
        >
          <div class="text-xl font-bold text-success">{s.running}</div>
          <div class="text-xs text-muted-foreground">
            {tr("ui.services.running")}
          </div>
        </button>
        <button
          type="button"
          onclick={() => (statusFilter = "pending")}
          class="rounded-lg border bg-card/70 p-3 text-center transition-colors cursor-pointer {statusFilter ===
          'pending'
            ? 'border-warning bg-warning/5'
            : 'border-border hover:border-border/80'}"
        >
          <div class="text-xl font-bold text-warning">{s.pending}</div>
          <div class="text-xs text-muted-foreground">
            {tr("ui.services.pending")}
          </div>
        </button>
        <button
          type="button"
          onclick={() => (statusFilter = "observed")}
          class="rounded-lg border bg-card/70 p-3 text-center transition-colors cursor-pointer {statusFilter ===
          'observed'
            ? 'border-info bg-info/5'
            : 'border-border hover:border-border/80'}"
        >
          <div class="text-xl font-bold text-info">{s.observed}</div>
          <div class="text-xs text-muted-foreground">
            {tr("ui.services.observed")}
          </div>
        </button>
        <button
          type="button"
          onclick={() => (statusFilter = "error")}
          class="rounded-lg border bg-card/70 p-3 text-center transition-colors cursor-pointer {statusFilter ===
          'error'
            ? 'border-destructive bg-destructive/5'
            : 'border-border hover:border-border/80'}"
        >
          <div class="text-xl font-bold text-destructive">{s.error}</div>
          <div class="text-xs text-muted-foreground">
            {tr("ui.services.error")}
          </div>
        </button>
      </div>
    {/if}

    {#if !inventoryLoading && !inventoryError}
      <ServiceList
        groups={applicationGroups}
        title={tr("ui.services.applications")}
        countLabel={trn(
          "ui.services.applicationsShown",
          applicationSummary().total,
          {
            shown: applicationRows.length,
          },
        )}
        emptyTitle={serviceApplications.length === 0
          ? tr("ui.services.noApplicationsReported")
          : tr("ui.services.noApplicationsMatching", { status: statusFilter })}
        emptyBody={serviceApplications.length === 0
          ? tr("ui.services.applicationsAppearAfterStackkitsRuntime")
          : tr("ui.services.clearTheFilterToShow")}
        testId="runtime-service-list"
        cardTestId="runtime-service-card"
      >
        {#snippet children(item)}
          {@const application = applicationById.get(item.id)}
          {#if application}
            <!-- Actions live on the card's capability row; governed operations
                 (verify, move, adopt, delete) live in the detail sheet. The
                 inline strip keeps each component's last action receipt
                 visible (an application id is not a service id, so the jobs
                 are looked up per component). -->
            {#each application.components as component (component.id)}
              {@const serviceActionJob = tracker.jobs[component.id]}
              {#if serviceActionJob && (serviceActionJob.action !== "logs" || actionFailed(serviceActionJob))}
                <div
                  class="mt-1 rounded border border-border/70 bg-muted/25 p-2 text-xs"
                  data-testid="managed-service-action-status"
                  data-service-id={component.id}
                  role={actionFailed(serviceActionJob) ? "alert" : "status"}
                >
                  <p class="font-medium text-foreground">
                    {tr("ui.serviceCards.jobLine", {
                      name: component.name,
                      action: actionLabel(serviceActionJob.action),
                      state: label(serviceActionJob.state),
                    })}
                    {#if serviceActionJob.converging}{tr(
                        "ui.services.verifying",
                      )}{/if}
                  </p>
                  {#if serviceActionJob.detail}<p
                      class="mt-1 text-muted-foreground"
                    >
                      {serviceActionJob.detail}
                    </p>{/if}
                </div>
              {/if}
            {/each}
          {/if}
        {/snippet}
      </ServiceList>

      {#if systemServiceGroups.length > 0}
        <div class="mt-10">
          <ServiceList
            groups={systemServiceGroups}
            title={tr("ui.services.systemServices")}
            countLabel={tr(
              "ui.services.observedTechnicalComponentsWithoutApplication",
            )}
            emptyTitle={tr("ui.services.noSystemServices")}
            emptyBody={tr(
              "ui.services.unclassifiedRuntimeComponentsAppearHere",
            )}
            display="adaptive"
            testId="system-service-list"
            cardTestId="system-service-card"
          />
        </div>
      {/if}

      <TrademarkNotice class="mt-6" />
    {/if}

    {#if registryError}
      <div
        class="flex gap-3 rounded-lg border border-warning/30 bg-warning/10 p-4 text-sm text-warning animate-in fade-in"
        role="status"
        data-testid="registry-services-warning"
      >
        <AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-warning" />
        <div class="min-w-0">
          <p class="font-semibold">
            {tr("ui.services.applicationManagementIsTemporarilyUnavailable")}
          </p>
          <p class="mt-1 text-warning/80">
            {tr("ui.services.anyRuntimeInventoryShownAbove")}
          </p>
          <p class="mt-2 break-words font-mono text-xs text-warning/70">
            {registryError}
          </p>
          <Button
            variant="secondary"
            size="sm"
            class="mt-3"
            onclick={() => void loadRegistryManagement()}
          >
            {tr("ui.services.retryManagementData")}
          </Button>
        </div>
      </div>
    {/if}
  {:else}
    <PlacementBoard
      columns={placementColumns}
      {loading}
      {registryError}
      migrationAvailable={registry.migration_available === true}
      migrationUnavailableReason={registry.migration_unavailable_reason}
      {cockpitStale}
      {cockpitError}
      onretry={() => void loadRegistryManagement()}
      onmove={openMigration}
    />
  {/if}
</div>

{#if showMigrationConfirmModal && migrationSourceService && migrationTargetServer}
  <ServiceMoveDialog
    applicationName={applicationLabel(migrationSourceService)}
    sourceNodeName={migrationSourceService.server_name ?? ""}
    targets={moveTargetsFor(migrationSourceService)}
    initialTargetId={migrationTargetServer.id}
    {saving}
    oncancel={cancelMigration}
    onconfirm={confirmMigration}
  />
{/if}


{#if detailOpen}
  {@const detailRuntime = detailRow?.runtime}
  {@const detailName =
    detailRow?.name ||
    (detailRegistryService
      ? applicationLabel(detailRegistryService)
      : tr("ui.services.service"))}
  <ServiceDetailSheet
    open={detailOpen}
    onclose={closeServiceSheet}
    name={detailName}
    meta={detailRow?.meta ||
      (detailRegistryService ? serviceMeta(detailRegistryService) : undefined)}
    status={detailRow?.status ||
      (detailRegistryService
        ? serviceCardStatus(detailRegistryService)
        : "unknown")}
    statusLabel={detailRow?.statusLabel}
    locked={serviceIsLocked(detailRuntimeService?.mutation_lock)}
    lockedReason={serviceLockReason(detailRuntimeService?.mutation_lock)}
    applicationHref={detailApplication
      ? `/services/${encodeURIComponent(detailApplication.application_id)}`
      : undefined}
    applicationLabel={detailApplication
      ? `${detailApplication.display_name} · ${trn("ui.services.componentCount", detailApplication.components.length)}`
      : undefined}
    bind:tab={detailTab}
  >
    {#snippet overview()}
      <div class="flex flex-col gap-4" data-testid="service-sheet-overview">
        {#if detailRuntime}
          <div class="grid grid-cols-3 gap-2">
            <div class="rounded-lg bg-muted/30 px-2.5 py-2">
              <div
                class="text-[9px] uppercase tracking-wider text-muted-foreground"
              >
                {tr("ui.services.desired")}
              </div>
              <div class="mt-0.5 font-mono text-[12.5px] font-semibold">
                {stateLabel(detailRuntime.desired_state)}
              </div>
            </div>
            <div class="rounded-lg bg-muted/30 px-2.5 py-2">
              <div
                class="text-[9px] uppercase tracking-wider text-muted-foreground"
              >
                {tr("ui.services.observed")}
              </div>
              <div class="mt-0.5 font-mono text-[12.5px] font-semibold">
                {stateLabel(detailRuntime.observed_state)}
              </div>
            </div>
            <div class="rounded-lg bg-muted/30 px-2.5 py-2">
              <div
                class="text-[9px] uppercase tracking-wider text-muted-foreground"
              >
                {tr("ui.services.health")}
              </div>
              <div class="mt-0.5 font-mono text-[12.5px] font-semibold">
                {stateLabel(detailRuntime.health.state)}
              </div>
            </div>
          </div>
          <div
            class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground"
          >
            <span
              >{tr("ui.serviceSheet.ownership", {
                value: detailRuntime.management_state,
              })}</span
            >
            <span
              >{tr("ui.services.targetValue", {
                value:
                  detailRow?.targetLabel || detailRow?.runtimeTargetId || "",
              })}</span
            >
            <span
              >{tr("ui.services.freshnessValue", {
                value: detailRow?.freshnessLabel ?? "",
              })}</span
            >
            {#if detailRow?.sourceLabel}<span
                >{tr("ui.common.sourceValue", {
                  value: detailRow.sourceLabel,
                })}</span
              >{/if}
          </div>
          {#if typeof detailRuntime.access.url === "string" && detailRuntime.access.url}
            <div
              class="flex items-center gap-2 rounded-lg bg-muted/30 px-2.5 py-2"
            >
              <span class="font-mono text-xs flex-1 truncate"
                >{detailRuntime.access.url}</span
              >
              <Button
                variant="secondary"
                size="sm"
                onclick={() =>
                  openServiceUrl({ url: String(detailRuntime.access.url) })}
                >{tr("ui.services.open")}</Button
              >
            </div>
          {/if}
        {:else if detailRegistryService}
          <p class="text-sm text-muted-foreground">
            {tr("ui.services.noCanonicalRuntimeObservationYet")}
          </p>
          <div
            class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground"
          >
            <span
              >{tr("ui.services.statusValue", {
                value: detailRegistryService.status || tr("ui.common.unknown"),
              })}</span
            >
            {#if detailRegistryService.server_name}<span
                >{tr("ui.serviceSheet.node", {
                  node: detailRegistryService.server_name,
                })}</span
              >{/if}
          </div>
        {/if}
        {#if detailApplication && detailApplication.components.length > 0}
          <div>
            <div
              class="mb-2 text-[9px] uppercase tracking-wider text-muted-foreground"
            >
              {tr("ui.services.components")}
            </div>
            <div
              class="flex flex-col gap-1.5"
              data-testid="service-sheet-components"
            >
              {#each detailApplication.components as component (component.id)}
                {@const componentHealthy = ["healthy", "running"].includes(
                  (
                    component.health.state ||
                    component.observed_state ||
                    ""
                  ).toLowerCase(),
                )}
                <div
                  class="flex items-center gap-2.5 rounded-lg bg-muted/30 px-2.5 py-2"
                >
                  <span
                    class="h-2 w-2 rounded-full {componentHealthy
                      ? 'bg-success'
                      : 'bg-warning'}"
                  ></span>
                  <span class="flex-1 truncate font-mono text-[11.5px]">
                    {component.name || component.service_key}
                  </span>
                  <span class="font-mono text-[10px] text-muted-foreground">
                    {stateLabel(component.observed_state)} · {stateLabel(
                      component.health.state,
                    )}
                  </span>
                </div>
              {/each}
            </div>
          </div>
        {/if}
      </div>
    {/snippet}
    {#snippet activity()}
      <div class="flex flex-col gap-3" data-testid="service-sheet-activity">
        {#if detailJob}
          <div
            class="rounded-lg border border-border/70 bg-muted/25 p-3 text-xs"
            role={actionFailed(detailJob) ? "alert" : "status"}
          >
            <p class="font-medium text-foreground">
              {actionLabel(detailJob.action)}: {label(detailJob.state)}
              {#if detailJob.converging}{tr("ui.services.verifying")}{/if}
            </p>
            {#if detailJob.jobId}<p
                class="mt-1 font-mono text-muted-foreground"
              >
                {tr("ui.services.jobId", { id: detailJob.jobId })}
              </p>{/if}
            {#if detailJob.detail}<p class="mt-1 text-muted-foreground">
                {detailJob.detail}
              </p>{/if}
          </div>
        {:else}
          <p class="text-sm text-muted-foreground">
            {tr("ui.services.noGovernedActionHasRun")}
          </p>
        {/if}
        {#if detailRow?.workflowLabel}
          <p class="text-xs text-muted-foreground">
            {tr("ui.services.operationValue", {
              value: detailRow.workflowLabel,
            })}
          </p>
        {/if}
      </div>
    {/snippet}
    {#snippet logs()}
      <div class="flex flex-col gap-3" data-testid="service-sheet-logs">
        {#if detailRuntime}
          <div class="flex items-center gap-2">
            <Button
              variant="secondary"
              size="sm"
              disabled={isActionActive(detailJob)}
              onclick={() => void requestServiceAction(detailRuntime, "logs")}
            >
              {detailLogState
                ? tr("ui.services.refreshLogs")
                : tr("ui.services.fetchLogs")}
            </Button>
            {#if detailLogState?.loading}<Loader2
                class="h-3.5 w-3.5 animate-spin text-muted-foreground"
              />{/if}
          </div>
        {/if}
        {#if detailLogState?.error}
          <ErrorCallout class="p-2 text-xs" message={detailLogState.error} />
        {/if}
        {#if detailLogState && detailLogState.entries.length > 0}
          <ol class="max-h-72 space-y-1 overflow-auto font-mono text-[11px]">
            {#each detailLogState.entries as entry (`${entry.timestamp}-${entry.message}`)}
              <li class="rounded bg-background/60 p-1.5">
                <time class="text-muted-foreground" datetime={entry.timestamp}
                  >{entry.timestamp}</time
                >
                <pre
                  class="whitespace-pre-wrap break-words text-foreground">{entry.message}</pre>
              </li>
            {/each}
          </ol>
          {#if detailLogState.nextCursor && detailRuntime}
            <Button
              variant="secondary"
              size="sm"
              disabled={isActionActive(detailJob)}
              onclick={() => tracker.loadMoreLogs(detailRuntime)}
              >{tr("ui.services.loadOlderLogs")}</Button
            >
          {/if}
        {:else if detailLogState && !detailLogState.loading}
          <p class="text-sm text-muted-foreground">
            {tr("ui.services.theLastLogPageWas")}
          </p>
        {/if}
      </div>
    {/snippet}
    {#snippet operations()}
      <div class="flex flex-col gap-2" data-testid="service-sheet-operations">
        {#if detailRegistryService?.status === "pending_verification"}
          <Button
            variant="primary"
            size="sm"
            onclick={() => handleVerifyService(detailRegistryService.id!)}
            disabled={saving}
          >
            <Check class="h-3.5 w-3.5" />
            {tr("ui.services.verifyFinish")}
          </Button>
        {/if}
        {#if detailRegistryService && canMoveApplication(detailRegistryService)}
          <Button
            variant="secondary"
            size="sm"
            onclick={() => openMigration(detailRegistryService)}
            disabled={saving ||
              moveTargetsFor(detailRegistryService).length === 0}
          >
            <ArrowRight class="h-3.5 w-3.5" />
            {tr("ui.services.moveToAnotherNode")}
          </Button>
        {/if}
        {#if detailRuntime?.management_state === "observed"}
          <Button
            variant="secondary"
            size="sm"
            onclick={() => openAdoptionDialog(detailRuntime, false)}
            disabled={!detailRuntime.adoption?.preview_available}
            testId="stack-it-service"
          >
            {tr("ui.services.stackIt")}
          </Button>
          {#if !detailRuntime.adoption?.preview_available}<p
              class="text-sm text-muted-foreground"
            >
              {adoptionUnavailableReason(detailRuntime.adoption)}
            </p>{/if}
        {/if}
        {#if detailRuntime?.adoption?.release_available}<Button
            variant="secondary"
            onclick={() => openAdoptionDialog(detailRuntime, true)}
            >{tr("ui.services.releaseBinding")}</Button
          >{/if}
        {#if detailRegistryService?.status === "archived"}
          <Button
            variant="destructive"
            size="sm"
            onclick={() => handleRemoveOld(detailRegistryService.id!)}
            disabled={saving}
          >
            <Trash2 class="h-3.5 w-3.5" />
            {tr("ui.services.removeOldCopy")}
          </Button>
        {/if}
        {#if !detailRegistryService && !detailRuntime}
          <p class="text-sm text-muted-foreground">
            {tr("ui.services.noOperationsAvailable")}
          </p>
        {/if}
        <p class="mt-1 text-[10.5px] text-muted-foreground">
          {tr("ui.services.mutationsRequireOwnerApprovalA")}
        </p>
      </div>
    {/snippet}
  </ServiceDetailSheet>
{/if}
