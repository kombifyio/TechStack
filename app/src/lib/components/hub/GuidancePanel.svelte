<script lang="ts">
  import ErrorCallout from "$lib/components/ui/ErrorCallout.svelte";
  import GuidedNextSteps from "$lib/components/hub/GuidedNextSteps.svelte";
  import {
    resolveGuidance,
    toAiHandoverContext,
    type ServerOutcome,
    type ServerOutcomeStatus,
    type GuidanceStep,
  } from "$lib/support/server-outcome";
  import { cn } from "$lib/utils";

  interface Props {
    outcome: ServerOutcome;
    surface: string;
    resourceId?: string;
    resourceName?: string;
    route?: string;
    onRetry?: () => void;
    retrying?: boolean;
    onStepAction?: (step: GuidanceStep) => void;
    compact?: boolean;
    class?: string;
  }

  let {
    outcome,
    surface,
    resourceId,
    resourceName,
    route,
    onRetry,
    retrying = false,
    onStepAction,
    compact = false,
    class: className = "",
  }: Props = $props();

  const toneByStatus: Record<
    ServerOutcomeStatus,
    "error" | "warning" | "info" | "success"
  > = {
    available: "success",
    disabled: "warning",
    blocked: "warning",
    degraded: "warning",
    pending: "info",
    failed: "error",
  };

  const tone = $derived(toneByStatus[outcome.status] ?? "error");
  const guidance = $derived(resolveGuidance(outcome));
  const aiHandoverContext = $derived(
    toAiHandoverContext(outcome, { surface, resourceId, resourceName, route }),
  );
  // Never offer retry for a non-retryable outcome (FEATURE-ENTITLEMENT-UX §3).
  const steps = $derived(
    outcome.retryable
      ? guidance.nextSteps
      : guidance.nextSteps.filter((step) => step.kind !== "retry"),
  );
  const metaBits = $derived(
    [
      outcome.reasonCode ? `reason: ${outcome.reasonCode}` : "",
      outcome.requestId ? `request: ${outcome.requestId}` : "",
    ].filter(Boolean),
  );

  function handleStepAction(step: GuidanceStep) {
    if (step.kind === "retry") {
      if (!retrying) onRetry?.();
      return;
    }
    onStepAction?.(step);
  }
</script>

<ErrorCallout
  {tone}
  {aiHandoverContext}
  class={cn(compact ? "text-sm" : "", className)}
>
  <div
    class="space-y-1"
    data-testid="guidance-panel"
    data-status={outcome.status}
  >
    {#if guidance.title}
      <p class="font-semibold text-foreground">{guidance.title}</p>
    {/if}
    {#if guidance.body}
      <p class="text-sm text-foreground/90">{guidance.body}</p>
    {/if}
    {#if steps.length > 0}
      <GuidedNextSteps {steps} onAction={handleStepAction} />
    {/if}
    {#if metaBits.length > 0}
      <p class="mt-2 font-mono text-xs text-muted-foreground">
        {metaBits.join("  ·  ")}
      </p>
    {/if}
    {#if retrying}
      <p class="text-xs text-muted-foreground">Wird erneut geladen…</p>
    {/if}
  </div>
</ErrorCallout>
