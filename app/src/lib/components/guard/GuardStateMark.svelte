<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { CircleCheck, CircleDashed, TriangleAlert } from "@lucide/svelte";
  import type { GuardFactState } from "#lib/guard/facts.js";

  interface Props {
    state: GuardFactState;
    /** Legacy credential, journal or layout: reduced protection. */
    reduced?: boolean;
  }

  let { state, reduced = false }: Props = $props();

  // State is always a glyph shape plus a word; colour only reinforces it.
  const tone: Record<GuardFactState, string> = {
    ok: "text-success",
    attention: "text-warning",
    unknown: "text-muted-foreground",
  };
</script>

<span
  class="inline-flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs font-semibold {tone[state]}"
  data-testid="guard-state"
  data-state={state}
>
  {#if state === "ok"}
    <CircleCheck class="h-4 w-4 shrink-0" aria-hidden="true" />
  {:else if state === "attention"}
    <TriangleAlert class="h-4 w-4 shrink-0" aria-hidden="true" />
  {:else}
    <CircleDashed class="h-4 w-4 shrink-0" aria-hidden="true" />
  {/if}
  <span>{tr(`guard.state.${state}`)}</span>
  {#if reduced}
    <span
      class="rounded border border-warning/50 px-1.5 py-px text-[10.5px] font-semibold uppercase tracking-wide text-warning"
      >{tr("guard.reduced")}</span
    >
  {/if}
</span>
