<script lang="ts">
  import { cn } from "#lib/utils.js";
  import type { Snippet } from "svelte";
  import ErrorAiHandoverButton from "#lib/components/support/ErrorAiHandoverButton.svelte";
  import type { ErrorAiHandoverContext } from "#lib/support/error-handover.js";

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
    error: "border-destructive/30 bg-destructive/5 text-destructive",
    warning: "border-warning/30 bg-warning/5 text-warning",
    info: "border-info/30 bg-info/5 text-info",
    success: "border-success/30 bg-success/5 text-success",
  };
</script>

{#if message || children}
  <div
    role="alert"
    class={cn(
      "relative flex w-full items-start gap-3 rounded-xl border p-4",
      alertClasses[tone],
      className,
    )}
  >
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
