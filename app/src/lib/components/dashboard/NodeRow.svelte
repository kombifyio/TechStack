<script lang="ts">
  import { tr, trn } from "#lib/i18n.svelte.js";
  import type { Snippet } from "svelte";
  import ServiceLogo from "#lib/components/dashboard/ServiceLogo.svelte";
  import {
    meterTone,
    nodeStatusSummary,
    serviceDisplayName,
    serviceState,
    type NodeView,
  } from "#lib/dashboard/homelab-model.js";

  interface Props {
    node: NodeView;
    detailsHref: string;
    onOpenService: (serviceId: string) => void;
    /** Tiles before "+ N more apps": 4 at two-thirds width, 6 at full. */
    maxTiles?: number;
    /** The rollout or failure that runs on this Node. */
    notice?: Snippet;
    /** Node-scoped controls (assign, managed-runtime actions, guidance). */
    actions?: Snippet;
  }

  let {
    node,
    detailsHref,
    onOpenService,
    maxTiles = 4,
    notice,
    actions,
  }: Props = $props();

  let showAllApps = $state(false);
  let showSystem = $state(false);

  const visibleApps = $derived(
    showAllApps ? node.apps : node.apps.slice(0, maxTiles),
  );
  const hiddenApps = $derived(node.apps.length - visibleApps.length);
  const meters = $derived([
    { label: "CPU", value: node.cpu },
    { label: "RAM", value: node.ram },
    { label: tr("ui.stacksIdServersServerId.disk"), value: node.disk },
  ]);
  const toneClass: Record<string, string> = {
    ok: "bg-success",
    warn: "bg-warning",
    error: "bg-destructive",
    off: "bg-muted-foreground/40",
  };
  const textTone: Record<string, string> = {
    ok: "text-success",
    warn: "text-warning",
    error: "text-destructive",
    off: "text-muted-foreground",
  };
  const status = $derived(nodeStatusSummary(node.axes));
  const facts = $derived(
    [node.role, node.kit, node.placement].filter(Boolean).join(" · "),
  );
  const address = $derived(
    node.address && node.address !== "not reported" ? node.address : "",
  );
  const systemNames = $derived(
    node.system
      .slice(0, 3)
      .map(serviceDisplayName)
      .join(", "),
  );
</script>

{#snippet tile(service: (typeof node.apps)[number])}
  {@const state = serviceState(service)}
  <button
    type="button"
    class="dashboard-tile flex min-w-0 items-center gap-2 rounded-[10px] border border-border bg-card/60 px-2 py-1.5 text-left transition-colors hover:border-primary/60 hover:bg-primary/5 focus-visible:outline-2 focus-visible:outline-primary"
    data-testid="node-app-tile"
    data-service-id={service.id}
    onclick={() => onOpenService(service.id)}
  >
    <ServiceLogo {service} class="h-6 w-6" />
    <span class="flex min-w-0 flex-col">
      <span class="truncate text-[13px] font-semibold text-foreground">
        {serviceDisplayName(service)}
      </span>
      {#if state.label}
        <span class="text-[11px] {textTone[state.tone]}">{state.label}</span>
      {/if}
    </span>
  </button>
{/snippet}

<article
  data-kx="plate"
  class="grid shrink-0 overflow-hidden md:grid-cols-[17.5rem_minmax(0,1fr)]"
  data-testid="node-row"
  data-node-id={node.nodeId}
>
  <div
    class="flex min-w-0 flex-col gap-2 border-b border-border bg-muted/20 px-3.5 py-3 md:border-r md:border-b-0"
  >
    <div class="flex min-w-0 items-center gap-2.5">
      <a
        href={detailsHref}
        class="min-w-0 truncate text-[17px] font-bold tracking-tight text-foreground hover:text-primary"
        data-testid="node-row-name"
      >
        {node.name}
      </a>
      <span
        class="ml-auto inline-flex shrink-0 items-center gap-1.5 text-[11px] font-medium {textTone[
          status.tone
        ]}"
        title={status.detail}
        data-testid="node-row-status"
      >
        <span class="h-2 w-2 rounded-full {toneClass[status.tone]}"></span>
        {status.label}
        <span class="sr-only">({status.detail})</span>
      </span>
    </div>
    <p
      class="-mt-1 truncate text-[12px] text-muted-foreground"
      title={[facts, address].filter(Boolean).join(" · ")}
      data-testid="node-row-facts"
    >
      {facts}{#if address}<span class="font-mono text-[11px] text-muted-foreground/80"
          >{facts ? " · " : ""}{address}</span
        >{/if}
    </p>
    <div class="grid grid-cols-3 gap-2">
      {#each meters as meter (meter.label)}
        {@const tone = meterTone(meter.value)}
        <div class="flex min-w-0 flex-col gap-1">
          <span class="text-[11px] text-muted-foreground">
            {meter.label}
            {meter.value === null ? "n/a" : `${Number(meter.value.toFixed(1))}%`}
          </span>
          <span class="h-1 overflow-hidden rounded-full bg-muted">
            <span
              class="block h-1 rounded-full {toneClass[tone]}"
              style={`width: ${meter.value ?? 0}%`}
            ></span>
          </span>
        </div>
      {/each}
    </div>
    {@render actions?.()}
  </div>

  <div class="flex min-w-0 flex-col gap-2 p-2.5">
    {@render notice?.()}
    {#if node.apps.length > 0}
      <div
        class="grid grid-cols-[repeat(auto-fill,minmax(9.5rem,1fr))] gap-2"
      >
        {#each visibleApps as service (service.id)}
          {@render tile(service)}
        {/each}
      </div>
    {:else}
      <p class="text-sm text-muted-foreground" data-testid="node-row-no-apps">
        {tr("ui.node.noApps")}
      </p>
    {/if}
    <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
      {#if hiddenApps > 0 || showAllApps}
        <button
          type="button"
          class="text-primary hover:underline"
          onclick={() => (showAllApps = !showAllApps)}
        >
          {showAllApps ? tr("ui.nodeRow.showFewerApps") : `+ ${hiddenApps} more apps`}
        </button>
      {/if}
      {#if node.system.length > 0}
        <button
          type="button"
          class="text-primary hover:underline"
          aria-expanded={showSystem}
          data-testid="node-row-system-toggle"
          onclick={() => (showSystem = !showSystem)}
        >
          {showSystem
            ? tr("ui.nodeRow.hideSystemServices")
            : `+ ${trn("ui.nodeRow.systemServices", node.system.length)} (${systemNames}${node.system.length > 3 ? "…" : ""})`}
        </button>
      {/if}
    </div>
    {#if showSystem}
      <div class="grid grid-cols-[repeat(auto-fill,minmax(9.5rem,1fr))] gap-2">
        {#each node.system as service (service.id)}
          {@render tile(service)}
        {/each}
      </div>
    {/if}
  </div>
</article>
