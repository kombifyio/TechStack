<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import type { Snippet } from "svelte";
  import OverlayPanel from "#lib/components/ui/OverlayPanel.svelte";
  import { ChevronDown, CircleAlert, X } from "@lucide/svelte";

  /**
   * A failure as one line: what failed, a Details toggle for the guidance
   * and a close button. Closing hides this failure until another one
   * replaces it; recovery stays on the Node's own actions.
   */
  interface Props {
    summary: string;
    /** Identifies the failure; a new failure shows again after a close. */
    failureId: string;
    testId?: string;
    details: Snippet;
  }

  let { summary, failureId, testId, details }: Props = $props();

  const STORAGE_KEY = "techstack.dashboard.dismissedFailures";
  const KEEP = 20;

  function readDismissed(): string[] {
    if (typeof localStorage === "undefined") return [];
    try {
      const value = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "[]");
      return Array.isArray(value) ? value.filter((id) => typeof id === "string") : [];
    } catch {
      return [];
    }
  }

  let dismissed = $state(readDismissed());
  let open = $state(false);
  let anchor = $state<HTMLButtonElement | null>(null);

  function dismiss() {
    const next = [failureId, ...dismissed.filter((id) => id !== failureId)].slice(0, KEEP);
    dismissed = next;
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
    } catch {
      // Storage can be unavailable; the close then lasts for this page view.
    }
  }
</script>

{#if !dismissed.includes(failureId)}
  <div
    class="failure-notice rounded-lg border border-destructive/30 bg-destructive/5"
    data-testid={testId}
    role="status"
  >
    <div class="flex min-w-0 items-center gap-2 py-1.5 pr-1.5 pl-3">
      <CircleAlert class="h-4 w-4 shrink-0 text-destructive" aria-hidden="true" />
      <p class="min-w-0 flex-1 truncate text-sm font-medium text-foreground" title={summary}>
        {summary}
      </p>
      <button
        type="button"
        class="failure-notice-action inline-flex shrink-0 items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground"
        bind:this={anchor}
        aria-haspopup="true"
        aria-expanded={open}
        data-testid="failure-notice-details"
        onclick={() => (open = !open)}
      >
        {open ? tr("ui.dashboard.hideFailureDetails") : tr("ui.dashboard.failureDetails")}
        <ChevronDown
          class="failure-notice-chevron h-3.5 w-3.5 {open ? 'rotate-180' : ''}"
          aria-hidden="true"
        />
      </button>
      <button
        type="button"
        class="failure-notice-action inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground"
        aria-label={tr("ui.common.close")}
        title={tr("ui.common.close")}
        data-testid="failure-notice-dismiss"
        onclick={dismiss}
      >
        <X class="h-4 w-4" aria-hidden="true" />
      </button>
    </div>
  </div>
  {#if open}
    <OverlayPanel
      {anchor}
      label={summary}
      testId={testId ? `${testId}-details` : undefined}
      onClose={() => (open = false)}
    >
      <div class="p-2">
        {@render details()}
      </div>
    </OverlayPanel>
  {/if}
{/if}

<style>
  .failure-notice-action {
    transition:
      background-color var(--kx-dur-fast) var(--kx-ease-out),
      color var(--kx-dur-fast) var(--kx-ease-out);
  }

  .failure-notice-action:hover,
  .failure-notice-action:focus-visible {
    background: color-mix(in oklab, var(--destructive) 10%, transparent);
    color: var(--foreground);
  }

  @media (prefers-reduced-motion: no-preference) {
    .failure-notice :global(.failure-notice-chevron) {
      transition: transform var(--kx-dur-fast) var(--kx-ease-out);
    }
  }
</style>
