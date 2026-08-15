<script lang="ts">
  import type { Snippet } from "svelte";
  import { Box, Server } from "@lucide/svelte";
  import {
    ServiceCard,
    ServiceCardCompact,
    type ServiceCardActions,
    type ServiceMetric,
    type ServicePlacement,
    type ServiceStatusKind,
  } from "$lib/components/open-core";

  export interface ServiceListItem extends ServiceCardActions {
    id: string;
    name: string;
    meta: string;
    description?: string;
    placement: ServicePlacement;
    status: ServiceStatusKind;
    statusLabel?: string;
    statusMessage?: string;
    managementLabel?: string;
    metrics?: ServiceMetric[];
    /** Stable key for the real runtime target, not necessarily a server. */
    runtimeTargetId: string;
    /** Human label supplied by the authoritative read model when available. */
    targetLabel?: string;
    targetKind?: "server" | "managed_workload" | "unknown" | string;
    workflowLabel?: string;
    freshnessLabel?: string;
    sourceLabel?: string;
    details?: string;
    /** Attention services use the detailed standard card in adaptive lists. */
    attention?: boolean;
  }

  export interface ServiceListGroup {
    id: string;
    name: string;
    meta?: string;
    targetKind?: "server" | "managed_workload" | "unknown" | string;
    items: ServiceListItem[];
  }

  interface Props {
    groups: ServiceListGroup[];
    title?: string;
    countLabel?: string;
    emptyTitle: string;
    emptyBody: string;
    display?: "detailed" | "adaptive";
    testId?: string;
    cardTestId?: string;
    children?: Snippet<[ServiceListItem]>;
  }

  let {
    groups,
    title = "Services by runtime target",
    countLabel,
    emptyTitle,
    emptyBody,
    display = "detailed",
    testId = "runtime-service-list",
    cardTestId = "runtime-service-card",
    children,
  }: Props = $props();

  let total = $derived(
    groups.reduce((sum, group) => sum + group.items.length, 0),
  );
</script>

{#snippet serviceFacts(item: ServiceListItem)}
  <div class="space-y-3">
    <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
      {#if item.details}<span>{item.details}</span>{/if}
      {#if item.workflowLabel}<span>Operation: {item.workflowLabel}</span>{/if}
      {#if item.freshnessLabel}<span>Freshness: {item.freshnessLabel}</span
        >{/if}
      {#if item.sourceLabel}<span>Source: {item.sourceLabel}</span>{/if}
      <span>
        Runtime target: {item.targetLabel || item.runtimeTargetId}
      </span>
    </div>
    {#if children}
      {@render children(item)}
    {/if}
  </div>
{/snippet}

{#snippet detailedCard(item: ServiceListItem)}
  <div data-testid={cardTestId} data-service-id={item.id}>
    <ServiceCard
      name={item.name}
      description={item.description || item.meta}
      placement={item.placement}
      status={item.status}
      statusLabel={item.statusLabel}
      statusMessage={item.statusMessage}
      managementLabel={item.managementLabel}
      metrics={item.metrics}
      onOpen={item.onOpen}
      onLogs={item.onLogs}
      onEdit={item.onEdit}
      onFreeze={item.onFreeze}
      onUnfreeze={item.onUnfreeze}
      onUpdate={item.onUpdate}
      onRestart={item.onRestart}
      onInfo={item.onInfo}
    >
      {#snippet footer()}
        {@render serviceFacts(item)}
      {/snippet}
    </ServiceCard>
  </div>
{/snippet}

{#snippet compactCard(item: ServiceListItem)}
  <div data-testid={cardTestId} data-service-id={item.id}>
    <ServiceCardCompact
      name={item.name}
      meta={item.meta}
      placement={item.placement}
      status={item.status}
      statusLabel={item.statusLabel}
      managementLabel={item.managementLabel}
      showGrip={false}
      onOpen={item.onOpen}
      onLogs={item.onLogs}
      onRestart={item.onRestart}
    />
  </div>
{/snippet}

<section
  class="rounded-lg border border-border bg-card p-4"
  data-testid={testId}
  aria-label={title}
>
  <div class="mb-4 flex items-center justify-between gap-3">
    <div>
      <h2 class="font-semibold text-foreground">{title}</h2>
      <p class="mt-1 text-xs text-muted-foreground">
        {countLabel || `${total} service${total === 1 ? "" : "s"}`}
      </p>
    </div>
  </div>

  {#if total === 0}
    <div
      class="rounded-lg border border-dashed border-border bg-background/40 p-6 text-center"
    >
      <p class="font-medium text-foreground">{emptyTitle}</p>
      <p class="mt-1 text-sm text-muted-foreground">{emptyBody}</p>
    </div>
  {:else}
    <div class="space-y-4">
      {#each groups as group (group.id)}
        <section
          class="rounded-lg border border-border/80 bg-background/35 p-3"
          data-testid="runtime-service-group"
          data-runtime-target-id={group.id}
        >
          <div class="mb-3 flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                {#if group.targetKind === "server"}
                  <Server class="h-4 w-4 shrink-0 text-primary" />
                {:else}
                  <Box class="h-4 w-4 shrink-0 text-primary" />
                {/if}
                <h3 class="truncate text-sm font-semibold text-foreground">
                  {group.name}
                </h3>
              </div>
              {#if group.meta}
                <p class="mt-1 truncate text-xs text-muted-foreground">
                  {group.meta}
                </p>
              {/if}
            </div>
            <span class="shrink-0 text-xs text-muted-foreground">
              {group.items.length} service{group.items.length === 1 ? "" : "s"}
            </span>
          </div>

          {#if display === "adaptive"}
            {@const attentionItems = group.items.filter(
              (item) => item.attention,
            )}
            {@const normalItems = group.items.filter((item) => !item.attention)}
            {#if attentionItems.length > 0}
              <div class="mb-3 grid gap-3 md:grid-cols-2">
                {#each attentionItems as item (item.id)}
                  {@render detailedCard(item)}
                {/each}
              </div>
            {/if}
            {#if normalItems.length > 0}
              <div class="grid gap-2 md:grid-cols-2 xl:grid-cols-3">
                {#each normalItems as item (item.id)}
                  {@render compactCard(item)}
                {/each}
              </div>
            {/if}
          {:else}
            <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
              {#each group.items as item (item.id)}
                {@render detailedCard(item)}
              {/each}
            </div>
          {/if}
        </section>
      {/each}
    </div>
  {/if}
</section>
