<!--
  NodeMessageLine - the one fixed status line of a Node card (and of the list
  itself). It always occupies the same height, so a message can neither grow a
  card nor move its neighbours. The most severe message shows as one truncated
  line; clicking it opens every message's details as an overlay above the page.
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { ChevronDown, CircleAlert, Info, TriangleAlert } from "@lucide/svelte";

  import OverlayPanel from "#lib/components/ui/OverlayPanel.svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    bySeverity,
    type NodeMessage,
    type NodeMessageTone,
  } from "#lib/dashboard/node-messages.js";

  interface Props {
    messages: readonly NodeMessage[];
    /** The details of one message; rendered inside the overlay. */
    details: Snippet<[NodeMessage]>;
    /** Accessible name of the overlay. */
    label: string;
    class?: string;
  }

  let { messages, details, label, class: className = "" }: Props = $props();

  let open = $state(false);
  let anchor = $state<HTMLButtonElement | null>(null);

  const ordered = $derived(bySeverity(messages));
  const primary = $derived(ordered[0]);
  const more = $derived(ordered.length - 1);

  const toneText: Record<NodeMessageTone, string> = {
    error: "text-destructive",
    warning: "text-warning",
    info: "text-primary",
  };

  // A message that goes away takes its overlay with it.
  $effect(() => {
    if (!primary) open = false;
  });
</script>

<div
  class="flex h-8 min-w-0 items-center border-t border-border px-3 {className}"
  data-testid="node-message-line"
>
  {#if primary}
    <button
      bind:this={anchor}
      type="button"
      class="node-message-toggle flex h-full min-w-0 flex-1 items-center gap-2 rounded-md text-left text-xs"
      aria-haspopup="true"
      aria-expanded={open}
      data-testid="node-message-toggle"
      data-tone={primary.tone}
      title={primary.summary}
      onclick={() => (open = !open)}
    >
      {#if primary.tone === "error"}
        <CircleAlert class="h-3.5 w-3.5 shrink-0 {toneText.error}" aria-hidden="true" />
      {:else if primary.tone === "warning"}
        <TriangleAlert class="h-3.5 w-3.5 shrink-0 {toneText.warning}" aria-hidden="true" />
      {:else}
        <Info class="h-3.5 w-3.5 shrink-0 {toneText.info}" aria-hidden="true" />
      {/if}
      <span class="min-w-0 truncate font-medium text-foreground">
        {primary.summary}
      </span>
      {#if more > 0}
        <span
          class="shrink-0 rounded-full bg-muted px-1.5 text-[11px] text-muted-foreground"
          >+{more}</span
        >
      {/if}
      <ChevronDown
        class="node-message-chevron ml-auto h-3.5 w-3.5 shrink-0 text-muted-foreground {open
          ? 'rotate-180'
          : ''}"
        aria-hidden="true"
      />
    </button>
  {:else}
    <span class="truncate text-xs text-muted-foreground/70">
      {tr("ui.nodeMessage.idle")}
    </span>
  {/if}
</div>

{#if open && primary}
  <OverlayPanel
    {anchor}
    {label}
    testId="node-message-overlay"
    onClose={() => (open = false)}
  >
    <div class="flex flex-col divide-y divide-border">
      {#each ordered as message (message.id)}
        <section class="flex flex-col gap-2 p-3" data-testid="node-message-detail">
          {#if ordered.length > 1}
            <h3 class="text-xs font-semibold {toneText[message.tone]}">
              {message.summary}
            </h3>
          {/if}
          {@render details(message)}
        </section>
      {/each}
    </div>
  </OverlayPanel>
{/if}

<style>
  .node-message-toggle:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 1px;
  }
  @media (prefers-reduced-motion: no-preference) {
    .node-message-toggle :global(.node-message-chevron) {
      transition: transform var(--kx-dur-fast) var(--kx-ease-out);
    }
  }
</style>
