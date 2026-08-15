<script lang="ts">
  import { Sparkles } from "@lucide/svelte";
  import { onDestroy, onMount } from "svelte";
  import { cn } from "$lib/utils";
  import {
    AI_HANDOVER_SCHEMA,
    SUPPORT_AGENT_ID,
    buildErrorAiHandoverPrompt,
    normalizeErrorAiHandoverContext,
    type ErrorAiHandoverContext,
  } from "$lib/support/error-handover";
  import {
    aiErrorHandoverAvailable,
    cancelAiErrorHandoverUpdates,
    requestAiErrorHandover,
    type AiErrorHandoverRequest,
    type AiErrorHandoverUpdate,
  } from "$lib/stores/postMessageBridge";

  interface Props {
    context?: ErrorAiHandoverContext | null;
    class?: string;
  }

  let { context = null, class: className = "" }: Props = $props();
  let handoverState = $state<
    "idle" | "connecting" | "accepted" | "ready" | "failed" | "standalone"
  >("idle");
  let handoverFailure = $state("");
  let handoverAttempt = 0;
  let cancelActiveHandover: () => void = () => undefined;
  let previousContext = $state<ErrorAiHandoverContext | null>(null);

  $effect(() => {
    if (context === previousContext) return;
    cancelActiveHandover();
    cancelActiveHandover = () => undefined;
    previousContext = context;
    handoverAttempt += 1;
    handoverState = "idle";
    handoverFailure = "";
  });

  function askKombifyAI() {
    if (!context) return;

    const attempt = ++handoverAttempt;
    handoverFailure = "";
    const errorContext = normalizeErrorAiHandoverContext(context);
    const request: AiErrorHandoverRequest = {
      schema: AI_HANDOVER_SCHEMA,
      targetPanelMode: "support",
      agentId: SUPPORT_AGENT_ID,
      prompt: buildErrorAiHandoverPrompt(errorContext),
      errorContext: errorContext as unknown as Record<string, unknown>,
    };
    const handleUpdate = (update: AiErrorHandoverUpdate) => {
      if (attempt !== handoverAttempt) return;
      if (update.status === "accepted") {
        handoverState = "accepted";
      } else if (update.status === "session-ready") {
        handoverState = "ready";
      } else {
        handoverFailure = update.error.message;
        handoverState = "failed";
      }
    };
    cancelActiveHandover();
    const sentToParent = requestAiErrorHandover(request, handleUpdate);
    cancelActiveHandover = sentToParent
      ? () => cancelAiErrorHandoverUpdates(request, handleUpdate)
      : () => undefined;

    handoverState = sentToParent ? "connecting" : "standalone";
  }

  // The action is only offered where it can be delivered: inside the kombify
  // Cloud shell. Standalone it used to render a button whose only effect was a
  // hint that it does not work here.
  let handoverReachable = $state(false);
  onMount(() => {
    handoverReachable = aiErrorHandoverAvailable();
  });

  onDestroy(() => cancelActiveHandover());
</script>

{#if context && handoverReachable}
  <div class={cn("mt-3 flex flex-wrap items-center gap-2", className)}>
    <button
      type="button"
      onclick={askKombifyAI}
      disabled={handoverState === "connecting" ||
        handoverState === "accepted" ||
        handoverState === "ready"}
      aria-busy={handoverState === "connecting" || handoverState === "accepted"}
      class="inline-flex min-h-9 items-center gap-2 rounded-md border border-red-400/45 bg-red-500/10 px-3 py-1.5 text-sm font-medium text-red-50 transition-colors hover:bg-red-500/20 focus:outline-none focus:ring-2 focus:ring-red-300/60 disabled:cursor-not-allowed disabled:opacity-60"
      data-testid="ask-kombify-ai-error"
    >
      <Sparkles class="h-4 w-4 shrink-0" aria-hidden="true" />
      <span>Ask Kombify AI about this</span>
    </button>
    {#if handoverState === "connecting"}
      <span class="text-xs text-red-200/80" role="status" aria-live="polite">
        Connecting to Kombify AI...
      </span>
    {:else if handoverState === "accepted"}
      <span class="text-xs text-red-200/80" role="status" aria-live="polite">
        Preparing support session...
      </span>
    {:else if handoverState === "ready"}
      <span class="text-xs text-red-200/80" role="status" aria-live="polite">
        Support panel opened.
      </span>
    {:else if handoverState === "failed"}
      <span class="text-xs text-red-200/80" role="alert">
        Kombify AI could not open: {handoverFailure}
      </span>
    {:else if handoverState === "standalone"}
      <span class="text-xs text-red-200/80">
        AI handover is available in kombify Cloud.
      </span>
    {/if}
  </div>
{/if}
