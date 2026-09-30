<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import type { Snippet } from "svelte";

  import NodeRow from "#lib/components/dashboard/NodeRow.svelte";
  import PeoplePanel from "#lib/components/dashboard/PeoplePanel.svelte";
  import {
    groupNodesByDeployment,
    type NodeView,
  } from "#lib/dashboard/homelab-model.js";
  import type {
    DeviceEntry,
    PersonEntry,
    SectionState,
  } from "#lib/dashboard/people.js";

  interface Props {
    nodes: NodeView[];
    deploymentName: (deploymentId: string) => string;
    deploymentHref: (deploymentId: string) => string;
    nodeHref: (node: NodeView) => string;
    onOpenService: (serviceId: string) => void;
    people: SectionState<PersonEntry>;
    devices: SectionState<DeviceEntry>;
    canonicalInventoryUnavailable?: boolean;
    /** The rollout or failure that belongs to one Node. */
    nodeNotice?: Snippet<[NodeView]>;
    /** Node-scoped controls rendered under the Node's facts. */
    nodeActions?: Snippet<[NodeView]>;
  }

  let {
    nodes,
    deploymentName,
    deploymentHref,
    nodeHref,
    onOpenService,
    people,
    devices,
    canonicalInventoryUnavailable = false,
    nodeNotice,
    nodeActions,
  }: Props = $props();

  // Below 1280 px the devices column becomes a tab beside the Nodes.
  let tab = $state<"nodes" | "devices">("nodes");
  const groups = $derived(groupNodesByDeployment(nodes, deploymentName));
</script>

<!-- Viewport-bound: the host gives this a bounded height; the Node list and
     the devices column scroll on their own, the page never grows. -->
<div
  class="flex flex-1 flex-col gap-3"
  data-testid="dashboard-complete"
>
  <div
    class="flex shrink-0 gap-1 xl:hidden"
    role="tablist"
    aria-label={tr("ui.dashboard.sections")}
  >
    {#each [["nodes", tr("ui.dashboard.nodes")], ["devices", tr("ui.dashboard.devicesPeople")]] as [id, label] (id)}
      <button
        type="button"
        role="tab"
        aria-selected={tab === id}
        data-kx={tab === id ? "control selection" : "control"}
        class="rounded-lg px-3 py-1.5 text-sm"
        data-testid="dashboard-tab-{id}"
        onclick={() => (tab = id as "nodes" | "devices")}
      >
        {label}
      </button>
    {/each}
  </div>

  <div
    class="grid min-h-[18rem] flex-1 basis-0 grid-rows-[minmax(0,1fr)] gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]"
  >
    <section
      class="{tab === 'devices' ? 'hidden xl:flex' : 'flex'} min-h-0 min-w-0 flex-col gap-2"
      aria-labelledby="dashboard-nodes-title"
      data-testid="server-inventory-panel"
    >
      <div class="flex shrink-0 items-baseline justify-between gap-2">
        <h2
          id="dashboard-nodes-title"
          class="text-base font-semibold text-foreground"
        >
          {tr("ui.completeDashboard.nodesApps")}
        </h2>
        <a class="text-sm text-primary hover:underline" href="/services"
          >{tr("ui.completeDashboard.allServices")}</a
        >
      </div>

      <div
        class="dashboard-scroll flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto pr-1"
        data-testid="dashboard-node-list"
      >
      {#if canonicalInventoryUnavailable}
          <p
            class="rounded-lg border border-dashed border-warning/40 bg-warning/10 p-3 text-sm text-warning"
            role="status"
            data-testid="dashboard-inventory-unavailable"
          >
            {tr("ui.dashboard.inventoryUnavailable")}
          </p>
        {/if}
  
        {#if nodes.length === 0}
          <p class="text-sm text-muted-foreground">
            {tr("ui.dashboard.noNodes")}
          </p>
        {/if}
  
        {#each groups as group (group.key)}
          <div class="flex shrink-0 items-stretch gap-3" data-testid="node-group">
            {#if group.federated}
              <!-- A federated deployment: its Nodes stay together, bundled by
                   a thin line carrying the deployment's name. -->
              <div
                class="federation-bundle flex w-[22px] shrink-0 flex-col items-center gap-1.5 py-1.5"
                data-testid="federation-bundle"
              >
                <span class="w-0.5 flex-1 rounded-sm"></span>
                <a
                  href={deploymentHref(group.deploymentId)}
                  class="federation-label font-mono text-[10px] tracking-[0.12em] uppercase"
                  >{group.label}</a
                >
                <span class="w-0.5 flex-1 rounded-sm"></span>
              </div>
            {/if}
            <div class="flex min-w-0 flex-1 flex-col gap-2.5">
              {#each group.nodes as node (node.key)}
                <NodeRow
                  {node}
                  detailsHref={nodeHref(node)}
                  {onOpenService}
                >
                  {#snippet notice()}
                    {@render nodeNotice?.(node)}
                  {/snippet}
                  {#snippet actions()}
                    {@render nodeActions?.(node)}
                  {/snippet}
                </NodeRow>
              {/each}
            </div>
          </div>
        {/each}
      </div>
    </section>

    <div
      class="{tab === 'nodes'
        ? 'hidden xl:block'
        : 'block'} dashboard-scroll min-h-0 min-w-0 overflow-y-auto pr-1"
      data-testid="dashboard-device-list"
    >
      <PeoplePanel {people} {devices} />
    </div>
  </div>
</div>

<style>
  /* Subtle scroll affordance: a thin bar and a short fade at both edges. */
  .dashboard-scroll {
    scrollbar-width: thin;
    scrollbar-color: color-mix(in oklab, var(--color-muted-foreground) 35%, transparent)
      transparent;
    mask-image: linear-gradient(
      to bottom,
      transparent 0,
      #000 10px,
      #000 calc(100% - 14px),
      transparent 100%
    );
    padding-block: 6px;
  }
  /* Federation is a relation, not a status, so it gets its own hue instead
     of borrowing a semantic token that already means something. */
  .federation-bundle {
    --federation: oklch(0.66 0.17 290);
  }
  .federation-bundle span {
    background: var(--federation);
  }
  .federation-label {
    writing-mode: vertical-rl;
    transform: rotate(180deg);
    color: color-mix(in oklab, var(--federation) 70%, var(--color-foreground));
    text-decoration: none;
  }
  .federation-label:hover {
    text-decoration: underline;
  }
</style>
