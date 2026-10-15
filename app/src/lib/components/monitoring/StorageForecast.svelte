<script lang="ts">
  import { untrack } from "svelte";
  import { tr, formatDateTime, formatNumber } from "#lib/i18n.svelte.js";
  import {
    getStorageForecast,
    type StorageForecast as Forecast,
    type StorageForecastReport,
    type StorageForecastResource,
  } from "#lib/api/storage-forecast.js";
  import type { CanonicalServer } from "#lib/api/registry.js";
  import type { CanonicalService } from "#lib/api/services.js";

  let {
    servers = [],
    services = [],
    servicesUnavailable = false,
    inventoryStale = false,
    refreshKey = 0,
    serverId = "",
  }: {
    servers?: CanonicalServer[];
    services?: CanonicalService[];
    servicesUnavailable?: boolean;
    inventoryStale?: boolean;
    refreshKey?: number;
    serverId?: string;
  } = $props();

  let report = $state<StorageForecastReport | null>(null);
  let loading = $state(true);
  let unavailable = $state(false);
  let reportScope = "";
  let request = 0;

  // Invalidate old requests on refresh, navigation and destruction. A failed
  // refresh retains timestamped measurements but suppresses old projections.
  $effect(() => {
    const scope = serverId;
    void refreshKey;
    const current = ++request;
    untrack(() => {
      if (reportScope !== scope) report = null;
      reportScope = scope;
      loading = true;
      void getStorageForecast(scope || undefined).then(
        (value) => {
          if (current !== request) return;
          report = value;
          unavailable = false;
          loading = false;
        },
        () => {
          if (current !== request) return;
          unavailable = true;
          loading = false;
        },
      );
    });
    return () => {
      request++;
    };
  });

  function stamp(value?: string): string {
    return value && Number.isFinite(Date.parse(value))
      ? formatDateTime(value, { dateStyle: "short", timeStyle: "short" })
      : tr("storage.unknown");
  }

  function bytes(value?: number): string {
    if (value === undefined || !Number.isFinite(value))
      return tr("storage.unknown");
    const magnitude = Math.abs(value);
    const index =
      magnitude > 0
        ? Math.min(
            4,
            Math.max(0, Math.floor(Math.log(magnitude) / Math.log(1024))),
          )
        : 0;
    return `${formatNumber(value / 1024 ** index, { maximumFractionDigits: 1 })} ${["B", "KiB", "MiB", "GiB", "TiB"][index]}`;
  }

  function matches(
    resource: StorageForecastResource,
    server: CanonicalServer,
  ): boolean {
    const labels = resource.labels;
    if (labels.server_id) return labels.server_id === server.id;
    if (labels.node_id)
      return labels.node_id === server.node_id || labels.node_id === server.id;
    if (labels.worker_id && server.worker_id)
      return labels.worker_id === server.worker_id;
    if (labels.agent_id)
      return server.channels.some(
        (channel) => channel.metadata?.agent_id === labels.agent_id,
      );
    return false;
  }

  function owner(
    resource: StorageForecastResource,
  ): CanonicalServer | undefined {
    const candidates = servers.filter((server) => matches(resource, server));
    return candidates.length === 1 ? candidates[0] : undefined;
  }

  function order(
    a: StorageForecastResource,
    b: StorageForecastResource,
  ): number {
    const full =
      Number(b.forecast.state === "capacity_reached") -
      Number(a.forecast.state === "capacity_reached");
    if (full) return full;
    const aDeadline = a.forecast.exhaustion?.earliest_days ?? Infinity;
    const bDeadline = b.forecast.exhaustion?.earliest_days ?? Infinity;
    if (aDeadline !== bDeadline) return aDeadline < bDeadline ? -1 : 1;
    return (
      (b.forecast.growth_bytes_per_day ?? -Infinity) -
      (a.forecast.growth_bytes_per_day ?? -Infinity)
    );
  }

  const groups = $derived.by(() => {
    // Rebuilt for each derivation; this map is never shared or mutated later.
    // eslint-disable-next-line svelte/prefer-svelte-reactivity
    const grouped = new Map<
      string,
      { server?: CanonicalServer; resources: StorageForecastResource[] }
    >();
    for (const resource of report?.resources ?? []) {
      const server = owner(resource);
      if (serverId && server?.id !== serverId) continue;
      const id = server?.id ?? "unmapped";
      const group = grouped.get(id) ?? { server, resources: [] };
      group.resources.push(resource);
      grouped.set(id, group);
    }
    return [...grouped.entries()]
      .map(([id, group]) => ({
        id,
        ...group,
        resources: group.resources.sort(order),
      }))
      .sort((a, b) => order(a.resources[0], b.resources[0]));
  });

  function volumeName(resource: StorageForecastResource): string {
    return resource.labels.mount || resource.labels.device || resource.id;
  }

  function percentage(forecast: Forecast): number | undefined {
    if (
      forecast.used_bytes === undefined ||
      !forecast.capacity_bytes ||
      forecast.capacity_bytes <= 0
    )
      return;
    const result = (forecast.used_bytes / forecast.capacity_bytes) * 100;
    return Number.isFinite(result) ? Math.max(0, result) : undefined;
  }

  function projection(forecast: Forecast): string {
    if (unavailable) return tr("storage.suppressed");
    if (forecast.state === "capacity_reached") return tr("storage.full");
    if (forecast.state === "beyond_horizon")
      return tr("storage.beyond", { days: forecast.horizon_days });
    if (forecast.state === "flat") return tr("storage.flat");
    if (forecast.state === "shrinking") return tr("storage.shrinking");
    if (forecast.state !== "growing" || !forecast.exhaustion)
      return tr("storage.insufficient");
    const earliest = forecast.exhaustion.earliest_days;
    const latest = forecast.exhaustion.latest_days;
    if (!Number.isFinite(earliest) || earliest < 0)
      return tr("storage.insufficient");
    if (earliest < 1 && latest !== undefined && latest < 1)
      return tr("storage.soon");
    const low = earliest < 1 ? "<1" : formatNumber(Math.floor(earliest));
    if (latest === undefined || !Number.isFinite(latest))
      return tr("storage.atLeast", { days: low });
    return tr("storage.range", { low, high: formatNumber(Math.ceil(latest)) });
  }

  const reasonKeys: Record<string, string> = {
    no_history: "history",
    insufficient_history: "history",
    sparse_history: "sparse",
    capacity_unknown: "capacity",
    invalid_measurements: "invalid",
    conflicting_measurements: "invalid",
    measurement_timestamp_unpaired: "invalid",
    capacity_measurement_unpaired: "invalid",
    stale_measurement: "staleMeasurement",
    capacity_changed: "changed",
    recent_outlier: "outlier",
    unstable_growth: "variable",
  };
  const noteKeys: Record<string, string> = {
    capacity_changed: "changed",
    robust_trend: "robust",
    variable_growth: "variable",
    limited_horizon: "horizon",
    effective_capacity: "effective",
    collection_timestamps: "collected",
    hourly_sampled: "hourly",
    duplicate_streams: "duplicates",
    measurement_timestamp_unpaired: "invalid",
    capacity_measurement_unpaired: "capacityGap",
    conflicting_measurements: "invalid",
  };

  function explanations(forecast: Forecast): string[] {
    const keys = [
      reasonKeys[forecast.reason ?? ""],
      ...(forecast.notes ?? []).map((note) => noteKeys[note]),
    ].filter(Boolean);
    return [...new Set(keys)].map((key) => tr(`storage.note.${key}`));
  }

  function hostVolumes(service: CanonicalService): StorageForecastResource[] {
    return (
      groups.find((group) => group.server?.id === service.server_id)
        ?.resources ?? []
    );
  }

  const visibleServices = $derived(
    services.filter((service) => !serverId || service.server_id === serverId),
  );
</script>

<section
  id="storage"
  class="mb-4 rounded-lg border border-border bg-card p-4"
  data-testid="storage-forecast"
>
  <div class="flex flex-wrap items-start justify-between gap-2">
    <div>
      <h2 class="text-sm font-semibold text-foreground">
        {tr("storage.title")}
      </h2>
      <p class="mt-1 text-xs text-muted-foreground">{tr("storage.intro")}</p>
    </div>
    {#if report}
      <span class="text-xs text-muted-foreground"
        >{tr("storage.updated", { when: stamp(report.generated_at) })}</span
      >
    {/if}
  </div>
  {#if unavailable}
    <p
      role="status"
      class="mt-3 rounded-md border border-warning/40 bg-warning/10 p-3 text-sm text-foreground"
    >
      {report ? tr("storage.staleReport") : tr("storage.loadFailed")}
    </p>
  {/if}
  {#if inventoryStale}<p class="mt-2 text-xs text-warning">
      {tr("storage.hostInventoryStale")}
    </p>{/if}
  {#if loading && !report}
    <p role="status" class="mt-3 text-sm text-muted-foreground">
      {tr("storage.loading")}
    </p>
  {:else if !unavailable && groups.length === 0}
    <p class="mt-3 text-sm text-muted-foreground">{tr("storage.noVolumes")}</p>
  {/if}
  {#each groups as group (group.id)}
    <div class="mt-4" id={`storage-host-${group.id}`}>
      <h3 class="mb-2 text-sm font-medium text-foreground">
        {#if group.server}
          <a
            class="text-primary hover:underline"
            href={`/monitoring/${encodeURIComponent(group.server.id)}#storage`}
            >{group.server.name}</a
          >
        {:else}
          {tr("storage.unmapped")}
        {/if}
      </h3>
      <div class="grid gap-3 lg:grid-cols-2">
        {#each group.resources as resource (resource.id)}
          {@const forecast = resource.forecast}
          {@const percent = percentage(forecast)}
          <article
            id={`storage-volume-${resource.id}`}
            class="rounded-lg border border-border bg-background p-3.5"
            data-state={unavailable ? "stale" : forecast.state}
          >
            <div class="flex flex-wrap items-start justify-between gap-2">
              <div class="min-w-0">
                <h4
                  class="break-all font-mono text-sm font-semibold text-foreground"
                >
                  {volumeName(resource)}
                </h4>
                <p class="mt-0.5 break-all text-xs text-muted-foreground">
                  {[resource.labels.device, resource.labels.fstype]
                    .filter(Boolean)
                    .join(" · ")}
                </p>
                {#if forecast.notes?.includes("duplicate_streams")}
                  <p class="mt-1 break-all text-xs text-muted-foreground">
                    {tr("storage.source", {
                      source:
                        resource.labels.origin ||
                        resource.labels.source ||
                        resource.labels.agent_id ||
                        resource.labels.worker_id ||
                        resource.id,
                    })}
                  </p>
                {/if}
              </div>
              <span
                class="text-sm font-semibold"
                class:text-destructive={!unavailable &&
                  forecast.state === "capacity_reached"}
                class:text-foreground={unavailable ||
                  forecast.state !== "capacity_reached"}
                >{projection(forecast)}</span
              >
            </div>
            <div
              class="mt-3 flex flex-wrap justify-between gap-2 text-sm text-foreground"
            >
              <span
                >{tr("storage.used", {
                  used: bytes(forecast.used_bytes),
                  capacity: bytes(forecast.capacity_bytes),
                })}</span
              >
              <span
                >{tr("storage.free", {
                  value: bytes(forecast.free_bytes),
                })}</span
              >
            </div>
            {#if percent !== undefined}
              <div
                class="mt-2 h-1.5 overflow-hidden rounded-full bg-muted"
                role="meter"
                aria-label={tr("storage.usage")}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={Math.min(100, percent)}
                aria-valuetext={`${formatNumber(percent, { maximumFractionDigits: 1 })} %`}
              >
                <div
                  class="h-full rounded-full"
                  class:bg-destructive={percent >= 90}
                  class:bg-primary={percent < 90}
                  style={`width: ${Math.min(100, percent)}%`}
                ></div>
              </div>
            {/if}
            <dl class="mt-3 grid gap-1 text-xs text-muted-foreground">
              <div class="flex flex-wrap justify-between gap-1">
                <dt>{tr("storage.growth")}</dt>
                <dd>
                  {unavailable
                    ? tr("storage.suppressed")
                    : tr("storage.perDay", {
                        value: bytes(forecast.growth_bytes_per_day),
                      })}
                </dd>
              </div>
              {#if !unavailable && forecast.growth_low_bytes_per_day !== undefined && forecast.growth_high_bytes_per_day !== undefined}
                <div class="flex flex-wrap justify-between gap-1">
                  <dt>{tr("storage.growthRange")}</dt>
                  <dd>
                    {tr("storage.growthValues", {
                      low: bytes(forecast.growth_low_bytes_per_day),
                      high: bytes(forecast.growth_high_bytes_per_day),
                    })}
                  </dd>
                </div>
              {/if}
              <div class="flex flex-wrap justify-between gap-1">
                <dt>{tr("storage.latest")}</dt>
                <dd>{stamp(forecast.latest_at)}</dd>
              </div>
              <div class="flex flex-wrap justify-between gap-1">
                <dt>{tr("storage.observed")}</dt>
                <dd>
                  {stamp(forecast.observed_from)} – {stamp(
                    forecast.observed_to,
                  )}
                </dd>
              </div>
              <div class="flex flex-wrap justify-between gap-1">
                <dt>{tr("storage.samples")}</dt>
                <dd>
                  {tr("storage.sampleValues", {
                    samples: forecast.sample_count,
                    days: forecast.bucket_count,
                  })}
                </dd>
              </div>
              <div class="flex flex-wrap justify-between gap-1">
                <dt>{tr("storage.horizon")}</dt>
                <dd>{tr("storage.days", { days: forecast.horizon_days })}</dd>
              </div>
            </dl>
            {#if !unavailable}
              {#if forecast.state === "growing" && forecast.exhaustion}<p
                  class="mt-2 text-xs text-muted-foreground"
                >
                  {tr("storage.fromLatest")}
                </p>{/if}
              {#each explanations(forecast) as explanation (explanation)}<p
                  class="mt-2 text-xs text-muted-foreground"
                >
                  {explanation}
                </p>{/each}
              {#if forecast.capacity_changed_at}<p
                  class="mt-2 text-xs text-muted-foreground"
                >
                  {tr("storage.capacityChanged", {
                    when: stamp(forecast.capacity_changed_at),
                  })}
                </p>{/if}
            {/if}
          </article>
        {/each}
      </div>
    </div>
  {/each}
  <p class="mt-3 text-xs text-muted-foreground">{tr("storage.assumptions")}</p>
  <details class="mt-4 border-t border-border pt-3">
    <summary class="cursor-pointer text-sm font-medium text-foreground"
      >{servicesUnavailable && visibleServices.length === 0
        ? tr("storage.servicesTitle")
        : tr("storage.services", { count: visibleServices.length })}</summary
    >
    <p class="mt-2 text-xs text-muted-foreground">
      {tr("storage.serviceIntro")}
    </p>
    {#if servicesUnavailable}<p role="status" class="mt-2 text-sm text-warning">
        {tr("storage.servicesUnavailable")}
      </p>{/if}
    {#each visibleServices as service (service.id)}
      <div
        class="mt-2 flex flex-wrap items-start justify-between gap-2 rounded-md border border-border bg-background p-3"
      >
        <div>
          <a
            class="text-sm font-medium text-primary hover:underline"
            href={`/services?service=${encodeURIComponent(service.id)}`}
            >{service.name}</a
          >
          <p class="mt-1 text-xs text-muted-foreground">
            {tr("storage.serviceUnknown")}
          </p>
        </div>
        <div class="flex flex-col gap-1 text-xs">
          {#each hostVolumes(service) as volume (volume.id)}
            <a
              class="text-primary hover:underline"
              href={`#storage-volume-${encodeURIComponent(volume.id)}`}
              >{tr("storage.sharedHost", { volume: volumeName(volume) })}</a
            >
          {:else}
            {#if service.server_id}
              <a
                class="text-primary hover:underline"
                href={`/monitoring/${encodeURIComponent(service.server_id)}#storage`}
                >{tr("storage.sharedUnavailable")}</a
              >
            {:else}<span class="text-muted-foreground"
                >{tr("storage.hostUnknown")}</span
              >{/if}
          {/each}
        </div>
      </div>
    {/each}
  </details>
</section>
