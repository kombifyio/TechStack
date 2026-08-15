<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  interface Props {
    variant?: "default" | "glass" | "hover";
    padding?: "none" | "sm" | "md" | "lg";
    class?: string;
    children: Snippet;
    header?: Snippet;
    footer?: Snippet;
  }

  let {
    variant = "default",
    padding = "md",
    class: className = "",
    children,
    header,
    footer,
  }: Props = $props();

  const paddings = {
    none: "",
    sm: "p-4",
    md: "p-6",
    lg: "p-8",
  };
</script>

<div
  class={cn(
    "card",
    variant === "hover" && "card-hover",
    variant === "glass" && "glass",
    !padding || padding === "none" ? "p-0" : "",
    className,
  )}
>
  {#if header}
    <div class="card-header">
      {@render header()}
    </div>
  {/if}

  <div class={cn(paddings[padding] || "")}>
    {@render children()}
  </div>

  {#if footer}
    <div class="card-footer">
      {@render footer()}
    </div>
  {/if}
</div>
