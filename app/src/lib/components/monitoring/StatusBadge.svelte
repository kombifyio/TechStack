<script lang="ts">
  /**
   * StatusBadge - Real-time status indicator for services/instances
   * Shows operational status with pulse animation for active states
   */

  type Status =
    | "operational"
    | "degraded"
    | "partial"
    | "down"
    | "maintenance"
    | "unknown";

  interface Props {
    status: Status;
    label?: string;
    showPulse?: boolean;
    size?: "sm" | "md" | "lg";
    class?: string;
  }

  let {
    status = "unknown",
    label,
    showPulse = true,
    size = "md",
    class: className = "",
  }: Props = $props();

  const statusConfig: Record<
    Status,
    { color: string; bg: string; text: string }
  > = {
    operational: {
      color: "bg-green-500",
      bg: "bg-green-500/10",
      text: "Operational",
    },
    degraded: {
      color: "bg-yellow-500",
      bg: "bg-yellow-500/10",
      text: "Degraded",
    },
    partial: {
      color: "bg-orange-500",
      bg: "bg-orange-500/10",
      text: "Partial Outage",
    },
    down: { color: "bg-red-500", bg: "bg-red-500/10", text: "Down" },
    maintenance: {
      color: "bg-blue-500",
      bg: "bg-blue-500/10",
      text: "Maintenance",
    },
    unknown: { color: "bg-muted-foreground", bg: "bg-muted", text: "Unknown" },
  };

  const sizeClasses = {
    sm: "text-xs px-2 py-0.5",
    md: "text-sm px-2.5 py-1",
    lg: "text-base px-3 py-1.5",
  };

  const dotSizes = {
    sm: "w-1.5 h-1.5",
    md: "w-2 h-2",
    lg: "w-2.5 h-2.5",
  };

  let config = $derived(statusConfig[status] || statusConfig.unknown);
</script>

<span
  class="inline-flex items-center gap-1.5 rounded-full font-medium {config.bg} {sizeClasses[
    size
  ]} {className}"
>
  <span class="relative flex {dotSizes[size]}">
    <span
      class="absolute inline-flex h-full w-full rounded-full {config.color} opacity-75"
      class:animate-ping={showPulse && status === "operational"}
    ></span>
    <span class="relative inline-flex rounded-full h-full w-full {config.color}"
    ></span>
  </span>
  <span class="text-foreground">{label || config.text}</span>
</span>
