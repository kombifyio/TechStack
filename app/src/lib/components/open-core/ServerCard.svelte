<script lang="ts">
  import { CircleAlert, HardDrive, Info, TriangleAlert } from "@lucide/svelte";
  import type { Snippet } from "svelte";
  import type { HTMLAttributes } from "svelte/elements";
  import type {
    ServerIcon,
    ServerFact,
    ServerKitInfo,
    ServerMetric,
    ServerNoteTone,
    ServerStatusKind,
  } from "./server";

  interface Props extends HTMLAttributes<HTMLElement> {
    hostname: string;
    meta?: string;
    icon?: ServerIcon;
    status?: ServerStatusKind;
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
    actions?: Snippet;
    class?: string;
  }

  let {
    hostname,
    meta,
    icon,
    status = "unknown",
    statusLabel,
    metrics = [],
    facts = [],
    address,
    domain,
    domainExtraCount,
    kit,
    note,
    noteTone = "warning",
    detailsHref,
    actions,
    class: className = "",
    ...rest
  }: Props = $props();

  const GlyphIcon = $derived(icon ?? HardDrive);
  const badge = $derived.by(() => {
    const labels: Record<ServerStatusKind, string> = {
      healthy: "Healthy",
      degraded: "Degraded",
      offline: "Offline",
      pending: "Pending",
      unknown: "Unknown",
    };
    const tones: Record<ServerStatusKind, string> = {
      healthy: "text-success bg-success/10 border-success/30",
      degraded: "text-destructive bg-destructive/10 border-destructive/30",
      offline: "text-muted-foreground bg-muted border-border",
      pending: "text-info bg-info/10 border-info/30",
      unknown: "text-muted-foreground bg-muted border-border",
    };
    return { label: statusLabel ?? labels[status], tone: tones[status] };
  });
  const NoteIcon = $derived(
    noteTone === "error"
      ? CircleAlert
      : noteTone === "warning"
        ? TriangleAlert
        : Info,
  );
</script>

<article
  class={`relative overflow-hidden rounded-[10px] border border-border bg-card p-4 transition-all hover:-translate-y-0.5 hover:border-primary/50 hover:shadow-lg ${className}`}
  data-status={status}
  {...rest}
>
  <div class="flex items-center gap-3">
    <span
      class="grid h-10 w-10 shrink-0 place-items-center rounded-[10px] bg-primary/10 text-primary"
      aria-hidden="true"
    >
      <GlyphIcon class="h-5 w-5" />
    </span>
    {#if detailsHref}
      <a href={detailsHref} class="min-w-0 flex-1">
        <h3
          class="truncate text-[13px] font-semibold text-foreground hover:underline"
          title={hostname}
        >
          {hostname}
        </h3>
        {#if meta}<p
            class="mt-px truncate font-mono text-[10px] text-muted-foreground"
            title={meta}
          >
            {meta}
          </p>{/if}
      </a>
    {:else}
      <div class="min-w-0 flex-1">
        <h3
          class="truncate text-[13px] font-semibold text-foreground"
          title={hostname}
        >
          {hostname}
        </h3>
        {#if meta}<p
            class="mt-px truncate font-mono text-[10px] text-muted-foreground"
            title={meta}
          >
            {meta}
          </p>{/if}
      </div>
    {/if}
    <span
      class={`shrink-0 rounded-full border px-2 py-0.5 text-[10px] font-semibold ${badge.tone}`}
      >{badge.label}</span
    >
  </div>

  {#if address || domain || kit}
    <div
      class="mt-3 flex min-w-0 items-center gap-1.5 font-mono text-[11px] text-muted-foreground"
    >
      {#if address}<span class="truncate" title={address}>{address}</span>{/if}
      {#if domain}
        {#if address}<span aria-hidden="true">·</span>{/if}
        <span class="truncate" title={domain}
          >{domain}{#if domainExtraCount && domainExtraCount > 0}<span
              class="opacity-70">{` +${domainExtraCount}`}</span
            >{/if}</span
        >
      {/if}
      {#if kit}
        {#if address || domain}<span aria-hidden="true">·</span>{/if}
        <span
          class="truncate"
          title={`${kit.name}${kit.detail ? ` ${kit.detail}` : ""}`}
          >{kit.name}{#if kit.detail}<span class="opacity-70"
              >{` ${kit.detail}`}</span
            >{/if}</span
        >
      {/if}
    </div>
  {/if}

  {#if metrics.length > 0}
    <div class="mt-3 grid grid-cols-3 gap-2">
      {#each metrics.slice(0, 3) as metric (metric.label)}
        <div class="rounded-lg bg-muted/40 px-2 py-1.5 text-center">
          <div class="font-mono text-[13px] font-semibold text-foreground">
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

  {#if facts.length > 0}
    <div class="mt-3 grid grid-cols-2 gap-2">
      {#each facts.slice(0, 4) as fact (fact.label)}
        <div
          class="rounded-lg bg-muted/40 px-2 py-1.5"
          data-testid={fact.testId}
        >
          <div class="text-[10px] uppercase tracking-wider text-muted-foreground">
            {fact.label}
          </div>
          <div class="mt-px truncate text-xs font-medium text-foreground" title={fact.value}>
            {fact.value}
          </div>
        </div>
      {/each}
    </div>
  {/if}

  {#if note}
    <div
      class={`mt-3 flex items-center gap-2 rounded-lg px-3 py-2 text-xs ${noteTone === "error" ? "bg-destructive/10 text-destructive" : noteTone === "warning" ? "bg-warning/10 text-warning" : "bg-info/10 text-info"}`}
    >
      <NoteIcon class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span class="truncate" title={note}>{note}</span>
    </div>
  {/if}

  {#if actions}
    <div class="mt-3 flex items-center gap-1.5 border-t border-border pt-3">
      {@render actions()}
    </div>
  {/if}
</article>
