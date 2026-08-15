<script lang="ts">
  /**
   * UptimeBar - 90-day uptime visualization
   * Shows daily uptime status as a bar chart with tooltips
   * Migrated from design-showcase for kombify-TechStack
   */

  type DayStatus =
    | "operational"
    | "degraded"
    | "partial"
    | "down"
    | "maintenance"
    | "no-data";

  interface DayData {
    date: Date | string;
    status: DayStatus;
    uptime?: number; // 0-100 percentage
  }

  interface Props {
    days: DayData[];
    label?: string;
    showPercentage?: boolean;
    class?: string;
  }

  let {
    days = [],
    label = "Uptime",
    showPercentage = true,
    class: className = "",
  }: Props = $props();

  const statusColors: Record<DayStatus, string> = {
    operational: "bg-success",
    degraded: "bg-warning",
    partial: "bg-warning",
    down: "bg-destructive",
    maintenance: "bg-info",
    "no-data": "bg-muted",
  };

  let averageUptime = $derived(() => {
    const validDays = days.filter((d) => d.uptime !== undefined);
    if (validDays.length === 0) return null;
    return (
      validDays.reduce((sum, d) => sum + (d.uptime || 0), 0) / validDays.length
    ).toFixed(2);
  });

  let hoveredDay = $state<DayData | null>(null);
  let tooltipPosition = $state({ x: 0, y: 0 });

  function handleMouseEnter(event: MouseEvent, day: DayData) {
    hoveredDay = day;
    const rect = (event.target as HTMLElement).getBoundingClientRect();
    tooltipPosition = { x: rect.left + rect.width / 2, y: rect.top };
  }

  function handleMouseLeave() {
    hoveredDay = null;
  }

  function formatDate(date: Date | string): string {
    const d = date instanceof Date ? date : new Date(date);
    return d.toLocaleDateString("en-US", { month: "short", day: "numeric" });
  }
</script>

<div class="uptime-bar {className}">
  <!-- Header -->
  <div class="flex items-center justify-between mb-2">
    <span class="text-sm font-medium text-foreground">{label}</span>
    {#if showPercentage && averageUptime()}
      <span class="text-sm text-muted-foreground">
        <span class="text-success font-medium">{averageUptime()}%</span> uptime
      </span>
    {/if}
  </div>

  <!-- Bar visualization -->
  <div class="flex gap-0.5 h-8 rounded overflow-hidden">
    {#each days as day}
      <button
        class="flex-1 min-w-1 transition-all hover:scale-y-110 {statusColors[
          day.status
        ]}"
        onmouseenter={(e) => handleMouseEnter(e, day)}
        onmouseleave={handleMouseLeave}
        title="{formatDate(day.date)}: {day.status}"
      ></button>
    {/each}
  </div>

  <!-- Date labels -->
  <div class="flex justify-between mt-1">
    <span class="text-xs text-muted-foreground">
      {days.length > 0 ? formatDate(days[0].date) : ""}
    </span>
    <span class="text-xs text-muted-foreground">
      {days.length > 0 ? formatDate(days[days.length - 1].date) : "Today"}
    </span>
  </div>

  <!-- Legend -->
  <div class="flex items-center gap-4 mt-3 text-xs text-muted-foreground">
    <div class="flex items-center gap-1">
      <span class="w-2.5 h-2.5 rounded-sm bg-success"></span>
      <span>Operational</span>
    </div>
    <div class="flex items-center gap-1">
      <span class="w-2.5 h-2.5 rounded-sm bg-warning"></span>
      <span>Degraded</span>
    </div>
    <div class="flex items-center gap-1">
      <span class="w-2.5 h-2.5 rounded-sm bg-destructive"></span>
      <span>Down</span>
    </div>
  </div>
</div>

<!-- Tooltip -->
{#if hoveredDay}
  <div
    class="fixed z-50 px-2 py-1 text-xs bg-popover border border-border rounded shadow-lg pointer-events-none transform -translate-x-1/2 -translate-y-full -mt-2"
    style="left: {tooltipPosition.x}px; top: {tooltipPosition.y}px;"
  >
    <div class="font-medium">{formatDate(hoveredDay.date)}</div>
    <div class="text-muted-foreground capitalize">
      {hoveredDay.status.replace("-", " ")}
    </div>
    {#if hoveredDay.uptime !== undefined}
      <div class="text-success">{hoveredDay.uptime}% uptime</div>
    {/if}
  </div>
{/if}
