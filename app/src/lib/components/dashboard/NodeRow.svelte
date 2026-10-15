<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import type { Snippet } from "svelte";
  import { Ellipsis } from "@lucide/svelte";
  import ApplicationCard from "#lib/components/dashboard/ApplicationCard.svelte";
  import type { CanonicalService } from "#lib/api/services.js";
  import NodeMessageLine from "#lib/components/dashboard/NodeMessageLine.svelte";
  import OverlayPanel from "#lib/components/ui/OverlayPanel.svelte";
  import GuardBadge from "#lib/components/guard/GuardBadge.svelte";
  import {
    meterTone,
    nodeStatusSummary,
    offeringPlacement,
    type NodeView,
  } from "#lib/dashboard/homelab-model.js";
  import type { NodeMessage } from "#lib/dashboard/node-messages.js";
  import { appQuickActions } from "#lib/services/app-quick-actions.svelte.js";

  interface Props {
    node: NodeView;
    detailsHref: string;
    onOpenService: (serviceId: string) => void;
    /** What this Node has to say; shown as one fixed status line. */
    messages?: readonly NodeMessage[];
    /** The details of one message; they open as an overlay on click. */
    messageDetails?: Snippet<[NodeMessage]>;
    /** Node-scoped controls (assign, managed-runtime actions); opened from the menu. */
    actions?: Snippet;
  }

  let {
    node,
    detailsHref,
    onOpenService,
    messages = [],
    messageDetails,
    actions,
  }: Props = $props();

  // Every card holds five rows of one-line app tiles, so a Node with many apps
  // and one with none are the same height. When the apps do not fit, the last
  // cell opens the rest as an overlay. Platform services stay on the server's
  // own service list.
  const TILE_ROWS = 5;
  const TILE_MIN_REM = 13;
  const TILE_GAP_PX = 6;
  let tileAreaWidth = $state(0);
  let moreAnchor = $state<HTMLButtonElement | null>(null);
  let showMore = $state(false);
  let menuAnchor = $state<HTMLButtonElement | null>(null);
  let showMenu = $state(false);

  const columns = $derived.by(() => {
    if (tileAreaWidth <= 0) return 2;
    const rem =
      parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
    const tile = TILE_MIN_REM * rem;
    return Math.max(
      1,
      Math.floor((tileAreaWidth + TILE_GAP_PX) / (tile + TILE_GAP_PX)),
    );
  });
  const capacity = $derived(columns * TILE_ROWS);
  const shown = $derived(
    node.apps.length > capacity ? capacity - 1 : node.apps.length,
  );
  const visibleApps = $derived(node.apps.slice(0, shown));
  const overflowApps = $derived(node.apps.slice(shown));
  const overflowCount = $derived(overflowApps.length);

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

  const quick = appQuickActions();

  function membersOf(service: CanonicalService): CanonicalService[] {
    return node.members[service.id] ?? [service];
  }

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
</script>

{#snippet tile(service: (typeof node.apps)[number], afterOpen?: () => void)}
  <ApplicationCard
    {service}
    members={membersOf(service)}
    testId="node-app-tile"
    placement={offeringPlacement(node.canonical?.offering)}
    actions={quick?.actionsFor(membersOf(service)) ?? []}
    onDetails={() => {
      afterOpen?.();
      onOpenService(service.id);
    }}
  />
{/snippet}

{#snippet noDetails()}{/snippet}

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
      {#if actions}
        <button
          bind:this={menuAnchor}
          type="button"
          class="-my-1 -mr-1.5 inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-primary"
          aria-haspopup="true"
          aria-expanded={showMenu}
          aria-label={tr("ui.nodeActions.menu", { node: node.name })}
          title={tr("ui.nodeActions.menu", { node: node.name })}
          data-testid="node-actions-toggle"
          onclick={() => (showMenu = !showMenu)}
        >
          <Ellipsis class="h-4 w-4" aria-hidden="true" />
        </button>
      {/if}
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
    <div class="-mt-0.5 flex h-[22px] items-center">
      {#if node.canonical}
        <GuardBadge
          server={node.canonical}
          capabilities={node.server?.capabilities}
          href={detailsHref}
        />
      {/if}
    </div>
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
  </div>

  <div class="flex h-[19rem] min-w-0 flex-col overflow-hidden p-2.5">
    <div
      class="grid content-start gap-1.5 {node.apps.length === 0 ? 'hidden' : ''}"
      style:grid-template-columns="repeat({columns}, minmax(0, 1fr))"
      bind:clientWidth={tileAreaWidth}
    >
      {#each visibleApps as service (service.id)}
        {@render tile(service)}
      {/each}
      {#if overflowCount > 0}
        <button
          bind:this={moreAnchor}
          type="button"
          class="flex h-[3.25rem] items-center justify-center rounded-lg border border-dashed border-border text-xs font-medium text-primary hover:border-primary/40 hover:bg-muted/40"
          aria-haspopup="true"
          aria-expanded={showMore}
          data-testid="node-row-more"
          onclick={() => (showMore = !showMore)}
        >
          {tr("ui.nodeRow.moreServices", { count: overflowCount })}
        </button>
      {/if}
    </div>
    {#if node.apps.length === 0}
      <p
        class="flex flex-1 items-center text-sm text-muted-foreground"
        data-testid="node-row-no-apps"
      >
        {tr("ui.node.noApps")}
      </p>
    {/if}
  </div>

  <NodeMessageLine
    class="md:col-span-2"
    {messages}
    details={messageDetails ?? noDetails}
    label={tr("ui.nodeMessage.panel", { node: node.name })}
  />
</article>

{#if showMore}
  <OverlayPanel
    anchor={moreAnchor}
    label={tr("ui.nodeRow.moreLabel", { node: node.name })}
    testId="node-more-overlay"
    onClose={() => (showMore = false)}
  >
    <div class="grid grid-cols-[repeat(auto-fill,minmax(13rem,1fr))] gap-1.5 p-3">
      {#each overflowApps as service (service.id)}
        {@render tile(service, () => (showMore = false))}
      {/each}
    </div>
  </OverlayPanel>
{/if}

{#if showMenu && actions}
  <OverlayPanel
    anchor={menuAnchor}
    label={tr("ui.nodeActions.menu", { node: node.name })}
    testId="node-actions-overlay"
    width={340}
    onClose={() => (showMenu = false)}
  >
    <div class="flex flex-col gap-2 p-3" data-testid="node-actions-menu">
      {@render actions()}
    </div>
  </OverlayPanel>
{/if}
