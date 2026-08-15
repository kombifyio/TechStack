<!--
  Collapsible

  Generic collapsed-by-default disclosure panel with a tone-colored header,
  a truncated one-line summary, an optional badge, and a rotating chevron.
  Used to keep secondary detail (e.g. the latest rollout failure) below the
  primary content without burying it entirely.
-->
<script lang="ts">
  import { slide } from "svelte/transition";
  import { AlertTriangle, ChevronDown } from "@lucide/svelte";
  import type { Snippet } from "svelte";

  interface Props {
    summary: string;
    tone?: "error" | "warning" | "info";
    badge?: string;
    open?: boolean;
    testId?: string;
    children: Snippet;
  }

  let {
    summary,
    tone = "info",
    badge,
    open = $bindable(false),
    testId,
    children,
  }: Props = $props();

  const toneClasses: Record<NonNullable<Props["tone"]>, string> = {
    error: "border-destructive/30 bg-destructive/5",
    warning: "border-warning/30 bg-warning/5",
    info: "border-border bg-card",
  };
  const iconClasses: Record<NonNullable<Props["tone"]>, string> = {
    error: "text-destructive",
    warning: "text-warning",
    info: "text-muted-foreground",
  };
  const badgeClasses: Record<NonNullable<Props["tone"]>, string> = {
    error: "badge badge-destructive",
    warning: "badge badge-warning",
    info: "badge badge-secondary",
  };
</script>

<div
  class="overflow-hidden rounded-lg border {toneClasses[tone]}"
  data-testid={testId}
>
  <button
    type="button"
    class="flex w-full items-center gap-2 px-4 py-3 text-left"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <AlertTriangle class="h-4 w-4 shrink-0 {iconClasses[tone]}" />
    <span
      class="min-w-0 flex-1 truncate text-sm font-medium text-foreground"
      title={summary}
    >
      {summary}
    </span>
    {#if badge}
      <span class="shrink-0 {badgeClasses[tone]}">{badge}</span>
    {/if}
    <ChevronDown
      class="h-4 w-4 shrink-0 text-muted-foreground transition-transform {open
        ? 'rotate-180'
        : ''}"
    />
  </button>
  {#if open}
    <div transition:slide={{ duration: 200 }} class="border-t border-border/60 p-4">
      {@render children()}
    </div>
  {/if}
</div>
