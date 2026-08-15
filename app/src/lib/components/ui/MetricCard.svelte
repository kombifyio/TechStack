<script lang="ts">
  /**
   * MetricCard - Display key metrics with trend indicators
   * Used in monitoring dashboards for quick stats overview
   */
  import { fly, fade } from "svelte/transition";

  type Trend = "up" | "down" | "stable";

  interface Props {
    title: string;
    value: string | number;
    unit?: string;
    trend?: Trend;
    trendValue?: string;
    icon?: string;
    description?: string;
    loading?: boolean;
    testId?: string;
    class?: string;
  }

  let {
    title,
    value,
    unit = "",
    trend,
    trendValue,
    icon,
    description,
    loading = false,
    testId,
    class: className = "",
  }: Props = $props();

  const trendConfig = {
    up: { icon: "↑", color: "text-green-500", bg: "bg-green-500/10" },
    down: { icon: "↓", color: "text-red-500", bg: "bg-red-500/10" },
    stable: { icon: "→", color: "text-muted-foreground", bg: "bg-muted" },
  };
</script>

<div
  data-testid={testId}
  class="metric-card relative overflow-hidden rounded-xl border border-border bg-card p-4 transition-all hover:shadow-md hover:border-primary/30 {className}"
>
  <!-- Loading skeleton -->
  {#if loading}
    <div class="space-y-3 animate-pulse">
      <div class="h-4 w-20 bg-muted rounded"></div>
      <div class="h-8 w-16 bg-muted rounded"></div>
      <div class="h-3 w-24 bg-muted rounded"></div>
    </div>
  {:else}
    <!-- Header -->
    <div class="flex items-start justify-between mb-2">
      <span
        class="text-sm text-muted-foreground"
        data-testid="metric-card-title">{title}</span
      >
      {#if icon}
        <span class="text-lg">{icon}</span>
      {/if}
    </div>

    <!-- Value -->
    <div
      class="flex items-baseline gap-1 mb-1"
      in:fly={{ y: 10, duration: 200 }}
    >
      <span
        class="text-2xl font-bold text-foreground"
        data-testid="metric-card-value">{value}</span
      >
      {#if unit}
        <span class="text-sm text-muted-foreground">{unit}</span>
      {/if}
    </div>

    <!-- Trend & Description -->
    <div class="flex items-center gap-2">
      {#if trend && trendValue}
        {@const config = trendConfig[trend]}
        <span
          class="inline-flex items-center gap-0.5 text-xs font-medium px-1.5 py-0.5 rounded {config.bg} {config.color}"
          in:fade={{ duration: 150 }}
        >
          <span>{config.icon}</span>
          <span>{trendValue}</span>
        </span>
      {/if}
      {#if description}
        <span class="text-xs text-muted-foreground">{description}</span>
      {/if}
    </div>
  {/if}

  <!-- Decorative gradient -->
  <div
    class="absolute inset-x-0 bottom-0 h-0.5 bg-gradient-to-r from-transparent via-primary/20 to-transparent"
  ></div>
</div>
