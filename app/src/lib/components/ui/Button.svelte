<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  interface Props {
    variant?:
      | "primary"
      | "secondary"
      | "outline"
      | "ghost"
      | "destructive"
      | "success";
    size?: "sm" | "md" | "lg" | "icon";
    disabled?: boolean;
    loading?: boolean;
    class?: string;
    type?: "button" | "submit" | "reset";
    onclick?: (e: MouseEvent) => void;
    children: Snippet;
  }

  let {
    variant = "primary",
    size = "md",
    disabled = false,
    loading = false,
    class: className = "",
    type = "button",
    onclick,
    children,
  }: Props = $props();

  const variantClasses = {
    primary: "btn-primary",
    secondary: "btn-secondary",
    outline: "btn-outline",
    ghost: "btn-ghost",
    destructive: "btn-destructive",
    success: "btn-success",
    // Legacy alias
    danger: "btn-destructive",
  };

  const sizeClasses = {
    sm: "btn-sm",
    md: "",
    lg: "btn-lg",
    icon: "btn-icon",
  };
</script>

<button
  {type}
  {disabled}
  {onclick}
  class={cn("btn", variantClasses[variant], sizeClasses[size], className)}
>
  {#if loading}
    <svg class="animate-spin h-4 w-4" viewBox="0 0 24 24">
      <circle
        class="opacity-25"
        cx="12"
        cy="12"
        r="10"
        stroke="currentColor"
        stroke-width="4"
        fill="none"
      />
      <path
        class="opacity-75"
        fill="currentColor"
        d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
      />
    </svg>
  {/if}
  {@render children()}
</button>
