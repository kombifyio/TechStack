<script lang="ts">
  import type {
    WizardRecommendation,
    WizardRecommendationResult,
  } from "#lib/api/unifier.js";
  import type { WizardPreviewState } from "#lib/wizard/WizardPreviewController.js";
  import { tr } from "#lib/i18n.svelte.js";

  interface Props {
    state: WizardPreviewState;
    onopen?: (recommendation: WizardRecommendation) => void;
  }

  let { state, onopen }: Props = $props();

  const result = $derived.by<WizardRecommendationResult | undefined>(() => {
    if (state.phase === "resolved") return state.result;
    if (state.phase === "loading" || state.phase === "degraded") {
      return state.previous;
    }
    return undefined;
  });
  const primary = $derived(
    result?.recommendations.find((item) => item.recommended) ??
      result?.recommendations[0],
  );

  function stackKitLabel(value: string): string {
    return value
      .split("-")
      .filter(Boolean)
      .map((part) => part[0]?.toUpperCase() + part.slice(1))
      .join(" ");
  }
</script>

{#if state.phase !== "idle"}
  <section
    data-kx="plate"
    data-testid="wizard-hint-zone"
    data-recommendation-status={result?.status ?? state.phase}
    class="rounded-xl border border-primary/25 bg-primary/5 p-4"
    aria-live="polite"
  >
    <div class="flex min-w-0 items-start gap-3">
      <div
        class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/15 text-primary"
        aria-hidden="true"
      >
        ✦
      </div>
      <div class="min-w-0 flex-1">
        <div class="flex flex-wrap items-center gap-2">
          <p class="font-semibold text-foreground">
            {tr("wizard.hints.title")}
          </p>
          {#if state.phase === "loading"}
            <span class="text-xs text-muted-foreground">
              {tr("wizard.hints.updating")}
            </span>
          {/if}
          {#if result?.status === "stale"}
            <span
              class="rounded-full border border-warning/30 bg-warning/10 px-2 py-0.5 text-xs text-warning"
            >
              {tr("wizard.hints.stale")}
            </span>
          {:else if result?.status === "degraded" || state.phase === "degraded"}
            <span
              class="rounded-full border border-warning/30 bg-warning/10 px-2 py-0.5 text-xs text-warning"
            >
              {tr("wizard.hints.degraded")}
            </span>
          {/if}
        </div>

        {#if primary}
          <p class="mt-1 text-sm text-foreground">
            <strong>{stackKitLabel(primary.stackkit)}</strong>
            {tr("wizard.hints.bestFit")}
          </p>
          <div class="mt-3 flex flex-wrap items-center gap-2">
            <button
              type="button"
              class="rounded-full border border-primary/30 bg-primary/10 px-3 py-1.5 text-xs font-medium text-primary transition hover:bg-primary/15"
              onclick={() => onopen?.(primary)}
            >
              {tr("wizard.hints.review")}
            </button>
            {#each primary.alternatives as alternative (alternative)}
              <span
                class="rounded-full border border-border bg-muted/30 px-3 py-1.5 text-xs text-muted-foreground"
              >
                {stackKitLabel(alternative)}
              </span>
            {/each}
          </div>
        {:else if result?.status === "incomplete"}
          <p class="mt-1 text-sm text-muted-foreground">
            {tr("wizard.hints.incomplete")}
          </p>
        {:else if state.phase === "degraded"}
          <p class="mt-1 text-sm text-muted-foreground">
            {tr("wizard.hints.unavailable")}
          </p>
        {:else}
          <p class="mt-1 text-sm text-muted-foreground">
            {tr("wizard.hints.evaluating")}
          </p>
        {/if}
      </div>
    </div>
  </section>
{/if}
