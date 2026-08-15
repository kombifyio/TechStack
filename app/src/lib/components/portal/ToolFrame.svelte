<script lang="ts">
  /**
   * ToolFrame - Embeddable frame wrapper for tool instances
   * Provides consistent navigation and context for embedded tools
   */
  import { fly, fade } from "svelte/transition";

  interface Props {
    toolName: string;
    toolIcon: string;
    brandColor?: string;
    isEmbedded?: boolean;
    onNavigateToFull?: () => void;
    onClose?: () => void;
    class?: string;
    children?: import("svelte").Snippet;
  }

  let {
    toolName,
    toolIcon,
    brandColor = "var(--color-primary)",
    isEmbedded = true,
    onNavigateToFull,
    onClose,
    class: className = "",
    children,
  }: Props = $props();
</script>

<div
  class="tool-frame flex flex-col h-full border border-border rounded-xl overflow-hidden bg-background {className}"
  in:fly={{ y: 20, duration: 300 }}
>
  <!-- Tool Header -->
  <header
    class="flex items-center justify-between px-4 py-3 border-b border-border"
    style="background: color-mix(in oklch, {brandColor} 5%, var(--color-card));"
  >
    <div class="flex items-center gap-3">
      <div
        class="w-8 h-8 rounded-lg flex items-center justify-center text-lg"
        style="background: color-mix(in oklch, {brandColor} 15%, transparent);"
      >
        {toolIcon}
      </div>
      <div>
        <h3 class="font-semibold text-foreground text-sm">{toolName}</h3>
        {#if isEmbedded}
          <span class="text-xs text-muted-foreground">Embedded view</span>
        {/if}
      </div>
    </div>

    <div class="flex items-center gap-2">
      {#if onNavigateToFull}
        <button
          onclick={onNavigateToFull}
          class="inline-flex items-center gap-1.5 px-2.5 py-1 text-xs font-medium rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
          title="Open in full view"
        >
          <svg
            class="w-3.5 h-3.5"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
          >
            <path d="M18 13v6a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2h6" />
            <polyline points="15 3 21 3 21 9" />
            <line x1="10" y1="14" x2="21" y2="3" />
          </svg>
          Full View
        </button>
      {/if}
      {#if onClose}
        <button
          onclick={onClose}
          class="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
          title="Close"
        >
          <svg
            class="w-4 h-4"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
          >
            <line x1="18" y1="6" x2="6" y2="18" />
            <line x1="6" y1="6" x2="18" y2="18" />
          </svg>
        </button>
      {/if}
    </div>
  </header>

  <!-- Tool Content -->
  <div class="flex-1 overflow-auto">
    {#if children}
      {@render children()}
    {:else}
      <div
        class="flex items-center justify-center h-full text-muted-foreground"
      >
        <p>Tool content goes here</p>
      </div>
    {/if}
  </div>
</div>
