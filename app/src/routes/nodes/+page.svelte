<script lang="ts">
  /**
   * Nodes: every server and device of the homelab on one page, across all
   * StackKit deployments (owner direction 2026-10-10). Servers group by where
   * they run (Local, Cloud) and open their server page; devices are the
   * kombify clients linked to the account and open the card family's detail
   * sheet. Each source keeps its own honest state, so one outage never empties
   * the other.
   */
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { Cloud, HardDrive, Laptop, Monitor, Server, Smartphone } from "@lucide/svelte";
  import { PageHeader } from "@kombiverselabs/ui/shell";
  import { SegmentedControl } from "@kombiverselabs/ui/primitives";
  import {
    ServerCard,
    ServerCardCompact,
    type ServerFact,
    type ServerIcon,
  } from "@kombiverselabs/ui/server";
  import {
    DEFAULT_ENTITY_LABELS,
    DeviceCard,
    EntityCollection,
    EntityDetailSheet,
    deviceFormFactorFromPlatform,
    type EntityCardSize,
    type EntityFact,
    type EntityIcon,
    type EntityLabels,
    type EntityPresence,
  } from "@kombiverselabs/ui/entity";
  import { tr } from "#lib/i18n.svelte.js";
  import { listCanonicalServers } from "#lib/api/registry.js";
  import { listWorkers } from "#lib/api/workers.js";
  import { getHomelab } from "#lib/api/homelab.js";
  import {
    loadDevices,
    platformLabel,
    type DeviceEntry,
    type SectionState,
  } from "#lib/dashboard/people.js";
  import { formatRelative } from "#lib/dashboard/time.js";
  import {
    buildServerNodes,
    nodeCapacity,
    nodesByPlacement,
    type NodePlacement,
    type ServerNode,
  } from "#lib/nodes/model.js";

  type ServerView = "compact" | "detailed";
  const VIEW_KEY = "techstack.nodes.view";
  const VIEWS: ServerView[] = ["compact", "detailed"];

  let servers = $state<SectionState<ServerNode>>({ state: "loading" });
  let devices = $state<SectionState<DeviceEntry>>({ state: "loading" });
  let stackNames = $state(new Map<string, string>());
  let view = $state<ServerView>("compact");
  let deviceView = $state<EntityCardSize>("row");

  const groups = $derived(
    servers.state === "ready" ? nodesByPlacement(servers.items) : [],
  );
  const counts = $derived({
    servers: servers.state === "ready" ? servers.items.length : 0,
    devices: devices.state === "ready" ? devices.items.length : 0,
  });

  const placementIcon: Record<NodePlacement, ServerIcon> = {
    local: HardDrive as unknown as ServerIcon,
    cloud: Cloud as unknown as ServerIcon,
    unknown: Server as unknown as ServerIcon,
  };

  const D = DEFAULT_ENTITY_LABELS;
  const labels = $derived<Partial<EntityLabels>>({
    presence: {
      ...D.presence,
      online: tr("actors.presence.online"),
      offline: tr("actors.presence.offline"),
      unknown: tr("actors.presence.unknown"),
    },
  });

  // --- device selection, deep-linked as ?device= ---------------------------
  const selectedDeviceId = $derived(page.url.searchParams.get("device"));
  const selectedDevice = $derived(
    devices.state === "ready"
      ? (devices.items.find((entry) => entry.id === selectedDeviceId) ?? null)
      : null,
  );

  function selectDevice(id: string | null) {
    const url = new URL(page.url.href);
    url.searchParams.delete("device");
    if (id) url.searchParams.set("device", id);
    void goto(`${url.pathname}${url.search}`, { reset: false });
  }

  function presence(online: boolean | null): EntityPresence {
    if (online === null) return "unknown";
    return online ? "online" : "offline";
  }

  function deviceIcon(entry: DeviceEntry) {
    if (entry.platform === "android" || entry.platform === "ios")
      return Smartphone;
    if (entry.platform === "windows" || entry.platform === "linux")
      return Monitor;
    return Laptop;
  }

  // --- server card content -------------------------------------------------
  function serverHref(node: ServerNode): string | undefined {
    return node.serverId
      ? `/monitoring/${encodeURIComponent(node.serverId)}`
      : undefined;
  }

  function serverMeta(node: ServerNode, withAddress = true): string {
    return [
      node.pending ? tr("nodes.pending") : "",
      node.role === "substrate" ? tr("nodes.hypervisor") : "",
      stackNames.get(node.deploymentId) ?? "",
      node.os,
      withAddress ? node.address : "",
    ]
      .filter(Boolean)
      .join(" · ");
  }

  function serverFacts(node: ServerNode): ServerFact[] {
    const capacity = nodeCapacity(
      node,
      tr("nodes.unit.ram"),
      tr("nodes.unit.disk"),
      tr("nodes.unit.cores"),
    );
    return [
      capacity.length && {
        label: tr("nodes.fact.capacity"),
        value: capacity.join(" · "),
      },
      node.gpu && { label: tr("nodes.fact.gpu"), value: node.gpu },
      node.lastSeen && {
        label: tr("nodes.fact.lastSeen"),
        value: formatRelative(node.lastSeen),
      },
    ].filter((fact): fact is ServerFact => Boolean(fact));
  }

  function serverActions(node: ServerNode) {
    const href = serverHref(node);
    return href
      ? [{ id: "details" as const, onSelect: () => void goto(href) }]
      : [];
  }

  function deviceFacts(entry: DeviceEntry): EntityFact[] {
    return [
      { label: tr("nodes.fact.os"), value: platformLabel(entry.platform) },
      entry.lastSeen && {
        label: tr("nodes.fact.lastSeen"),
        value: formatRelative(entry.lastSeen),
      },
      entry.detail && { label: tr("nodes.fact.address"), value: entry.detail },
    ].filter((fact): fact is EntityFact => Boolean(fact && fact.value));
  }

  function setView(next: string) {
    view = next as ServerView;
    try {
      localStorage.setItem(VIEW_KEY, next);
    } catch {
      // Storage is a convenience; the view still switches.
    }
  }

  async function loadServers() {
    try {
      const [canonical, workers] = await Promise.all([
        listCanonicalServers(),
        listWorkers().catch(() => []),
      ]);
      servers = { state: "ready", items: buildServerNodes(canonical, workers) };
    } catch {
      servers = { state: "error", message: tr("nodes.serversError") };
    }
    try {
      const homelab = await getHomelab();
      stackNames = new Map(
        (homelab?.kit_deployments ?? []).map((item) => [item.id, item.name]),
      );
    } catch {
      // Stack names only label the cards; the list stands without them.
    }
  }

  onMount(() => {
    try {
      const stored = localStorage.getItem(VIEW_KEY);
      if (stored && (VIEWS as string[]).includes(stored))
        view = stored as ServerView;
    } catch {
      // No stored preference.
    }
    void loadServers();
    void loadDevices().then((result) => (devices = result));
  });
</script>

{#snippet sectionState(state: SectionState<unknown>, empty: string, testId: string)}
  {#if state.state === "loading"}
    <p role="status" class="text-sm text-muted-foreground">
      {tr("nodes.loading")}
    </p>
  {:else if state.state === "unavailable"}
    <p class="text-sm text-muted-foreground" data-testid={testId}>
      {state.message}
    </p>
  {:else if state.state === "error"}
    <p role="alert" class="text-sm text-destructive" data-testid={testId}>
      {state.message}
    </p>
  {:else if state.items.length === 0}
    <p class="text-sm text-muted-foreground" data-testid={testId}>{empty}</p>
  {/if}
{/snippet}

<div class="space-y-8 p-4 md:p-6" data-testid="nodes-page">
  <PageHeader title={tr("nav.nodes")} description={tr("nodes.description")}>
    {#snippet actions()}
      <SegmentedControl
        items={VIEWS.map((value) => ({
          value,
          label: tr(`nodes.view.${value}`),
        }))}
        value={view}
        label={tr("nodes.view.label")}
        onchange={setView}
      />
    {/snippet}
  </PageHeader>

  {#if servers.state === "ready" && devices.state === "ready"}
    <p class="text-sm text-muted-foreground" data-testid="nodes-summary">
      {tr("nodes.summary", counts)}
    </p>
  {/if}

  <section class="space-y-4" aria-labelledby="nodes-servers">
    <h2 id="nodes-servers" class="text-base font-semibold">
      {tr("nodes.servers")}
    </h2>
    {@render sectionState(servers, tr("nodes.serversEmpty"), "nodes-servers-state")}
    {#each groups as group (group.placement)}
      {@const Icon = placementIcon[group.placement]}
      <div class="space-y-2" data-testid={`nodes-group-${group.placement}`}>
        <h3
          class="flex items-center gap-1.5 text-[11px] font-semibold tracking-wider text-muted-foreground uppercase"
        >
          <Icon class="size-3.5" />
          {tr(`nodes.placement.${group.placement}`)} · {group.nodes.length}
        </h3>
        {#if view === "compact"}
          <ul class="grid grid-cols-1 gap-2 md:grid-cols-2 xl:grid-cols-3">
            {#each group.nodes as node (node.key)}
              <li class="min-w-0" data-testid="nodes-server">
                <ServerCardCompact
                  hostname={node.name}
                  meta={serverMeta(node)}
                  icon={placementIcon[node.placement]}
                  axes={node.axes}
                  actions={serverActions(node)}
                />
              </li>
            {/each}
          </ul>
        {:else}
          <ul class="grid grid-cols-1 gap-3 lg:grid-cols-2">
            {#each group.nodes as node (node.key)}
              <li class="min-w-0" data-testid="nodes-server">
                <ServerCard
                  hostname={node.name}
                  meta={serverMeta(node, false)}
                  icon={placementIcon[node.placement]}
                  axes={node.axes}
                  facts={serverFacts(node)}
                  address={node.address || undefined}
                  detailsHref={serverHref(node)}
                  nodeActions={serverActions(node)}
                />
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    {/each}
  </section>

  <section class="space-y-3" aria-labelledby="nodes-devices">
    <h2 id="nodes-devices" class="text-base font-semibold">
      {tr("nodes.devices")}
    </h2>
    {@render sectionState(devices, tr("nodes.devicesEmpty"), "nodes-devices-state")}
    {#if devices.state === "ready" && devices.items.length}
      <EntityCollection
        items={devices.items}
        key={(entry) => entry.id}
        label={tr("nodes.devices")}
        bind:view={deviceView}
        views={["mark", "row", "card"]}
        {labels}
      >
        {#snippet item(entry: DeviceEntry, size: EntityCardSize)}
          <DeviceCard
            {size}
            name={entry.name}
            formFactor={deviceFormFactorFromPlatform(entry.platform)}
            model={platformLabel(entry.platform) || undefined}
            lastSeen={entry.lastSeen ? formatRelative(entry.lastSeen) : undefined}
            clients={entry.clients}
            presence={presence(entry.online)}
            selected={entry.id === selectedDeviceId}
            onselect={() => selectDevice(entry.id)}
            {labels}
          />
        {/snippet}
      </EntityCollection>
    {/if}
  </section>
</div>

{#if selectedDevice}
  <EntityDetailSheet
    open
    onclose={() => selectDevice(null)}
    kind="device"
    name={selectedDevice.name}
    meta={platformLabel(selectedDevice.platform) || undefined}
    icon={deviceIcon(selectedDevice) as unknown as EntityIcon}
    presence={presence(selectedDevice.online)}
    tags={selectedDevice.clients}
    facts={deviceFacts(selectedDevice)}
    {labels}
  />
{/if}
