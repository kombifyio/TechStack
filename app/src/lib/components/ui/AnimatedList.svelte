<script lang="ts">
  /**
   * AnimatedList - Svelte port of MagicUI's AnimatedList
   *
   * Sequential item animation with spring physics
   * Based on: https://magicui.design/docs/components/animated-list
   */
  import { onMount } from "svelte";
  import { flip } from "svelte/animate";
  import { scale } from "svelte/transition";
  import { backOut } from "svelte/easing";
  import type { Snippet } from "svelte";

  interface Props {
    items: unknown[];
    delay?: number;
    reverse?: boolean;
    class?: string;
    itemClass?: string;
    children: Snippet<[{ item: unknown; index: number }]>;
  }

  let {
    items = [],
    delay = 1000,
    reverse = false,
    class: className = "",
    itemClass = "",
    children,
  }: Props = $props();

  let visibleCount = $state(0);
  let mounted = $state(false);
  let timeouts: ReturnType<typeof setTimeout>[] = [];

  function clearAllTimeouts() {
    timeouts.forEach(clearTimeout);
    timeouts = [];
  }

  function animateItems(itemCount: number) {
    clearAllTimeouts();
    visibleCount = 0;

    let currentIndex = 0;
    const showNextItem = () => {
      if (currentIndex < itemCount && mounted) {
        visibleCount = currentIndex + 1;
        currentIndex++;
        if (currentIndex < itemCount) {
          const t = setTimeout(showNextItem, delay);
          timeouts.push(t);
        }
      }
    };

    // Start with first item after brief delay
    const t = setTimeout(showNextItem, 100);
    timeouts.push(t);
  }

  onMount(() => {
    mounted = true;
    animateItems(items.length);

    return () => {
      mounted = false;
      clearAllTimeouts();
    };
  });

  // Re-animate when items change
  $effect(() => {
    const itemCount = items.length;
    if (mounted && itemCount > 0) {
      animateItems(itemCount);
    }
  });

  // Items to show (optionally reversed like MagicUI notification demo)
  let visibleItems = $derived(() => {
    const sliced = items.slice(0, visibleCount);
    return reverse ? [...sliced].reverse() : sliced;
  });
</script>

<div class="flex flex-col items-center gap-4 {className}">
  {#each visibleItems() as item, displayIndex (reverse ? items.length - 1 - displayIndex : displayIndex)}
    {@const actualIndex = reverse
      ? items.length - 1 - displayIndex
      : displayIndex}
    <div
      class="mx-auto w-full {itemClass}"
      in:scale={{
        start: 0,
        duration: 400,
        easing: backOut,
        opacity: 0,
      }}
      animate:flip={{ duration: 300 }}
      style="transform-origin: top center;"
    >
      {@render children({ item, index: actualIndex })}
    </div>
  {/each}
</div>

<style>
  /* Spring-like enter animation to match MagicUI */
  div > :global(div) {
    will-change: transform, opacity;
  }
</style>
