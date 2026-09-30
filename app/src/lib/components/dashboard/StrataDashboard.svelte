<script lang="ts">
  import { tr, formatDateTime } from "#lib/i18n.svelte.js";
  import { onMount, type Snippet } from "svelte";
  import { portal } from "@kombiverselabs/ui/shell";

  import type { StackIdentity } from "@kombiverselabs/ui/identity";
  import HomelabTitle from "#lib/components/dashboard/HomelabTitle.svelte";
  import ServiceLogo from "#lib/components/dashboard/ServiceLogo.svelte";
  import {
    groupNodesByDeployment,
    meterTone,
    nodeStatusSummary,
    serviceDisplayName,
    serviceState,
    type NodeView,
  } from "#lib/dashboard/homelab-model.js";
  import type {
    DeviceEntry,
    PeopleEntry,
    PersonEntry,
    SectionState,
  } from "#lib/dashboard/people.js";
  import {
    cpuCurvePath,
    DAY_MS,
    loadCpuSeries,
    loadTimeline,
    statusSentence,
    type CpuSeries,
    type StatusFacts,
    type Timeline,
  } from "#lib/dashboard/strata.js";
  import { formatClock } from "#lib/dashboard/time.js";

  interface Rollout {
    nodeKey: string;
    label: string;
    progress: number;
  }

  interface Props {
    title: string;
    /** The saved Stack Identity; it becomes the title when present. */
    identity?: StackIdentity | null;
    nodes: NodeView[];
    facts: StatusFacts;
    stats: {
      apps: number;
      appsNote: string;
      alerts: number;
      connected: number;
    };
    rollout: Rollout | null;
    deploymentName: (deploymentId: string) => string;
    deploymentHref: (deploymentId: string) => string;
    nodeHref: (node: NodeView) => string;
    onOpenService: (serviceId: string) => void;
    people: SectionState<PersonEntry>;
    devices: SectionState<DeviceEntry>;
    actions?: Snippet;
    /** Per-Node notice (e.g. the wizard run's rollout) under its band. */
    nodeNotice?: Snippet<[NodeView]>;
  }

  let {
    title,
    identity = null,
    nodes,
    facts,
    stats,
    rollout,
    deploymentName,
    deploymentHref,
    nodeHref,
    onOpenService,
    people,
    devices,
    actions,
    nodeNotice,
  }: Props = $props();

  const groups = $derived(groupNodesByDeployment(nodes, deploymentName));
  const sentence = $derived(statusSentence(facts));

  let windowEnd = $state(Date.now());
  let series = $state<Record<string, CpuSeries | undefined>>({});
  let timeline = $state<Timeline | null>(null);
  let timelineFailed = $state(false);

  const nodeIdsKey = $derived(
    [...new Set(nodes.map((node) => node.nodeId))].sort().join("\u0000"),
  );

  async function refreshSeries(ids: string[]) {
    const end = new Date();
    windowEnd = end.getTime();
    const results = await Promise.all(
      ids.map(async (id) => [id, await loadCpuSeries(id, end)] as const),
    );
    series = Object.fromEntries(results);
  }

  async function refreshTimeline() {
    try {
      timeline = await loadTimeline();
      timelineFailed = false;
    } catch {
      timelineFailed = true;
    }
  }

  $effect(() => {
    const ids = nodeIdsKey ? nodeIdsKey.split("\u0000") : [];
    if (ids.length > 0) void refreshSeries(ids);
  });

  onMount(() => {
    void refreshTimeline();
    // A 24-hour picture only needs to move every few minutes.
    const timer = setInterval(() => {
      void refreshTimeline();
      const ids = nodeIdsKey ? nodeIdsKey.split("\u0000") : [];
      if (ids.length > 0) void refreshSeries(ids);
    }, 5 * 60_000);
    return () => clearInterval(timer);
  });

  const ring = (radius: number, value: number | null) => {
    const circumference = 2 * Math.PI * radius;
    return `${((circumference * (value ?? 0)) / 100).toFixed(1)} ${circumference.toFixed(1)}`;
  };
  const toneVar: Record<string, string> = {
    ok: "var(--color-success)",
    warn: "var(--color-warning)",
    error: "var(--color-destructive)",
    off: "var(--color-muted-foreground)",
  };
  const nodeTone = (node: NodeView) => {
    if (node.axes.connection === "offline" || node.axes.health === "unhealthy") {
      return toneVar.error;
    }
    if (node.axes.health === "degraded" || node.axes.connection !== "connected") {
      return toneVar.warn;
    }
    return toneVar.ok;
  };
  let gaugeTip = $state<{ node: NodeView; right: number; top: number } | null>(
    null,
  );
  /** Opens the ring tooltip to the left of the ring, kept inside the window. */
  function showGaugeTip(node: NodeView, target: EventTarget | null) {
    if (!(target instanceof HTMLElement)) return;
    const rect = target.getBoundingClientRect();
    const half = 130;
    const middle = rect.top + rect.height / 2;
    gaugeTip = {
      node,
      right: Math.max(8, window.innerWidth - rect.left + 12),
      top: Math.min(Math.max(middle, half + 8), window.innerHeight - half - 8),
    };
  }
  function hideGaugeTip() {
    gaugeTip = null;
  }
  /** Tooltip rows for the load rings, in ring order, outside in. */
  const gaugeRows = (node: NodeView) =>
    [
      { key: "cpu", label: "CPU", ring: tr("ui.strataDashboard.outerRing"), meaning: "processor load", percent: node.cpu, color: toneVar[meterTone(node.cpu)] },
      { key: "ram", label: "Memory", ring: tr("ui.strataDashboard.middleRing"), meaning: "RAM in use", percent: node.ram, color: "var(--color-info)" },
      { key: "disk", label: "Disk", ring: tr("ui.strataDashboard.innerRing"), meaning: "storage used", percent: node.disk, color: toneVar[meterTone(node.disk)] },
    ].map((row) => ({
      ...row,
      value: row.percent === null ? "not reported" : `${Math.round(row.percent)} %`,
    }));
  const nodeFacts = (node: NodeView) =>
    [...new Set([node.role, node.kit, node.placement].filter(Boolean))];
  const beadLeft = (index: number, count: number) =>
    count <= 1 ? "8%" : `${(8 + index * (84 / (count - 1))).toFixed(1)}%`;

  const peopleItems = $derived<PeopleEntry[]>([
    ...(people.state === "ready" ? people.items : []),
    ...(devices.state === "ready" ? devices.items : []),
  ]);
  const deviceCount = $derived(
    devices.state === "ready" ? String(devices.items.length) : "—",
  );
  const deviceNote = $derived(
    devices.state === "ready"
      ? `${devices.items.filter((item) => item.online).length} online`
      : devices.state === "loading"
        ? "loading"
        : "not available",
  );
  /** One lamp per Node / device: lit when connected or online. */
  const statCells = $derived([
    {
      value: String(facts.nodes),
      label: tr("ui.strataDashboard.nodes"),
      note: `${stats.connected}/${facts.nodes} connected`,
      href: "/monitoring",
      lamps: nodes.map(nodeTone),
    },
    {
      value: String(stats.apps),
      label: tr("ui.services.services"),
      note: stats.appsNote,
      href: "/services",
      lamps: [] as string[],
    },
    {
      value: deviceCount,
      label: tr("ui.strataDashboard.devices"),
      note: deviceNote,
      href: "#strata-devices",
      lamps:
        devices.state === "ready"
          ? devices.items.map((item) => (item.online ? toneVar.ok : toneVar.off))
          : [],
    },
    {
      value: String(stats.alerts),
      label: tr("ui.strataDashboard.alerts"),
      note: stats.alerts === 0 ? tr("ui.strata.quiet") : tr("ui.strata.activeNote"),
      href: "/monitoring",
      lamps: [] as string[],
    },
  ]);
  const orbitMessages = $derived(
    [
      people.state === "unavailable" || people.state === "error"
        ? people.message
        : "",
      devices.state === "unavailable" || devices.state === "error"
        ? devices.message
        : "",
    ].filter(Boolean),
  );
  const orbitPoint = (index: number, count: number) => {
    const t = count <= 1 ? 0.5 : 0.06 + (index / (count - 1)) * 0.88;
    return {
      left: `${(t * 100).toFixed(1)}%`,
      top: `${(40 - Math.sin(Math.PI * t) * 24).toFixed(0)}px`,
    };
  };
  const eventTone: Record<string, string> = {
    ok: "var(--color-success)",
    info: "var(--color-info)",
    warn: "var(--color-warning)",
    error: "var(--color-destructive)",
  };
  const now = $derived(windowEnd);
  const today = $derived(
    formatDateTime(now, {
      weekday: "long",
      hour: "2-digit",
      minute: "2-digit",
    }),
  );
</script>

<!-- Viewport-bound like Complete: only the bands and the device list scroll. -->
<div
  class="strata relative isolate flex flex-1 flex-col overflow-hidden"
  data-testid="dashboard-strata"
>
  <svg class="strata-grain" aria-hidden="true">
    <filter id="strata-grain-filter">
      <feTurbulence
        type="fractalNoise"
        baseFrequency="0.9"
        numOctaves="2"
        stitchTiles="stitch"
      />
    </filter>
    <rect width="100%" height="100%" filter="url(#strata-grain-filter)" />
  </svg>

  <div class="relative flex flex-1 flex-col gap-4">
    <header
      class="grid shrink-0 items-end gap-3 lg:grid-cols-[minmax(0,1fr)_auto] lg:gap-8"
    >
      <!-- Below 48rem the fixed menu toggle sits top-left: the title moves
           beside it and the date line is dropped. -->
      <div class="flex min-w-0 flex-col gap-1.5 pl-12 min-[48rem]:pl-0">
        <div
          class="hidden items-center gap-3.5 font-mono text-[11px] tracking-[0.18em] text-muted-foreground uppercase min-[48rem]:flex"
        >
          <span>{tr("ui.dashboard.yourHomelab")}</span>
          <span class="h-px w-12 bg-border"></span>
          <span>{today}</span>
        </div>
        <HomelabTitle
          {identity}
          fallback={title}
          class="strata-title text-3xl leading-none font-bold text-foreground md:text-4xl"
        />
      </div>
      <div class="flex flex-col gap-2">
        {#if sentence}
          <p
            class="text-sm leading-snug text-foreground/85 md:text-base"
            data-testid="strata-status"
          >
            {sentence}
          </p>
        {/if}
        {@render actions?.()}
      </div>
    </header>

    <div
      class="grid shrink-0 grid-cols-2 border-y border-border md:grid-cols-4"
      data-testid="strata-stats"
    >
      {#each statCells as stat (stat.label)}
        <a
          href={stat.href}
          class="flex items-baseline gap-2.5 border-l border-border py-1.5 pl-4 text-foreground no-underline first:border-l-0 hover:bg-muted/20 md:first:border-l"
        >
          <span
            class="strata-title text-2xl font-medium tabular-nums"
            >{stat.value}</span
          >
          <span class="flex min-w-0 items-baseline gap-2">
            <span class="text-sm font-medium">{stat.label}</span>
            {#if stat.lamps.length > 0}
              <span
                class="flex items-center gap-1 self-center"
                aria-hidden="true"
                data-testid="strata-stat-lamps"
              >
                {#each stat.lamps.slice(0, 8) as lamp, index (index)}
                  <span
                    class="strata-lamp h-2 w-2 rounded-full"
                    style={`--lamp:${lamp}`}
                  ></span>
                {/each}
              </span>
            {/if}
            <span class="truncate font-mono text-[11px] text-muted-foreground"
              >{stat.note}</span
            >
          </span>
        </a>
      {/each}
    </div>

    <section
      aria-label={tr("ui.dashboard.nodes")}
      class="strata-scroll flex min-h-[9rem] flex-1 basis-0 flex-col overflow-y-auto"
      data-testid="strata-node-list"
    >
      {#each groups as group (group.key)}
        <div
          class="relative flex shrink-0 flex-col {group.federated ? 'strata-federated' : ''}"
          data-testid={group.federated ? "federation-bundle" : undefined}
        >
          {#if group.federated}
            <svg
              class="strata-bracket"
              viewBox="0 0 40 330"
              preserveAspectRatio="none"
              aria-hidden="true"
            >
              <path
                d="M34 6 C 8 6, 14 60, 14 110 C 14 140, 4 150, 2 165 C 4 180, 14 190, 14 220 C 14 270, 8 324, 34 324"
                fill="none"
                stroke-width="1.5"
                vector-effect="non-scaling-stroke"
              />
            </svg>
            <a
              class="strata-bracket-label font-mono text-[10px] tracking-[0.22em] uppercase"
              href={deploymentHref(group.deploymentId)}>{group.label}</a
            >
          {/if}
          {#each group.nodes as node (node.key)}
            {@const cpu = series[node.nodeId]}
            {@const curve =
              cpu?.state === "ready"
                ? cpuCurvePath(cpu.points, now - DAY_MS, now)
                : null}
            {@const tone = nodeTone(node)}
            {@const rolling = rollout?.nodeKey === node.key}
            {@const status = nodeStatusSummary(node.axes)}
            {@const address =
              node.address && node.address !== "not reported"
                ? node.address
                : ""}
            <!-- A Node that only runs platform services still shows them. -->
            {@const beads = node.apps.length > 0 ? node.apps : node.system}
            <article
              class="grid items-center gap-3 border-b border-border/60 py-2.5 md:grid-cols-[15.5rem_minmax(0,1fr)_4.5rem] md:gap-6"
              data-testid="strata-band"
              data-node-id={node.nodeId}
            >
              <!-- Row 1: dot, name and one combined status; row 2: role ·
                   StackKit · placement. All three axes and the address sit
                   in the tooltip and the Node page. -->
              <div class="flex min-w-0 flex-col gap-1">
                <div class="flex min-w-0 items-center gap-2.5">
                  <svg
                    width="14"
                    height="14"
                    viewBox="0 0 14 14"
                    class="shrink-0"
                    aria-hidden="true"
                  >
                    <circle class="strata-halo" cx="7" cy="7" r="5" fill={tone} />
                    <circle cx="7" cy="7" r="4" fill={tone} />
                  </svg>
                  <a
                    href={nodeHref(node)}
                    class="strata-title min-w-0 truncate text-xl font-bold text-foreground no-underline hover:text-primary"
                    >{node.name}</a
                  >
                  <span
                    class="ml-auto shrink-0 font-mono text-[10.5px]"
                    style={`color:${tone}`}
                    title={status.detail}
                    data-testid="strata-band-status"
                  >
                    {status.label}
                    <span class="sr-only">({status.detail})</span>
                  </span>
                </div>
                <span
                  class="truncate pl-6 text-xs text-muted-foreground"
                  title={[nodeFacts(node).join(" · "), address]
                    .filter(Boolean)
                    .join(" · ")}
                  data-testid="strata-band-facts"
                >
                  {nodeFacts(node).join(" · ")}
                </span>
              </div>

              <div class="relative h-[100px] min-w-0">
                <svg
                  class="absolute inset-x-0 top-0 h-[46px] w-full"
                  viewBox="0 0 800 70"
                  preserveAspectRatio="none"
                  role="img"
                  aria-label={curve
                    ? tr("ui.strata.cpuCurve", { name: node.name })
                    : tr("ui.strata.noCpuCurve", { name: node.name })}
                  data-testid="strata-curve"
                  data-state={curve ? "ready" : (cpu?.state ?? "loading")}
                >
                  {#if curve}
                    <defs>
                      <linearGradient
                        id="strata-fill-{node.nodeId}"
                        x1="0"
                        x2="0"
                        y1="0"
                        y2="1"
                      >
                        <stop offset="0" stop-color={tone} stop-opacity="0.28" />
                        <stop offset="1" stop-color={tone} stop-opacity="0" />
                      </linearGradient>
                    </defs>
                    <path d={curve.area} fill="url(#strata-fill-{node.nodeId})" />
                    <path
                      class="strata-wave"
                      pathLength="1"
                      d={curve.line}
                      fill="none"
                      stroke={tone}
                      stroke-width="1.4"
                      vector-effect="non-scaling-stroke"
                    />
                  {:else}
                    <line
                      x1="0"
                      x2="800"
                      y1="66"
                      y2="66"
                      stroke="var(--color-muted-foreground)"
                      stroke-opacity="0.45"
                      stroke-dasharray="3 6"
                      vector-effect="non-scaling-stroke"
                    />
                  {/if}
                </svg>
                {#if !curve}
                  <span
                    class="absolute top-5 left-0 font-mono text-[11px] text-muted-foreground"
                  >
                    {cpu?.state === "error"
                      ? tr("ui.strataDashboard.cpuSeriesUnavailable")
                      : cpu
                        ? tr("ui.strataDashboard.noCpuDataInThe")
                        : tr("ui.strataDashboard.loadingCpu")}
                  </span>
                {/if}
                <div class="strata-rail absolute inset-x-0 top-[68px] h-px"></div>
                {#if rolling && rollout}
                  <div class="strata-pulse" aria-hidden="true"></div>
                  <span
                    class="absolute top-0 right-0 max-w-[70%] truncate rounded bg-background/70 px-1.5 font-mono text-[11px] text-warning"
                    data-testid="strata-rollout"
                  >
                    {rollout.label} · {rollout.progress}%
                  </span>
                {/if}
                {#each beads.slice(0, 6) as service, index (service.id)}
                  {@const state = serviceState(service)}
                  <button
                    type="button"
                    class="strata-bead absolute top-[52px] flex w-20 -translate-x-1/2 flex-col items-center gap-1"
                    style={`left:${beadLeft(index, Math.min(6, beads.length))}`}
                    data-testid="node-app-tile"
                    data-service-id={service.id}
                    onclick={() => onOpenService(service.id)}
                  >
                    <span
                      class="strata-bead-logo flex h-8 w-8 items-center justify-center rounded-full"
                      style={`--bead-ring:${state.tone === "ok" ? "var(--color-border)" : toneVar[state.tone]}`}
                    >
                      <ServiceLogo {service} class="h-6 w-6" />
                    </span>
                    <span
                      class="strata-bead-name max-w-full truncate text-[11px] text-muted-foreground"
                      >{serviceDisplayName(service)}</span
                    >
                  </button>
                {/each}
              </div>

              <!-- Load rings: CPU outside, memory in the middle, disk inside,
                   services in the centre. Hover or focus explains each value. -->
              <a
                href={nodeHref(node)}
                class="strata-gauge relative hidden md:block"
                aria-describedby={gaugeTip?.node.key === node.key ? "strata-gauge-tip" : undefined}
                onpointerenter={(event) => showGaugeTip(node, event.currentTarget)}
                onpointerleave={hideGaugeTip}
                onfocus={(event) => showGaugeTip(node, event.currentTarget)}
                onblur={hideGaugeTip}
                aria-label={tr("ui.strata.gaugeAria", {
                  name: node.name,
                  cpu: node.cpu ?? tr("ui.common.unknown"),
                  ram: node.ram ?? tr("ui.common.unknown"),
                  disk: node.disk ?? tr("ui.common.unknown"),
                  services: node.apps.length + node.system.length,
                })}
                data-testid="strata-gauge"
              >
                <svg
                  width="72"
                  height="72"
                  viewBox="0 0 96 96"
                  aria-hidden="true"
                >
                  {#each [{ r: 40, v: node.cpu }, { r: 31, v: node.ram }, { r: 22, v: node.disk }] as gauge (gauge.r)}
                    <circle
                      cx="48"
                      cy="48"
                      r={gauge.r}
                      fill="none"
                      stroke="var(--color-muted)"
                      stroke-width="5"
                    />
                    {#if gauge.v !== null}
                      <circle
                        cx="48"
                        cy="48"
                        r={gauge.r}
                        fill="none"
                        stroke={gauge.r === 31
                          ? "var(--color-info)"
                          : toneVar[meterTone(gauge.v)]}
                        stroke-width="5"
                        stroke-linecap="round"
                        stroke-dasharray={ring(gauge.r, gauge.v)}
                        transform="rotate(-90 48 48)"
                      />
                    {/if}
                  {/each}
                  <!-- Every service placed on the Node, apps and platform
                       alike, so the rings add up to the Services stat. -->
                  <text
                    x="48"
                    y="54"
                    text-anchor="middle"
                    fill="var(--color-muted-foreground)"
                    font-size="15"
                    font-family="ui-monospace, monospace"
                    >{node.apps.length + node.system.length}</text
                  >
                </svg>
              </a>
            </article>
            {#if nodeNotice}
              <div class="pb-2.5" data-testid="strata-node-notice">
                {@render nodeNotice(node)}
              </div>
            {/if}
          {/each}
        </div>
      {/each}
    </section>

    <section
      id="strata-devices"
      class="grid shrink-0 items-start gap-2 md:grid-cols-[15.5rem_minmax(0,1fr)] md:gap-6"
      aria-labelledby="strata-devices-title"
      data-testid="strata-devices"
    >
      <h2
        id="strata-devices-title"
        class="strata-title text-lg font-bold text-foreground md:pt-1"
      >
        {tr("ui.dashboard.devices")}
      </h2>
      <div class="flex min-w-0 flex-col gap-1">
      <ul
        class="strata-scroll flex max-h-28 flex-col gap-2 overflow-y-auto md:hidden"
        data-testid="strata-device-list"
      >
        {#each peopleItems as entry (entry.id)}
          <li class="flex items-center gap-2">
            <span
              class="h-2.5 w-2.5 rounded-full {entry.audience === 'owner'
                ? 'strata-owner'
                : 'strata-household'}"
            ></span>
            <span class="text-sm font-semibold text-foreground">{entry.name}</span>
            <span class="truncate font-mono text-[10px] text-muted-foreground">
              {entry.kind === "device" ? entry.clients.join(" · ") || entry.detail : entry.status}
            </span>
          </li>
        {/each}
      </ul>
      <div class="relative hidden h-14 min-w-0 md:block">
        <svg
          class="absolute inset-0 h-full w-full"
          viewBox="0 0 1000 56"
          preserveAspectRatio="none"
          aria-hidden="true"
        >
          <path
            d="M0 46 C 250 8, 750 8, 1000 46"
            fill="none"
            stroke="var(--color-border)"
            stroke-dasharray="2 6"
            vector-effect="non-scaling-stroke"
          />
        </svg>
        {#each peopleItems.slice(0, 8) as entry, index (entry.id)}
          {@const point = orbitPoint(index, Math.min(8, peopleItems.length))}
          <span
            class="absolute flex -translate-x-1.5 -translate-y-1.5 items-center gap-2"
            style={`left:${point.left};top:${point.top}`}
            data-testid="strata-orbit-entry"
          >
            <span
              class="h-3 w-3 rounded-full {entry.audience === 'owner'
                ? 'strata-owner'
                : 'strata-household'}"
            ></span>
            <span class="flex flex-col">
              <span
                class="text-[13px] font-semibold {entry.audience === 'owner'
                  ? 'text-foreground'
                  : 'text-muted-foreground'}">{entry.name}</span
              >
              <span class="font-mono text-[10px] text-muted-foreground/80">
                {entry.kind === "device" ? entry.clients.join(" · ") || entry.detail : entry.status}
              </span>
            </span>
          </span>
        {/each}
        {#if people.state === "loading" || devices.state === "loading"}
          <p class="absolute top-2 left-0 text-sm text-muted-foreground">
            {tr("ui.dashboard.loadingDevicesPeople")}
          </p>
        {/if}
      </div>
      {#if orbitMessages.length > 0}
        <div
          class="flex flex-col gap-0.5 text-[11px] text-muted-foreground"
          role="status"
          data-testid="strata-orbit-state"
        >
          {#each orbitMessages as message (message)}
            <span>{message}</span>
          {/each}
        </div>
      {/if}
      </div>
    </section>

    <section class="flex shrink-0 flex-col gap-1" aria-label={tr("ui.dashboard.last24h")}>
      <div
        class="flex justify-between font-mono text-[10px] tracking-[0.16em] text-muted-foreground uppercase"
      >
        <span class="flex min-w-0 items-baseline gap-3">
          <span>{tr("ui.dashboard.last24h")}</span>
          {#if timelineFailed}
            <span class="truncate tracking-normal text-warning normal-case" role="status"
              >{tr("ui.dashboard.eventHistoryUnavailable")}</span
            >
          {:else if timeline && timeline.events.length === 0 && timeline.unavailable.length === 0}
            <span class="truncate tracking-normal normal-case"
              >{tr("ui.strataDashboard.nothingHappenedNoRolloutsRestores")}</span
            >
          {:else if timeline && timeline.unavailable.length > 0}
            <span class="truncate tracking-normal normal-case" role="status"
              >{tr("ui.strata.notShown", { items: timeline.unavailable.join(", ") })}</span
            >
          {/if}
        </span>
        <a href="/monitoring" class="shrink-0 tracking-normal text-primary normal-case"
          >{tr("ui.strataDashboard.monitoring")}</a
        >
      </div>
      <div class="relative h-9" data-testid="strata-timeline">
        <div class="absolute inset-x-0 top-5 h-px bg-border"></div>
        <div class="strata-ticks absolute inset-x-0 top-4 h-[9px]"></div>
        {#if timeline}
          {#each timeline.events.slice(-12) as event (event.id)}
            {@const pct = Math.max(
              0,
              Math.min(100, ((event.at - (now - DAY_MS)) / DAY_MS) * 100),
            )}
            <!-- Late events hang their label to the left so it stays on the line. -->
            <span
              class="absolute top-3 flex flex-col gap-1.5 {pct > 70
                ? 'items-end'
                : 'items-start'}"
              style={pct > 70
                ? `right:${(100 - pct).toFixed(1)}%`
                : `left:${pct.toFixed(1)}%`}
              title={`${event.label} · ${formatClock(event.at)}`}
            >
              <span
                class="box-border h-3.5 w-3.5 rounded-full border-2 bg-background"
                style={`border-color:${eventTone[event.tone]}`}
              ></span>
              <span
                class="hidden max-w-40 truncate text-[11px] text-muted-foreground md:inline"
                >{event.label} · {formatClock(event.at)}</span
              >
            </span>
          {/each}
        {/if}
      </div>
      {#if timeline && timeline.events.length > 0}
        <ol class="flex flex-col gap-1 text-xs text-muted-foreground md:hidden">
          {#each timeline.events.slice(-3) as event (event.id)}
            <li>
              <span style={`color:${eventTone[event.tone]}`}>●</span>
              {formatClock(event.at)} · {event.label}
            </li>
          {/each}
        </ol>
      {/if}
    </section>
  </div>
</div>

<!-- One portaled node per open tooltip, so the scrolling Node list never
     clips it (and Svelte's block removal stays inside the rail). -->
{#if gaugeTip}
  {@const tipNode = gaugeTip.node}
  <div
    id="strata-gauge-tip"
    class="strata-gauge-tip"
    role="tooltip"
    data-kx="menu-plate"
    data-testid="strata-gauge-tip"
    use:portal
    style={`right:${gaugeTip.right}px;top:${gaugeTip.top}px`}
  >
    <p class="strata-gauge-tip-title">
      {tipNode.name}
      <span>{tr("ui.strata.rightNow")}</span>
    </p>
    {#each gaugeRows(tipNode) as row (row.key)}
      <div class="strata-gauge-row">
        <span
          class="strata-gauge-swatch"
          style={`--swatch:${row.color}`}
          aria-hidden="true"
        ></span>
        <span class="strata-gauge-label">
          {row.label}
          <small>{row.ring} · {row.meaning}</small>
        </span>
        <span class="strata-gauge-value">{row.value}</span>
        <span class="strata-gauge-bar" aria-hidden="true">
          <span style={`width:${row.percent ?? 0}%;background:${row.color}`}
          ></span>
        </span>
      </div>
    {/each}
    <div class="strata-gauge-row strata-gauge-row--count">
      <span class="strata-gauge-count" aria-hidden="true"
        >{tipNode.apps.length + tipNode.system.length}</span
      >
      <span class="strata-gauge-label">
        {tr("ui.dashboard.servicesOnNode")}
        <small
          >{tr("ui.strata.centre", { apps: tipNode.apps.length, system: tipNode.system.length })}</small
        >
      </span>
    </div>
    <p class="strata-gauge-tip-foot">
      {tr("ui.strataDashboard.greenBelow75AmberFrom")}
    </p>
  </div>
{/if}

<style>
  .strata {
    --strata-federation: oklch(0.66 0.17 290);
    margin: -0.5rem;
    padding: 1rem;
    border-radius: var(--radius-card, 1rem);
    background:
      radial-gradient(
        1200px 600px at 12% -10%,
        color-mix(in oklab, var(--color-primary) 16%, transparent),
        transparent 60%
      ),
      radial-gradient(
        900px 500px at 100% 110%,
        color-mix(in oklab, var(--strata-federation) 10%, transparent),
        transparent 60%
      ),
      repeating-linear-gradient(
        90deg,
        color-mix(in oklab, var(--color-foreground) 3%, transparent) 0,
        color-mix(in oklab, var(--color-foreground) 3%, transparent) 1px,
        transparent 1px,
        transparent 120px
      ),
      var(--color-background);
  }
  @media (min-width: 768px) {
    .strata {
      padding: 1.5rem 2.25rem;
    }
  }
  .strata-grain {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    opacity: 0.06;
    pointer-events: none;
    z-index: -1;
  }
  .strata-title {
    letter-spacing: -0.04em;
    text-wrap: balance;
  }
  .strata-rail {
    background: linear-gradient(
      90deg,
      var(--color-border),
      var(--color-border) 60%,
      transparent
    );
  }
  .strata-bead {
    transition: transform 0.2s ease;
  }
  .strata-bead:hover,
  .strata-bead:focus-visible {
    transform: translate(-50%, -3px);
  }
  .strata-bead:hover .strata-bead-name {
    color: var(--color-foreground);
  }
  .strata-bead-logo {
    background: oklch(0.975 0.003 250);
    box-shadow:
      0 0 0 3px var(--color-background),
      0 0 0 4px var(--bead-ring);
  }
  .strata-federated {
    margin-left: 2.25rem;
  }
  .strata-bracket {
    position: absolute;
    left: -2.25rem;
    top: 1rem;
    bottom: 1rem;
    width: 1.75rem;
    height: calc(100% - 2rem);
    stroke: var(--strata-federation);
  }
  .strata-bracket-label {
    position: absolute;
    left: -3.4rem;
    top: 50%;
    transform: translateY(-50%) rotate(180deg);
    writing-mode: vertical-rl;
    color: color-mix(in oklab, var(--strata-federation) 70%, var(--color-foreground));
    text-decoration: none;
  }
  .strata-scroll {
    scrollbar-width: thin;
    scrollbar-color: color-mix(in oklab, var(--color-muted-foreground) 35%, transparent)
      transparent;
    mask-image: linear-gradient(
      to bottom,
      transparent 0,
      #000 8px,
      #000 calc(100% - 14px),
      transparent 100%
    );
  }
  .strata-owner {
    background: var(--color-primary);
    box-shadow: 0 0 0 4px color-mix(in oklab, var(--color-primary) 20%, transparent);
  }
  .strata-household {
    background: var(--color-muted-foreground);
    box-shadow: 0 0 0 4px color-mix(in oklab, var(--color-muted-foreground) 15%, transparent);
  }
  .strata-ticks {
    background-image: repeating-linear-gradient(
      90deg,
      var(--color-border) 0,
      var(--color-border) 1px,
      transparent 1px,
      transparent calc(100% / 24)
    );
  }
  .strata-pulse {
    position: absolute;
    top: 64px;
    left: 0;
    width: 8px;
    height: 8px;
    border-radius: 999px;
    background: var(--color-warning);
    box-shadow: 0 0 0 6px color-mix(in oklab, var(--color-warning) 18%, transparent);
    animation: strata-travel 2.8s linear infinite;
  }
  .strata-halo {
    transform-origin: center;
    transform-box: fill-box;
    animation: strata-breathe 3.2s ease-in-out infinite;
  }
  .strata-wave {
    stroke-dasharray: 1;
    animation: strata-draw 2.4s cubic-bezier(0.2, 0.7, 0.2, 1) both;
  }
  @keyframes strata-travel {
    from {
      left: 0;
      opacity: 1;
    }
    to {
      left: 30%;
      opacity: 0.2;
    }
  }
  @keyframes strata-breathe {
    0%,
    100% {
      opacity: 0.35;
      transform: scale(1);
    }
    50% {
      opacity: 0;
      transform: scale(2.2);
    }
  }
  @keyframes strata-draw {
    from {
      stroke-dashoffset: 1;
    }
    to {
      stroke-dashoffset: 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .strata-halo,
    .strata-wave,
    .strata-pulse {
      animation: none;
    }
    .strata-bead {
      transition: none;
    }
  }

  /* KPI lamps: one per Node or device, lit in its state colour. */
  .strata-lamp {
    background: var(--lamp);
    box-shadow: 0 0 6px color-mix(in oklab, var(--lamp) 70%, transparent);
  }

  .strata-gauge {
    border-radius: 999px;
    cursor: help;
    outline: none;
  }
  .strata-gauge:focus-visible {
    box-shadow: 0 0 0 2px var(--color-ring);
  }
  /* Portaled to <body>; the menu-plate role owns fill and blur. */
  .strata-gauge-tip {
    position: fixed;
    z-index: 60;
    width: 17.5rem;
    transform: translateY(-50%);
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
    padding: 0.85rem 0.95rem;
    border-radius: 0.9rem;
    pointer-events: none;
    animation: strata-tip-in 140ms ease-out;
  }
  .strata-gauge-tip-title {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
    font-size: 0.875rem;
    font-weight: 700;
    color: var(--color-foreground);
  }
  .strata-gauge-tip-title span {
    font-family: ui-monospace, monospace;
    font-size: 10px;
    font-weight: 500;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--color-muted-foreground);
  }
  .strata-gauge-row {
    display: grid;
    grid-template-columns: 1.25rem minmax(0, 1fr) auto;
    align-items: center;
    column-gap: 0.6rem;
    row-gap: 0.3rem;
  }
  .strata-gauge-swatch {
    width: 0.75rem;
    height: 0.75rem;
    border-radius: 999px;
    border: 3px solid var(--swatch);
    justify-self: center;
  }
  .strata-gauge-label {
    display: flex;
    flex-direction: column;
    font-size: 0.8rem;
    font-weight: 600;
    color: var(--color-foreground);
    line-height: 1.2;
  }
  .strata-gauge-label small {
    font-size: 0.7rem;
    font-weight: 400;
    color: var(--color-muted-foreground);
  }
  .strata-gauge-value {
    font-family: ui-monospace, monospace;
    font-size: 0.8rem;
    font-variant-numeric: tabular-nums;
    color: var(--color-foreground);
  }
  .strata-gauge-bar {
    grid-column: 2 / 4;
    height: 3px;
    border-radius: 999px;
    background: var(--color-muted);
    overflow: hidden;
  }
  .strata-gauge-bar > span {
    display: block;
    height: 100%;
    border-radius: inherit;
  }
  .strata-gauge-row--count {
    padding-top: 0.55rem;
    border-top: 1px solid var(--color-border);
  }
  .strata-gauge-count {
    font-family: ui-monospace, monospace;
    font-size: 0.8rem;
    font-weight: 700;
    color: var(--color-muted-foreground);
    text-align: center;
  }
  .strata-gauge-tip-foot {
    font-size: 0.7rem;
    color: var(--color-muted-foreground);
  }
  @keyframes strata-tip-in {
    from {
      opacity: 0;
      transform: translate(4px, -50%);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .strata-gauge-tip {
      animation: none;
    }
  }
</style>
