<script lang="ts">
  let { kind }: { kind: string } = $props();
</script>

<svg viewBox="0 0 220 150" fill="none" aria-hidden="true" focusable="false">
  <ellipse class="orbit" cx="111" cy="80" rx="93" ry="58" />
  <path class="guide" d="M8 118 208 35" />
  {#if kind === "install-command"}
    <g transform="rotate(-5 110 75)">
      <rect class="surface" x="35" y="31" width="153" height="100" rx="12" />
      <path class="edge" d="M35 51h153" />
      <circle class="dot" cx="48" cy="41" r="2" /><circle
        class="dot"
        cx="56"
        cy="41"
        r="2"
      /><circle class="dot" cx="64" cy="41" r="2" />
      <path class="ink" d="m57 73 12 10-12 10m23 1h18" />
      <path class="muted-line" d="M58 111h92" />
      <path class="ink" d="m148 83 7 7 14-17" />
    </g>
  {:else if kind === "connect-remote"}
    <rect class="surface" x="16" y="64" width="81" height="53" rx="7" />
    <path class="edge" d="M9 123h96M38 123l3-6h31l3 6" />
    <path class="ink" d="m39 83 8 7-8 7m15 0h12" />
    <path class="connection" d="M92 75h29c16 0 15-37 31-37" />
    <rect class="surface" x="149" y="22" width="47" height="91" rx="10" />
    <path class="edge" d="M157 43h30m-30 19h30m-30 19h30" />
    <circle class="dot" cx="160" cy="34" r="2" /><circle
      class="dot"
      cx="160"
      cy="54"
      r="2"
    />
    <circle class="status" cx="172" cy="99" r="4" />
  {:else if kind === "kombify-cloud"}
    <path
      class="cloud"
      d="M55 57c-16 0-19-26-3-31 1-24 36-30 47-9 22-9 38 23 21 34"
    />
    <g transform="rotate(5 125 88)">
      <rect class="surface" x="65" y="48" width="115" height="83" rx="12" />
      <path class="edge" d="M65 76h115M65 104h115" />
      <path class="ink" d="M96 62h61M96 90h61M96 117h42" />
      <circle class="dot" cx="81" cy="62" r="3" /><circle
        class="dot"
        cx="81"
        cy="90"
        r="3"
      /><circle class="status" cx="81" cy="117" r="3" />
    </g>
  {:else}
    <circle class="surface" cx="102" cy="79" r="49" />
    <ellipse class="edge" cx="102" cy="79" rx="21" ry="49" />
    <path class="edge" d="M53 79h98M61 54h82M61 103h82" />
    <path class="ink" d="M143 34h36v36m-36 0 36-36" />
    <circle class="status" cx="54" cy="112" r="6" />
  {/if}
</svg>

<style>
  svg {
    width: 100%;
    height: auto;
    overflow: visible;
    color: var(--primary);
  }
  .orbit {
    stroke: currentColor;
    opacity: 0.09;
  }
  .guide {
    stroke: currentColor;
    stroke-dasharray: 3 5;
    opacity: 0.18;
  }
  .surface {
    fill: var(--card);
    stroke: color-mix(in oklch, var(--primary) 32%, var(--border));
    stroke-width: 1.2;
  }
  .edge {
    stroke: currentColor;
    opacity: 0.22;
    stroke-width: 1.3;
  }
  .ink,
  .connection,
  .cloud {
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .connection {
    stroke-dasharray: 4 4;
  }
  .muted-line {
    stroke: currentColor;
    opacity: 0.12;
    stroke-width: 4;
    stroke-linecap: round;
  }
  .dot {
    fill: currentColor;
    opacity: 0.65;
  }
  .status {
    fill: var(--primary);
  }
</style>
