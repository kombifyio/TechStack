<script lang="ts">
  import type { Snippet } from "svelte";
  import { Box, Server } from "@lucide/svelte";
  import {
    ServiceCard,
    ServiceCardCompact,
    type ServiceCardAction,
    type ServiceCardActions,
    type ServiceMetric,
    type ServicePlacement,
    type ServiceStatusKind,
  } from "@kombiverselabs/ui/service";
  import BrandLogoIcon from "#lib/components/BrandLogoIcon.svelte";
  import BrandLogoScope from "#lib/components/BrandLogoScope.svelte";

  export interface ServiceListItem extends ServiceCardActions {
    id: string;
    name: string;
    meta: string;
    /**
     * Capability-driven card actions (backend allowed_actions). Takes
     * precedence over the deprecated per-callback fields when present.
     */
    actions?: ServiceCardAction[];
    /** Desired lifecycle state — renders the drift chip/dot with observedState. */
    desiredState?: string;
    /** Observed lifecycle state — renders the drift chip/dot with desiredState. */
    observedState?: string;
    description?: string;
    address?: string;
    addressUnavailableReason?: string;
    detailsHref?: string;
    placement: ServicePlacement;
    status: ServiceStatusKind;
    statusLabel?: string;
    statusMessage?: string;
    managementLabel?: string;
    metrics?: ServiceMetric[];
    /** Stable key for the real runtime target, not necessarily a server. */
    runtimeTargetId: string;
    /* Row facts consumed by the detail sheet — the card no longer renders
     * them (the fact wall moved off the card, 2026-08-30). */
    freshnessLabel?: string;
    sourceLabel?: string;
    workflowLabel?: string;
    /** Human label supplied by the authoritative read model when available. */
    targetLabel?: string;
    targetKind?: "server" | "managed_workload" | "unknown" | string;
    /** Vendor domain for context.dev Logo Link; empty keeps the generic glyph. */
    logoDomain?: string;
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

{#snippet detailedCard(item: ServiceListItem)}
  {@const cardProps = {
    name: item.name,
    description: item.description || item.meta,
    address: item.address,
    addressUnavailableReason: item.addressUnavailableReason,
    detailsHref: item.detailsHref,
    icon: BrandLogoIcon,
    placement: item.placement,
    status: item.status,
    statusLabel: item.statusLabel,
    statusMessage: item.statusMessage,
    managementLabel: item.managementLabel,
    metrics: item.metrics,
    actions: item.actions,
    desiredState: item.desiredState,
    observedState: item.observedState,
    onOpen: item.onOpen,
    onLogs: item.onLogs,
    onEdit: item.onEdit,
    onFreeze: item.onFreeze,
    onUnfreeze: item.onUnfreeze,
    onUpdate: item.onUpdate,
    onRestart: item.onRestart,
    onInfo: item.onInfo,
  }}
  <BrandLogoScope domain={item.logoDomain || ""}>
    <div data-testid={cardTestId} data-service-id={item.id}>
      {#if children}
        <ServiceCard {...cardProps}>
          {#snippet footer()}
            {@render children(item)}
          {/snippet}
        </ServiceCard>
      {:else}
        <ServiceCard {...cardProps} />
      {/if}
    </div>
  </BrandLogoScope>
{/snippet}

{#snippet compactCard(item: ServiceListItem)}
  <BrandLogoScope domain={item.logoDomain || ""}>
    <div data-testid={cardTestId} data-service-id={item.id}>
      <ServiceCardCompact
        name={item.name}
        meta={item.meta}
        icon={BrandLogoIcon}
        placement={item.placement}
        status={item.status}
        statusLabel={item.statusLabel}
        managementLabel={item.managementLabel}
        actions={item.actions}
        desiredState={item.desiredState}
        observedState={item.observedState}
        showGrip={false}
        onOpen={item.onOpen}
        onLogs={item.onLogs}
        onRestart={item.onRestart}
      />
    </div>
  </BrandLogoScope>
{/snippet}

<!-- Boxless section (operator direction 2026-08-19): heading and service
     tiles directly on the page ground — no wrapper container card. -->
<section data-testid={testId} aria-label={title}>
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
        <!-- Groups separate with a hairline, not a nested box. -->
        <section
          class="border-t border-border/40 pt-4 first:border-t-0 first:pt-0"
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
              <div class="mb-3 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
                {#each attentionItems as item (item.id)}
                  {@render detailedCard(item)}
                {/each}
              </div>
            {/if}
            {#if normalItems.length > 0}
              <div
                class="grid gap-2 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4"
              >
                {#each normalItems as item (item.id)}
                  {@render compactCard(item)}
                {/each}
              </div>
            {/if}
          {:else}
            <!-- Up to four tiles side by side on wide screens; the tiles stay
                 compact instead of stretching (operator direction 2026-08-19). -->
            <div
              class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
            >
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
