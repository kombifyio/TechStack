<script lang="ts">
  import { cn } from "#lib/utils.js";
  import type { Snippet } from "svelte";
  import {
    AlertTriangle,
    CircleAlert,
    Info,
    CircleCheck,
    RefreshCw,
  } from "@lucide/svelte";
  import ErrorAiHandoverButton from "#lib/components/support/ErrorAiHandoverButton.svelte";
  import type { ErrorAiHandoverContext } from "#lib/support/error-handover.js";
  import { tr } from "#lib/i18n.svelte.js";

  /**
   * The one way a failed or degraded state is shown.
   *
   * - `message` (optionally with a `title`) is the plain-language sentence the
   *   person reads; `details` is the technical text, collapsed by default.
   * - `onRetry` adds the retry action; `retryInSeconds` states the automatic
   *   next attempt ("Next attempt in 5 s") so a self-retrying page is never
   *   silently busy.
   * - `children` keeps the original contract: the caller lays out the body.
   *
   * Errors and warnings are `role="alert"`; info and success are `status`.
   */
  interface Props {
    message?: string | null;
    title?: string | null;
    details?: string | null;
    tone?: "error" | "warning" | "info" | "success";
    aiHandoverContext?: ErrorAiHandoverContext | null;
    /** Shows a retry button when given. */
    onRetry?: (() => void) | null;
    retryLabel?: string;
    retrying?: boolean;
    /** Seconds until the page tries again on its own; null hides the line. */
    retryInSeconds?: number | null;
    testId?: string;
    class?: string;
    children?: Snippet;
  }

  let {
    message = null,
    title = null,
    details = null,
    tone = "error",
    aiHandoverContext = null,
    onRetry = null,
    retryLabel,
    retrying = false,
    retryInSeconds = null,
    testId,
    class: className = "",
    children,
  }: Props = $props();

  const alertClasses = {
    error: "border-destructive/30 bg-destructive/5 text-destructive",
    warning: "border-warning/30 bg-warning/5 text-warning",
    info: "border-info/30 bg-info/5 text-info",
    success: "border-success/30 bg-success/5 text-success",
  };
  const icons = {
    error: CircleAlert,
    warning: AlertTriangle,
    info: Info,
    success: CircleCheck,
  };
  const Icon = $derived(icons[tone]);
  const liveRole = $derived(
    tone === "error" || tone === "warning" ? "alert" : "status",
  );
</script>

{#if message || title || children}
  <div
    role={liveRole}
    data-testid={testId}
    class={cn(
      "relative flex w-full items-start gap-3 rounded-xl border p-4",
      alertClasses[tone],
      className,
    )}
  >
    {#if children}
      {@render children()}
    {:else}
      <Icon class="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <div class="min-w-0 flex-1 space-y-1">
        {#if title}
          <p class="font-semibold text-foreground">{title}</p>
        {/if}
        {#if message}
          <p class={title ? "text-sm text-foreground/90" : "text-foreground"}>
            {message}
          </p>
        {/if}
        {#if details}
          <details class="text-xs text-muted-foreground">
            <summary class="cursor-pointer select-none">
              {tr("errorCallout.details")}
            </summary>
            <p class="mt-1 font-mono break-words">{details}</p>
          </details>
        {/if}
        {#if retryInSeconds !== null && retryInSeconds > 0 && !retrying}
          <p
            class="text-xs text-muted-foreground"
            data-testid="retry-countdown"
          >
            {tr("errorCallout.nextAttempt", { seconds: retryInSeconds })}
          </p>
        {/if}
      </div>
    {/if}
    {#if onRetry}
      <button
        type="button"
        data-kx="control"
        class="inline-flex shrink-0 items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm text-foreground disabled:opacity-60"
        disabled={retrying}
        onclick={() => onRetry?.()}
      >
        <RefreshCw
          class="size-3.5 {retrying ? 'animate-spin' : ''}"
          aria-hidden="true"
        />
        {retrying
          ? tr("errorCallout.retrying")
          : (retryLabel ?? tr("errorCallout.retry"))}
      </button>
    {/if}
    {#if tone === "error" && aiHandoverContext}
      <ErrorAiHandoverButton context={aiHandoverContext} />
    {/if}
  </div>
{/if}
