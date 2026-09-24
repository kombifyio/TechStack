<script lang="ts">
  import type { AvailabilityDay } from "#lib/api/monitoring.js";

  interface Props {
    days: AvailabilityDay[];
    /** Accessible summary; the ribbon is a graphic, not a table. */
    label: string;
    height?: number;
    class?: string;
  }

  let { days, label, height = 26, class: className = "" }: Props = $props();

  /**
   * A day with no observation stays visibly empty. Painting it healthy would
   * be the one lie this whole projection exists to avoid.
   */
  const FILL: Record<string, string> = {
    connected: "oklch(0.74 0.15 155)",
    degraded: "oklch(0.79 0.14 80)",
    stale: "oklch(0.79 0.14 80)",
    offline: "oklch(0.67 0.2 25)",
    revoked: "oklch(0.67 0.2 25)",
  };
  const NO_DATA = "#262c31";

  const GAP = 3;
  const cells = $derived(
    days.map((day, index) => ({
      key: day.day,
      x: index * (10 + GAP),
      fill: FILL[day.state] ?? NO_DATA,
      title: `${day.day} — ${day.state || "no data"}`,
    })),
  );
  const width = $derived(Math.max(1, days.length * (10 + GAP) - GAP));
</script>

<svg
  width="100%"
  {height}
  viewBox="0 0 {width} {height}"
  preserveAspectRatio="none"
  role="img"
  aria-label={label}
  class={className}
>
  {#each cells as cell (cell.key)}
    <rect x={cell.x} y="0" width="10" {height} rx="3" fill={cell.fill}>
      <title>{cell.title}</title>
    </rect>
  {/each}
</svg>
