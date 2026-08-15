<script lang="ts">
  import {
    Box,
    ExternalLink,
    GripVertical,
    Lock,
    RotateCw,
    SquareTerminal,
  } from "@lucide/svelte";
  import type { HTMLAttributes } from "svelte/elements";
  import type {
    ServiceIcon,
    ServiceCardDisplay,
    ServiceMigration,
    ServicePlacement,
    ServiceStatusKind,
  } from "./service";

  interface Props extends HTMLAttributes<HTMLDivElement>, ServiceCardDisplay {
    name: string;
    meta?: string;
    icon?: ServiceIcon;
    placement: ServicePlacement;
    status?: ServiceStatusKind;
    statusLabel?: string;
    managementLabel?: string;
    migration?: ServiceMigration;
    showGrip?: boolean;
    onOpen?: () => void;
    onLogs?: () => void;
    onRestart?: () => void;
    class?: string;
  }

  let {
    name,
    meta,
    icon,
    placement,
    status = "running",
    statusLabel,
    managementLabel,
    migration,
    showGrip = true,
    onOpen,
    onLogs,
    onRestart,
    class: className = "",
    ...rest
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

<div
  role="article"
  class={`grid grid-cols-[18px_38px_1fr_auto] gap-x-2.5 gap-y-2 rounded-[10px] border border-border bg-card px-3 py-3 transition-all hover:-translate-y-0.5 hover:border-primary/50 hover:shadow-lg ${className}`}
  data-status={status}
  {...rest}
>
  {#if showGrip}
    <span
      class="row-span-2 grid place-items-center text-muted-foreground/60"
      aria-hidden="true"
      >{#if frozen}<Lock class="h-3.5 w-3.5" />{:else}<GripVertical
          class="h-4 w-4"
        />{/if}</span
    >
  {:else}<span class="row-span-2"></span>{/if}
  <span
    class="row-span-2 grid h-[38px] w-[38px] place-items-center self-center rounded-[10px] bg-primary/10 text-primary"
    aria-hidden="true"><GlyphIcon class="h-[18px] w-[18px]" /></span
  >
  <div class="min-w-0">
    <p class="truncate text-[13px] font-semibold text-foreground">{name}</p>
    {#if meta}<p
        class="mt-px truncate font-mono text-[10px] text-muted-foreground"
      >
        {meta}
      </p>{/if}
  </div>
  <span
    class={`self-start rounded-full border px-2 py-0.5 text-[10px] font-semibold ${badge.tone}`}
    >{status === "migrating" && migration
      ? `${Math.round(migration.progress)}%`
      : badge.label}</span
  >
  <div
    class="flex min-w-0 items-center gap-2 font-mono text-[9px] uppercase tracking-wider text-muted-foreground"
  >
    <span class="h-1.5 w-1.5 rounded-full bg-primary"
    ></span>{placementLabel}{#if managementLabel}<span class="truncate">{managementLabel}</span>{/if}{#if migration}<span class="text-info"
        >→ {migration.to}</span
      >{/if}
  </div>
  <div class="flex justify-end gap-0.5">
    {#if status === "error" && onRestart}<button
        type="button"
        class="rounded p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground"
        title="Restart"
        aria-label="Restart"
        onclick={onRestart}><RotateCw class="h-3.5 w-3.5" /></button
      >{/if}
    {#if onLogs}<button
        type="button"
        class="rounded p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground"
        title="Logs"
        aria-label="Logs"
        onclick={onLogs}><SquareTerminal class="h-3.5 w-3.5" /></button
      >{/if}
    {#if onOpen}<button
        type="button"
        class="rounded p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-40"
        title="Open"
        aria-label="Open"
        disabled={locked}
        onclick={onOpen}><ExternalLink class="h-3.5 w-3.5" /></button
      >{/if}
  </div>
</div>
