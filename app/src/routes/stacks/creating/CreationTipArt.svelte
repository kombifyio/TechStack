<script lang="ts" module>
  export type TipArtKind = "next" | "reach" | "restore" | "code" | "grow" | "own";
  let instances = 0;
</script>

<script lang="ts">
  /**
   * Line illustrations for the Good-to-know tips. Strokes draw in once when
   * a tip appears (`draw`), small parts settle in after them (`pop`); `--i`
   * staggers them. Reduced motion shows the finished drawing.
   */
  interface Props {
    kind: TipArtKind;
  }

  let { kind }: Props = $props();
  const gridId = `tip-art-grid-${++instances}`;
</script>

<svg
  class="tip-art h-full w-full"
  viewBox="0 0 320 180"
  fill="none"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
>
  <defs>
    <pattern id={gridId} width="16" height="16" patternUnits="userSpaceOnUse">
      <circle cx="1" cy="1" r="1" class="fill-muted-foreground/15" />
    </pattern>
  </defs>
  <rect width="320" height="180" fill="url(#{gridId})" />

  {#if kind === "next"}
    <path
      class="draw stroke-primary"
      style="--i: 0"
      pathLength="1"
      stroke-width="2.5"
      d="M40 142 C 104 142, 112 82, 168 86 S 244 44, 278 44"
    />
    <circle class="pop fill-primary" style="--i: 0" cx="40" cy="142" r="6" />
    <circle
      class="pop fill-card stroke-primary"
      style="--i: 2"
      stroke-width="2.5"
      cx="168"
      cy="86"
      r="7"
    />
    <rect
      class="pop fill-card stroke-border"
      style="--i: 3"
      x="52"
      y="96"
      width="72"
      height="24"
      rx="6"
    />
    <path class="pop stroke-muted-foreground/40" style="--i: 3" stroke-width="3" d="M62 108 h40" />
    <rect
      class="pop fill-card stroke-border"
      style="--i: 4"
      x="160"
      y="110"
      width="84"
      height="24"
      rx="6"
    />
    <path class="pop stroke-muted-foreground/40" style="--i: 4" stroke-width="3" d="M170 122 h52" />
    <circle
      class="pop fill-success/15 stroke-success"
      style="--i: 5"
      stroke-width="2.5"
      cx="278"
      cy="44"
      r="16"
    />
    <path
      class="draw stroke-success"
      style="--i: 6"
      pathLength="1"
      stroke-width="3"
      d="M271 44 l5 5 l10 -11"
    />
  {:else if kind === "reach"}
    <rect
      class="pop fill-primary/10 stroke-primary/40"
      style="--i: 0"
      x="100"
      y="12"
      width="120"
      height="24"
      rx="12"
    />
    <text
      class="pop fill-primary font-mono"
      style="--i: 0"
      x="160"
      y="28"
      font-size="11"
      text-anchor="middle"
      direction="ltr">you.kombify.me</text
    >
    <circle class="draw stroke-primary" style="--i: 1" pathLength="1" stroke-width="2" cx="160" cy="104" r="44" />
    <ellipse class="draw stroke-primary/50" style="--i: 2" pathLength="1" stroke-width="1.5" cx="160" cy="104" rx="18" ry="44" />
    <ellipse class="draw stroke-primary/50" style="--i: 2" pathLength="1" stroke-width="1.5" cx="160" cy="104" rx="44" ry="15" />
    <path
      class="pop fill-card stroke-primary"
      style="--i: 3"
      stroke-width="2"
      d="M34 150 v-24 l20 -16 l20 16 v24 z"
    />
    <path class="pop stroke-primary" style="--i: 3" stroke-width="2" d="M48 150 v-12 h12 v12" />
    <rect
      class="pop fill-card stroke-primary"
      style="--i: 4"
      stroke-width="2"
      x="258"
      y="104"
      width="32"
      height="50"
      rx="7"
    />
    <path class="pop stroke-primary/50" style="--i: 4" stroke-width="2" d="M268 146 h12" />
    <path
      class="draw stroke-success"
      style="--i: 5"
      pathLength="1"
      stroke-width="2.5"
      d="M74 122 Q 96 70 122 80"
    />
    <path
      class="draw stroke-success"
      style="--i: 6"
      pathLength="1"
      stroke-width="2.5"
      d="M198 80 Q 236 70 258 112"
    />
  {:else if kind === "restore"}
    {#each [52, 82, 112] as y, row (y)}
      <rect
        class="pop fill-card stroke-primary"
        style="--i: {row}"
        stroke-width="2"
        x="118"
        y={y}
        width="84"
        height="24"
        rx="6"
      />
      <circle class="pop fill-success" style="--i: {row}" cx="132" cy={y + 12} r="3" />
      <path class="pop stroke-muted-foreground/40" style="--i: {row}" stroke-width="3" d="M156 {y + 12} h32" />
    {/each}
    <path
      class="draw stroke-primary"
      style="--i: 3"
      pathLength="1"
      stroke-width="2.5"
      d="M232 98 A 72 72 0 1 1 204 40"
    />
    <path class="draw stroke-primary" style="--i: 5" pathLength="1" stroke-width="2.5" d="M190 34 l14 6 l-6 14" />
    <circle
      class="pop fill-success/15 stroke-success"
      style="--i: 5"
      stroke-width="2.5"
      cx="236"
      cy="140"
      r="16"
    />
    <path
      class="draw stroke-success"
      style="--i: 6"
      pathLength="1"
      stroke-width="3"
      d="M229 140 l5 5 l10 -11"
    />
  {:else if kind === "code"}
    <rect
      class="draw fill-card stroke-primary"
      style="--i: 0"
      pathLength="1"
      stroke-width="2"
      x="56"
      y="22"
      width="208"
      height="136"
      rx="12"
    />
    <path class="draw stroke-border" style="--i: 1" pathLength="1" stroke-width="2" d="M56 46 h208" />
    {#each [72, 84, 96] as x (x)}
      <circle class="pop fill-muted-foreground/40" style="--i: 1" cx={x} cy="34" r="3.5" />
    {/each}
    {#each [
      { x: 76, y: 68, w: 54, tone: "stroke-primary" },
      { x: 92, y: 84, w: 92, tone: "stroke-muted-foreground/40" },
      { x: 92, y: 100, w: 70, tone: "stroke-success" },
      { x: 108, y: 116, w: 84, tone: "stroke-muted-foreground/40" },
      { x: 76, y: 132, w: 36, tone: "stroke-primary" },
    ] as line, n (line.y)}
      <path
        class="draw {line.tone}"
        style="--i: {Math.min(n + 2, 6)}"
        pathLength="1"
        stroke-width="4"
        d="M{line.x} {line.y} h{line.w}"
      />
    {/each}
    <circle
      class="pop fill-success/15 stroke-success"
      style="--i: 5"
      stroke-width="2.5"
      cx="236"
      cy="132"
      r="15"
    />
    <path
      class="draw stroke-success"
      style="--i: 6"
      pathLength="1"
      stroke-width="3"
      d="M229 132 l5 5 l10 -11"
    />
  {:else if kind === "grow"}
    <circle
      class="pop fill-primary/10 stroke-primary"
      style="--i: 0"
      stroke-width="2"
      cx="160"
      cy="32"
      r="12"
    />
    {#each [72, 160] as x, n (x)}
      <path
        class="draw stroke-primary/60"
        style="--i: {n + 1}"
        pathLength="1"
        stroke-width="2"
        d="M160 44 L {x} 84"
      />
      <rect
        class="pop fill-card stroke-primary"
        style="--i: {n + 2}"
        stroke-width="2"
        x={x - 34}
        y="84"
        width="68"
        height="48"
        rx="9"
      />
      <circle class="pop fill-success" style="--i: {n + 2}" cx={x - 20} cy="108" r="3" />
      <path class="pop stroke-muted-foreground/40" style="--i: {n + 2}" stroke-width="3" d="M{x - 8} 108 h28" />
    {/each}
    <path class="draw stroke-primary/40" style="--i: 3" pathLength="1" stroke-width="2" d="M160 44 L 248 84" />
    <rect
      class="pop stroke-primary/50"
      style="--i: 4"
      stroke-width="2"
      stroke-dasharray="6 6"
      x="214"
      y="84"
      width="68"
      height="48"
      rx="9"
    />
    <path class="pop stroke-primary" style="--i: 5" stroke-width="2.5" d="M248 98 v20 M238 108 h20" />
  {:else}
    <rect
      class="draw fill-card stroke-primary"
      style="--i: 0"
      pathLength="1"
      stroke-width="2"
      x="116"
      y="34"
      width="104"
      height="112"
      rx="14"
    />
    {#each [60, 86] as y, n (y)}
      <path class="pop stroke-muted-foreground/40" style="--i: {n + 1}" stroke-width="4" d="M136 {y} h64" />
      <circle class="pop fill-success" style="--i: {n + 1}" cx="204" cy={y} r="3" />
    {/each}
    <ellipse class="pop fill-primary/10 stroke-primary" style="--i: 2" stroke-width="2" cx="62" cy="96" rx="22" ry="7" />
    <path class="pop fill-primary/10 stroke-primary" style="--i: 2" stroke-width="2" d="M40 96 v30 a22 7 0 0 0 44 0 v-30" />
    <path
      class="draw stroke-primary/60"
      style="--i: 3"
      pathLength="1"
      stroke-width="2"
      d="M86 110 Q 100 110 114 104"
    />
    <path
      class="pop fill-card stroke-success"
      style="--i: 4"
      stroke-width="2.5"
      d="M220 96 l26 9 v17 c0 17 -12 27 -26 33 c-14 -6 -26 -16 -26 -33 v-17 z"
    />
    <path
      class="draw stroke-success"
      style="--i: 6"
      pathLength="1"
      stroke-width="3"
      d="M211 125 l6 6 l12 -13"
    />
  {/if}
</svg>

<style>
  @media (prefers-reduced-motion: no-preference) {
    .tip-art :global(.draw) {
      stroke-dasharray: 1;
      stroke-dashoffset: 1;
      animation: tip-draw var(--kx-dur-slow) var(--kx-ease-out) forwards;
      animation-delay: calc(var(--i, 0) * 70ms);
    }

    .tip-art :global(.pop) {
      opacity: 0;
      transform: scale(0.95);
      transform-box: fill-box;
      transform-origin: center;
      animation: tip-pop var(--kx-dur-morph) var(--kx-ease-out) forwards;
      animation-delay: calc(var(--i, 0) * 70ms);
    }
  }

  @keyframes tip-draw {
    to {
      stroke-dashoffset: 0;
    }
  }

  @keyframes tip-pop {
    to {
      opacity: 1;
      transform: none;
    }
  }
</style>
