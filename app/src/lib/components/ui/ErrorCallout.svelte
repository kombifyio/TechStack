<script lang="ts">
  import { cn } from "$lib/utils";
  import type { Snippet } from "svelte";
  import ErrorAiHandoverButton from "$lib/components/support/ErrorAiHandoverButton.svelte";
  import type { ErrorAiHandoverContext } from "$lib/support/error-handover";

  interface Props {
    message?: string | null;
    tone?: "error" | "warning" | "info" | "success";
    aiHandoverContext?: ErrorAiHandoverContext | null;
    class?: string;
    children?: Snippet;
  }

  let {
    message = null,
    tone = "error",
    aiHandoverContext = null,
    class: className = "",
    children,
  }: Props = $props();

  const alertClasses = {
    error: "alert-destructive",
    warning: "alert-warning",
    info: "alert-info",
    success: "alert-success",
  };
</script>

{#if message || children}
  <div role="alert" class={cn("alert", alertClasses[tone], className)}>
    {#if children}
      {@render children()}
    {:else}
      {message}
    {/if}
    {#if tone === "error" && aiHandoverContext}
      <ErrorAiHandoverButton context={aiHandoverContext} />
    {/if}
  </div>
{/if}
