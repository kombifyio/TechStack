<script lang="ts">
  let { mode }: { mode: "home" | "anywhere" } = $props();
</script>

<svg viewBox="0 0 320 150" fill="none" aria-hidden="true" focusable="false">
  <path class="guide" d="M52 76h216" />
  <g
    ><rect class="surface" x="22" y="49" width="74" height="52" rx="8" /><path
      class="line"
      d="M32 91h54M46 108h26M52 101l-6 7m20-7 6 7"
    /></g
  >
  <g
    ><circle class="halo" cx="160" cy="76" r="35" />
    {#if mode === "anywhere"}
      <path class="ink" d="M148 72v-7a12 12 0 0 1 24 0v7" /><rect
        class="surface"
        x="143"
        y="71"
        width="34"
        height="28"
        rx="8"
      /><circle class="status" cx="160" cy="84" r="3" />
    {:else}
      <path class="ink" d="m140 76 20-17 20 17v21h-40Z" /><path
        class="line"
        d="M154 97V84h12v13"
      />
    {/if}
  </g>
  <g
    ><rect class="surface" x="224" y="38" width="74" height="76" rx="12" /><rect
      class="tile"
      x="238"
      y="52"
      width="18"
      height="18"
      rx="5"
    /><rect class="tile" x="266" y="52" width="18" height="18" rx="5" /><rect
      class="tile"
      x="238"
      y="80"
      width="18"
      height="18"
      rx="5"
    /><rect class="tile" x="266" y="80" width="18" height="18" rx="5" /></g
  >
  <circle class="pulse one" cx="111" cy="76" r="3" /><circle
    class="pulse two"
    cx="209"
    cy="76"
    r="3"
  />
</svg>

<style>
  svg {
    width: 100%;
    height: auto;
    color: var(--primary);
  }
  .guide {
    stroke: currentColor;
    stroke-width: 1.5;
    stroke-dasharray: 4 6;
    opacity: 0.32;
  }
  .surface {
    fill: var(--card);
    stroke: color-mix(in oklch, var(--primary) 34%, var(--border));
    stroke-width: 1.4;
  }
  .line,
  .ink {
    stroke: currentColor;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .line {
    opacity: 0.28;
    stroke-width: 1.4;
  }
  .ink {
    stroke-width: 2;
  }
  .halo {
    fill: color-mix(in oklch, var(--primary) 7%, transparent);
    stroke: color-mix(in oklch, var(--primary) 18%, transparent);
  }
  .tile {
    fill: color-mix(in oklch, var(--primary) 11%, var(--muted));
    stroke: color-mix(in oklch, var(--primary) 24%, var(--border));
  }
  .status,
  .pulse {
    fill: currentColor;
  }
  .pulse {
    opacity: 0.55;
  }
  @media (prefers-reduced-motion: no-preference) {
    .pulse {
      animation: route-pulse 2.8s ease-in-out infinite;
    }
    .two {
      animation-delay: 1.1s;
    }
  }
  @keyframes route-pulse {
    50% {
      opacity: 1;
      transform: scale(1.35);
      transform-origin: center;
    }
  }
</style>
