<script lang="ts">
  import type { PromQLPoint } from "#lib/api/monitoring.js";

  interface Props {
    points: PromQLPoint[];
    /** Accessible summary of the shape; a chart is not a table. */
    label: string;
    /** Dashed reference line in data units, e.g. an alert threshold. */
    threshold?: number;
    /** Fixed axis top. Percentages pass 100 so panels stay comparable. */
    max?: number;
    height?: number;
  }

  let {
    points,
    label,
    threshold,
    max,
    height = 104,
  }: Props = $props();

  const WIDTH = 400;
  const SERIES = "#4a90d9";

  // A single series needs no categorical palette and no legend: the panel
  // title names it. The dashed reference is a rule, not a second series.
  const scale = $derived.by(() => {
    if (points.length === 0) return null;
    const values = points.map((point) => point.value);
    const observedTop = Math.max(...values, threshold ?? 0) * 1.08;
    const top = max ?? (observedTop > 0 ? observedTop : 1);
    const first = points[0].at;
    const span = points[points.length - 1].at - first || 1;
    return {
      top,
      x: (at: number) => ((at - first) / span) * WIDTH,
      y: (value: number) => height - Math.min(value / top, 1) * height,
    };
  });

  const line = $derived.by(() => {
    if (!scale) return "";
    return points
      .map((point, index) => `${index === 0 ? "M" : "L"}${scale.x(point.at).toFixed(1)},${scale.y(point.value).toFixed(1)}`)
      .join(" ");
  });
  const area = $derived(line ? `${line} L${WIDTH},${height} L0,${height} Z` : "");
  const thresholdY = $derived(
    scale && threshold !== undefined ? scale.y(threshold) : null,
  );
</script>

{#if points.length === 0}
  <div
    class="flex items-center justify-center rounded-md border border-dashed border-border text-xs text-muted-foreground"
    style="height: {height}px"
  >
    No samples in this window
  </div>
{:else}
  <svg
    width="100%"
    {height}
    viewBox="0 0 {WIDTH} {height}"
    preserveAspectRatio="none"
    role="img"
    aria-label={label}
  >
    {#each [0.25, 0.5, 0.75] as fraction (fraction)}
      <line
        x1="0"
        y1={height * fraction}
        x2={WIDTH}
        y2={height * fraction}
        stroke="rgba(244,244,245,0.07)"
        stroke-width="1"
      />
    {/each}
    {#if thresholdY !== null}
      <line
        x1="0"
        y1={thresholdY}
        x2={WIDTH}
        y2={thresholdY}
        stroke="rgba(244,244,245,0.28)"
        stroke-width="1"
        stroke-dasharray="3 3"
      />
    {/if}
    <path d={area} fill={SERIES} fill-opacity="0.12" />
    <path
      d={line}
      fill="none"
      stroke={SERIES}
      stroke-width="2"
      stroke-linejoin="round"
      stroke-linecap="round"
      vector-effect="non-scaling-stroke"
    />
  </svg>
{/if}
