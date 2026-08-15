<script lang="ts">
  import { page } from "$app/stores";
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { parseApiError } from "$lib/api/errors";
  import {
    getRuntimeLogs,
    streamRuntimeLogs,
    type RuntimeLogEntry,
  } from "$lib/api/runtime-logs";
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
  } from "$lib/api/stacks";
  import {
    serverLifecycleActions,
    type ServerLifecycleAction,
  } from "$lib/server-lifecycle-actions";
  import { isLegacyOrUnboundCustody } from "$lib/custody/server-custody";
  import {
    openServiceUrl,
    serviceCardMeta,
    serviceCardName,
    serviceCardPlacement,
    serviceCardStatus,
  } from "$lib/service-card-adapter";
  import { ServiceCardCompact } from "$lib/components/open-core";
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

  const stackId = $derived($page.params.id);
  const serverId = $derived($page.params.serverId);

  let details = $state<StackServerDetailsPayload | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  let showDecommissionConfirmation = $state(false);
  let showCustodyResolutionConfirmation = $state(false);
  let actionLoading = $state<
    "ssh" | "reconnect" | "decommission" | "resolve-custody" | null
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
    "overview" | "services" | "checks" | "logs" | "settings"
  >("overview");
  let availableLifecycleActions = $derived.by(() =>
    details ? serverLifecycleActions(details.server) : [],
  );
  let pendingLifecycleAction = $derived(
    availableLifecycleActions.find(
      (action) => action.operation === lifecycleConfirmation,
    ) ?? null,
  );

  onMount(() => {
    void load();
  });

  $effect(() => {
    if (stackId && serverId) {
      void load();
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
      if (active) runtimeLogsError = "Live stream reconnecting…";
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
    loading = true;
    error = null;
    try {
      details = await getStackServerDetails(stackId, serverId);
    } catch (err) {
      const parsed = parseApiError(err);
      error = parsed.message;
    } finally {
      loading = false;
    }
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
      actionError = parsed.message || "Could not load server access context.";
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
      actionError = parsed.message || "Reconnect failed.";
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
      actionError = parsed.message || "Decommission failed.";
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
      actionError = parsed.message || "Custody resolution failed.";
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
      actionError = parsed.message || `${action.label} could not be started.`;
    } finally {
      lifecycleLoading = null;
    }
  }

  function badgeClass(status: string): string {
    switch (status) {
      case "healthy":
      case "running":
      case "passed":
      case "ok":
        return "badge badge-success";
      case "stale":
      case "pending":
      case "unknown":
        return "badge badge-warning";
      case "offline":
      case "failed":
      case "error":
      case "degraded":
        return "badge badge-destructive";
      default:
        return "badge badge-secondary";
    }
  }

  function formatMetric(metric: StackMetricValue | undefined): string {
    if (!metric || metric.value === undefined || metric.status !== "ok") {
      return "unknown";
    }
    return `${metric.value}${metric.unit || ""}`;
  }

  function formatStatus(status: string): string {
    return status.replace(/_/g, " ");
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
    const os = server.os?.trim() || "os unknown";
    const version = server.os_version?.trim();
    const osWithVersion =
      version && !os.toLowerCase().includes(version.toLowerCase())
        ? `${os} ${version}`
        : os;
    return `${osWithVersion}/${server.arch?.trim() || "arch unknown"}`;
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
    return stackId
      ? `/stacks?stack=${encodeURIComponent(stackId)}&phase=review`
      : "/stacks";
  }

  function setTab(tab: string) {
    if (
      tab === "overview" ||
      tab === "services" ||
      tab === "checks" ||
      tab === "logs" ||
      tab === "settings"
    ) {
      activeTab = tab;
    }
  }
</script>

<svelte:head>
  <title>Server Details | kombify-TechStack</title>
</svelte:head>

<div class="mx-auto max-w-7xl p-6 md:p-8" data-testid="server-details-page">
  <button class="btn btn-ghost mb-6" onclick={() => goto(backHref())}>
    <ArrowLeft class="h-4 w-4" />
    Back to operations
  </button>

  {#if loading}
    <div class="rounded-lg border border-border bg-card p-6">
      <div class="h-6 w-48 animate-pulse rounded bg-muted"></div>
      <div class="mt-4 grid gap-3 md:grid-cols-4">
        {#each Array(4) as _, i (i)}
          <div class="h-24 animate-pulse rounded-lg bg-muted/50"></div>
        {/each}
      </div>
    </div>
  {:else if error}
    <div class="rounded-lg border border-destructive/30 bg-destructive/10 p-4">
      <p class="text-foreground">{error}</p>
    </div>
  {:else if details}
    <nav
      class="mb-6 border-b border-border"
      aria-label="Server detail sections"
      data-testid="server-details-tabs"
    >
      <div class="flex flex-wrap gap-1" role="tablist">
        {#each ["overview", "services", "checks", "logs", "settings"] as tab (tab)}
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
      <header class="mb-6 rounded-lg border border-border bg-card p-5">
        <div
          class="flex flex-col gap-4 md:flex-row md:items-start md:justify-between"
        >
          <div class="min-w-0">
            <div class="mb-2 flex flex-wrap items-center gap-2">
              <span class={badgeClass(details.server.health.state)}>
                {formatStatus(details.server.health.state)}
              </span>
              <span class="badge badge-secondary">{details.server.role}</span>
              <span class="badge badge-secondary">
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
              <button
                class="btn btn-secondary"
                data-testid="server-access-button"
                onclick={requestSSHInfo}
                disabled={actionLoading !== null}
              >
                <ShieldCheck class="h-4 w-4" />
                {actionLoading === "ssh" ? "Checking..." : "Access"}
              </button>
              <button
                class="btn btn-secondary"
                data-testid="server-reconnect-button"
                onclick={reconnectServer}
                disabled={actionLoading !== null}
              >
                <RefreshCw class="h-4 w-4" />
                {actionLoading === "reconnect"
                  ? "Reconnecting..."
                  : "Reconnect"}
              </button>
            {/if}
            <button class="btn btn-secondary" onclick={load}>
              <Activity class="h-4 w-4" />
              Refresh
            </button>
          </div>
        </div>
      </header>

      <div class="mb-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div class="rounded-lg border border-border bg-card p-4">
          <div class="mb-3 flex items-center justify-between">
            <span class="text-sm text-muted-foreground">CPU</span>
            <Gauge class="h-4 w-4 text-primary" />
          </div>
          <p class="text-2xl font-semibold text-foreground">
            {formatMetric(details.health.cpu_percent)}
          </p>
        </div>
        <div class="rounded-lg border border-border bg-card p-4">
          <div class="mb-3 flex items-center justify-between">
            <span class="text-sm text-muted-foreground">Memory</span>
            <Server class="h-4 w-4 text-success" />
          </div>
          <p class="text-2xl font-semibold text-foreground">
            {formatMetric(details.health.memory_percent)}
          </p>
        </div>
        <div class="rounded-lg border border-border bg-card p-4">
          <div class="mb-3 flex items-center justify-between">
            <span class="text-sm text-muted-foreground">Disk</span>
            <HardDrive class="h-4 w-4 text-info" />
          </div>
          <p class="text-2xl font-semibold text-foreground">
            {formatMetric(details.health.disk_percent)}
          </p>
        </div>
        <div class="rounded-lg border border-border bg-card p-4">
          <div class="mb-3 flex items-center justify-between">
            <span class="text-sm text-muted-foreground">Pre-checks</span>
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
        <div class="rounded-lg border border-border bg-card p-5">
          <div class="mb-4 flex items-center gap-2">
            <ShieldCheck class="h-5 w-5 text-primary" />
            <h2 class="text-lg font-semibold text-foreground">Access</h2>
          </div>
          {#if managedLeaseId()}
            <dl class="grid grid-cols-2 gap-3 text-sm">
              <div class="rounded-lg bg-muted/40 p-3">
                <dt class="text-muted-foreground">Host</dt>
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
              Managed runtime lease context.
            </p>
          {:else}
            <p class="text-sm text-muted-foreground">
              This server is registered through the worker inventory; managed
              runtime access actions are not attached.
            </p>
          {/if}
        </div>

        <div class="rounded-lg border border-border bg-card p-5">
          <div class="mb-4 flex items-center gap-2">
            <Server class="h-5 w-5 text-primary" />
            <h2 class="text-lg font-semibold text-foreground">Metadata</h2>
          </div>
          <dl class="grid grid-cols-2 gap-3 text-sm">
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">Agent ID</dt>
              <dd class="mt-1 break-all font-mono text-foreground">
                {details.server.agent_id}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">Last seen</dt>
              <dd class="mt-1 text-foreground">
                {details.server.last_seen || "unknown"}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">CPU cores</dt>
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
              <dt class="text-muted-foreground">Provider</dt>
              <dd class="mt-1 text-foreground">
                {details.server.capabilities.provider || "unknown"}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">Operating system</dt>
              <dd class="mt-1 text-foreground">
                {details.server.os || "unknown"}
                {details.server.os_version
                  ? ` ${details.server.os_version}`
                  : ""}
              </dd>
            </div>
            <div class="rounded-lg bg-muted/40 p-3">
              <dt class="text-muted-foreground">Architecture</dt>
              <dd class="mt-1 text-foreground">
                {details.server.arch || "unknown"}
              </dd>
            </div>
          </dl>
        </div>

        <div
          class="rounded-lg border border-border bg-card p-5"
          data-testid="server-lifecycle-actions"
        >
          <div class="flex items-start gap-3">
            <ListChecks class="mt-0.5 h-5 w-5 shrink-0 text-primary" />
            <div class="min-w-0 flex-1">
              <h2 class="text-lg font-semibold text-foreground">
                StackKit actions
              </h2>
              <p class="mt-2 max-w-3xl text-sm text-muted-foreground">
                These actions target {details.server.hostname} directly. The available
                controls follow its current connection and StackKit state; no server
                or lifecycle status needs to be selected.
              </p>

              {#if availableLifecycleActions.length === 0}
                <p
                  class="mt-4 rounded-lg bg-muted/40 p-3 text-sm text-muted-foreground"
                  data-testid="server-lifecycle-unavailable"
                >
                  Lifecycle actions become available when this server is
                  connected, approved, and assigned to the stack.
                </p>
              {:else}
                <div class="mt-4 grid gap-3 sm:grid-cols-2">
                  {#each availableLifecycleActions as action (action.operation)}
                    <button
                      type="button"
                      class={action.mutates
                        ? "btn btn-secondary h-auto justify-start px-4 py-3 text-left"
                        : "btn btn-outline h-auto justify-start px-4 py-3 text-left"}
                      data-testid={`server-lifecycle-${action.operation}`}
                      onclick={() => requestLifecycleAction(action)}
                      disabled={lifecycleLoading !== null ||
                        actionLoading !== null}
                    >
                      <span>
                        <span class="block font-medium">
                          {lifecycleLoading === action.operation
                            ? "Starting..."
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
                    Confirm {pendingLifecycleAction.label.toLowerCase()} for
                    {details.server.hostname}
                  </p>
                  <p class="mt-1 text-sm text-muted-foreground">
                    This changes the selected server through its enrolled
                    StackKit agent.
                  </p>
                  <div class="mt-4 flex flex-wrap gap-2">
                    <button
                      type="button"
                      class="btn btn-primary"
                      data-testid="server-lifecycle-confirm-button"
                      onclick={() =>
                        executeLifecycleAction(pendingLifecycleAction)}
                      disabled={lifecycleLoading !== null}
                    >
                      Confirm action
                    </button>
                    <button
                      type="button"
                      class="btn btn-secondary"
                      onclick={() => (lifecycleConfirmation = null)}
                      disabled={lifecycleLoading !== null}
                    >
                      Cancel
                    </button>
                  </div>
                </div>
              {/if}

              {#if lifecycleAccepted}
                <div
                  class="mt-4 rounded-lg border border-success/30 bg-success/10 p-3 text-sm"
                  role="status"
                  data-testid="server-lifecycle-accepted"
                >
                  Job {lifecycleAccepted.job_id} was accepted for this server.
                </div>
              {/if}
            </div>
          </div>
        </div>

        <div
          class="rounded-lg border border-border bg-card p-5"
          data-testid="server-stackkit-details"
        >
          <div class="mb-4 flex items-center justify-between gap-2">
            <div class="flex items-center gap-2">
              <ClipboardCheck class="h-5 w-5 text-primary" />
              <h2 class="text-lg font-semibold text-foreground">StackKit</h2>
            </div>
            {#if details.server.stackkit}
              <span class={badgeClass(details.server.stackkit.state)}>
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
              Evidence: {details.server.stackkit.sources.length
                ? details.server.stackkit.sources.join(", ")
                : "source not reported"}
            </p>
            {#if details.server.stackkit.state !== "observed"}
              <p
                class="mt-3 rounded-lg border border-warning/30 bg-warning/10 p-3 text-sm text-muted-foreground"
              >
                This is configured StackKit intent; the Guard has not reported
                matching deployment evidence yet.
              </p>
            {/if}
          {:else}
            <p class="text-sm text-muted-foreground">
              No StackKit deployment evidence has been reported by this server.
            </p>
          {/if}
        </div>

        <div
          class="rounded-lg border border-border bg-card p-5"
          data-testid="server-network-details"
        >
          <div class="mb-4 flex items-center gap-2">
            <Server class="h-5 w-5 text-primary" />
            <h2 class="text-lg font-semibold text-foreground">
              Addresses & domains
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
                    <span class="badge badge-secondary">
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
              No host address has been reported.
            </p>
          {/if}
          <div class="mt-4 flex flex-wrap gap-2" data-testid="server-domains">
            {#each details.server.domains || [] as domain (domain)}
              <span class="badge badge-secondary break-all">{domain}</span>
            {:else}
              <span class="text-sm text-muted-foreground">
                No service domains reported.
              </span>
            {/each}
          </div>
        </div>

        <div
          class="rounded-lg border border-border bg-card p-5 lg:col-span-2"
          data-testid="server-service-endpoints"
        >
          <div class="mb-4 flex items-center gap-2">
            <Activity class="h-5 w-5 text-info" />
            <h2 class="text-lg font-semibold text-foreground">
              Service endpoints
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
                      {endpoint.name || endpoint.service_key || "Service"}
                    </p>
                    <span class={badgeClass(endpoint.health || "unknown")}>
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
              No observed service endpoints have been reported by this server.
            </p>
          {/if}
        </div>

        <div class="rounded-lg border border-border bg-card p-5">
          <div class="mb-4 flex items-center gap-2">
            <CheckCircle2 class="h-5 w-5 text-success" />
            <h2 class="text-lg font-semibold text-foreground">Health</h2>
          </div>
          <p class="text-sm text-muted-foreground">
            Source: {details.health.source}. Monitoring backend: {details
              .monitoring.queryBackend}.
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
      <section class="rounded-lg border border-border bg-card p-5">
        <div class="mb-4 flex items-center gap-2">
          <ClipboardCheck class="h-5 w-5 text-info" />
          <h2 class="text-lg font-semibold text-foreground">Services</h2>
        </div>
        {#if details.services.length === 0}
          <p class="text-sm text-muted-foreground">
            No service placement is recorded for this server.
          </p>
        {:else}
          <div class="grid gap-2 md:grid-cols-2">
            {#each details.services as service (service.id || service.name)}
              <ServiceCardCompact
                name={serviceCardName(service)}
                meta={serviceCardMeta(service)}
                placement={serviceCardPlacement(service)}
                status={serviceCardStatus(service)}
                showGrip={false}
                onOpen={service.url ? () => openServiceUrl(service) : undefined}
              />
            {/each}
          </div>
        {/if}
      </section>
    {:else if activeTab === "checks"}
      <section class="rounded-lg border border-border bg-card p-5">
        <div class="mb-4 flex items-center gap-2">
          <ListChecks class="h-5 w-5 text-warning" />
          <h2 class="text-lg font-semibold text-foreground">Checks</h2>
        </div>
        {#if details.checks.length === 0}
          <p class="text-sm text-muted-foreground">
            No pre-check result is recorded yet.
          </p>
        {:else}
          <div class="space-y-3">
            {#each details.checks as check (check.id)}
              <div class="rounded-lg border border-border bg-background/40 p-3">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <p class="font-medium text-foreground">{check.check_type}</p>
                  <span class={badgeClass(check.status)}
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
      <section class="rounded-lg border border-border bg-card p-5">
        <div class="mb-4 flex items-center gap-2">
          <TerminalSquare class="h-5 w-5 text-primary" />
          <h2 class="text-lg font-semibold text-foreground">Logs</h2>
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
                    >{new Date(log.timestamp).toLocaleString()}</span
                  >
                </div>
                <p class="mt-1 whitespace-pre-wrap break-words text-foreground">
                  {log.message}
                </p>
                {#if log.job_id}
                  <p class="mt-1 text-muted-foreground">Job {log.job_id}</p>
                {/if}
              </div>
            {/each}
          </div>
        {:else if details.logs.length === 0}
          <p class="text-sm text-muted-foreground">
            No server, installer, or StackKits logs are recorded yet.
          </p>
        {/if}
        {#if details.logs.length > 0}
          <h3 class="mb-2 text-sm font-medium text-muted-foreground">
            Activity history
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
        <div class="rounded-lg border border-border bg-card p-5">
          <div class="flex items-center gap-2">
            <Settings2 class="h-5 w-5 text-primary" />
            <h1
              id="server-settings-heading"
              class="text-lg font-semibold text-foreground"
            >
              Server settings
            </h1>
          </div>
          <p class="mt-2 text-sm text-muted-foreground">
            Manage settings that affect this server's lifecycle. Routine status
            and service controls remain in their respective tabs.
          </p>
        </div>

        <div
          class="rounded-lg border border-destructive/40 bg-card p-5"
          data-testid="server-danger-zone"
        >
          <div class="flex items-start gap-3">
            <Trash2 class="mt-0.5 h-5 w-5 shrink-0 text-destructive" />
            <div class="min-w-0 flex-1">
              {#if managedLeaseId()}
                {#if hasLegacyOrUnboundCustody()}
                  <h2 class="text-lg font-semibold text-foreground">
                    Resolve stale custody record
                  </h2>
                  <p class="mt-2 max-w-3xl text-sm text-muted-foreground">
                    This server is a legacy or unbound custody record. Confirm
                    that the provider resource has already been removed;
                    Techstack will archive only this record and will not call or
                    delete a provider resource.
                  </p>

                  {#if showCustodyResolutionConfirmation}
                    <div
                      class="mt-4 rounded-lg border border-warning/40 bg-warning/10 p-4"
                      data-testid="server-custody-resolution-confirmation"
                    >
                      <p class="text-sm font-medium text-foreground">
                        Confirm provider removal for {details.server.hostname}
                      </p>
                      <p class="mt-1 text-sm text-muted-foreground">
                        This only archives the stale Techstack custody record.
                        It does not delete anything at the provider.
                      </p>
                      <div class="mt-4 flex flex-wrap gap-2">
                        <button
                          type="button"
                          class="btn btn-warning"
                          data-testid="server-custody-resolution-confirm-button"
                          onclick={resolveCustody}
                          disabled={actionLoading !== null}
                        >
                          <Trash2 class="h-4 w-4" />
                          {actionLoading === "resolve-custody"
                            ? "Resolving..."
                            : "Resolve record"}
                        </button>
                        <button
                          type="button"
                          class="btn btn-secondary"
                          onclick={() =>
                            (showCustodyResolutionConfirmation = false)}
                          disabled={actionLoading !== null}
                        >
                          Cancel
                        </button>
                      </div>
                    </div>
                  {:else}
                    <button
                      type="button"
                      class="btn btn-outline mt-4 text-warning hover:bg-warning/10 hover:text-warning"
                      data-testid="server-custody-resolution-button"
                      onclick={() => (showCustodyResolutionConfirmation = true)}
                      disabled={actionLoading !== null}
                    >
                      <Trash2 class="h-4 w-4" />
                      Resolve stale record
                    </button>
                  {/if}
                {:else}
                  <h2 class="text-lg font-semibold text-foreground">
                    Decommission server
                  </h2>
                  <p class="mt-2 max-w-3xl text-sm text-muted-foreground">
                    Request the controlled removal of
                    <strong class="font-medium text-foreground">
                      {details.server.hostname}
                    </strong>. Techstack keeps the provider custody record until
                    absence is verified. This action is intentionally available
                    only here.
                  </p>

                  {#if showDecommissionConfirmation}
                    <div
                      class="mt-4 rounded-lg border border-destructive/40 bg-destructive/10 p-4"
                      data-testid="server-decommission-confirmation"
                    >
                      <p class="text-sm font-medium text-foreground">
                        Confirm decommission for {details.server.hostname}
                      </p>
                      <p class="mt-1 text-sm text-muted-foreground">
                        Services on this server may become unavailable. The
                        request cannot be treated as complete until provider
                        absence has been verified.
                      </p>
                      <div class="mt-4 flex flex-wrap gap-2">
                        <button
                          type="button"
                          class="btn btn-destructive"
                          data-testid="server-decommission-confirm-button"
                          onclick={decommissionServer}
                          disabled={actionLoading !== null}
                        >
                          <Trash2 class="h-4 w-4" />
                          {actionLoading === "decommission"
                            ? "Decommissioning..."
                            : "Confirm decommission"}
                        </button>
                        <button
                          type="button"
                          class="btn btn-secondary"
                          onclick={() => (showDecommissionConfirmation = false)}
                          disabled={actionLoading !== null}
                        >
                          Cancel
                        </button>
                      </div>
                    </div>
                  {:else}
                    <button
                      type="button"
                      class="btn btn-outline mt-4 text-destructive hover:bg-destructive/10 hover:text-destructive"
                      data-testid="server-decommission-button"
                      onclick={() => (showDecommissionConfirmation = true)}
                      disabled={actionLoading !== null}
                    >
                      <Trash2 class="h-4 w-4" />
                      Decommission server
                    </button>
                  {/if}
                {/if}
              {:else}
                <p class="mt-2 text-sm text-muted-foreground">
                  This server has no managed provider lease. Managed
                  decommission is therefore not available for this server.
                </p>
              {/if}
            </div>
          </div>
        </div>
      </section>
    {/if}
  {/if}
</div>
