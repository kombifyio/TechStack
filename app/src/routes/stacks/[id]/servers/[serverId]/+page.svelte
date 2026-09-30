<script lang="ts">
  import { tr, trParts, formatDateTime, stateLabel } from "#lib/i18n.svelte.js";
  import { page } from "$app/state";
  import { untrack } from "svelte";
  import { goto } from "$app/navigation";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    isGatewayLoginRedirecting,
    isGatewayAuthFailure,
  } from "#lib/auth/session-recovery.js";
  import { authHandler } from "#lib/stores/authHandler.svelte.js";
  import SessionRenewalPanel from "#lib/components/SessionRenewalPanel.svelte";
  import {
    getServerPortInventory,
    type ServerPortInventory,
  } from "#lib/api/port-inventory.js";
  import {
    getRuntimeLogs,
    streamRuntimeLogs,
    type RuntimeLogEntry,
  } from "#lib/api/runtime-logs.js";
  import {
    detachSelfOwnedServer,
    getCanonicalServer,
    type CanonicalServer,
  } from "#lib/api/registry.js";

  import {
    decommissionMonthlyRuntime,
    getStackServerDetails,
    getMonthlyRuntimeSSHInfo,
    reconnectMonthlyRuntime,
    resolveMonthlyRuntimeCustody,
    runStackKitLifecycleOperation,
    type MonthlyRuntimeStatus,
    type StackKitLifecycleAcceptedResponse,
    type StackKitLifecycleOperation,
    type StackMetricValue,
    type StackServerAddress,
    type StackServerDetailsPayload,
    type StackServerEndpoint,
  } from "#lib/api/stacks.js";
  import {
    serverLifecycleActions,
    type ServerLifecycleAction,
  } from "#lib/server-lifecycle-actions.js";
  import { isLegacyOrUnboundCustody } from "#lib/custody/server-custody.js";
  import {
    mergeServerDetailsPayload,
    retainServerDetailsSnapshot,
    serverDetailsIdentity,
  } from "#lib/server-details-snapshot.js";
  import {
    openServiceUrl,
    serviceCardMeta,
    serviceCardName,
    serviceCardPlacement,
    serviceCardStatus,
  } from "#lib/service-card-adapter.js";
  import { ServiceCardCompact } from "@kombiverselabs/ui/service";
  import BrandLogoIcon from "#lib/components/BrandLogoIcon.svelte";
  import BrandLogoScope from "#lib/components/BrandLogoScope.svelte";
  import { brandDomainForTool } from "#lib/brand-logo.js";
  import Button from "#lib/components/ui/Button.svelte";
  import ServerAccessActions from "#lib/components/server-access/ServerAccessActions.svelte";
  import ManagedRuntimeRecreatePanel from "#lib/components/managed-runtime/ManagedRuntimeRecreatePanel.svelte";
  import {
    Activity,
    ArrowLeft,
    CheckCircle2,
    ClipboardCheck,
    Gauge,
    HardDrive,
    ListChecks,
    Server,
    TerminalSquare,
    RefreshCw,
    Settings2,
    ShieldCheck,
    Trash2,
  } from "@lucide/svelte";

  const stackId = $derived(page.params.id);
  const serverId = $derived(page.params.serverId);

  let details = $state<StackServerDetailsPayload | null>(null);
  let canonicalServer = $state<CanonicalServer | null>(null);
  let portInventory = $state<ServerPortInventory | null>(null);
  let portInventoryError = $state<string | null>(null);
  let loading = $state(true);
  let refreshing = $state(false);
  let loadGeneration = 0;
  let loadedIdentity = "";
  let error = $state<string | null>(null);
  let sessionRenewalRequired = $state(false);
  let actionError = $state<string | null>(null);
  let showDecommissionConfirmation = $state(false);
  let showDetachConfirmation = $state(false);
  let showCustodyResolutionConfirmation = $state(false);
  let actionLoading = $state<
    "ssh" | "reconnect" | "decommission" | "detach" | "resolve-custody" | null
  >(null);
  let lifecycleLoading = $state<StackKitLifecycleOperation | null>(null);
  let lifecycleConfirmation = $state<StackKitLifecycleOperation | null>(null);
  let lifecycleAccepted = $state<StackKitLifecycleAcceptedResponse | null>(
    null,
  );
  let runtimeStatus = $state<MonthlyRuntimeStatus | null>(null);
  let runtimeLogs = $state<RuntimeLogEntry[]>([]);
  let runtimeLogsError = $state<string | null>(null);
  let activeTab = $state<
    "overview" | "services" | "ports" | "checks" | "logs" | "settings"
  >("overview");
  let availableLifecycleActions = $derived.by(() =>
    serverLifecycleActions(canonicalServer ?? undefined),
  );
  let pendingLifecycleAction = $derived(
    availableLifecycleActions.find(
      (action) => action.operation === lifecycleConfirmation,
    ) ?? null,
  );
  let canDetachSelfOwnedServer = $derived(
    canonicalServer?.operations_owner === "customer" &&
      !canonicalServer.provider?.lease_id &&
      ((canonicalServer.environment_class === "local" &&
        canonicalServer.offering === "self_owned_device") ||
        (canonicalServer.environment_class === "cloud" &&
          canonicalServer.offering === "external_vps")) &&
      canonicalServer.target_evidence?.freshness.state === "recorded" &&
      canonicalServer.lifecycle.state !== "decommissioned",
  );
  let isDecommissionedManagedRuntime = $derived(
    Boolean(
      managedLeaseId() &&
      details?.server.capabilities?.lifecycle_state === "decommissioned",
    ),
  );
  let isManagedRuntimeCleanupStarted = $derived(
    Boolean(
      managedLeaseId() &&
      ["decommissioning", "decommissioned"].includes(
        details?.server.capabilities?.lifecycle_state || "",
      ),
    ),
  );

  $effect(() => {
    if (stackId && serverId) {
      untrack(() => {
        void load();
      });
    }
  });

  $effect(() => {
    const agentId = details?.server.agent_id?.trim();
    const observedServerId = details?.server.id?.trim();
    if (activeTab !== "logs" || (!agentId && !observedServerId)) return;
    const logScope = { agentId, serverId: observedServerId };
    let active = true;
    const merge = (entries: RuntimeLogEntry[]) => {
      if (!active) return;
      const byKey = new Map(
        runtimeLogs.map((entry) => [runtimeLogKey(entry), entry]),
      );
      for (const entry of entries) byKey.set(runtimeLogKey(entry), entry);
      runtimeLogs = [...byKey.values()]
        .sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp))
        .slice(0, 500);
      runtimeLogsError = null;
    };
    const refresh = async () => {
      try {
        merge(await getRuntimeLogs(logScope));
      } catch (err) {
        if (active) runtimeLogsError = parseApiError(err).message;
      }
    };
    void refresh();
    const closeStream = streamRuntimeLogs(logScope, merge, () => {
      if (active) runtimeLogsError = tr("ui.stacksIdServersServerId.liveStreamReconnecting");
    });
    const poll = window.setInterval(refresh, 5_000);
    return () => {
      active = false;
      closeStream();
      window.clearInterval(poll);
    };
  });

  async function load() {
    if (!stackId || !serverId) return;
    const identity = serverDetailsIdentity(stackId, serverId);
    const mine = ++loadGeneration;
    const retain = retainServerDetailsSnapshot(
      details,
      loadedIdentity,
      stackId,
      serverId,
    );
    const current = () =>
      mine === loadGeneration &&
      serverDetailsIdentity(stackId, serverId) === identity;

    if (retain) {
      refreshing = true;
      loading = false;
    } else {
      details = null;
      canonicalServer = null;
      portInventory = null;
      portInventoryError = null;
      loading = true;
      refreshing = false;
    }
    error = null;
    sessionRenewalRequired = false;

    const detailsReq = getStackServerDetails(stackId, serverId).then((next) => {
      if (!current()) return;
      details = mergeServerDetailsPayload(details, next);
      loadedIdentity = identity;
    });
    const canonicalReq = getCanonicalServer(serverId)
      .then((next) => {
        if (!current()) return;
        canonicalServer = next;
      })
      .catch(() => {
        if (!current()) return;
        if (!retain) canonicalServer = null;
      });
    const portsReq = getServerPortInventory(serverId)
      .then((next) => {
        if (!current()) return;
        portInventory = next;
        portInventoryError = null;
      })
      .catch((err) => {
        if (!current()) return;
        if (!retain) portInventory = null;
        portInventoryError = parseApiError(err).message;
      });

    const [detailsSettled] = await Promise.allSettled([
      detailsReq,
      canonicalReq,
      portsReq,
    ]);
    if (!current()) return;
    if (detailsSettled.status === "rejected") {
      const parsed = parseApiError(detailsSettled.reason);
      if (isGatewayLoginRedirecting(detailsSettled.reason)) {
        loading = false;
        refreshing = false;
        return;
      }
      if (parsed.isAuthError || isGatewayAuthFailure(detailsSettled.reason)) {
        const outcome = await authHandler.handleUnauthorized(
          () => load(),
          undefined,
          detailsSettled.reason,
        );
        if (outcome === "redirecting") {
          return;
        }
        if (outcome === "reauth_required") sessionRenewalRequired = true;
        loading = false;
        refreshing = false;
        return;
      }
      error = parsed.message;
    }
    loading = false;
    refreshing = false;
  }

  function managedLeaseId(): string {
    return details?.server.lease_id?.trim() || "";
  }

  function hasLegacyOrUnboundCustody(): boolean {
    return details ? isLegacyOrUnboundCustody(details.server) : false;
  }

  function persistedAccessContext(): {
    host: string;
    user: string;
    port: number;
  } {
    const caps = details?.server.capabilities;
    return {
      host:
        runtimeStatus?.ssh?.host ||
        caps?.runtime_ssh_host ||
        runtimeStatus?.status?.public_ip ||
        details?.server.ip ||
        "",
      user: runtimeStatus?.ssh?.user || caps?.runtime_ssh_user || "",
      port: runtimeStatus?.ssh?.port || caps?.runtime_ssh_port || 22,
    };
  }

  async function requestSSHInfo() {
    const leaseId = managedLeaseId();
    if (!leaseId) return;
    actionLoading = "ssh";
    actionError = null;
    try {
      runtimeStatus = await getMonthlyRuntimeSSHInfo(leaseId);
      await load();
    } catch (err) {
      const parsed = parseApiError(err);
      actionError = parsed.message || tr("ui.stacksIdServersServerId.couldNotLoadServerAccess");
    } finally {
      actionLoading = null;
    }
  }

  async function reconnectServer() {
    const leaseId = managedLeaseId();
    if (!leaseId) return;
    actionLoading = "reconnect";
    actionError = null;
    try {
      runtimeStatus = await reconnectMonthlyRuntime(leaseId);
      await load();
    } catch (err) {
      const parsed = parseApiError(err);
      actionError = parsed.message || tr("ui.stacksIdServersServerId.reconnectFailed");
    } finally {
      actionLoading = null;
    }
  }

  async function decommissionServer() {
    const leaseId = managedLeaseId();
    if (!leaseId) return;
    actionLoading = "decommission";
    actionError = null;
    try {
      runtimeStatus = await decommissionMonthlyRuntime(leaseId);
      showDecommissionConfirmation = false;
      await load();
    } catch (err) {
      const parsed = parseApiError(err);
      actionError = parsed.message || tr("ui.stacksIdServersServerId.decommissionFailed");
    } finally {
      actionLoading = null;
    }
  }

  async function detachServer() {
    if (!serverId || !stackId || !canDetachSelfOwnedServer) return;
    actionLoading = "detach";
    actionError = null;
    try {
      await detachSelfOwnedServer(serverId);
      showDetachConfirmation = false;
      await goto(`/stacks/${encodeURIComponent(stackId)}`);
    } catch (err) {
      const parsed = parseApiError(err);
      actionError = parsed.message || tr("ui.stacksIdServersServerId.serverDetachFailed");
    } finally {
      actionLoading = null;
    }
  }

  async function resolveCustody() {
    const leaseId = managedLeaseId();
    if (!leaseId || !hasLegacyOrUnboundCustody()) return;
    actionLoading = "resolve-custody";
    actionError = null;
    try {
      runtimeStatus = await resolveMonthlyRuntimeCustody(leaseId);
      showCustodyResolutionConfirmation = false;
      await load();
    } catch (err) {
      const parsed = parseApiError(err);
      actionError = parsed.message || tr("ui.stacksIdServersServerId.custodyResolutionFailed");
    } finally {
      actionLoading = null;
    }
  }

  function requestLifecycleAction(action: ServerLifecycleAction) {
    actionError = null;
    lifecycleAccepted = null;
    if (action.mutates) {
      lifecycleConfirmation = action.operation;
      return;
    }
    void executeLifecycleAction(action);
  }

  async function executeLifecycleAction(action: ServerLifecycleAction) {
    const currentStackId = stackId;
    if (!currentStackId || !details?.server.agent_id || lifecycleLoading)
      return;
    lifecycleLoading = action.operation;
    lifecycleConfirmation = null;
    actionError = null;
    lifecycleAccepted = null;
    try {
      lifecycleAccepted = await runStackKitLifecycleOperation(currentStackId, {
        operation: action.operation,
        agent_id: details.server.agent_id,
        target_release: action.operation === "upgrade" ? "latest" : undefined,
        owner_approved: action.mutates ? true : undefined,
      });
      await load();
    } catch (err) {
      const parsed = parseApiError(err);
      actionError =
        parsed.message || tr("ui.serverDetail.actionNotStarted", { action: action.label });
    } finally {
      lifecycleLoading = null;
    }
  }

  function statusKind(status: string): "ok" | "warn" | "error" | "off" {
    switch (status) {
      case "healthy":
      case "running":
      case "passed":
      case "ok":
      case "present":
      case "consistent":
      case "reserved":
      case "active":
        return "ok";
      case "stale":
      case "pending":
      case "mutating":
      case "unknown":
        return "warn";
      case "offline":
      case "failed":
      case "error":
      case "degraded":
      case "missing":
      case "unexpected":
      case "uncertain":
        return "error";
      default:
        return "off";
    }
  }

  function formatMetric(metric: StackMetricValue | undefined): string {
    if (!metric || metric.value === undefined || metric.status !== "ok") {
      return "unknown";
    }
    return `${metric.value}${metric.unit || ""}`;
  }

  function formatStatus(status: string): string {
    return stateLabel(status);
  }

  function runtimeLogKey(entry: RuntimeLogEntry): string {
    return `${entry.timestamp}:${entry.source || ""}:${entry.job_id || ""}:${entry.message}`;
  }

  function runtimeLogLevelClass(level: string): string {
    if (level === "error") return "text-destructive";
    if (level === "warn") return "text-warning";
    return "text-muted-foreground";
  }

  function serverOSLabel(): string {
    const server = details?.server;
    if (!server) return "os unknown/arch unknown";
    const os = server.os?.trim() || tr("ui.serverCard.osUnknown");
    const version = server.os_version?.trim();
    const osWithVersion =
      version && !os.toLowerCase().includes(version.toLowerCase())
        ? `${os} ${version}`
        : os;
    return `${osWithVersion}/${server.arch?.trim() || tr("ui.serverCard.archUnknown")}`;
  }

  function serverAddresses(): StackServerAddress[] {
    const addresses = [...(details?.server.host_addresses || [])];
    const primary = details?.server.ip?.trim();
    if (primary && !addresses.some((address) => address.address === primary)) {
      addresses.unshift({
        address: primary,
        scope: "primary",
        provenance: details?.server.health.source || "server inventory",
      });
    }
    return addresses;
  }

  function stackKitName(): string {
    return (
      details?.server.stackkit?.name?.trim() ||
      details?.server.stackkit?.catalog_ref?.trim() ||
      "StackKit"
    );
  }

  function stackKitVariant(): string {
    const stackkit = details?.server.stackkit;
    if (!stackkit) return "not reported";

    return (
      [
        stackkit.version,
        stackkit.mode,
        stackkit.context,
        stackkit.paas,
        stackkit.compute_tier,
      ]
        .map((part) => part?.trim())
        .filter(Boolean)
        .join(" · ") || "variant not reported"
    );
  }

  function safeEndpointHref(endpoint: StackServerEndpoint): string | null {
    const health = endpoint.health?.trim().toLowerCase();
    if (health !== "healthy" && health !== "ok" && health !== "reachable") {
      return null;
    }
    try {
      const parsed = new URL(endpoint.url);
      return parsed.protocol === "http:" || parsed.protocol === "https:"
        ? endpoint.url
        : null;
    } catch {
      return null;
    }
  }

  function backHref(): string {
    return "/dashboard";
  }

  function setTab(tab: string) {
    if (
      tab === "overview" ||
      tab === "services" ||
      tab === "ports" ||
      tab === "checks" ||
      tab === "logs" ||
      tab === "settings"
    ) {
      activeTab = tab;
    }
  }
</script>

<svelte:head>
  <title>{tr("ui.stacksIdServersServerId.serverDetailsKombifyTechstack")}</title>
</svelte:head>

<div
  class="mx-auto max-w-7xl p-6 md:p-8"
  data-testid="server-details-page"
  aria-busy={loading || refreshing}
>
  <Button variant="ghost" class="mb-6" onclick={() => goto(backHref())}>
    <ArrowLeft class="h-4 w-4" />
    {tr("ui.stacksIdServersServerId.backToOperations")}
  </Button>

  {#if loading && !details}
    <div
      data-kx="plate"
      class="p-6"
      data-testid="server-details-loading-state"
      aria-label={tr("ui.stacksIdServersServerId.loadingServerDetails")}
    >
      <div class="h-6 w-48 animate-pulse rounded bg-muted"></div>
      <div class="mt-4 grid gap-3 md:grid-cols-4">
        {#each Array(4) as _, i (i)}
          <div class="h-24 animate-pulse rounded-lg bg-muted/50"></div>
        {/each}
      </div>
    </div>
  {:else if sessionRenewalRequired && !details}
    <SessionRenewalPanel />
  {:else if error && !details}
    <div class="rounded-lg border border-destructive/30 bg-destructive/10 p-4">
      <p class="text-foreground">{error}</p>
    </div>
  {:else if details}
    {#if error}
      <div
        class="mb-6 rounded-lg border border-destructive/30 bg-destructive/10 p-4"
        role="alert"
        data-testid="server-details-refresh-error"
      >
        <p class="text-foreground">{error}</p>
      </div>
    {/if}
    <nav
      class="mb-6 border-b border-border"
      aria-label={tr("ui.stacksIdServersServerId.serverDetailSections")}
      data-testid="server-details-tabs"
    >
      <div class="flex flex-wrap gap-1" role="tablist">
        {#each ["overview", "services", "ports", "checks", "logs", "settings"] as tab (tab)}
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === tab}
            class="px-3 py-2 text-sm capitalize {activeTab === tab
              ? 'border-b-2 border-primary text-foreground'
              : 'text-muted-foreground hover:text-foreground'}"
            data-testid={`server-tab-${tab}`}
            onclick={() => setTab(tab)}
          >
            {tab}
          </button>
        {/each}
      </div>
    </nav>

    {#if activeTab === "overview"}
      <header data-kx="plate" class="mb-6 p-5">
        <div
          class="flex flex-col gap-4 md:flex-row md:items-start md:justify-between"
        >
          <div class="min-w-0">
            <div class="mb-2 flex flex-wrap items-center gap-2">
              <span
                data-kx="status"
                data-status={statusKind(details.server.health.state)}
                class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
              >
                {formatStatus(details.server.health.state)}
              </span>
              <span
                data-kx="tag"
                class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
                >{details.server.role}</span
              >
              <span
                data-kx="tag"
                class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
              >
                {details.server.assignment}
              </span>
            </div>
            <h1 class="truncate text-2xl font-semibold text-foreground">
              {details.server.hostname}
            </h1>
            <p class="mt-1 text-sm text-muted-foreground">
              {serverOSLabel()}
              - {details.server.ip || "no ip"}
            </p>
          </div>
          <div class="flex flex-wrap gap-2">
            {#if managedLeaseId()}
              <Button
                variant="secondary"
                testId="server-access-button"
                onclick={requestSSHInfo}
                disabled={actionLoading !== null}
              >
                <ShieldCheck class="h-4 w-4" />
                {actionLoading === "ssh" ? tr("ui.stacksIdServersServerId.checking") : tr("ui.stacksCreatingCreationLease.access")}
              </Button>
              <Button
                variant="secondary"
                testId="server-reconnect-button"
                onclick={reconnectServer}
                disabled={actionLoading !== null}
              >
                <RefreshCw class="h-4 w-4" />
                {actionLoading === "reconnect"
                  ? tr("ui.homelabDashboardPage.reconnecting")
                  : tr("ui.homelabDashboardPage.reconnect")}
              </Button>
            {/if}
            <Button
              variant="secondary"
              testId="server-details-refresh"
              onclick={() => void load()}
              disabled={refreshing}
            >
              <RefreshCw class="h-4 w-4 {refreshing ? 'animate-spin' : ''}" />
              {refreshing ? tr("ui.monitoring.refreshing") : tr("ui.homelabDashboardPage.refresh")}
            </Button>
          </div>
        </div>
      </header>

      <div class="mb-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div data-kx="plate" class="p-4">
          <div class="mb-3 flex items-center justify-between">
            <span class="text-sm text-muted-foreground">CPU</span>
            <Gauge class="h-4 w-4 text-primary" />
          </div>
          <p class="text-2xl font-semibold text-foreground">
            {formatMetric(details.health.cpu_percent)}
          </p>
        </div>
        <div data-kx="plate" class="p-4">
          <div class="mb-3 flex items-center justify-between">
            <span class="text-sm text-muted-foreground">{tr("ui.stacksIdServersServerId.memory")}</span>
            <Server class="h-4 w-4 text-success" />
          </div>
          <p class="text-2xl font-semibold text-foreground">
            {formatMetric(details.health.memory_percent)}
          </p>
        </div>
        <div data-kx="plate" class="p-4">
          <div class="mb-3 flex items-center justify-between">
            <span class="text-sm text-muted-foreground">{tr("ui.stacksIdServersServerId.disk")}</span>
            <HardDrive class="h-4 w-4 text-info" />
          </div>
          <p class="text-2xl font-semibold text-foreground">
            {formatMetric(details.health.disk_percent)}
          </p>
        </div>
        <div data-kx="plate" class="p-4">
          <div class="mb-3 flex items-center justify-between">
            <span class="text-sm text-muted-foreground">{tr("ui.stacksIdServersServerId.preChecks")}</span>
            <ListChecks class="h-4 w-4 text-warning" />
          </div>
          <p class="text-2xl font-semibold text-foreground">
            {details.checks.length}
          </p>
        </div>
      </div>
    {/if}

    {#if actionError}
      <div
        class="mb-6 rounded-lg border border-destructive/30 bg-destructive/10 p-4"
        role="alert"
      >
        <p class="text-sm text-destructive">{actionError}</p>
      </div>
    {/if}

    {#if activeTab === "overview"}
      {@const access = persistedAccessContext()}
      <section class="grid gap-6 lg:grid-cols-2">
        <div data-kx="plate" class="p-5">
          <div class="mb-4 flex items-center gap-2">
            <ShieldCheck class="h-5 w-5 text-primary" />
            <h2 class="text-lg font-semibold text-foreground">{tr("ui.stacksCreatingCreationLease.access")}</h2>
          </div>
          <ServerAccessActions
            serverId={serverId || ""}
            serverName={details.server.hostname || serverId || tr("ui.servicesApplicationId.server")}
            unavailable={canonicalServer?.connection.state === "offline" ||
              canonicalServer?.connection.state === "revoked"}
            unavailableReason={canonicalServer
              ? tr("ui.serverDetail.connectionState", {
                state: formatStatus(canonicalServer.connection.state),
              })
              : tr("ui.stacksIdServersServerId.canonicalServerAccessIsNot")}
          />
          {#if managedLeaseId()}
            <dl class="grid grid-cols-2 gap-3 text-sm">
              <div class="rounded-lg bg-muted/40 p-3">
                <dt class="text-muted-foreground">{tr("ui.stacksCreatingCreationLease.host")}</dt>
                <dd class="mt-1 break-all font-mono text-foreground">
                  {access.host || "pending"}
                </dd>
              </div>
              <div class="rounded-lg bg-muted/40 p-3">
                <dt class="text-muted-foreground">SSH</dt>
                <dd class="mt-1 font-mono text-foreground">
                  {access.user || "user pending"}@{access.host ||
                    "host pending"}:{access.port}
                </dd>
              </div>
            </dl>
            <p class="mt-3 text-sm text-muted-foreground">
              {tr("ui.stacksIdServersServerId.managedRuntimeLeaseContext")}
            </p>
          {:else}
            <p class="text-sm text-muted-foreground">
              {tr("ui.stacksIdServersServerId.thisServerIsRegisteredThrough")}
            </p>
          {/if}
        </div>

        <div data-kx="plate" class="p-5">
          <div class="mb-4 flex items-center gap-2">
            <Server class="h-5 w-5 text-primary" />
            <h2 class="text-lg font-semibold text-foreground">{tr("ui.stacksIdServersServerId.metadata")}</h2>
          </div>
          <dl class="grid grid-cols-2 gap-3 text-sm">
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">{tr("ui.stacksIdServersServerId.agentId")}</dt>
              <dd class="mt-1 break-all font-mono text-foreground">
                {details.server.agent_id}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">{tr("ui.stacksIdServersServerId.lastSeen")}</dt>
              <dd class="mt-1 text-foreground">
                {details.server.last_seen || "unknown"}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">{tr("ui.stacksIdServersServerId.cpuCores")}</dt>
              <dd class="mt-1 text-foreground">
                {details.server.capabilities.cpu_cores || "unknown"}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">RAM</dt>
              <dd class="mt-1 text-foreground">
                {details.server.capabilities.ram_mb
                  ? `${details.server.capabilities.ram_mb} MB`
                  : "unknown"}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">Docker</dt>
              <dd class="mt-1 text-foreground">
                {details.server.capabilities.docker_version || "unknown"}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">{tr("ui.stacksCreatingCreationLease.provider")}</dt>
              <dd class="mt-1 text-foreground">
                {details.server.capabilities.provider || "unknown"}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">{tr("ui.stacksIdServersServerId.operatingSystem")}</dt>
              <dd class="mt-1 text-foreground">
                {details.server.os || "unknown"}
                {details.server.os_version
                  ? ` ${details.server.os_version}`
                  : ""}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">{tr("ui.stacksIdServersServerId.architecture")}</dt>
              <dd class="mt-1 text-foreground">
                {details.server.arch || "unknown"}
              </dd>
            </div>
          </dl>
        </div>

        <div data-kx="plate" class="p-5" data-testid="server-lifecycle-actions">
          <div class="flex items-start gap-3">
            <ListChecks class="mt-0.5 h-5 w-5 shrink-0 text-primary" />
            <div class="min-w-0 flex-1">
              <h2 class="text-lg font-semibold text-foreground">
                {tr("ui.stacksIdServersServerId.stackkitActions")}
              </h2>
              <p class="mt-2 max-w-3xl text-sm text-muted-foreground">
                {tr("ui.serverDetail.actionsTarget", { name: details.server.hostname })}
              </p>

              {#if availableLifecycleActions.length === 0}
                <p
                  class="mt-4 rounded-lg bg-muted/40 p-3 text-sm text-muted-foreground"
                  data-testid="server-lifecycle-unavailable"
                >
                  {tr("ui.stacksIdServersServerId.lifecycleActionsBecomeAvailableWhen")}
                </p>
              {:else}
                <div class="mt-4 grid gap-3 sm:grid-cols-2">
                  {#each availableLifecycleActions as action (action.operation)}
                    <button
                      type="button"
                      data-kx="control"
                      class="inline-flex h-auto items-center justify-start gap-2 whitespace-nowrap rounded-lg px-4 py-3 text-left text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
                      data-testid={`server-lifecycle-${action.operation}`}
                      onclick={() => requestLifecycleAction(action)}
                      disabled={lifecycleLoading !== null ||
                        actionLoading !== null}
                    >
                      <span>
                        <span class="block font-medium">
                          {lifecycleLoading === action.operation
                            ? tr("ui.homelabDashboardPage.starting")
                            : action.label}
                        </span>
                        <span class="mt-1 block text-xs font-normal opacity-75">
                          {action.description}
                        </span>
                      </span>
                    </button>
                  {/each}
                </div>
              {/if}

              {#if pendingLifecycleAction}
                <div
                  class="mt-4 rounded-lg border border-warning/40 bg-warning/10 p-4"
                  data-testid="server-lifecycle-confirmation"
                >
                  <p class="text-sm font-medium text-foreground">
                    {tr("ui.serverDetail.confirmActionFor", { action: pendingLifecycleAction.label, name: details.server.hostname })}
                  </p>
                  <p class="mt-1 text-sm text-muted-foreground">
                    {tr("ui.stacksIdServersServerId.thisChangesTheSelectedServer")}
                  </p>
                  <div class="mt-4 flex flex-wrap gap-2">
                    <Button
                      variant="primary"
                      testId="server-lifecycle-confirm-button"
                      onclick={() =>
                        executeLifecycleAction(pendingLifecycleAction)}
                      disabled={lifecycleLoading !== null}
                    >
                      {tr("ui.stacksIdServersServerId.confirmAction")}
                    </Button>
                    <Button
                      variant="secondary"
                      onclick={() => (lifecycleConfirmation = null)}
                      disabled={lifecycleLoading !== null}
                    >
                      {tr("ui.importExportModal.cancel")}
                    </Button>
                  </div>
                </div>
              {/if}

              {#if lifecycleAccepted}
                <div
                  class="mt-4 rounded-lg border border-success/30 bg-success/10 p-3 text-sm"
                  role="status"
                  data-testid="server-lifecycle-accepted"
                >
                  {tr("ui.serverDetail.jobAccepted", { id: lifecycleAccepted.job_id })}
                </div>
              {/if}
            </div>
          </div>
        </div>

        <div data-kx="plate" class="p-5" data-testid="server-stackkit-details">
          <div class="mb-4 flex items-center justify-between gap-2">
            <div class="flex items-center gap-2">
              <ClipboardCheck class="h-5 w-5 text-primary" />
              <h2 class="text-lg font-semibold text-foreground">StackKit</h2>
            </div>
            {#if details.server.stackkit}
              <span
                data-kx="status"
                data-status={statusKind(details.server.stackkit.state)}
                class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
              >
                {formatStatus(details.server.stackkit.state)}
              </span>
            {/if}
          </div>
          {#if details.server.stackkit}
            <p class="font-medium text-foreground">{stackKitName()}</p>
            <p class="mt-1 text-sm text-muted-foreground">
              {stackKitVariant()}
            </p>
            <p class="mt-4 text-xs text-muted-foreground">
              {tr("ui.serverDetail.evidence", { sources: details.server.stackkit.sources.length ? details.server.stackkit.sources.join(", ") : tr("ui.serverDetail.sourceNotReported") })}
            </p>
            {#if details.server.stackkit.state !== "observed"}
              <p
                class="mt-3 rounded-lg border border-warning/30 bg-warning/10 p-3 text-sm text-muted-foreground"
              >
                {tr("ui.stacksIdServersServerId.thisIsConfiguredStackkitIntent")}
              </p>
            {/if}
          {:else}
            <p class="text-sm text-muted-foreground">
              {tr("ui.stacksIdServersServerId.noStackkitDeploymentEvidenceHas")}
            </p>
          {/if}
        </div>

        <div data-kx="plate" class="p-5" data-testid="server-network-details">
          <div class="mb-4 flex items-center gap-2">
            <Server class="h-5 w-5 text-primary" />
            <h2 class="text-lg font-semibold text-foreground">
              {tr("ui.stacksIdServersServerId.addressesDomains")}
            </h2>
          </div>
          {#if serverAddresses().length}
            <div class="space-y-2">
              {#each serverAddresses() as address (`${address.scope}:${address.address}`)}
                <div class="rounded-lg bg-muted/40 p-3 text-sm">
                  <div
                    class="flex flex-wrap items-center justify-between gap-2"
                  >
                    <span class="break-all font-mono text-foreground">
                      {address.address}
                    </span>
                    <span
                      data-kx="tag"
                      class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
                    >
                      {address.scope || "address"}
                    </span>
                  </div>
                  <p class="mt-1 text-xs text-muted-foreground">
                    {address.provenance || "source not reported"}
                  </p>
                </div>
              {/each}
            </div>
          {:else}
            <p class="text-sm text-muted-foreground">
              {tr("ui.stacksIdServersServerId.noHostAddressHasBeen")}
            </p>
          {/if}
          <div class="mt-4 flex flex-wrap gap-2" data-testid="server-domains">
            {#each details.server.domains || [] as domain (domain)}
              <span
                data-kx="tag"
                class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium break-all"
                >{domain}</span
              >
            {:else}
              <span class="text-sm text-muted-foreground">
                {tr("ui.stacksIdServersServerId.noServiceDomainsReported")}
              </span>
            {/each}
          </div>
        </div>

        <div
          data-kx="plate"
          class="p-5 lg:col-span-2"
          data-testid="server-service-endpoints"
        >
          <div class="mb-4 flex items-center gap-2">
            <Activity class="h-5 w-5 text-info" />
            <h2 class="text-lg font-semibold text-foreground">
              {tr("ui.stacksIdServersServerId.serviceEndpoints")}
            </h2>
          </div>
          {#if details.server.service_endpoints?.length}
            <div class="grid gap-3 md:grid-cols-2">
              {#each details.server.service_endpoints as endpoint (endpoint.url)}
                {@const href = safeEndpointHref(endpoint)}
                <div class="min-w-0 rounded-lg bg-muted/40 p-3 text-sm">
                  <div
                    class="flex flex-wrap items-center justify-between gap-2"
                  >
                    <p class="font-medium text-foreground">
                      {endpoint.name || endpoint.service_key || tr("ui.services.service")}
                    </p>
                    <span
                      data-kx="status"
                      data-status={statusKind(endpoint.health || "unknown")}
                      class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
                    >
                      {formatStatus(endpoint.health || "unknown")}
                    </span>
                  </div>
                  {#if href}
                    <a
                      class="mt-2 block truncate font-mono text-primary underline-offset-4 hover:underline"
                      {href}
                      target="_blank"
                      rel="noopener noreferrer"
                      title={endpoint.url}
                    >
                      {endpoint.url}
                    </a>
                  {:else}
                    <p class="mt-2 break-all font-mono text-muted-foreground">
                      {endpoint.url}
                    </p>
                  {/if}
                  <p class="mt-2 text-xs text-muted-foreground">
                    {endpoint.visibility || "visibility unknown"} · {endpoint.provenance ||
                      endpoint.source}
                  </p>
                </div>
              {/each}
            </div>
          {:else}
            <p class="text-sm text-muted-foreground">
              {tr("ui.stacksIdServersServerId.noObservedServiceEndpointsHave")}
            </p>
          {/if}
        </div>

        <div data-kx="plate" class="p-5">
          <div class="mb-4 flex items-center gap-2">
            <CheckCircle2 class="h-5 w-5 text-success" />
            <h2 class="text-lg font-semibold text-foreground">{tr("ui.services.health")}</h2>
          </div>
          <p class="text-sm text-muted-foreground">
            {tr("ui.serverDetail.healthSource", { source: details.health.source, backend: details.monitoring.queryBackend })}
          </p>
          {#if details.health.notes?.length}
            <div class="mt-4 space-y-2">
              {#each details.health.notes as note (note)}
                <p
                  class="rounded-lg bg-muted/40 p-3 text-sm text-muted-foreground"
                >
                  {note}
                </p>
              {/each}
            </div>
          {/if}
        </div>
      </section>
    {:else if activeTab === "services"}
      <!-- Boxless section (operator direction 2026-08-19) -->
      <section>
        <div class="mb-4 flex items-center gap-2">
          <ClipboardCheck class="h-5 w-5 text-info" />
          <h2 class="text-lg font-semibold text-foreground">{tr("ui.services.services")}</h2>
        </div>
        {#if details.services.length === 0}
          <p class="text-sm text-muted-foreground">
            {tr("ui.stacksIdServersServerId.noServicePlacementIsRecorded")}
          </p>
        {:else}
          <div class="grid gap-2 md:grid-cols-2">
            {#each details.services as service (service.id || service.name)}
              <BrandLogoScope
                domain={brandDomainForTool(
                  service.name,
                  service.display_name,
                  service.type,
                )}
              >
                <ServiceCardCompact
                  name={serviceCardName(service)}
                  meta={serviceCardMeta(service)}
                  icon={BrandLogoIcon}
                  placement={serviceCardPlacement(service)}
                  status={serviceCardStatus(service)}
                  showGrip={false}
                  onOpen={service.url
                    ? () => openServiceUrl(service)
                    : undefined}
                />
              </BrandLogoScope>
            {/each}
          </div>
        {/if}
      </section>
    {:else if activeTab === "ports"}
      <section id="port-inventory" data-testid="server-port-inventory">
        <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
          <div>
            <div class="flex items-center gap-2">
              <Activity class="h-5 w-5 text-info" />
              <h2 class="text-lg font-semibold text-foreground">
                {tr("ui.stacksIdServersServerId.portAllocations")}
              </h2>
            </div>
            <p class="mt-1 text-sm text-muted-foreground">
              {tr("ui.stacksIdServersServerId.desiredListenersDurableReservationsAnd")}
            </p>
          </div>
          {#if portInventory?.observed_at}
            <p class="text-xs text-muted-foreground">
              {tr("ui.serverDetail.observedAt", { time: formatDateTime(portInventory.observed_at) })}
            </p>
          {/if}
        </div>

        {#if portInventoryError}
          <div class="rounded-lg border border-warning/40 bg-warning/10 p-4">
            <p class="font-medium text-foreground">
              {tr("ui.stacksIdServersServerId.portInventoryIsNotAvailable")}
            </p>
            <p class="mt-1 text-sm text-muted-foreground">
              {portInventoryError}
            </p>
            <Button
              variant="secondary"
              class="mt-3"
              onclick={() => void load()}
              disabled={refreshing}
            >
              <RefreshCw class="h-4 w-4 {refreshing ? 'animate-spin' : ''}" />
              {refreshing ? tr("ui.monitoring.refreshing") : tr("ui.homelabDashboardPage.refresh")}
            </Button>
          </div>
        {:else if !portInventory || portInventory.allocations.length === 0}
          <div class="rounded-lg border border-border bg-background/40 p-4">
            <p class="font-medium text-foreground">
              {portInventory?.observed_at
                ? portInventory.listeners_complete
                  ? tr("ui.stacksIdServersServerId.noPortAllocationsRecorded")
                  : tr("ui.stacksIdServersServerId.portEvidenceIsPartial")
                : tr("ui.stacksIdServersServerId.noPortEvidenceYet")}
            </p>
            <p class="mt-1 text-sm text-muted-foreground">
              {portInventory?.observed_at
                ? portInventory.listeners_complete
                  ? tr("ui.stacksIdServersServerId.noCompilerDeclaredReservationOr")
                  : tr("ui.stacksIdServersServerId.noDesiredListenerIsReserved")
                : tr("ui.stacksIdServersServerId.noDesiredListenerIsReserved2")}
            </p>
          </div>
        {:else}
          <div class="overflow-x-auto rounded-lg border border-border">
            <table class="w-full min-w-[760px] text-left text-sm">
              <thead
                class="bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground"
              >
                <tr>
                  <th class="px-4 py-3 font-medium">{tr("ui.stacksIdServersServerId.listener")}</th>
                  <th class="px-4 py-3 font-medium">{tr("ui.stacksIdServersServerId.intent")}</th>
                  <th class="px-4 py-3 font-medium">{tr("ui.stacksIdServersServerId.reservation")}</th>
                  <th class="px-4 py-3 font-medium">{tr("ui.services.observed")}</th>
                  <th class="px-4 py-3 font-medium">{tr("ui.stacksIdServersServerId.exposed")}</th>
                  <th class="px-4 py-3 font-medium">{tr("ui.stacksIdServersServerId.drift")}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-border">
                {#each portInventory.allocations as allocation (allocation.id)}
                  <tr class="bg-background/30 align-top">
                    <td class="px-4 py-3">
                      <p class="font-mono font-medium text-foreground">
                        {allocation.bind_address}:{allocation.port}/{allocation.transport}
                      </p>
                      <p class="mt-1 text-xs text-muted-foreground">
                        {allocation.node_ref ||
                          (allocation.desired
                            ? "primary Node"
                            : "runtime only")}
                      </p>
                    </td>
                    <td class="px-4 py-3 text-foreground">
                      <p>
                        {allocation.desired
                          ? formatStatus(allocation.exposure)
                          : tr("ui.stacksIdServersServerId.notDeclared")}
                      </p>
                      {#if allocation.kit_deployment_id}
                        <p
                          class="mt-1 max-w-40 truncate text-xs text-muted-foreground"
                          title={allocation.kit_deployment_id}
                        >
                          {allocation.kit_deployment_id}
                        </p>
                      {/if}
                    </td>
                    <td class="px-4 py-3">
                      <span
                        data-kx="status"
                        data-status={statusKind(
                          allocation.claim_state ||
                            allocation.reservation_state ||
                            "unknown",
                        )}
                        class="inline-flex rounded-full px-2.5 py-0.5 text-xs font-medium"
                      >
                        {formatStatus(
                          allocation.claim_state ||
                            allocation.reservation_state ||
                            "unknown",
                        )}
                      </span>
                    </td>
                    <td class="px-4 py-3">
                      <span
                        data-kx="status"
                        data-status={statusKind(allocation.observed_state)}
                        class="inline-flex rounded-full px-2.5 py-0.5 text-xs font-medium"
                      >
                        {formatStatus(allocation.observed_state)}
                      </span>
                    </td>
                    <td class="px-4 py-3">
                      <span
                        data-kx="status"
                        data-status={statusKind(allocation.exposed_state)}
                        class="inline-flex rounded-full px-2.5 py-0.5 text-xs font-medium"
                      >
                        {formatStatus(allocation.exposed_state)}
                      </span>
                    </td>
                    <td class="px-4 py-3">
                      <span
                        data-kx="status"
                        data-status={statusKind(allocation.drift_state)}
                        class="inline-flex rounded-full px-2.5 py-0.5 text-xs font-medium"
                      >
                        {formatStatus(allocation.drift_state)}
                      </span>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
          {#if !portInventory.listeners_complete || !portInventory.exposures_complete}
            <p class="mt-3 text-sm text-muted-foreground">
              {tr("ui.stacksIdServersServerId.runtimeEvidenceIsPartialUnknown")}
            </p>
          {/if}
        {/if}
      </section>
    {:else if activeTab === "checks"}
      <!-- Boxless section (operator direction 2026-08-19) -->
      <section>
        <div class="mb-4 flex items-center gap-2">
          <ListChecks class="h-5 w-5 text-warning" />
          <h2 class="text-lg font-semibold text-foreground">{tr("ui.stacksIdServersServerId.checks")}</h2>
        </div>
        {#if details.checks.length === 0}
          <p class="text-sm text-muted-foreground">
            {tr("ui.stacksIdServersServerId.noPreCheckResultIs")}
          </p>
        {:else}
          <div class="space-y-3">
            {#each details.checks as check (check.id)}
              <div class="rounded-lg border border-border bg-background/40 p-3">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <p class="font-medium text-foreground">{check.check_type}</p>
                  <span
                    data-kx="status"
                    data-status={statusKind(check.status)}
                    class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
                    >{formatStatus(check.status)}</span
                  >
                </div>
                {#if check.message}
                  <p class="mt-1 text-sm text-muted-foreground">
                    {check.message}
                  </p>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      </section>
    {:else if activeTab === "logs"}
      <!-- Boxless section (operator direction 2026-08-19) -->
      <section>
        <div class="mb-4 flex items-center gap-2">
          <TerminalSquare class="h-5 w-5 text-primary" />
          <h2 class="text-lg font-semibold text-foreground">{tr("ui.stacksIdServersServerId.logs")}</h2>
        </div>
        {#if runtimeLogsError}
          <p class="mb-3 text-sm text-warning">{runtimeLogsError}</p>
        {/if}
        {#if runtimeLogs.length > 0}
          <div
            class="mb-5 max-h-[32rem] space-y-2 overflow-y-auto font-mono text-xs"
            data-testid="runtime-live-logs"
          >
            {#each runtimeLogs as log (runtimeLogKey(log))}
              <div class="rounded-lg border border-border bg-background/60 p-3">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <span class={runtimeLogLevelClass(log.level)}
                    >{log.level.toUpperCase()} · {log.source || "runtime"}</span
                  >
                  <span class="text-muted-foreground"
                    >{formatDateTime(log.timestamp)}</span
                  >
                </div>
                <p class="mt-1 whitespace-pre-wrap break-words text-foreground">
                  {log.message}
                </p>
                {#if log.job_id}
                  <p class="mt-1 text-muted-foreground">{tr("ui.serverDetail.jobLabel", { id: log.job_id })}</p>
                {/if}
              </div>
            {/each}
          </div>
        {:else if details.logs.length === 0}
          <p class="text-sm text-muted-foreground">
            {tr("ui.stacksIdServersServerId.noServerInstallerOrStackkits")}
          </p>
        {/if}
        {#if details.logs.length > 0}
          <h3 class="mb-2 text-sm font-medium text-muted-foreground">
            {tr("ui.stacksIdServersServerId.activityHistory")}
          </h3>
          <div class="space-y-2">
            {#each details.logs as log (log.id || `${log.created}-${log.action}`)}
              <div class="rounded-lg bg-muted/40 p-3 text-sm">
                <div class="flex items-center justify-between gap-3">
                  <p class="font-medium text-foreground">{log.action}</p>
                  <span class="text-xs text-muted-foreground">
                    {String(log.created || "")}
                  </span>
                </div>
                <p class="mt-1 text-muted-foreground">
                  {String(log.details || "")}
                </p>
              </div>
            {/each}
          </div>
        {/if}
      </section>
    {:else}
      <section
        class="space-y-6"
        aria-labelledby="server-settings-heading"
        data-testid="server-settings-panel"
      >
        <!-- Boxless heading block (operator direction 2026-08-19) -->
        <div>
          <div class="flex items-center gap-2">
            <Settings2 class="h-5 w-5 text-primary" />
            <h1
              id="server-settings-heading"
              class="text-lg font-semibold text-foreground"
            >
              {tr("ui.stacksIdServersServerId.serverSettings")}
            </h1>
          </div>
          <p class="mt-2 text-sm text-muted-foreground">
            {tr("ui.stacksIdServersServerId.manageSettingsThatAffectThis")}
          </p>
        </div>

        {#if isManagedRuntimeCleanupStarted && managedLeaseId()}
          <ManagedRuntimeRecreatePanel
            stackId={stackId || ""}
            leaseId={managedLeaseId()}
            serverName={details.server.hostname}
          />
        {/if}

        <div
          class="rounded-lg border border-destructive/40 bg-destructive/5 p-5"
          data-testid="server-danger-zone"
        >
          <div class="flex items-start gap-3">
            <Trash2 class="mt-0.5 h-5 w-5 shrink-0 text-destructive" />
            <div class="min-w-0 flex-1">
              {#if managedLeaseId()}
                {#if isManagedRuntimeCleanupStarted}
                  <h2 class="text-lg font-semibold text-foreground">
                    {isDecommissionedManagedRuntime
                      ? tr("ui.stacksIdServersServerId.serverGenerationDecommissioned")
                      : tr("ui.stacksIdServersServerId.serverCleanupInProgress")}
                  </h2>
                  <p class="mt-2 max-w-3xl text-sm text-muted-foreground">
                    {tr("ui.stacksIdServersServerId.theOldProviderGenerationCannot")}
                  </p>
                {:else if hasLegacyOrUnboundCustody()}
                  <h2 class="text-lg font-semibold text-foreground">
                    {tr("ui.stacksIdServersServerId.resolveStaleCustodyRecord")}
                  </h2>
                  <p class="mt-2 max-w-3xl text-sm text-muted-foreground">
                    {tr("ui.stacksIdServersServerId.thisServerIsALegacy")}
                  </p>

                  {#if showCustodyResolutionConfirmation}
                    <div
                      class="mt-4 rounded-lg border border-warning/40 bg-warning/10 p-4"
                      data-testid="server-custody-resolution-confirmation"
                    >
                      <p class="text-sm font-medium text-foreground">
                        {tr("ui.serverDetail.confirmProviderRemoval", { name: details.server.hostname })}
                      </p>
                      <p class="mt-1 text-sm text-muted-foreground">
                        {tr("ui.stacksIdServersServerId.thisOnlyArchivesTheStale")}
                      </p>
                      <div class="mt-4 flex flex-wrap gap-2">
                        <Button
                          variant="secondary"
                          testId="server-custody-resolution-confirm-button"
                          onclick={resolveCustody}
                          disabled={actionLoading !== null}
                        >
                          <Trash2 class="h-4 w-4" />
                          {actionLoading === "resolve-custody"
                            ? tr("ui.custodyLeasesPanel.resolving")
                            : tr("ui.homelabDashboardPage.resolveRecord")}
                        </Button>
                        <Button
                          variant="secondary"
                          onclick={() =>
                            (showCustodyResolutionConfirmation = false)}
                          disabled={actionLoading !== null}
                        >
                          {tr("ui.importExportModal.cancel")}
                        </Button>
                      </div>
                    </div>
                  {:else}
                    <Button
                      variant="secondary"
                      class="mt-4"
                      testId="server-custody-resolution-button"
                      onclick={() => (showCustodyResolutionConfirmation = true)}
                      disabled={actionLoading !== null}
                    >
                      <Trash2 class="h-4 w-4" />
                      {tr("ui.stacksIdServersServerId.resolveStaleRecord")}
                    </Button>
                  {/if}
                {:else}
                  <h2 class="text-lg font-semibold text-foreground">
                    {tr("ui.stacksIdServersServerId.decommissionServer")}
                  </h2>
                  <p class="mt-2 max-w-3xl text-sm text-muted-foreground">
                    {trParts("ui.serverDetail.decommissionIntro")[0]}<strong class="font-medium text-foreground">
                      {details.server.hostname}
                    </strong>{trParts("ui.serverDetail.decommissionIntro")[1]}
                  </p>

                  {#if showDecommissionConfirmation}
                    <div
                      class="mt-4 rounded-lg border border-destructive/40 bg-destructive/10 p-4"
                      data-testid="server-decommission-confirmation"
                    >
                      <p class="text-sm font-medium text-foreground">
                        {tr("ui.serverDetail.confirmDecommissionFor", { name: details.server.hostname })}
                      </p>
                      <p class="mt-1 text-sm text-muted-foreground">
                        {tr("ui.stacksIdServersServerId.servicesOnThisServerMay")}
                      </p>
                      <div class="mt-4 flex flex-wrap gap-2">
                        <Button
                          variant="destructive"
                          testId="server-decommission-confirm-button"
                          onclick={decommissionServer}
                          disabled={actionLoading !== null}
                        >
                          <Trash2 class="h-4 w-4" />
                          {actionLoading === "decommission"
                            ? tr("ui.custodyLeasesPanel.decommissioning")
                            : tr("ui.stacksIdServersServerId.confirmDecommission")}
                        </Button>
                        <Button
                          variant="secondary"
                          onclick={() => (showDecommissionConfirmation = false)}
                          disabled={actionLoading !== null}
                        >
                          {tr("ui.importExportModal.cancel")}
                        </Button>
                      </div>
                    </div>
                  {:else}
                    <Button
                      variant="destructive"
                      class="mt-4"
                      testId="server-decommission-button"
                      onclick={() => (showDecommissionConfirmation = true)}
                      disabled={actionLoading !== null}
                    >
                      <Trash2 class="h-4 w-4" />
                      {tr("ui.stacksIdServersServerId.decommissionServer")}
                    </Button>
                  {/if}
                {/if}
              {:else if canDetachSelfOwnedServer}
                <h2 class="text-lg font-semibold text-foreground">
                  {tr("ui.stacksIdServersServerId.detachSelfOwnedServer")}
                </h2>
                <p class="mt-2 max-w-3xl text-sm text-muted-foreground">
                  {trParts("ui.serverDetail.detachIntro")[0]}<strong class="font-medium text-foreground">
                    {details.server.hostname}
                  </strong>{trParts("ui.serverDetail.detachIntro")[1]}
                </p>

                {#if showDetachConfirmation}
                  <div
                    class="mt-4 rounded-lg border border-destructive/40 bg-destructive/10 p-4"
                    data-testid="server-detach-confirmation"
                  >
                    <p class="text-sm font-medium text-foreground">
                      {tr("ui.serverDetail.confirmDetachFor", { name: details.server.hostname })}
                    </p>
                    <p class="mt-1 text-sm text-muted-foreground">
                      {tr("ui.stacksIdServersServerId.theAgentIdentityWillBe")}
                    </p>
                    <div class="mt-4 flex flex-wrap gap-2">
                      <Button
                        variant="destructive"
                        testId="server-detach-confirm-button"
                        onclick={detachServer}
                        disabled={actionLoading !== null}
                      >
                        <Trash2 class="h-4 w-4" />
                        {actionLoading === "detach"
                          ? tr("ui.stacksIdServersServerId.detaching")
                          : tr("ui.stacksIdServersServerId.confirmDetach")}
                      </Button>
                      <Button
                        variant="secondary"
                        onclick={() => (showDetachConfirmation = false)}
                        disabled={actionLoading !== null}
                      >
                        {tr("ui.importExportModal.cancel")}
                      </Button>
                    </div>
                  </div>
                {:else}
                  <Button
                    variant="destructive"
                    class="mt-4"
                    testId="server-detach-button"
                    onclick={() => (showDetachConfirmation = true)}
                    disabled={actionLoading !== null}
                  >
                    <Trash2 class="h-4 w-4" />
                    {tr("ui.stacksIdServersServerId.detachServer")}
                  </Button>
                {/if}
              {:else}
                <p class="mt-2 text-sm text-muted-foreground">
                  {tr("ui.stacksIdServersServerId.thisServerHasNoManaged")}
                </p>
              {/if}
            </div>
          </div>
        </div>
      </section>
    {/if}
  {/if}
</div>
