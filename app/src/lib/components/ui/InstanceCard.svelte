<script lang="ts">
  /**
   * InstanceCard - Card for displaying a tool instance in the portal
   * Shows status, metrics, and quick actions for each kombify tool
   * Migrated from design-showcase for kombify-TechStack
   */
  import StatusBadge from "./StatusBadge.svelte";
  import { fly } from "svelte/transition";
  import { backOut } from "svelte/easing";

  type Status =
    | "operational"
    | "degraded"
    | "partial"
    | "down"
    | "maintenance"
    | "unknown";

  interface Props {
    name: string;
    description?: string;
    status: Status;
    url?: string;
    icon?: string;
    brandColor?: string;
    metrics?: Array<{ label: string; value: string }>;
    lastSync?: string;
    onOpen?: () => void;
    onSettings?: () => void;
    class?: string;
  }

  let {
    name,
    description,
    status = "unknown",
    url,
    icon = "",
    brandColor = "var(--primary)",
    metrics = [],
    lastSync,
    onOpen,
    onSettings,
    class: className = "",
  }: Props = $props();

  let isHovered = $state(false);

  // Map operational status to StatusBadge status
  function mapStatus(
    s: Status,
  ): "healthy" | "warning" | "error" | "unknown" | "pending" | "info" {
    switch (s) {
      case "operational":
        return "healthy";
      case "degraded":
        return "warning";
      case "partial":
        return "warning";
      case "down":
        return "error";
      case "maintenance":
        return "info";
      default:
        return "unknown";
    }
  }

  function getStatusLabel(s: Status): string {
    switch (s) {
      case "operational":
        return "Operational";
      case "degraded":
        return "Degraded";
      case "partial":
        return "Partial Outage";
      case "down":
        return "Down";
      case "maintenance":
        return "Maintenance";
      default:
        return "Unknown";
    }
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="instance-card group relative overflow-hidden rounded-xl border bg-card transition-all duration-300 {className} {isHovered
    ? 'shadow-lg border-primary/50'
    : 'border-border'}"
  role="article"
  onmouseenter={() => (isHovered = true)}
  onmouseleave={() => (isHovered = false)}
  onfocusin={() => (isHovered = true)}
  onfocusout={() => (isHovered = false)}
  in:fly={{ y: 20, duration: 300, easing: backOut }}
>
  <!-- Brand color accent -->
  <div
    class="absolute inset-x-0 top-0 h-1 transition-all duration-300"
    class:h-1.5={isHovered}
    style="background: {brandColor};"
  ></div>

  <div class="p-5">
    <!-- Header -->
    <div class="flex items-start justify-between mb-4">
      <div class="flex items-center gap-3">
        <div
          class="w-12 h-12 rounded-lg flex items-center justify-center text-2xl transition-transform duration-300"
          class:scale-110={isHovered}
          style="background: color-mix(in oklch, {brandColor} 15%, transparent);"
        >
          {icon}
        </div>
        <div>
          <h3 class="font-semibold text-foreground">{name}</h3>
          {#if description}
            <p class="text-sm text-muted-foreground">{description}</p>
          {/if}
        </div>
      </div>
      <StatusBadge
        status={mapStatus(status)}
        label={getStatusLabel(status)}
        size="sm"
        variant="badge"
      />
    </div>

    <!-- Metrics Row -->
    {#if metrics.length > 0}
      <div class="grid grid-cols-3 gap-3 mb-4 p-3 rounded-lg bg-muted/50">
        {#each metrics as metric}
          <div class="text-center">
            <div class="text-lg font-semibold text-foreground">
              {metric.value}
            </div>
            <div class="text-xs text-muted-foreground">{metric.label}</div>
          </div>
        {/each}
      </div>
    {/if}

    <!-- Footer -->
    <div class="flex items-center justify-between pt-3 border-t border-border">
      <div class="text-xs text-muted-foreground">
        {#if lastSync}
          Last sync: {lastSync}
        {:else}
          Ready
        {/if}
      </div>
      <div class="flex items-center gap-2">
        {#if onSettings}
          <button
            onclick={onSettings}
            class="p-2 rounded-lg text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
            title="Settings"
          >
            <svg
              class="w-4 h-4"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
            >
              <path d="M12 15a3 3 0 100-6 3 3 0 000 6z" />
              <path
                d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06a1.65 1.65 0 00.33-1.82 1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06a1.65 1.65 0 001.82.33H9a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06a1.65 1.65 0 00-.33 1.82V9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"
              />
            </svg>
          </button>
        {/if}
        {#if onOpen || url}
          <button
            onclick={() =>
              onOpen ? onOpen() : url && window.open(url, "_blank")}
            class="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm font-medium rounded-lg bg-primary text-primary-foreground hover:bg-primary/90 transition-colors"
          >
            Open
            <svg
              class="w-3.5 h-3.5"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
            >
              <path d="M18 13v6a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2h6" />
              <polyline points="15 3 21 3 21 9" />
              <line x1="10" y1="14" x2="21" y2="3" />
            </svg>
          </button>
        {/if}
      </div>
    </div>
  </div>
</div>
