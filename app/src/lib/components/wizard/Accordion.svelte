<!--
  Accordion Component
  
  Collapsible section for advanced settings.
-->
<script lang="ts">
  import { slide } from "svelte/transition";

  interface Props {
    title?: string;
    open?: boolean;
    children?: import("svelte").Snippet;
  }

  let {
    title = "Advanced Settings",
    open = $bindable(false),
    children,
  }: Props = $props();

  function toggle() {
    open = !open;
  }
</script>

<div class="border border-border rounded-lg overflow-hidden">
  <button
    type="button"
    onclick={toggle}
    class="w-full flex items-center justify-between px-4 py-3 bg-muted/50 hover:bg-muted transition-colors text-left"
  >
    <span class="text-sm font-medium text-foreground">{title}</span>
    <svg
      class="w-5 h-5 text-muted-foreground transition-transform {open
        ? 'rotate-180'
        : ''}"
      fill="none"
      stroke="currentColor"
      viewBox="0 0 24 24"
    >
      <path
        stroke-linecap="round"
        stroke-linejoin="round"
        stroke-width="2"
        d="M19 9l-7 7-7-7"
      />
    </svg>
  </button>
  {#if open}
    <div transition:slide={{ duration: 200 }} class="p-4 bg-card">
      {#if children}
        {@render children()}
      {/if}
    </div>
  {/if}
</div>
