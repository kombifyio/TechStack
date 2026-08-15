<!--
  TipBox Component
  
  Information box for tips and hints.
-->
<script lang="ts">
  interface Props {
    variant?: "tip" | "info" | "warning";
    children?: import("svelte").Snippet;
  }

  let { variant = "tip", children }: Props = $props();

  const styles = {
    tip: "bg-muted/50 border-border text-muted-foreground",
    info: "bg-info/10 border-info/30 text-foreground",
    warning: "bg-warning/10 border-warning/30 text-foreground",
  };

  const iconColors = {
    tip: "text-muted-foreground",
    info: "text-info",
    warning: "text-warning",
  };
</script>

<div class="p-4 rounded-lg border {styles[variant]}">
  <p class="text-sm flex items-start gap-2">
    <span class="flex-shrink-0 mt-0.5 {iconColors[variant]}">
      {#if variant === "tip"}
        <svg
          class="w-4 h-4"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0014 18.469V19a2 2 0 11-4 0v-.531c0-.895-.356-1.754-.988-2.386l-.548-.547z"
          />
        </svg>
      {:else if variant === "info"}
        <svg
          class="w-4 h-4"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
          />
        </svg>
      {:else}
        <svg
          class="w-4 h-4"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
          />
        </svg>
      {/if}
    </span>
    <span>
      {#if children}
        {@render children()}
      {/if}
    </span>
  </p>
</div>
