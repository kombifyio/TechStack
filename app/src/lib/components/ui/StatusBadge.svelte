<script lang="ts">
  import { cn } from "$lib/utils";

  interface Props {
    status:
      | "healthy"
      | "success"
      | "warning"
      | "error"
      | "unknown"
      | "pending"
      | "info";
    label?: string;
    pulse?: boolean;
    size?: "sm" | "md" | "lg";
    variant?: "dot" | "badge";
    class?: string;
  }

  let {
    status,
    label,
    pulse = false,
    size = "md",
    variant = "dot",
    class: className = "",
  }: Props = $props();

  const statusColors = {
    healthy: "bg-success",
    success: "bg-success",
    warning: "bg-warning",
    error: "bg-destructive",
    unknown: "bg-muted-foreground",
    pending: "bg-primary",
    info: "bg-info",
  };

  const badgeClasses = {
    healthy: "badge-success",
    success: "badge-success",
    warning: "badge-warning",
    error: "badge-destructive",
    unknown: "badge-secondary",
    pending: "badge-primary",
    info: "badge-info",
  };

  const statusLabels = {
    healthy: "Healthy",
    success: "Success",
    warning: "Warning",
    error: "Error",
    unknown: "Unknown",
    pending: "Pending",
    info: "Info",
  };

  const dotSizes = {
    sm: "w-2 h-2",
    md: "w-3 h-3",
    lg: "w-4 h-4",
  };
</script>

{#if variant === "badge"}
  <span class={cn("badge", badgeClasses[status], className)}>
    {label || statusLabels[status]}
  </span>
{:else}
  <div class={cn("inline-flex items-center gap-2", className)}>
    <span class="relative flex">
      <span class={cn(dotSizes[size], "rounded-full", statusColors[status])}
      ></span>
      {#if pulse}
        <span
          class={cn(
            "absolute inset-0 rounded-full animate-ping opacity-75",
            dotSizes[size],
            statusColors[status],
          )}
        ></span>
      {/if}
    </span>
    {#if label !== undefined}
      <span class="text-sm text-muted-foreground"
        >{label || statusLabels[status]}</span
      >
    {/if}
  </div>
{/if}
