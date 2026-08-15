<script lang="ts">
  import type { GuidanceStep } from "$lib/support/server-outcome";
  import { cn } from "$lib/utils";

  interface Props {
    steps: GuidanceStep[];
    onAction?: (step: GuidanceStep) => void;
    layout?: "row" | "stack";
    class?: string;
  }

  let {
    steps,
    onAction,
    layout = "stack",
    class: className = "",
  }: Props = $props();

  const notes = $derived(steps.filter((step) => step.kind === "note"));
  const actions = $derived(steps.filter((step) => step.kind !== "note"));

  function isLink(step: GuidanceStep): boolean {
    return (step.kind === "link" || step.kind === "external") && !!step.href;
  }
</script>

{#if notes.length > 0}
  <ol class="mt-2 list-decimal space-y-1 pl-5 text-sm text-muted-foreground">
    {#each notes as note (note.id)}
      <li>{note.label}</li>
    {/each}
  </ol>
{/if}

{#if actions.length > 0}
  <div
    class={cn(
      "mt-3 flex flex-wrap gap-2",
      layout === "row" ? "flex-row items-center" : "flex-col items-start",
      className,
    )}
  >
    {#each actions as action (action.id)}
      {#if isLink(action)}
        <a
          href={action.href}
          class="btn btn-sm btn-secondary"
          rel={action.kind === "external" ? "noopener noreferrer" : undefined}
          target={action.kind === "external" ? "_blank" : undefined}
        >
          {action.label}
        </a>
      {:else}
        <button
          type="button"
          class="btn btn-sm btn-secondary"
          onclick={() => onAction?.(action)}
        >
          {action.label}
        </button>
      {/if}
    {/each}
  </div>
{/if}
