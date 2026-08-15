<script lang="ts">
  import type { DriftStatus } from "$lib/api/drift";
  import { getDriftStatusLabel, getDriftStatusColor } from "$lib/api/drift";

  interface Props {
    status: DriftStatus;
    size?: "sm" | "md" | "lg";
    showLabel?: boolean;
    animated?: boolean;
  }

  let {
    status,
    size = "md",
    showLabel = true,
    animated = true,
  }: Props = $props();

  const sizeClasses = {
    sm: "h-2 w-2",
    md: "h-3 w-3",
    lg: "h-4 w-4",
  };

  const textSizeClasses = {
    sm: "text-xs",
    md: "text-sm",
    lg: "text-base",
  };

  const colorClasses = {
    green: "bg-green-500",
    yellow: "bg-yellow-500",
    red: "bg-red-500",
    gray: "bg-gray-500",
    blue: "bg-blue-500",
  };

  const textColorClasses = {
    green: "text-green-400",
    yellow: "text-yellow-400",
    red: "text-red-400",
    gray: "text-gray-400",
    blue: "text-blue-400",
  };

  const color = $derived(getDriftStatusColor(status));
  const label = $derived(getDriftStatusLabel(status));
  const isChecking = $derived(status === "checking");
</script>

<div class="inline-flex items-center gap-2">
  <span class="relative flex {sizeClasses[size]}">
    <!-- Pulse animation for checking state -->
    {#if animated && isChecking}
      <span
        class="animate-ping absolute inline-flex h-full w-full rounded-full {colorClasses[
          color
        ]} opacity-75"
      ></span>
    {/if}
    <span
      class="relative inline-flex rounded-full {sizeClasses[
        size
      ]} {colorClasses[color]}"
    ></span>
  </span>
  {#if showLabel}
    <span class="{textSizeClasses[size]} {textColorClasses[color]} font-medium">
      {label}
    </span>
  {/if}
</div>
