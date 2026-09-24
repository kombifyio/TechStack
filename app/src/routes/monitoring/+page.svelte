<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { ArrowRight, RefreshCw, Trash2 } from "@lucide/svelte";

  import { parseApiError } from "#lib/api/errors.js";
  import {
    getAvailabilityReport,
    type AvailabilityReport,
    type AvailabilityWindow,
  } from "#lib/api/monitoring.js";
  import {
    detachSelfOwnedServer,
    listCanonicalServers,
    type CanonicalServer,
  } from "#lib/api/registry.js";
  import {
    listCanonicalServices,
    type CanonicalService,
  } from "#lib/api/services.js";
  import {
    canonicalServerAxes,
    canonicalServerMeta,
  } from "#lib/server-card-adapter.js";
  import { serverStatusAxes } from "@kombiverselabs/ui/server";
  import { PageHeader } from "@kombiverselabs/ui/shell";
  import Button from "#lib/components/ui/Button.svelte";
  import StateRibbon from "#lib/components/monitoring/StateRibbon.svelte";

  const REFRESH_INTERVAL_MS = 30_000;

  let inventoryServers = $state<CanonicalServer[]>([]);
  let canonicalServices = $state<CanonicalService[]>([]);
  let inventoryUnavailable = $state(false);
  let inventoryStale = $state(false);
  let availability = $state<AvailabilityReport | null>(null);
  let availabilityUnavailable = $state(false);
  let availabilityWindow = $state<AvailabilityWindow>("30d");
  let loading = $state(true);
  let error = $state<string | null>(null);
  let pendingDetach = $state<CanonicalServer | null>(null);
  let detachError = $state<string | null>(null);
  let detaching = $state(false);

  let pageActive = true;
  let refreshInterval: ReturnType<typeof setInterval> | null = null;

  const AVAILABILITY_WINDOWS: AvailabilityWindow[] = ["7d", "30d", "90d"];

  /**
   * Status-axis dots reuse the design system's state hues rather than a
   * categorical palette: these are states, not series.
   */
  const AXIS_TONE: Record<string, string> = {
    success: "oklch(0.74 0.15 155)",
    warning: "oklch(0.79 0.14 80)",
    destructive: "oklch(0.67 0.2 25)",
    info: "oklch(0.65 0.15 230)",
    primary: "oklch(0.74 0.15 155)",
    muted: "oklch(0.68 0.02 240)",
  };

  const RIBBON_LEGEND = [
    { label: "connected", fill: "oklch(0.74 0.15 155)" },
    { label: "degraded", fill: "oklch(0.79 0.14 80)" },
    { label: "offline", fill: "oklch(0.67 0.2 25)" },
    { label: "no data", fill: "#262c31" },
  ];

  async function refreshInventory() {
    const [serverResult, serviceResult] = await Promise.allSettled([
      listCanonicalServers(),
      listCanonicalServices(),
    ]);
    if (!pageActive) return;
    if (serverResult.status === "fulfilled") {
      inventoryServers = serverResult.value;
      inventoryUnavailable = false;
      inventoryStale = false;
    } else {
      // Keep the last verified fleet visible and say it is stale. Replacing it
      // with an error hides state we still know and makes a reachable node
      // look like a missing one.
      inventoryUnavailable = inventoryServers.length === 0;
      inventoryStale = inventoryServers.length > 0;
    }
    canonicalServices =
      serviceResult.status === "fulfilled" ? serviceResult.value : [];
  }

  async function refreshAvailability() {
    try {
      const report = await getAvailabilityReport(availabilityWindow);
      if (!pageActive) return;
      availability = report;
      availabilityUnavailable = false;
    } catch {
      // A deployment whose backend predates the projection loses the section
      // rather than showing an invented zero.
      if (pageActive) availabilityUnavailable = true;
    }
  }

  async function refresh() {
    loading = true;
    error = null;
    await refreshInventory();
    await refreshAvailability();
    if (!pageActive) return;
    if (inventoryUnavailable && availabilityUnavailable) {
      error = "Monitoring data could not be loaded.";
    }
    loading = false;
  }

  function selectAvailabilityWindow(next: AvailabilityWindow) {
    if (next === availabilityWindow) return;
    availabilityWindow = next;
    void refreshAvailability();
  }

  /** Null ratio means no observed time — never render it as 0 %. */
  function formatRatio(ratio: number | null | undefined): string {
    if (ratio === null || ratio === undefined) return "no data";
    return `${(ratio * 100).toFixed(ratio >= 0.9995 ? 0 : 2)} %`;
  }

  function formatDuration(seconds: number | null | undefined): string {
    if (seconds === null || seconds === undefined) return "—";
    if (seconds < 60) return `${Math.round(seconds)} s`;
    const minutes = Math.round(seconds / 60);
    if (minutes < 60) return `${minutes} min`;
    const hours = Math.floor(minutes / 60);
    const rest = minutes % 60;
    if (hours < 24)
      return rest ? `${hours} h ${String(rest).padStart(2, "0")}` : `${hours} h`;
    return `${Math.floor(hours / 24)} d ${hours % 24} h`;
  }

  function formatStamp(value: string | undefined): string {
    if (!value) return "—";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "—";
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "short",
      timeStyle: "short",
    }).format(date);
  }

  function serviceLabel(count: number): string {
    return `${count} ${count === 1 ? "service" : "services"}`;
  }

  function serverAllowsDetach(server: CanonicalServer | undefined): boolean {
    return server?.allowed_actions?.includes("detach") === true;
  }

  function requestDetach(server: CanonicalServer) {
    pendingDetach = server;
    detachError = null;
  }

  async function confirmDetach() {
    const server = pendingDetach;
    if (!server || detaching) return;
    detaching = true;
    detachError = null;
    try {
      await detachSelfOwnedServer(server.id);
      pendingDetach = null;
      await refresh();
    } catch (err) {
      detachError = parseApiError(err).message || "Server could not be removed.";
    } finally {
      detaching = false;
    }
  }

  /** Share of the largest cause, so the bars compare against a real maximum. */
  function causeShare(seconds: number): number {
    const largest = availability?.by_cause[0]?.seconds ?? 0;
    if (largest <= 0) return 4;
    return Math.max(4, Math.round((seconds / largest) * 100));
  }

  const inventoryById = $derived.by(() => {
    const byId: Record<string, CanonicalServer> = {};
    for (const server of inventoryServers) {
      byId[server.id] = server;
    }
    return byId;
  });

  const serviceCountByServer = $derived.by(() => {
    const counts: Record<string, number> = {};
    for (const service of canonicalServices) {
      if (!service.server_id) continue;
      counts[service.server_id] = (counts[service.server_id] ?? 0) + 1;
    }
    return counts;
  });

  /** Nodes whose connection axis is not currently carrying service. */
  const offlineCount = $derived(
    inventoryServers.filter((server) => {
      const state = server.connection?.state ?? "";
      return state === "offline" || state === "revoked" || state === "stale";
    }).length,
  );

  /** Episodes across the fleet, worst-first: ongoing before closed. */
  const availabilityEpisodes = $derived.by(() => {
    const rows = (availability?.servers ?? []).flatMap((server) =>
      server.episodes.map((episode) => ({
        ...episode,
        server_name: episode.server_name || server.name,
      })),
    );
    return rows.sort((a, b) => {
      if (a.ongoing !== b.ongoing) return a.ongoing ? -1 : 1;
      return new Date(b.started_at).getTime() - new Date(a.started_at).getTime();
    });
  });

  /**
   * The history section only exists once the availability read resolves, so a
   * `#history` arrival is past the browser's own scroll attempt by the time
   * there is anything to scroll to. Honour the hash when the section appears,
   * once, and never fight a scroll the reader started themselves.
   */
  let hashHandled = false;
  $effect(() => {
    if (hashHandled || !availability) return;
    if (typeof window === "undefined" || window.location.hash !== "#history")
      return;
    const target = document.getElementById("history");
    if (!target) return;
    hashHandled = true;
    target.scrollIntoView({ behavior: "smooth", block: "start" });
  });

  onMount(() => {
    pageActive = true;
    void refresh();
    refreshInterval = setInterval(() => void refresh(), REFRESH_INTERVAL_MS);
  });

  onDestroy(() => {
    pageActive = false;
    if (refreshInterval) clearInterval(refreshInterval);
  });
</script>

<div class="mx-auto max-w-7xl p-4 md:p-6" data-testid="monitoring-page">
  <PageHeader title="Monitoring">
    {#snippet actions()}
      <div class="flex flex-wrap items-center gap-2">
        <Button variant="secondary" onclick={() => refresh()} disabled={loading}>
          <RefreshCw class="h-4 w-4" />
          {loading ? "Refreshing..." : "Refresh"}
        </Button>
      </div>
    {/snippet}
  </PageHeader>

  {#if error}
    <div
      class="mb-6 rounded-lg border border-destructive/40 bg-destructive/10 p-4 text-foreground"
      data-testid="monitoring-error"
    >
      {error}
    </div>
  {/if}

  {#if pendingDetach}
    <div
      class="mb-4 rounded-lg border border-destructive/40 bg-destructive/10 p-4"
      data-testid="monitoring-detach-confirmation"
    >
      <p class="text-sm font-medium text-foreground">
        Remove {pendingDetach.name} from Monitoring?
      </p>
      <p class="mt-1 text-sm text-muted-foreground">
        This revokes the bound Guard Agent and hides the node from the current
        fleet. The physical server and its provider account stay untouched.
      </p>
      {#if detachError}
        <p class="mt-2 text-sm text-destructive" data-testid="monitoring-detach-error">
          {detachError}
        </p>
      {/if}
      <div class="mt-4 flex flex-wrap gap-2">
        <Button
          variant="destructive"
          testId="monitoring-detach-confirm"
          onclick={() => void confirmDetach()}
          disabled={detaching}
        >
          <Trash2 class="h-4 w-4" />
          {detaching ? "Removing..." : "Remove from view"}
        </Button>
        <Button
          variant="secondary"
          onclick={() => {
            pendingDetach = null;
            detachError = null;
          }}
          disabled={detaching}
        >
          Cancel
        </Button>
      </div>
    </div>
  {/if}

  <!--
    Right now: the three orthogonal axes per node. A monitoring home that
    opened on a 30-day retrospective could not answer "is anything on fire",
    so current state comes before history.
  -->
  <section
    class="mb-4 rounded-lg border border-border bg-card p-4"
    data-testid="monitoring-section-right-now"
  >
    <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-2.5">
        <h2 class="text-sm font-semibold text-foreground">Right now</h2>
        {#if offlineCount > 0}
          <span
            class="inline-flex h-[22px] items-center gap-1.5 rounded-full px-2.5 text-[11px] font-semibold"
            style="border: 1px solid color-mix(in srgb, oklch(0.67 0.2 25) 42%, transparent); background: color-mix(in srgb, oklch(0.67 0.2 25) 13%, transparent); color: color-mix(in srgb, oklch(0.67 0.2 25) 84%, var(--foreground))"
            data-testid="monitoring-offline-badge"
          >
            <span
              class="h-1.5 w-1.5 rounded-full"
              style="background: oklch(0.67 0.2 25)"
            ></span>
            {offlineCount} not reporting
          </span>
        {/if}
      </div>
      {#if inventoryStale}
        <span
          class="text-[11.5px] font-medium text-warning"
          data-testid="monitoring-inventory-stale"
        >
          Last verified count retained &mdash; the inventory refresh failed
        </span>
      {:else}
        <span class="text-[11.5px] text-muted-foreground">
          Lifecycle, connection and health are read together and never collapsed
        </span>
      {/if}
    </div>

    {#if inventoryUnavailable}
      <p class="text-sm text-muted-foreground" data-testid="monitoring-inventory-unavailable">
        The canonical server inventory is unavailable.
      </p>
    {:else if inventoryServers.length === 0}
      <p class="text-sm text-muted-foreground">
        No nodes have reported yet. Enroll a node and its state appears here.
      </p>
    {:else}
      <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {#each inventoryServers as server (server.id)}
          <div
            class="flex flex-col rounded-lg border border-border bg-background transition-colors hover:border-primary/50"
            data-testid="monitoring-node-summary"
            data-server-id={server.id}
            data-connection={server.connection?.state ?? "unknown"}
          >
            <a
              href={`/monitoring/${encodeURIComponent(server.id)}`}
              class="flex flex-col gap-2.5 p-3.5"
            >
              <div class="flex items-center justify-between gap-2">
                <span class="truncate text-[13px] font-semibold text-foreground"
                  >{server.name}</span
                >
                <ArrowRight class="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              </div>
              <span class="truncate text-[10.5px] text-muted-foreground">
                {canonicalServerMeta(server)}
              </span>
              <div class="flex flex-wrap gap-x-3.5 gap-y-1.5">
                {#each serverStatusAxes(canonicalServerAxes(server)) as axis (axis.key)}
                  <span
                    class="inline-flex items-center gap-1.5 text-[10.5px] text-muted-foreground"
                    data-testid="monitoring-node-axis"
                    data-axis={axis.key}
                  >
                    <span
                      class="h-[7px] w-[7px] shrink-0 rounded-full"
                      style={`background: ${AXIS_TONE[axis.tone] ?? AXIS_TONE.muted}`}
                    ></span>{axis.label}
                  </span>
                {/each}
              </div>
              <span
                class="font-mono text-[10.5px] text-muted-foreground"
                data-testid="monitoring-node-service-count"
              >
                {serviceLabel(serviceCountByServer[server.id] ?? 0)}
              </span>
            </a>
            {#if serverAllowsDetach(server)}
              <div class="flex justify-end border-t border-border px-3 py-2">
                <Button
                  variant="ghost"
                  size="sm"
                  testId="monitoring-node-detach"
                  ariaLabel={`Remove ${server.name} from Monitoring`}
                  onclick={() => requestDetach(server)}
                  disabled={detaching}
                >
                  <Trash2 class="h-3.5 w-3.5" />
                  Remove
                </Button>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </section>

  <!--
    Availability, downtime and recovery are a projection over the recorded
    transition timeline: every episode is bounded by two committed
    transitions, so it is evidence rather than a sample.
  -->
  {#if availabilityUnavailable}
    <section
      class="mb-4 rounded-lg border border-border bg-card p-4 text-sm text-muted-foreground"
      data-testid="monitoring-availability-unavailable"
    >
      Availability history is unavailable on this deployment.
    </section>
  {:else if availability}
    <div
      class="mb-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4"
      data-testid="monitoring-section-availability"
    >
      <section class="flex flex-col gap-2 rounded-lg border border-border bg-card p-4">
        <span class="text-xs text-muted-foreground"
          >Availability &middot; {availability.window}</span
        >
        <span
          class="text-[32px] font-semibold leading-none tracking-tight text-foreground"
          data-testid="monitoring-availability-ratio"
          >{formatRatio(availability.uptime_ratio)}</span
        >
        <span class="font-mono text-[11px] text-muted-foreground">
          over {formatDuration(availability.observed_seconds)} observed
        </span>
      </section>

      <section class="flex flex-col gap-2 rounded-lg border border-border bg-card p-4">
        <span class="text-xs text-muted-foreground">Total downtime</span>
        <span
          class="text-[32px] font-semibold leading-none tracking-tight text-foreground"
          >{formatDuration(availability.down_seconds)}</span
        >
        <span class="font-mono text-[11px] text-muted-foreground">
          across {availabilityEpisodes.length} episodes
        </span>
      </section>

      <section class="flex flex-col gap-2 rounded-lg border border-border bg-card p-4">
        <span class="text-xs text-muted-foreground">Mean time to recover</span>
        <span
          class="text-[32px] font-semibold leading-none tracking-tight text-foreground"
          >{formatDuration(availability.mttr_seconds)}</span
        >
        <span class="font-mono text-[11px] text-muted-foreground">
          {availability.closed_episodes} recovered &middot; ongoing excluded
        </span>
      </section>

      <section class="flex flex-col gap-2.5 rounded-lg border border-border bg-card p-4">
        <span class="text-xs text-muted-foreground">Downtime by cause</span>
        {#if availability.by_cause.length === 0}
          <span class="text-[11.5px] text-muted-foreground"
            >No downtime in this window.</span
          >
        {:else}
          <div class="flex flex-col gap-2">
            {#each availability.by_cause.slice(0, 3) as cause (cause.reason_code)}
              <div class="flex items-center gap-2.5">
                <span
                  class="w-[104px] shrink-0 truncate font-mono text-[10.5px] text-muted-foreground"
                  title={cause.reason_code}>{cause.reason_code}</span
                >
                <div class="h-2 flex-1 overflow-hidden rounded-full bg-muted">
                  <span
                    class="block h-2 rounded-full bg-destructive"
                    style={`width: ${causeShare(cause.seconds)}%`}
                  ></span>
                </div>
                <span class="shrink-0 font-mono text-[10.5px] text-foreground"
                  >{formatDuration(cause.seconds)}</span
                >
              </div>
            {/each}
          </div>
        {/if}
      </section>
    </div>

    <!-- Anchor target for "Open history" on the dashboard. -->
    <section
      id="history"
      class="mb-4 scroll-mt-4 rounded-lg border border-border bg-card p-4"
      data-testid="monitoring-section-ribbons"
    >
      <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div class="flex items-baseline gap-3">
          <h2 class="text-[15px] font-semibold text-foreground">Daily state</h2>
          <span class="text-[11.5px] text-muted-foreground"
            >select a node for its metrics and its own history</span
          >
        </div>
        <div class="flex flex-wrap items-center gap-4">
          <div class="flex items-center gap-1">
            {#each AVAILABILITY_WINDOWS as option (option)}
              <button
                type="button"
                class="h-7 rounded-md px-3 text-xs font-medium transition-colors"
                class:bg-muted={availabilityWindow === option}
                class:text-foreground={availabilityWindow === option}
                class:text-muted-foreground={availabilityWindow !== option}
                aria-pressed={availabilityWindow === option}
                data-testid="monitoring-window-option"
                onclick={() => selectAvailabilityWindow(option)}>{option}</button
              >
            {/each}
          </div>
          <div class="flex items-center gap-3">
            {#each RIBBON_LEGEND as entry (entry.label)}
              <span
                class="inline-flex items-center gap-1.5 text-[11.5px] text-muted-foreground"
              >
                <span
                  class="h-[9px] w-[9px] rounded-sm"
                  style={`background: ${entry.fill}`}
                ></span>{entry.label}
              </span>
            {/each}
          </div>
        </div>
      </div>

      {#if availability.servers.length === 0}
        <p class="text-sm text-muted-foreground">No servers in this window.</p>
      {:else}
        <div class="flex flex-col gap-1.5">
          {#each availability.servers as server (server.server_id)}
            <div
              class="flex items-center gap-4 rounded-lg px-2 py-1.5 transition-colors hover:bg-muted"
              data-testid="monitoring-ribbon-row"
              data-server-id={server.server_id}
            >
              <a
                href={`/monitoring/${encodeURIComponent(server.server_id)}`}
                class="grid min-w-0 flex-1 grid-cols-[176px_minmax(0,1fr)_112px] items-center gap-4"
              >
                <div class="flex min-w-0 flex-col gap-0.5">
                  <span class="truncate text-[12.5px] font-semibold text-foreground"
                    >{server.name || server.server_id}</span
                  >
                  <span class="font-mono text-[10.5px] text-muted-foreground">
                    {serviceLabel(serviceCountByServer[server.server_id] ?? 0)}
                  </span>
                </div>
                <StateRibbon
                  days={server.days}
                  label={`${server.name || server.server_id}: daily connection state`}
                />
                <div class="flex flex-col items-end gap-0.5">
                  <span class="font-mono text-[13px] font-semibold text-foreground"
                    >{formatRatio(server.uptime_ratio)}</span
                  >
                  <span class="font-mono text-[10.5px] text-muted-foreground"
                    >{formatDuration(server.down_seconds)} down</span
                  >
                </div>
              </a>
              <div class="flex shrink-0 items-center gap-1">
                {#if serverAllowsDetach(inventoryById[server.server_id])}
                  <Button
                    variant="ghost"
                    size="sm"
                    testId="monitoring-ribbon-detach"
                    ariaLabel={`Remove ${server.name || server.server_id} from Monitoring`}
                    onclick={() => {
                      const current = inventoryById[server.server_id];
                      if (current) requestDetach(current);
                    }}
                    disabled={detaching}
                  >
                    <Trash2 class="h-3.5 w-3.5" />
                  </Button>
                {/if}
                <ArrowRight class="h-3.5 w-3.5 text-muted-foreground" />
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </section>

    <section
      class="rounded-lg border border-border bg-card p-4"
      data-testid="monitoring-section-episodes"
    >
      <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
        <h2 class="text-[15px] font-semibold text-foreground">
          Downtime episodes
        </h2>
        <span class="text-[11.5px] text-muted-foreground">
          stale counts as downtime &mdash; service cannot be confirmed
        </span>
      </div>

      {#if availabilityEpisodes.length === 0}
        <p class="text-sm text-muted-foreground">
          No recorded downtime in this window.
        </p>
      {:else}
        <div class="flex flex-col">
          {#each availabilityEpisodes.slice(0, 12) as episode (episode.server_id + episode.started_at)}
            <div
              class="grid grid-cols-[160px_190px_88px_minmax(0,1fr)_128px] items-center gap-4 border-t border-border py-2.5 first:border-t-0"
              data-testid="monitoring-episode-row"
            >
              <div class="flex min-w-0 items-center gap-2.5">
                <span
                  class="h-2 w-2 shrink-0 rounded-full"
                  style={`background: ${episode.ongoing ? "oklch(0.67 0.2 25)" : "oklch(0.79 0.14 80)"}`}
                ></span>
                <span class="truncate text-[12.5px] font-medium text-foreground"
                  >{episode.server_name || episode.server_id}</span
                >
              </div>
              <span class="font-mono text-[11.5px] text-muted-foreground">
                {formatStamp(episode.started_at)} &rarr; {episode.ongoing
                  ? "ongoing"
                  : formatStamp(episode.ended_at)}
              </span>
              <span class="font-mono text-xs font-semibold text-foreground"
                >{formatDuration(episode.seconds)}</span
              >
              <span class="truncate text-[12.5px] text-foreground">
                <span class="font-mono"
                  >{episode.trigger_reason || "unrecorded"}</span
                >
                {#if episode.ongoing}
                  <span class="text-muted-foreground"
                    >&middot; not yet recovered</span
                  >
                {:else if episode.recovery_reason}
                  <span class="text-muted-foreground"
                    >&middot; recovered by <span class="font-mono"
                      >{episode.recovery_reason}</span
                    ></span
                  >
                {/if}
              </span>
              <span
                class="truncate text-right font-mono text-[11px] text-muted-foreground"
                title={episode.evidence_ref || episode.state}
                >{episode.evidence_ref || episode.state}</span
              >
            </div>
          {/each}
        </div>
      {/if}
    </section>
  {/if}
</div>
