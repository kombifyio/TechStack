<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import {
    getMonitoringCockpit,
    type MonitoringCockpitPayload,
  } from "$lib/api/monitoring";
  import {
    listCanonicalServers,
    type CanonicalServer,
  } from "$lib/api/registry";
  import {
    listCanonicalServices,
    type CanonicalService,
  } from "$lib/api/services";
  import type {
    StackOperationServer,
    StackOperationService,
  } from "$lib/api/stacks";
  import {
    openServiceUrl,
    serviceCardMetrics,
    serviceCardStatus,
    serviceCardStatusMessage,
  } from "$lib/service-card-adapter";
  import { mergeCockpitSnapshot } from "$lib/monitoring/cockpit-snapshot";
  import {
    serverCardKit,
    serverCardMeta,
    serverCardMetrics,
    serverCardStatus,
    serverDomains,
    serverPrimaryAddress,
  } from "$lib/server-card-adapter";
  import ServerList, {
    type ServerListItem,
  } from "$lib/components/inventory/ServerList.svelte";
  import ServiceList, {
    type ServiceListGroup,
    type ServiceListItem,
  } from "$lib/components/inventory/ServiceList.svelte";
  import {
    ServiceCardCompact,
    type ServicePlacement,
  } from "$lib/components/open-core";
  import {
    Activity,
    AlertTriangle,
    BriefcaseBusiness,
    Cpu,
    HardDrive,
    HeartPulse,
    RefreshCw,
    Server,
    Waypoints,
  } from "@lucide/svelte";

  let cockpit = $state<MonitoringCockpitPayload | null>(null);
  let selectedTechstackId = $state("");
  let focus = $state("");
  let loading = $state(true);
  let error = $state<string | null>(null);
  let cockpitEvidenceStale = $state(false);
  // Canonical, secret-free server inventory (ADR-0036 observation surface).
  // It lives here rather than on the dashboard: the dashboard shows the
  // servers you act on, this is the authoritative record of what exists.
  let inventoryServers = $state<CanonicalServer[]>([]);
  let canonicalServices = $state<CanonicalService[]>([]);
  let inventoryUnavailable = $state(false);
  let servicesUnavailable = $state(false);
  let refreshInterval: ReturnType<typeof setInterval> | null = null;

  const REFRESH_INTERVAL_MS = 10000;
  let servers = $derived<StackOperationServer[]>(cockpit?.servers ?? []);
  let telemetryServices = $derived<StackOperationService[]>(
    cockpit?.services ?? [],
  );
  let jobs = $derived(cockpit?.jobs ?? []);
  let activeJobs = $derived(
    jobs.filter((job) => job.state === "running" || job.state === "waiting"),
  );
  let queuedJobs = $derived(jobs.filter((job) => job.state === "pending"));
  let connectedAgentCount = $derived(
    cockpit?.readiness?.connected_servers ?? 0,
  );
  function inventoryStatus(server: CanonicalServer) {
    const health = server.health.state.toLowerCase();
    const connection = server.connection.state.toLowerCase();
    if (health === "healthy" && connection === "connected") return "healthy";
    if (["offline", "stale", "revoked"].includes(connection)) return "offline";
    if (["degraded", "unhealthy", "error", "failed"].includes(health)) {
      return "degraded";
    }
    if (["pending", "connecting", "provisioning"].includes(connection)) {
      return "pending";
    }
    return "unknown";
  }

  function telemetryForInventory(
    inventory: CanonicalServer,
  ): StackOperationServer | undefined {
    return servers.find(
      (server) =>
        server.id === inventory.id || server.agent_id === inventory.id,
    );
  }

  function inventoryServerItem(server: CanonicalServer): ServerListItem {
    const telemetry = telemetryForInventory(server);
    const domains = telemetry ? serverDomains(telemetry) : [];
    const environment = label(server.environment_class || "unknown");
    const offering = label(server.offering || "unknown offering");
    return {
      id: server.id,
      hostname: server.name,
      meta: [environment, offering, server.provider_id || server.provider.ref]
        .filter(Boolean)
        .join(" · "),
      status: inventoryStatus(server),
      statusLabel: label(server.health.state),
      metrics: telemetry ? serverCardMetrics(telemetry) : [],
      address: telemetry ? serverPrimaryAddress(telemetry) : "not reported",
      domain: domains[0],
      domainExtraCount: Math.max(0, domains.length - 1),
      kit: telemetry ? serverCardKit(telemetry) : undefined,
      facts: [
        {
          label: "Connection",
          value: label(server.connection.state),
          testId: "inventory-server-connection-state",
        },
        {
          label: "Lifecycle",
          value: label(server.lifecycle.state),
          testId: "inventory-server-lifecycle-state",
        },
        {
          label: "Freshness",
          value: label(server.target_evidence?.freshness.state),
        },
        {
          label: "Source",
          value: telemetry ? "Inventory + telemetry" : "Inventory",
        },
      ],
    };
  }

  function telemetryServerItem(server: StackOperationServer): ServerListItem {
    const domains = serverDomains(server);
    return {
      id: server.id || server.agent_id || server.hostname,
      hostname: server.hostname || "Telemetry server",
      meta: serverCardMeta(server),
      status: serverCardStatus(server),
      metrics: serverCardMetrics(server),
      address: serverPrimaryAddress(server),
      domain: domains[0],
      domainExtraCount: Math.max(0, domains.length - 1),
      kit: serverCardKit(server),
      facts: [
        {
          label: "Connection",
          value: "Telemetry only",
          testId: "inventory-server-connection-state",
        },
        { label: "Source", value: "Telemetry only" },
      ],
    };
  }

  const monitoringServerItems = $derived.by<ServerListItem[]>(() => {
    const items = inventoryServers.map(inventoryServerItem);
    const knownTelemetryIds = new Set(
      inventoryServers.flatMap((inventory) => [inventory.id]),
    );
    // Cockpit telemetry may supplement canonical identities, but it may only
    // stand in for a missing identity authority when that authority is
    // explicitly unavailable. A successful empty inventory remains empty.
    for (const server of inventoryUnavailable ? servers : []) {
      if (
        knownTelemetryIds.has(server.id) ||
        (server.agent_id && knownTelemetryIds.has(server.agent_id))
      ) {
        continue;
      }
      items.push(telemetryServerItem(server));
    }
    return items.sort(
      (left, right) =>
        Number(right.status !== "healthy") -
          Number(left.status !== "healthy") ||
        left.hostname.localeCompare(right.hostname),
    );
  });

  interface MonitoringServiceRow extends ServiceListItem {
    service: CanonicalService;
    telemetry?: StackOperationService;
  }

  const monitoringServiceRows = $derived.by<MonitoringServiceRow[]>(() =>
    canonicalServices.map((service) => {
      const telemetry = telemetryServices.find(
        (candidate) => candidate.id === service.id,
      );
      const state = service.health.state || service.observed_state || "unknown";
      const server = servers.find(
        (candidate) =>
          candidate.id === service.server_id ||
          candidate.agent_id === service.server_id,
      );
      const inventoryServer = inventoryServers.find(
        (candidate) => candidate.id === service.server_id,
      );
      const runtimeTargetId =
        service.target_kind === "managed_workload"
          ? service.placement.managed_target_ref || "managed-workload:unknown"
          : service.server_id || "runtime-target:unknown";
      const attention = [
        "unknown",
        "offline",
        "error",
        "failed",
        "unhealthy",
        "degraded",
      ].includes(state.toLowerCase());
      const placement: ServicePlacement =
        service.target_kind === "managed_workload"
          ? "serverless"
          : inventoryServer?.environment_class === "cloud"
            ? "cloud"
            : inventoryServer?.environment_class === "local"
              ? "local"
              : "unknown";
      const cardSource = {
        name: service.name,
        type: service.service_key,
        status: state,
        management_state: service.management_state,
        server_id: service.server_id,
        server_name: server?.hostname,
        provider: service.placement.provider_id,
        placement,
      };
      const accessUrl =
        typeof service.access.url === "string" ? service.access.url : undefined;
      return {
        id: service.id,
        name: service.name || "Service",
        meta: `${service.service_key || "service"} · ${service.target_kind === "managed_workload" ? "Managed workload" : "Server runtime"}`,
        placement,
        status: serviceCardStatus(cardSource),
        statusLabel: label(state),
        statusMessage: serviceCardStatusMessage(cardSource),
        metrics: serviceCardMetrics(cardSource),
        runtimeTargetId,
        targetKind: service.target_kind,
        targetLabel:
          service.target_kind === "managed_workload"
            ? service.placement.provider_id || "Managed provider target"
            : server?.hostname || service.server_id || "Placement unknown",
        managementLabel:
          service.management_state === "managed" ? "Configured" : "Observed",
        freshnessLabel: label(service.placement.freshness.state),
        sourceLabel: telemetry ? "Canonical + telemetry" : service.source,
        details: `Desired ${service.desired_state || "unknown"} · Observed ${service.observed_state || "unknown"}`,
        attention,
        onOpen: accessUrl
          ? () => openServiceUrl({ url: accessUrl })
          : undefined,
        service,
        telemetry,
      };
    }),
  );

  const monitoringServiceGroups = $derived.by<ServiceListGroup[]>(() => {
    const groups = new Map<string, ServiceListGroup>();
    for (const service of monitoringServiceRows) {
      const existing = groups.get(service.runtimeTargetId);
      if (existing) {
        existing.items.push(service);
        continue;
      }
      const server = servers.find(
        (candidate) =>
          candidate.id === service.runtimeTargetId ||
          candidate.agent_id === service.runtimeTargetId,
      );
      groups.set(service.runtimeTargetId, {
        id: service.runtimeTargetId,
        name:
          server?.hostname || service.targetLabel || service.runtimeTargetId,
        meta:
          service.targetKind === "managed_workload"
            ? "Managed provider target"
            : server
              ? serverCardMeta(server)
              : "Runtime target not linked",
        targetKind: service.targetKind,
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

  function statusBadge(status?: string): string {
    switch (status) {
      case "healthy":
      case "ok":
      case "running":
      case "completed":
        return "bg-green-950/70 text-green-200 border border-green-900";
      case "stale":
      case "pending":
      case "queued":
      case "waiting":
        return "bg-yellow-950/70 text-yellow-200 border border-yellow-900";
      case "offline":
      case "error":
      case "failed":
        return "bg-red-950/70 text-red-200 border border-red-900";
      case "deploying":
      case "migrating":
        return "bg-amber-950/70 text-amber-200 border border-amber-900";
      default:
        return "bg-muted text-muted-foreground border border-border";
    }
  }

  function label(status?: string): string {
    return (status || "unknown").replace(/_/g, " ");
  }

  // Identity/state reads stay authoritative even when the telemetry cockpit is
  // available. Each unavailable authority is retained as a typed UI state and
  // never collapsed into a contradictory zero count.
  async function refreshInventory() {
    const [serverResult, serviceResult] = await Promise.allSettled([
      listCanonicalServers(selectedTechstackId || undefined),
      listCanonicalServices(selectedTechstackId || undefined),
    ]);
    if (serverResult.status === "fulfilled") {
      inventoryServers = serverResult.value;
      inventoryUnavailable = false;
    } else {
      inventoryUnavailable = true;
    }
    if (serviceResult.status === "fulfilled") {
      canonicalServices = serviceResult.value;
      servicesUnavailable = false;
    } else {
      servicesUnavailable = true;
    }
  }

  function resumeLabel(value?: string): string {
    if (!value) return "";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "";
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "short",
      timeStyle: "medium",
    }).format(date);
  }

  async function refresh(techstackId = selectedTechstackId) {
    loading = true;
    error = null;
    try {
      const nextCockpit = await getMonitoringCockpit(techstackId || undefined);
      const activeServerIDs = new Set(
        inventoryServers
          .filter(
            (server) =>
              !nextCockpit.techstack_id ||
              server.techstack_id === nextCockpit.techstack_id,
          )
          .map((server) => server.id),
      );
      const merged = mergeCockpitSnapshot(
        cockpit,
        nextCockpit,
        activeServerIDs,
      );
      cockpit = merged.snapshot;
      selectedTechstackId = cockpit.techstack_id || techstackId || "";
      cockpitEvidenceStale = merged.retainedTelemetry;
    } catch (err) {
      // Keep the last verified snapshot visible. Stale presentation makes the
      // uncertainty explicit without fabricating a zero inventory.
      cockpitEvidenceStale = cockpit !== null;
      error =
        err instanceof Error
          ? err.message
          : "Monitoring cockpit could not be loaded.";
    }
    // Inventory must use the stack that the cockpit actually selected. Running
    // both reads concurrently races the initially empty selector against the
    // cockpit response and briefly (or permanently on a slow edge response)
    // renders an unscoped inventory as a contradictory zero.
    await refreshInventory();
    loading = false;
  }

  function onStackChange(event: Event) {
    const value = (event.target as HTMLSelectElement).value;
    selectedTechstackId = value;
    void refresh(value);
  }

  onMount(() => {
    const params = new URLSearchParams(window.location.search);
    selectedTechstackId = params.get("techstack_id") || "";
    focus = params.get("focus") || "";
    void refresh(selectedTechstackId);
    refreshInterval = setInterval(() => {
      void refresh(selectedTechstackId);
    }, REFRESH_INTERVAL_MS);
  });

  onDestroy(() => {
    if (refreshInterval) clearInterval(refreshInterval);
  });
</script>

<div class="p-4 md:p-6 max-w-7xl mx-auto" data-testid="monitoring-page">
  <div
    class="mb-4 flex flex-col gap-3 md:flex-row md:items-center md:justify-end"
  >
    <div class="flex flex-wrap items-center gap-2">
      <select
        class="rounded-lg border border-border bg-card px-3 py-2 text-sm text-foreground"
        value={selectedTechstackId}
        onchange={onStackChange}
        aria-label="Select Homelab stack"
      >
        {#each cockpit?.stacks ?? [] as item (item.id)}
          <option value={item.id}>{item.name}</option>
        {/each}
      </select>
      <button
        class="btn btn-secondary"
        onclick={() => refresh()}
        disabled={loading}
      >
        <RefreshCw class="h-4 w-4" />
        {loading ? "Refreshing..." : "Refresh"}
      </button>
    </div>
  </div>

  {#if error}
    <div
      class="mb-6 rounded-lg border border-destructive/40 bg-destructive/10 p-4 text-foreground"
    >
      {error}
    </div>
  {/if}

  <!-- Legacy duplicate inventory projection removed from the rendered information architecture.
  <section
    class="mb-6 rounded-lg border border-border bg-card p-5"
    data-testid="inventory-server-list"
    aria-label="Canonical server inventory"
  >
    <div class="mb-4 flex items-center justify-between gap-3">
      <div class="flex items-center gap-2">
        <HardDrive class="h-5 w-5 text-primary" />
        <h2 class="text-lg font-semibold text-foreground">Server inventory</h2>
      </div>
      <span class="text-sm text-muted-foreground">
        {inventoryServers.length} server{inventoryServers.length === 1
          ? ""
          : "s"}
      </span>
    </div>
    {#if inventoryUnavailable}
      <p
        class="rounded-lg border border-dashed border-border bg-background/40 p-4 text-sm text-muted-foreground"
        data-testid="inventory-unavailable"
      >
        The canonical inventory is not available for this account right now.
        Server state below still reflects live telemetry.
      </p>
    {:else if inventoryServers.length === 0}
      <p
        class="rounded-lg border border-dashed border-border bg-background/40 p-4 text-sm text-muted-foreground"
      >
        No servers recorded yet.
      </p>
    {:else}
      <div class="grid gap-3 lg:grid-cols-2">
        {#each inventoryServers as server (server.id)}
          <article
            class="rounded-lg border border-border bg-background/40 p-4"
            data-testid="inventory-server-card"
            data-server-id={server.id}
          >
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <p class="truncate font-medium text-foreground">
                  {server.name}
                </p>
                <p class="mt-1 truncate text-xs text-muted-foreground">
                  {server.provider || "provider not reported"} · {inventoryServerPlatform(
                    server,
                  )}
                </p>
              </div>
              <div class="flex shrink-0 flex-col items-end gap-1">
                <span
                  class={`rounded px-2 py-1 text-xs font-semibold ${statusBadge(server.health.state)}`}
                >
                  {label(server.health.state)}
                </span>
                <span
                  class={`rounded px-2 py-1 text-xs font-semibold ${statusBadge(server.connection.state)}`}
                  data-testid="inventory-server-connection-state"
                  aria-label="Server connection"
                >
                  {label(server.connection.state)}
                </span>
                <span
                  class={`rounded px-2 py-1 text-xs font-semibold ${statusBadge(server.lifecycle.state)}`}
                  data-testid="inventory-server-lifecycle-state"
                  aria-label="Server lifecycle"
                >
                  {label(server.lifecycle.state)}
                </span>
              </div>
            </div>
            <dl class="mt-3 grid gap-2 text-xs sm:grid-cols-2">
              <div class="min-w-0 rounded-md bg-muted/40 p-2">
                <dt class="text-muted-foreground">Main address</dt>
                <dd class="mt-1 truncate font-mono text-foreground">
                  {inventoryServerAddress(server)}
                </dd>
              </div>
              <div class="min-w-0 rounded-md bg-muted/40 p-2">
                <dt class="text-muted-foreground">Main domain</dt>
                <dd class="mt-1 truncate text-foreground">
                  {server.addresses.domains[0] || "not reported"}
                </dd>
              </div>
              <div class="min-w-0 rounded-md bg-muted/40 p-2 sm:col-span-2">
                <dt class="text-muted-foreground">StackKit</dt>
                <dd class="mt-1 truncate text-foreground">
                  {inventoryServerStackKit(server)}
                </dd>
              </div>
            </dl>
          </article>
        {/each}
      </div>
    {/if}
  </section>
  -->

  {#if loading && !cockpit}
    <div class="grid gap-4 md:grid-cols-4">
      {#each Array(4) as _, i (i)}
        <div
          class="h-32 animate-pulse rounded-lg border border-border bg-card/40"
        ></div>
      {/each}
    </div>
  {:else if !cockpit || cockpit.stacks.length === 0}
    <div class="rounded-lg border border-border bg-card p-10 text-center">
      <p class="text-lg font-semibold text-foreground">
        No Homelab stack found
      </p>
      <p class="mt-2 text-sm text-muted-foreground">
        Create a stack first; live telemetry appears here once workers report
        data.
      </p>
    </div>
  {:else}
    <div class="mb-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <section
        class="rounded-lg border border-border bg-card p-4"
        data-testid="monitoring-metric-running-services"
      >
        <div class="mb-3 flex items-center justify-between">
          <span class="text-sm text-muted-foreground">Running Services</span>
          <Waypoints class="h-4 w-4 text-info" />
        </div>
        <div class="text-2xl font-semibold text-foreground">
          {cockpit.kpis.running_services}
        </div>
        <p class="mt-1 text-xs text-muted-foreground">
          {servicesUnavailable
            ? "Canonical service state unavailable"
            : `${canonicalServices.length} canonical services`}
        </p>
      </section>
      <section
        class="rounded-lg border border-border bg-card p-4"
        data-testid="monitoring-metric-active-jobs"
      >
        <div class="mb-3 flex items-center justify-between">
          <span class="text-sm text-muted-foreground">Active Jobs</span>
          <Activity class="h-4 w-4 text-primary" />
        </div>
        <div class="text-2xl font-semibold text-foreground">
          {activeJobs.length}
        </div>
        <p class="mt-1 text-xs text-muted-foreground">
          Current rollout or migration jobs
        </p>
      </section>
      <section
        class="rounded-lg border border-border bg-card p-4"
        data-testid="monitoring-metric-queued-jobs"
      >
        <div class="mb-3 flex items-center justify-between">
          <span class="text-sm text-muted-foreground">Queued Jobs</span>
          <BriefcaseBusiness class="h-4 w-4 text-warning" />
        </div>
        <div class="text-2xl font-semibold text-foreground">
          {queuedJobs.length}
        </div>
        <p class="mt-1 text-xs text-muted-foreground">Pending backend work</p>
      </section>
      <section
        class="rounded-lg border p-4 {cockpitEvidenceStale
          ? 'border-warning/40 bg-warning/10'
          : 'border-border bg-card'}"
        data-testid="monitoring-metric-connected-agents"
        aria-live="polite"
      >
        <div class="mb-3 flex items-center justify-between">
          <span class="text-sm text-muted-foreground">Connected Agents</span>
          {#if cockpitEvidenceStale}
            <AlertTriangle class="h-4 w-4 text-warning" />
          {:else}
            <Server class="h-4 w-4 text-success" />
          {/if}
        </div>
        <div
          class="text-2xl font-semibold text-foreground"
          data-testid="monitoring-connected-agent-count"
        >
          {connectedAgentCount}
        </div>
        {#if cockpitEvidenceStale}
          <p
            class="mt-1 text-xs text-warning"
            data-testid="monitoring-connected-agents-stale"
          >
            Last verified count retained; current connection evidence
            unavailable.
          </p>
        {:else}
          <p class="mt-1 text-xs text-muted-foreground">
            {cockpit.kpis.healthy_servers} healthy
          </p>
        {/if}
      </section>
      <section
        class="rounded-lg border border-border bg-card p-4"
        data-testid="monitoring-metric-otlp-status"
      >
        <div class="mb-3 flex items-center justify-between">
          <span class="text-sm text-muted-foreground">OTLP Status</span>
          <HeartPulse class="h-4 w-4 text-info" />
        </div>
        <div class="text-2xl font-semibold text-foreground">
          {label(cockpit.monitoring.otlpStatus || cockpit.monitoring.status)}
        </div>
        <p class="mt-1 text-xs text-muted-foreground">
          {cockpit.monitoring.collectorMode} collector
        </p>
      </section>
    </div>

    <!-- Legacy duplicate server/OTLP projection removed from the rendered information architecture.
    <div class="mb-6 grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
      <section
        class="rounded-lg border border-border bg-card p-5"
        data-testid="monitoring-section-servers"
      >
        <div class="mb-4 flex items-center justify-between gap-3">
          <div>
            <h2 class="text-xl font-semibold text-foreground">Server Health</h2>
            <p class="text-sm text-muted-foreground">
              Multi-server Homelab telemetry.
            </p>
          </div>
          <span
            class={`rounded px-2 py-1 text-xs font-semibold ${statusBadge(cockpit.monitoring.status)}`}
          >
            {label(cockpit.monitoring.status)}
          </span>
        </div>
        {#if servers.length === 0}
          <p
            class="rounded-lg border border-dashed border-border bg-background/40 p-4 text-sm text-muted-foreground"
          >
            No server telemetry is available yet.
          </p>
        {:else}
          <div class="grid gap-3 xl:grid-cols-2">
            {#each servers as server (server.id)}
              <article
                class="rounded-lg border border-border bg-background/40 p-4"
              >
                <div class="flex items-start justify-between gap-3">
                  <div class="min-w-0">
                    <h3 class="truncate font-semibold text-foreground">
                      {server.hostname}
                    </h3>
                    <p class="mt-1 truncate text-xs text-muted-foreground">
                      {server.role} · {server.source || "worker"} · {server.ip ||
                        "no ip"}
                    </p>
                  </div>
                  <span
                    class={`rounded px-2 py-1 text-xs font-semibold ${statusBadge(server.health.state)}`}
                  >
                    {label(server.health.state)}
                  </span>
                </div>
                <div class="mt-4 grid gap-3 sm:grid-cols-3">
                  <div>
                    <div
                      class="mb-1 flex items-center gap-1 text-xs text-muted-foreground"
                    >
                      <Cpu class="h-3.5 w-3.5" /> CPU
                    </div>
                    <div class="h-1.5 overflow-hidden rounded-full bg-muted">
                      <div
                        class="h-full rounded-full bg-primary"
                        style="width: {percent(server.health.cpu_percent)}%"
                      ></div>
                    </div>
                    <p class="mt-1 text-xs text-foreground">
                      {metric(server.health.cpu_percent)}
                    </p>
                  </div>
                  <div>
                    <div
                      class="mb-1 flex items-center gap-1 text-xs text-muted-foreground"
                    >
                      <HeartPulse class="h-3.5 w-3.5" /> RAM
                    </div>
                    <div class="h-1.5 overflow-hidden rounded-full bg-muted">
                      <div
                        class="h-full rounded-full bg-info"
                        style="width: {percent(server.health.memory_percent)}%"
                      ></div>
                    </div>
                    <p class="mt-1 text-xs text-foreground">
                      {metric(server.health.memory_percent)}
                    </p>
                  </div>
                  <div>
                    <div
                      class="mb-1 flex items-center gap-1 text-xs text-muted-foreground"
                    >
                      <HardDrive class="h-3.5 w-3.5" /> Disk
                    </div>
                    <div class="h-1.5 overflow-hidden rounded-full bg-muted">
                      <div
                        class="h-full rounded-full bg-warning"
                        style="width: {percent(server.health.disk_percent)}%"
                      ></div>
                    </div>
                    <p class="mt-1 text-xs text-foreground">
                      {metric(server.health.disk_percent)}
                    </p>
                  </div>
                </div>
              </article>
            {/each}
          </div>
        {/if}
      </section>

      <aside class="space-y-4">
        <section
          class="rounded-lg border border-border bg-card p-5"
          data-testid="monitoring-section-alerts"
        >
          <div class="mb-4 flex items-center gap-2">
            <AlertTriangle class="h-5 w-5 text-warning" />
            <h2 class="text-lg font-semibold text-foreground">Alerts</h2>
          </div>
          {#if cockpit.alerts.length === 0}
            <p class="text-sm text-muted-foreground">
              No active stack-scoped alerts.
            </p>
          {:else}
            <div class="space-y-2">
              {#each cockpit.alerts as alert (alert.name)}
                <div
                  class="rounded-lg border border-warning/30 bg-warning/10 p-3"
                >
                  <div class="flex items-center justify-between gap-2">
                    <p class="font-medium text-foreground">{alert.name}</p>
                    <span
                      class="rounded bg-warning/20 px-2 py-1 text-xs text-warning"
                      >{alert.severity}</span
                    >
                  </div>
                  <p class="mt-1 text-sm text-muted-foreground">
                    {alert.message}
                  </p>
                </div>
              {/each}
            </div>
          {/if}
        </section>

        <section
          class="rounded-lg border border-border bg-card p-5"
          data-testid="monitoring-section-otlp-baseline"
        >
          <h2 class="text-lg font-semibold text-foreground">OTLP Baseline</h2>
          <dl class="mt-4 space-y-3 text-sm">
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Query backend</dt>
              <dd
                class="text-right font-medium text-foreground"
                data-testid="monitoring-query-backend"
              >
                {cockpit.monitoring.queryBackend}
                {cockpit.monitoring.queryBackendStatus ||
                  cockpit.monitoring.status}
              </dd>
            </div>
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Ingest</dt>
              <dd
                class="text-right font-medium text-foreground"
                data-testid="monitoring-ingest-status"
              >
                {cockpit.monitoring.ingestBackend}
                {cockpit.monitoring.ingestStatus || cockpit.monitoring.status}
              </dd>
            </div>
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">OTLP lane</dt>
              <dd
                class="text-right font-medium text-foreground"
                data-testid="monitoring-otlp-status"
              >
                {cockpit.monitoring.otlpStatus || cockpit.monitoring.status}
              </dd>
            </div>
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Alert rules</dt>
              <dd
                class="text-right font-medium text-foreground"
                data-testid="monitoring-alert-rules"
              >
                {cockpit.monitoring.alertRuleCount ?? 0} rules
              </dd>
            </div>
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Series</dt>
              <dd class="text-right font-medium text-foreground">
                {cockpit.monitoring.seriesCount ?? "unknown"}
              </dd>
            </div>
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Instant query</dt>
              <dd
                class="text-right font-medium text-foreground"
                data-testid="monitoring-query-proof"
              >
                {cockpit.monitoring.queryProof || "vector:pending"}
              </dd>
            </div>
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Range query</dt>
              <dd
                class="text-right font-medium text-foreground"
                data-testid="monitoring-range-proof"
              >
                {cockpit.monitoring.rangeProof || "matrix:pending"}
              </dd>
            </div>
          </dl>
        </section>
      </aside>
    </div>
    -->

    <section
      class="mb-6 rounded-lg border border-border bg-card p-5"
      data-testid="monitoring-section-alerts"
    >
      <div class="mb-4 flex items-center gap-2">
        <AlertTriangle class="h-5 w-5 text-warning" />
        <h2 class="text-lg font-semibold text-foreground">Alerts</h2>
      </div>
      {#if cockpit.alerts.length === 0}
        <p class="text-sm text-muted-foreground">
          No active stack-scoped alerts.
        </p>
      {:else}
        <div class="grid gap-2 md:grid-cols-2">
          {#each cockpit.alerts as alert (alert.name)}
            <article
              class="rounded-lg border border-warning/30 bg-warning/10 p-3"
            >
              <div class="flex items-center justify-between gap-2">
                <p class="font-medium text-foreground">{alert.name}</p>
                <span
                  class="rounded bg-warning/20 px-2 py-1 text-xs text-warning"
                  >{alert.severity}</span
                >
              </div>
              <p class="mt-1 text-sm text-muted-foreground">{alert.message}</p>
            </article>
          {/each}
        </div>
      {/if}
    </section>

    <!-- Server deviations are sorted into the canonical server list instead of rendered twice.
      <section
        class="mb-6 rounded-lg border border-warning/35 bg-warning/5 p-5"
        data-testid="monitoring-section-deviations"
      >
        <div class="mb-4 flex items-center gap-2">
          <AlertTriangle class="h-5 w-5 text-warning" />
          <div>
            <h2 class="text-lg font-semibold text-foreground">Server deviations</h2>
            <p class="text-sm text-muted-foreground">These targets need attention before the full telemetry list.</p>
          </div>
        </div>
        <div class="grid gap-3 md:grid-cols-2">
          {#each serverDeviationItems as server (server.id)}
            <article class="rounded-lg border border-warning/30 bg-card/80 p-4" data-testid="monitoring-server-deviation" data-server-id={server.id}>
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <h3 class="truncate font-semibold text-foreground">{server.hostname}</h3>
                  <p class="mt-1 truncate text-xs text-muted-foreground">{server.meta}</p>
                </div>
                <span class="shrink-0 rounded border border-warning/35 bg-warning/10 px-2 py-1 text-xs text-warning">{server.statusLabel || "attention"}</span>
              </div>
              <p class="mt-3 text-sm text-muted-foreground">
                {server.connectionLabel || "Connection state not reported"}
                {server.freshnessLabel ? ` · ${server.freshnessLabel}` : ""}
              </p>
            </article>
          {/each}
        </div>
      </section>
    -->

    <div class="mb-6">
      <ServerList
        items={monitoringServerItems}
        title="Server health"
        subtitle="Canonical inventory joined with current runtime telemetry."
        unavailable={inventoryUnavailable}
        emptyTitle={inventoryUnavailable
          ? "No authorized server telemetry is available"
          : "No servers recorded yet"}
        emptyBody={inventoryUnavailable
          ? "Reconnect inventory access or refresh once a runtime target reports telemetry."
          : "Connect a runtime target to begin collecting telemetry."}
        testId="inventory-server-list"
        cardTestId="inventory-server-card"
      />
    </div>

    <div
      class="mb-6 {focus === 'services'
        ? 'ring-1 ring-primary/30 rounded-lg'
        : ''}"
    >
      {#if servicesUnavailable}
        <p
          class="mb-3 rounded-lg border border-dashed border-warning/40 bg-warning/10 p-3 text-sm text-warning"
          data-testid="service-inventory-unavailable"
          role="status"
        >
          Canonical service identity and state are unavailable. Telemetry is
          retained for diagnostics, but is not presented as a zero-service
          inventory.
        </p>
      {/if}
      <ServiceList
        groups={monitoringServiceGroups}
        title="Runtime services"
        countLabel={servicesUnavailable
          ? "Service inventory unavailable"
          : `${monitoringServiceRows.length} canonical service${monitoringServiceRows.length === 1 ? "" : "s"}`}
        emptyTitle="No service telemetry is available"
        emptyBody="Services remain summarized here; use Services for lifecycle controls and logs."
        display="adaptive"
        testId="monitoring-service-list"
        cardTestId="monitoring-service-card"
      />
    </div>

    <section
      class="mb-6 rounded-lg border border-border bg-card p-5"
      data-testid="monitoring-section-otlp-baseline"
    >
      <h2 class="text-lg font-semibold text-foreground">OTLP Baseline</h2>
      <dl class="mt-4 grid gap-3 text-sm sm:grid-cols-2 xl:grid-cols-4">
        <div>
          <dt class="text-muted-foreground">Query backend</dt>
          <dd
            class="mt-1 font-medium text-foreground"
            data-testid="monitoring-query-backend"
          >
            {cockpit.monitoring.queryBackend || "not reported"}
          </dd>
        </div>
        <div>
          <dt class="text-muted-foreground">Ingest</dt>
          <dd
            class="mt-1 font-medium text-foreground"
            data-testid="monitoring-ingest-status"
          >
            {cockpit.monitoring.ingestBackend ||
              cockpit.monitoring.ingestStatus ||
              cockpit.monitoring.status}
          </dd>
        </div>
        <div>
          <dt class="text-muted-foreground">OTLP lane</dt>
          <dd
            class="mt-1 font-medium text-foreground"
            data-testid="monitoring-otlp-status"
          >
            {cockpit.monitoring.otlpStatus || cockpit.monitoring.status}
          </dd>
        </div>
        <div>
          <dt class="text-muted-foreground">Alert rules</dt>
          <dd
            class="mt-1 font-medium text-foreground"
            data-testid="monitoring-alert-rules"
          >
            {cockpit.monitoring.alertRuleCount ?? 0} rules
          </dd>
        </div>
        <div>
          <dt class="text-muted-foreground">Series</dt>
          <dd class="mt-1 font-medium text-foreground">
            {cockpit.monitoring.seriesCount ?? "unknown"}
          </dd>
        </div>
        <div>
          <dt class="text-muted-foreground">Instant query</dt>
          <dd
            class="mt-1 font-medium text-foreground"
            data-testid="monitoring-query-proof"
          >
            {cockpit.monitoring.queryProof || "vector:pending"}
          </dd>
        </div>
        <div>
          <dt class="text-muted-foreground">Range query</dt>
          <dd
            class="mt-1 font-medium text-foreground"
            data-testid="monitoring-range-proof"
          >
            {cockpit.monitoring.rangeProof || "matrix:pending"}
          </dd>
        </div>
      </dl>
    </section>

    <div class="grid gap-4 xl:grid-cols-2">
      <!-- Legacy service cards removed from the rendered information architecture.
      <section
        class="rounded-lg border border-border bg-card p-5"
        data-testid="monitoring-section-services"
      >
        <h2 class="mb-4 text-xl font-semibold text-foreground">
          Services By Server
        </h2>
        <div class="space-y-4">
          {#each servicesByServer() as group (group.server.id)}
            <div
              class={`rounded-lg border border-border bg-background/40 p-4 ${focus === "services" ? "ring-1 ring-primary/30" : ""}`}
            >
              <div class="mb-3 flex items-center justify-between gap-3">
                <h3 class="truncate font-semibold text-foreground">
                  {group.server.hostname}
                </h3>
                <span class="text-xs text-muted-foreground"
                  >{group.services.length} services</span
                >
              </div>
              {#if group.services.length === 0}
                <p class="text-sm text-muted-foreground">
                  No services reported on this server.
                </p>
              {:else}
                <div class="grid gap-2">
                  {#each group.services as service (service.id || service.name)}
                    <ServiceCardCompact
                      name={serviceCardName(service)}
                      meta={serviceCardMeta(service)}
                      placement={serviceCardPlacement(
                        service,
                        group.server.source,
                      )}
                      status={serviceCardStatus(service)}
                      showGrip={false}
                      onOpen={service.url
                        ? () => openServiceUrl(service)
                        : undefined}
                    />
                  {/each}
                </div>
              {/if}
            </div>
          {/each}
        </div>
      </section>
      -->

      <section
        class="rounded-lg border border-border bg-card p-5"
        data-testid="monitoring-section-recent-jobs"
        id="history"
      >
        <h2 class="mb-4 text-xl font-semibold text-foreground">
          History & Audit
        </h2>
        {#if jobs.length === 0}
          <p class="text-sm text-muted-foreground">
            No history entries available for this Homelab yet.
          </p>
        {:else}
          <div class="divide-y divide-border">
            {#each jobs.slice(0, 10) as job (job.id)}
              <div class="flex items-center justify-between gap-3 py-3">
                <div class="min-w-0">
                  <p class="truncate text-sm font-medium text-foreground">
                    {job.type || "job"}
                  </p>
                  <p class="truncate text-xs text-muted-foreground">
                    {job.message || job.step || job.id}
                  </p>
                  {#if job.state === "waiting"}
                    <p
                      class="mt-1 text-xs text-warning"
                      data-testid="monitoring-job-wait"
                    >
                      {label(job.wait_reason || "waiting")}
                      {#if resumeLabel(job.next_resume_at)}
                        · resumes {resumeLabel(job.next_resume_at)}
                      {/if}
                    </p>
                  {/if}
                </div>
                <div class="shrink-0 text-right">
                  <span
                    class={`rounded px-2 py-1 text-xs ${statusBadge(job.state)}`}
                  >
                    {label(job.state)}
                  </span>
                  <p class="mt-1 text-xs text-muted-foreground">
                    {job.progress}%
                  </p>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </section>
    </div>
  {/if}
</div>
