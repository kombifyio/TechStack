<script lang="ts">
  import {
    ServerCard,
    type ServerKitInfo,
    type ServerFact,
    type ServerMetric,
    type ServerNoteTone,
    type ServerStatusKind,
  } from "$lib/components/open-core";

  export interface ServerListItem {
    id: string;
    hostname: string;
    meta: string;
    status: ServerStatusKind;
    statusLabel?: string;
    metrics?: ServerMetric[];
    facts?: ServerFact[];
    address?: string;
    domain?: string;
    domainExtraCount?: number;
    kit?: ServerKitInfo;
    note?: string;
    noteTone?: ServerNoteTone;
    detailsHref?: string;
  }

  interface Props {
    items: ServerListItem[];
    title?: string;
    subtitle?: string;
    unavailable?: boolean;
    unavailableMessage?: string;
    emptyTitle: string;
    emptyBody: string;
    testId?: string;
    cardTestId?: string;
  }

  let {
    items,
    title = "Servers",
    subtitle,
    unavailable = false,
    unavailableMessage = "The canonical inventory is unavailable. Showing only telemetry that is currently authorized.",
    emptyTitle,
    emptyBody,
    testId = "server-list",
    cardTestId = "server-list-card",
  }: Props = $props();
</script>

<section
  class="rounded-lg border border-border bg-card p-5"
  data-testid={testId}
  aria-label={title}
>
  <div class="mb-4 flex items-start justify-between gap-3">
    <div>
      <h2 class="text-xl font-semibold text-foreground">{title}</h2>
      {#if subtitle}<p class="mt-1 text-sm text-muted-foreground">
          {subtitle}
        </p>{/if}
    </div>
    <span class="shrink-0 text-sm text-muted-foreground">
      {#if unavailable}
        {items.length > 0
          ? `${items.length} telemetry server${items.length === 1 ? "" : "s"}`
          : "Inventory unavailable"}
      {:else}
        {items.length} server{items.length === 1 ? "" : "s"}
      {/if}
    </span>
  </div>

  {#if unavailable}
    <p
      class="mb-4 rounded-lg border border-dashed border-warning/40 bg-warning/10 p-3 text-sm text-warning"
      data-testid="inventory-unavailable"
    >
      {unavailableMessage}
    </p>
  {/if}

  {#if items.length === 0}
    <div
      class="rounded-lg border border-dashed border-border bg-background/40 p-5 text-center"
    >
      <p class="font-medium text-foreground">{emptyTitle}</p>
      <p class="mt-1 text-sm text-muted-foreground">{emptyBody}</p>
    </div>
  {:else}
    <div class="grid gap-3 xl:grid-cols-2">
      {#each items as item (item.id)}
        <div data-testid={cardTestId} data-server-id={item.id}>
          <ServerCard
            hostname={item.hostname}
            meta={item.meta}
            status={item.status}
            statusLabel={item.statusLabel}
            metrics={item.metrics}
            facts={item.facts}
            address={item.address}
            domain={item.domain}
            domainExtraCount={item.domainExtraCount}
            kit={item.kit}
            note={item.note}
            noteTone={item.noteTone}
            detailsHref={item.detailsHref}
          />
        </div>
      {/each}
    </div>
  {/if}
</section>
