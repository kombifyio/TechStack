<script lang="ts">
  /**
   * QuickAction - Shortcut action cards for portal dashboard
   * Provides quick access to common operations across tools
   */
  import { scale } from "svelte/transition";
  import { backOut } from "svelte/easing";

  interface Props {
    title: string;
    description?: string;
    icon: string;
    toolName?: string;
    toolColor?: string;
    disabled?: boolean;
    onClick?: () => void;
    class?: string;
  }

  let {
    title,
    description,
    icon,
    toolName,
    toolColor = "var(--color-primary)",
    disabled = false,
    onClick,
    class: className = "",
  }: Props = $props();

  let isHovered = $state(false);
</script>

<button
  onclick={onClick}
  {disabled}
  class="quick-action group relative text-left w-full p-4 rounded-xl border border-border bg-card transition-all duration-200 {disabled
    ? 'opacity-50 cursor-not-allowed'
    : 'hover:shadow-md hover:border-primary/30'} {className}"
  onmouseenter={() => (isHovered = true)}
  onmouseleave={() => (isHovered = false)}
>
  <div class="flex items-start gap-3">
    <!-- Icon -->
    <div
      class="w-10 h-10 rounded-lg flex items-center justify-center text-xl transition-transform duration-200"
      class:scale-110={isHovered && !disabled}
      style="background: color-mix(in oklch, {toolColor} 15%, transparent);"
    >
      {icon}
    </div>

    <!-- Content -->
    <div class="flex-1 min-w-0">
      <h4
        class="font-medium text-foreground group-hover:text-primary transition-colors"
      >
        {title}
      </h4>
      {#if description}
        <p class="text-sm text-muted-foreground mt-0.5 line-clamp-2">
          {description}
        </p>
      {/if}
      {#if toolName}
        <div
          class="mt-2 inline-flex items-center gap-1.5 text-xs text-muted-foreground"
        >
          <span
            class="w-1.5 h-1.5 rounded-full"
            style="background: {toolColor};"
          ></span>
          {toolName}
        </div>
      {/if}
    </div>

    <!-- Arrow -->
    <svg
      class="w-4 h-4 text-muted-foreground opacity-0 group-hover:opacity-100 transition-all duration-200 transform translate-x-0 group-hover:translate-x-1"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="2"
    >
      <polyline points="9 18 15 12 9 6" />
    </svg>
  </div>
</button>
