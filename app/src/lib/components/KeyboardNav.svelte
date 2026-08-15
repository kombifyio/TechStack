<script lang="ts">
  /**
   * KeyboardNav
   *
   * A wrapper component that enables keyboard navigation for a list of items.
   * Registers with the shortcuts store and marks selected items.
   *
   * Usage:
   * ```svelte
   * <KeyboardNav
   *   listId="agents-list"
   *   items={agents}
   *   onselect={(agent, index) => handleSelect(agent)}
   * >
   *   {#snippet children(item, index, isSelected)}
   *     <AgentCard
   *       {agent}
   *       selected={isSelected}
   *       data-keyboard-selected={isSelected}
   *     />
   *   {/snippet}
   * </KeyboardNav>
   * ```
   */
  import { onMount, onDestroy, type Snippet } from "svelte";
  import { shortcutsStore, selectedIndex } from "$lib/stores/shortcuts";

  interface Props<T> {
    /** Unique ID for this navigable list */
    listId: string;
    /** Array of items to navigate */
    items: T[];
    /** Callback when Enter is pressed on selected item */
    onselect?: (item: T, index: number) => void;
    /** Additional class for the container */
    class?: string;
    /** Children snippet receiving (item, index, isSelected) */
    children: Snippet<[T, number, boolean]>;
  }

  let {
    listId,
    items,
    onselect,
    class: className = "",
    children,
  }: Props<any> = $props();

  // Track the current selected index locally to trigger callbacks
  let prevSelectedIndex = -1;

  onMount(() => {
    // Register this list with the shortcuts store
    shortcutsStore.registerList(listId, items.length);

    // Listen for Enter key on selected items
    const handleEnter = () => {
      const idx = $selectedIndex;
      if (idx >= 0 && idx < items.length) {
        onselect?.(items[idx], idx);
      }
    };

    // Custom event listener for keyboard selection
    window.addEventListener("keyboard-enter", handleEnter);

    return () => {
      window.removeEventListener("keyboard-enter", handleEnter);
    };
  });

  onDestroy(() => {
    shortcutsStore.unregisterList(listId);
  });

  // Update total items when the list changes
  $effect(() => {
    shortcutsStore.setTotalItems(items.length);
  });
</script>

<div class="keyboard-nav-container {className}" data-keyboard-nav={listId}>
  {#each items as item, index}
    {@const isSelected = $selectedIndex === index}
    <div
      data-keyboard-item={index}
      data-keyboard-selected={isSelected}
      class:keyboard-selected={isSelected}
    >
      {@render children(item, index, isSelected)}
    </div>
  {/each}
</div>

<style>
  /* Default selected indicator - can be overridden */
  :global([data-keyboard-selected="true"]) {
    outline: 2px solid rgb(34 211 238 / 0.5);
    outline-offset: 2px;
    border-radius: 0.5rem;
  }
</style>
