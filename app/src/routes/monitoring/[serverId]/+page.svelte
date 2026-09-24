<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { ArrowLeft, Trash2 } from "@lucide/svelte";

  import { parseApiError } from "#lib/api/errors.js";
  import {
    getAvailabilityReport,
    getAlertRules,
    rangeQuery,
    type AlertState,
    type AvailabilityReport,
    type AvailabilityWindow,
    type PromQLPoint,
  } from "#lib/api/monitoring.js";
  import {
    detachSelfOwnedServer,
    getCanonicalServer,
    type CanonicalServer,
  } from "#lib/api/registry.js";
  import { canonicalServerAxes } from "#lib/server-card-adapter.js";
  import { serverStatusAxes } from "@kombiverselabs/ui/server";
  import Button from "#lib/components/ui/Button.svelte";
  import MetricSparkline from "#lib/components/monitoring/MetricSparkline.svelte";
  import StateRibbon from "#lib/components/monitoring/StateRibbon.svelte";

  const REFRESH_INTERVAL_MS = 30_000;

  const serverId = $derived(page.params.serverId ?? "");

  let server = $state<CanonicalServer | null>(null);
  let availability = $state<AvailabilityReport | null>(null);
  let alerts = $state<AlertState[]>([]);
  let panels = $state<Record<string, PromQLPoint[]>>({});
  let loading = $state(true);
  let error = $state<string | null>(null);
  let metricsUnavailable = $state(false);
  let availabilityUnavailable = $state(false);
  let pageActive = true;
  let refreshInterval: ReturnType<typeof setInterval> | null = null;
  let showDetachConfirmation = $state(false);
  let detachError = $state<string | null>(null);
  let detaching = $state(false);

  function serverAllowsDetach(current: CanonicalServer | null): boolean {
    return current?.allowed_actions?.includes("detach") === true;
  }

  async function confirmDetach() {
    const current = server;
    if (!current || detaching || !serverAllowsDetach(current)) return;
    detaching = true;
    detachError = null;
    try {
      await detachSelfOwnedServer(current.id);
      showDetachConfirmation = false;
      await goto("/monitoring");
    } catch (err) {
      detachError = parseApiError(err).message || "Server could not be removed.";
    } finally {
      detaching = false;
    }
  }

  /** Wire enums are snake_case; the header reads them as words. */
  function humanize(value: string | undefined): string {
    return (value ?? "").replace(/_/g, " ");
  }

  const AXIS_TONE: Record<string, string> = {
    success: "oklch(0.74 0.15 155)",
    warning: "oklch(0.79 0.14 80)",
    destructive: "oklch(0.67 0.2 25)",
    info: "oklch(0.65 0.15 230)",
    primary: "oklch(0.74 0.15 155)",
    muted: "oklch(0.68 0.02 240)",
  };

  /**
   * Every panel is exactly one range query, scoped to this node, and the query
   * is shown on the panel. That is what keeps the build-or-embed decision
   * open: the same panel definition can move to any PromQL-speaking frontend.
   */
  const PANELS = [
    {
      id: "cpu",
      title: "CPU utilisation",
      query: (id: string) =>
        `node_cpu_usage_percent{node_id="${id}",core="total"}`,
      unit: "%",
      max: 100,
      threshold: 90,
    },
    {
      id: "memory",
      title: "Memory used",
      query: (id: string) => `node_memory_usage_percent{node_id="${id}"}`,
      unit: "%",
      max: 100,
      threshold: 90,
    },
    {
      id: "disk",
      title: "Disk used",
      query: (id: string) => `node_disk_usage_percent{node_id="${id}"}`,
      unit: "%",
      max: 100,
      threshold: 90,
    },
    {
      id: "containers",
      title: "Containers running",
      query: (id: string) => `sum(container_running{node_id="${id}"})`,
      unit: "",
    },
  ] as const;

  function latest(points: PromQLPoint[]): number | null {
    return points.length ? points[points.length - 1].value : null;
  }

  function peak(points: PromQLPoint[]): number | null {
    return points.length ? Math.max(...points.map((point) => point.value)) : null;
  }

  function formatValue(value: number | null, unit: string): string {
    if (value === null) return "—";
    const rounded = Math.abs(value) >= 100 ? Math.round(value) : Number(value.toFixed(1));
    return unit ? `${rounded} ${unit}` : String(rounded);
  }

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

  /** Nanoseconds, as the alert rule records them. */
  function formatFor(nanoseconds: number): string {
    const seconds = Math.round(nanoseconds / 1_000_000_000);
    if (seconds === 0) return "0 s";
    return seconds < 60 ? `${seconds} s` : `${Math.round(seconds / 60)} m`;
  }

  const serverReport = $derived(
    availability?.servers.find((entry) => entry.server_id === serverId) ?? null,
  );

  /**
   * Alert rules whose expression mentions this node, plus the fleet-wide rules
   * that carry no node scope. A rule that names another node is not this
   * node's business.
   */
  const relevantAlerts = $derived(
    alerts.filter(
      (alert) =>
        !alert.rule.expr.includes("node_id=") ||
        alert.rule.expr.includes(`node_id="${serverId}"`),
    ),
  );

  async function loadMetrics(id: string) {
    const end = new Date();
    const start = new Date(end.getTime() - 24 * 60 * 60 * 1000);
    const results = await Promise.allSettled(
      PANELS.map((panel) =>
        rangeQuery(panel.query(id), start.toISOString(), end.toISOString(), "2m"),
      ),
    );
    if (!pageActive) return;

    const next: Record<string, PromQLPoint[]> = {};
    let anySucceeded = false;
    results.forEach((result, index) => {
      const panel = PANELS[index];
      if (result.status !== "fulfilled") {
        next[panel.id] = [];
        return;
      }
      anySucceeded = true;
      // One query can carry several series (one per mount, per core). The
      // panel plots the busiest one and says so, rather than averaging
      // unrelated series into a number nothing actually measured.
      const series = result.value.series ?? [];
      let best: PromQLPoint[] = [];
      for (const entry of series) {
        const entryPeak = peak(entry.points) ?? -Infinity;
        if (entryPeak > (peak(best) ?? -Infinity)) best = entry.points;
      }
      next[panel.id] = best;
    });
    panels = next;
    metricsUnavailable = !anySucceeded;
  }

  async function load() {
    const id = serverId;
    if (!id) return;
    loading = true;
    error = null;
    try {
      server = await getCanonicalServer(id);
    } catch (err) {
      error =
        err instanceof Error ? err.message : "This server could not be loaded.";
      loading = false;
      return;
    }

    const [availabilityResult, alertResult] = await Promise.allSettled([
      getAvailabilityReport("30d" satisfies AvailabilityWindow, id),
      getAlertRules(),
    ]);
    if (!pageActive) return;
    if (availabilityResult.status === "fulfilled") {
      availability = availabilityResult.value;
      availabilityUnavailable = false;
    } else {
      availabilityUnavailable = true;
    }
    alerts = alertResult.status === "fulfilled" ? alertResult.value : [];

    await loadMetrics(id);
    loading = false;
  }

  onMount(() => {
    pageActive = true;
    void load();
    refreshInterval = setInterval(() => void load(), REFRESH_INTERVAL_MS);
  });

  onDestroy(() => {
    pageActive = false;
    if (refreshInterval) clearInterval(refreshInterval);
  });
</script>

<svelte:head>
  <title>{server?.name ?? "Server"} — Monitoring</title>
</svelte:head>

<div class="mx-auto max-w-7xl p-4 md:p-6" data-testid="server-monitoring-page">
  <a
    href="/monitoring"
    class="mb-4 inline-flex items-center gap-2 text-[12.5px] font-medium text-primary hover:underline"
  >
    <ArrowLeft class="h-3.5 w-3.5" />
    Monitoring
  </a>

  {#if error}
    <div
      class="rounded-lg border border-destructive/40 bg-destructive/10 p-4 text-foreground"
      data-testid="server-monitoring-error"
    >
      {error}
    </div>
  {:else if !server}
    <div class="h-32 animate-pulse rounded-lg border border-border bg-card/40"></div>
  {:else}
    <section class="mb-4 flex flex-wrap items-start justify-between gap-6 rounded-lg border border-border bg-card p-5">
      <div class="flex min-w-0 flex-col gap-2.5">
        <h1 class="text-xl font-semibold tracking-tight text-foreground">
          {server.name}
        </h1>
        <div class="flex flex-wrap gap-2">
          {#each serverStatusAxes(canonicalServerAxes(server)) as axis (axis.key)}
            <span
              class="inline-flex h-[26px] items-center gap-1.5 rounded-md border border-border bg-background px-2.5 text-[11.5px] text-foreground"
              data-testid="server-monitoring-axis"
            >
              <span class="font-mono text-[10px] uppercase tracking-wide text-muted-foreground"
                >{axis.key}</span
              >
              <span
                class="h-[7px] w-[7px] rounded-full"
                style={`background: ${AXIS_TONE[axis.tone] ?? AXIS_TONE.muted}`}
              ></span>{axis.label}
            </span>
          {/each}
        </div>
        <span
          class="font-mono text-[11.5px] text-muted-foreground"
          data-testid="server-monitoring-identity"
        >
          {[
            server.environment_class,
            server.offering,
            server.provider_id || server.provider?.ref,
            server.worker_id ? `agent ${server.worker_id}` : "no agent bound",
          ]
            .filter(Boolean)
            .map(humanize)
            .join(" · ")}
        </span>
      </div>
      <div class="flex flex-col items-end gap-2">
        <span class="font-mono text-[11.5px] text-muted-foreground">
          inventory revision {server.inventory_revision}
        </span>
        {#if loading}
          <span class="text-[11.5px] text-muted-foreground">Refreshing…</span>
        {/if}
        {#if serverAllowsDetach(server)}
          {#if showDetachConfirmation}
            <div
              class="max-w-sm rounded-lg border border-destructive/40 bg-destructive/10 p-3"
              data-testid="server-monitoring-detach-confirmation"
            >
              <p class="text-sm font-medium text-foreground">
                Remove {server.name} from Monitoring?
              </p>
              <p class="mt-1 text-xs text-muted-foreground">
                Guard is revoked immediately. The physical server stays in place.
              </p>
              {#if detachError}
                <p class="mt-2 text-xs text-destructive">{detachError}</p>
              {/if}
              <div class="mt-3 flex flex-wrap gap-2">
                <Button
                  variant="destructive"
                  size="sm"
                  testId="server-monitoring-detach-confirm"
                  onclick={() => void confirmDetach()}
                  disabled={detaching}
                >
                  <Trash2 class="h-3.5 w-3.5" />
                  {detaching ? "Removing..." : "Remove from view"}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onclick={() => {
                    showDetachConfirmation = false;
                    detachError = null;
                  }}
                  disabled={detaching}
                >
                  Cancel
                </Button>
              </div>
            </div>
          {:else}
            <Button
              variant="ghost"
              size="sm"
              testId="server-monitoring-detach"
              onclick={() => (showDetachConfirmation = true)}
              disabled={detaching}
            >
              <Trash2 class="h-3.5 w-3.5" />
              Remove from view
            </Button>
          {/if}
        {/if}
      </div>
    </section>

    <div class="mb-3 flex items-center justify-between gap-4">
      <h2 class="text-[15px] font-semibold text-foreground">Metrics · last 24 h</h2>
      <span class="font-mono text-[11px] text-muted-foreground">
        every panel is one range query scoped to node_id="{serverId}"
      </span>
    </div>

    {#if metricsUnavailable}
      <section
        class="mb-4 rounded-lg border border-border bg-card p-4 text-sm text-muted-foreground"
        data-testid="server-monitoring-metrics-unavailable"
      >
        No metrics backend answered for this node. Availability below is derived
        from the transition timeline and does not depend on it.
      </section>
    {:else}
      <div class="mb-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {#each PANELS as panel (panel.id)}
          <section
            class="flex flex-col gap-3 rounded-lg border border-border bg-card p-4"
            data-testid="server-monitoring-panel"
            data-panel={panel.id}
          >
            <div class="flex items-start justify-between gap-3">
              <div class="flex min-w-0 flex-col gap-1.5">
                <span class="text-[13px] font-semibold text-foreground"
                  >{panel.title}</span
                >
                <code
                  class="truncate rounded border border-border bg-background px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                  title={panel.query(serverId)}>{panel.query(serverId)}</code
                >
              </div>
              <div class="flex shrink-0 flex-col items-end gap-0.5">
                <span class="text-lg font-semibold leading-none text-foreground">
                  {formatValue(latest(panels[panel.id] ?? []), panel.unit)}
                </span>
                {#if peak(panels[panel.id] ?? []) !== null}
                  <span class="font-mono text-[10.5px] text-muted-foreground">
                    peak {formatValue(peak(panels[panel.id] ?? []), panel.unit)}
                  </span>
                {/if}
              </div>
            </div>
            <MetricSparkline
              points={panels[panel.id] ?? []}
              label={`${panel.title} over the last 24 hours`}
              threshold={"threshold" in panel ? panel.threshold : undefined}
              max={"max" in panel ? panel.max : undefined}
            />
          </section>
        {/each}
      </div>
    {/if}

    <div class="grid gap-3 lg:grid-cols-[minmax(0,1fr)_400px]">
      <section class="rounded-lg border border-border bg-card p-4" data-testid="server-monitoring-alerts">
        <div class="mb-3 flex items-center justify-between gap-3">
          <h2 class="text-[15px] font-semibold text-foreground">Alert rules</h2>
          <span class="text-[11.5px] text-muted-foreground"
            >{relevantAlerts.length} in scope for this node</span
          >
        </div>
        {#if relevantAlerts.length === 0}
          <p class="text-sm text-muted-foreground">No alert rules apply here.</p>
        {:else}
          <div class="flex flex-col">
            {#each relevantAlerts as alert (alert.rule.name)}
              <div
                class="grid grid-cols-[160px_84px_56px_minmax(0,1fr)_110px] items-center gap-3 border-t border-border py-2.5 first:border-t-0"
              >
                <span class="truncate text-[12.5px] font-medium text-foreground"
                  >{alert.rule.name}</span
                >
                <span
                  class="inline-flex h-5 w-fit items-center rounded-full px-2 text-[10.5px] font-bold uppercase tracking-wide"
                  style={`border: 1px solid color-mix(in srgb, ${alert.rule.severity === "critical" ? "oklch(0.67 0.2 25)" : "oklch(0.79 0.14 80)"} 42%, transparent); background: color-mix(in srgb, ${alert.rule.severity === "critical" ? "oklch(0.67 0.2 25)" : "oklch(0.79 0.14 80)"} 13%, transparent)`}
                  >{alert.rule.severity}</span
                >
                <span class="font-mono text-[11.5px] text-muted-foreground"
                  >{formatFor(alert.rule.for)}</span
                >
                <code
                  class="truncate font-mono text-[11px] text-muted-foreground"
                  title={alert.rule.expr}>{alert.rule.expr}</code
                >
                <span
                  class="text-right font-mono text-[11.5px]"
                  class:text-destructive={alert.active}
                  class:text-muted-foreground={!alert.active}
                >
                  {alert.active
                    ? `firing since ${formatStamp(alert.fired_at ?? alert.active_since ?? undefined)}`
                    : "inactive"}
                </span>
              </div>
            {/each}
          </div>
        {/if}
      </section>

      <section class="rounded-lg border border-border bg-card p-4" data-testid="server-monitoring-history">
        <div class="mb-3 flex items-center justify-between gap-3">
          <h2 class="text-[15px] font-semibold text-foreground">
            This node&rsquo;s history
          </h2>
          <a href="/monitoring" class="text-[11.5px] font-medium text-primary hover:underline"
            >All episodes &rarr;</a
          >
        </div>

        {#if availabilityUnavailable}
          <p class="text-sm text-muted-foreground">
            Availability history is unavailable on this deployment.
          </p>
        {:else if serverReport}
          <div class="mb-3 flex flex-col gap-2">
            <div class="flex items-baseline justify-between">
              <span class="text-xs text-muted-foreground">Availability · 30 d</span>
              <span
                class="font-mono text-[13px] font-semibold text-foreground"
                data-testid="server-monitoring-uptime"
                >{formatRatio(serverReport.uptime_ratio)}</span
              >
            </div>
            <StateRibbon
              days={serverReport.days}
              height={22}
              label={`${server.name}: daily connection state over 30 days`}
            />
          </div>

          <div class="h-px bg-border"></div>

          {#if serverReport.episodes.length === 0}
            <p class="mt-3 text-sm text-muted-foreground">
              No recorded downtime in this window.
            </p>
          {:else}
            <div class="mt-3 flex flex-col gap-3">
              {#each serverReport.episodes.slice(0, 6) as episode (episode.started_at)}
                <div class="flex flex-col gap-1">
                  <div class="flex items-center gap-2.5">
                    <span
                      class="h-[7px] w-[7px] shrink-0 rounded-full"
                      style={`background: ${episode.ongoing ? "oklch(0.67 0.2 25)" : "oklch(0.79 0.14 80)"}`}
                    ></span>
                    <span class="font-mono text-[11.5px] text-foreground">
                      {formatStamp(episode.started_at)} &rarr; {episode.ongoing
                        ? "ongoing"
                        : formatStamp(episode.ended_at)}
                    </span>
                    <span
                      class="ml-auto font-mono text-[11.5px] font-semibold text-foreground"
                      >{formatDuration(episode.seconds)}</span
                    >
                  </div>
                  <span class="pl-4 text-xs text-muted-foreground">
                    <span class="font-mono">{episode.trigger_reason || "unrecorded"}</span
                    >{#if !episode.ongoing && episode.recovery_reason}
                      &middot; recovered by <span class="font-mono"
                        >{episode.recovery_reason}</span
                      >{/if}
                  </span>
                </div>
              {/each}
            </div>
          {/if}
        {:else}
          <p class="text-sm text-muted-foreground">No history for this node yet.</p>
        {/if}
      </section>
    </div>
  {/if}
</div>
