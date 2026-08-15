<script lang="ts">
  import {
    ArrowUpCircle,
    Box,
    ExternalLink,
    Info,
    Settings,
    Snowflake,
    SquareTerminal,
  } from "@lucide/svelte";
  import type { Snippet } from "svelte";
  import type {
    ServiceCardActions,
    ServiceCardDisplay,
    ServiceIcon,
    ServiceMetric,
    ServiceMigration,
    ServicePlacement,
    ServiceStatusKind,
    ServiceUpdate,
  } from "./service";

  interface Props extends ServiceCardActions, ServiceCardDisplay {
    name: string;
    description?: string;
    icon?: ServiceIcon;
    placement: ServicePlacement;
    status?: ServiceStatusKind;
    metrics?: ServiceMetric[];
    migration?: ServiceMigration;
    update?: ServiceUpdate;
    statusMessage?: string;
    children?: Snippet;
    footer?: Snippet;
    class?: string;
  }

  let {
    name,
    description,
    icon,
    placement,
    status = "running",
    metrics = [],
    migration,
    update,
    statusLabel,
    statusMessage,
    managementLabel,
    children,
    footer,
    onOpen,
    onLogs,
    onEdit,
    onFreeze,
    onUnfreeze,
    onUpdate,
    onRestart,
    onInfo,
    class: className = "",
  }: Props = $props();

  const GlyphIcon = $derived(icon ?? Box);
  const locked = $derived(status === "migrating");
  const frozen = $derived(status === "frozen");
  const placementLabel = $derived(
    placement === "serverless"
      ? "Serverless"
      : placement === "cloud"
        ? "Cloud"
        : placement === "local"
          ? "Local"
          : "Unknown",
  );
  const badge = $derived.by(() => {
    const labels: Record<ServiceStatusKind, string> = {
      running: "Healthy",
      stopped: "Stopped",
      migrating: "Migrating",
      update: "Update",
      frozen: "Frozen",
      error: "Error",
      pending: "Pending",
      unknown: "Unknown",
    };
    const tones: Record<ServiceStatusKind, string> = {
      running: "text-success bg-success/10 border-success/30",
      stopped: "text-muted-foreground bg-muted border-border",
      migrating: "text-info bg-info/10 border-info/30",
      update: "text-warning bg-warning/10 border-warning/30",
      frozen: "text-info bg-info/10 border-info/30",
      error: "text-destructive bg-destructive/10 border-destructive/30",
      pending: "text-info bg-info/10 border-info/30",
      unknown: "text-muted-foreground bg-muted border-border",
    };
    return { label: statusLabel ?? labels[status], tone: tones[status] };
  });
</script>

<article
  class={`rounded-[10px] border border-border bg-card p-5 transition-all hover:-translate-y-0.5 hover:border-primary/50 hover:shadow-lg ${className}`}
  data-status={status}
>
  <div class="mb-4 flex items-start gap-3.5">
    <span
      class="grid h-12 w-12 shrink-0 place-items-center rounded-xl bg-primary/10 text-primary"
      aria-hidden="true"><GlyphIcon class="h-6 w-6" /></span
    >
    <div class="min-w-0 flex-1">
      <h3 class="truncate text-base font-bold tracking-tight text-foreground">
        {name}
      </h3>
      {#if description}<p class="mt-px truncate text-xs text-muted-foreground">
          {description}
        </p>{/if}
      {#if managementLabel}<p class="mt-1 truncate text-[10px] text-muted-foreground">
          {managementLabel}
        </p>{/if}
    </div>
    <span
      class={`shrink-0 rounded-full border px-2 py-0.5 text-[10px] font-semibold ${badge.tone}`}
      >{badge.label}</span
    >
  </div>

  <div
    class="mb-4 flex items-center gap-2 font-mono text-[10px] uppercase tracking-wider text-muted-foreground"
    aria-label={`Placement: ${placementLabel}`}
  >
    <span
      class={`h-2 w-2 rounded-full ${placement === "local" ? "bg-primary" : "bg-muted-foreground/40"}`}
    ></span>Local
    <span class="h-px flex-1 bg-border"></span>
    <span
      class={`h-2 w-2 rounded-full ${placement !== "local" ? "bg-primary" : "bg-muted-foreground/40"}`}
    ></span>{placementLabel}
    {#if migration}<span class="ml-1 text-info"
        >{Math.round(migration.progress)}%</span
      >{/if}
  </div>

  {#if status === "migrating" && migration?.message}
    <div class="mb-4 rounded-lg bg-info/10 px-3 py-2 text-xs text-info">
      {migration.message}
    </div>
  {:else if status === "update" && update}
    <div
      class="mb-4 flex items-center gap-2 rounded-lg bg-warning/10 px-3 py-2 text-xs text-warning"
    >
      <ArrowUpCircle class="h-3.5 w-3.5" />{update.from} → {update.to} available{#if onUpdate}<button
          type="button"
          class="ml-auto font-semibold underline"
          onclick={onUpdate}>Update</button
        >{/if}
    </div>
  {:else if frozen}
    <div
      class="mb-4 flex items-center gap-2 rounded-lg bg-info/10 px-3 py-2 text-xs text-info"
    >
      <Snowflake class="h-3.5 w-3.5" />{statusMessage ??
        "Frozen — configuration and updates are locked"}{#if onUnfreeze}<button
          type="button"
          class="ml-auto font-semibold underline"
          onclick={onUnfreeze}>Unfreeze</button
        >{/if}
    </div>
  {:else if status === "error"}
    <div
      class="mb-4 flex items-center gap-2 rounded-lg bg-destructive/10 px-3 py-2 text-xs text-destructive"
    >
      <Info class="h-3.5 w-3.5" />{statusMessage ??
        "Service reported an error"}{#if onRestart}<button
          type="button"
          class="ml-auto font-semibold underline"
          onclick={onRestart}>Restart</button
        >{/if}
    </div>
  {/if}

  {#if metrics.length > 0}
    <div
      class={`mb-4 grid grid-cols-3 gap-2 ${locked || frozen ? "opacity-60" : ""}`}
    >
      {#each metrics.slice(0, 3) as metric (metric.label)}
        <div class="rounded-lg bg-muted/40 px-2 py-2 text-center">
          <div class="font-mono text-sm font-semibold text-foreground">
            {metric.value}
          </div>
          <div
            class="mt-px text-[10px] uppercase tracking-wider text-muted-foreground"
          >
            {metric.label}
          </div>
        </div>
      {/each}
    </div>
  {/if}

  {#if footer}
    <div class="mb-4 border-t border-border pt-3">
      {@render footer()}
    </div>
  {:else if children}
    <div class="mb-4 border-t border-border pt-3">
      {@render children()}
    </div>
  {/if}

  <div class="flex items-center gap-1.5">
    {#if onLogs}<button
        type="button"
        class="rounded p-2 text-muted-foreground hover:bg-muted hover:text-foreground"
        title="Logs"
        aria-label="Logs"
        onclick={onLogs}><SquareTerminal class="h-4 w-4" /></button
      >{/if}
    {#if onEdit}<button
        type="button"
        class="rounded p-2 text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-40"
        title="Edit"
        aria-label="Edit"
        disabled={locked || frozen}
        onclick={onEdit}><Settings class="h-4 w-4" /></button
      >{/if}
    {#if !frozen && onFreeze}<button
        type="button"
        class="rounded p-2 text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-40"
        title="Freeze"
        aria-label="Freeze"
        disabled={locked}
        onclick={onFreeze}><Snowflake class="h-4 w-4" /></button
      >{/if}
    {#if onInfo}<button
        type="button"
        class="rounded p-2 text-muted-foreground hover:bg-muted hover:text-foreground"
        title="Info"
        aria-label="Info"
        onclick={onInfo}><Info class="h-4 w-4" /></button
      >{/if}
    {#if onOpen}<button
        type="button"
        class="btn btn-primary ml-auto gap-2 text-xs"
        disabled={locked}
        onclick={onOpen}>Open <ExternalLink class="h-3.5 w-3.5" /></button
      >{/if}
  </div>
</article>
