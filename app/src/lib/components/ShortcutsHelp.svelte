<script lang="ts">
  /**
   * ShortcutsHelp Modal
   *
   * Displays all available keyboard shortcuts grouped by category.
   * Triggered by pressing ? key.
   */
  import { SHORTCUTS, type ShortcutDef } from "$lib/stores/shortcuts";

  interface Props {
    show: boolean;
    onclose: () => void;
  }

  let { show, onclose }: Props = $props();

  // Group shortcuts by category
  const shortcutsByCategory = $derived(() => {
    const grouped: Record<string, ShortcutDef[]> = {
      navigation: [],
      list: [],
      actions: [],
      dialogs: [],
    };

    for (const shortcut of SHORTCUTS) {
      grouped[shortcut.category].push(shortcut);
    }

    return grouped;
  });

  const categoryLabels: Record<string, string> = {
    navigation: "Page Navigation",
    list: "List Navigation",
    actions: "Actions",
    dialogs: "Dialogs",
  };

  const categoryOrder = ["list", "navigation", "actions", "dialogs"];

  function handleKeydown(e: KeyboardEvent) {
    if (show && e.key === "Escape") {
      e.preventDefault();
      onclose();
    }
  }

  function handleBackdropClick(e: MouseEvent) {
    if (e.target === e.currentTarget) {
      onclose();
    }
  }

  function formatKeys(keys: string[]): string {
    return keys.join(" / ");
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if show}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div
    class="fixed inset-0 bg-black/70 flex items-center justify-center z-100 p-4"
    onclick={handleBackdropClick}
    role="presentation"
  >
    <div
      class="bg-gray-900 rounded-xl border border-gray-700 max-w-2xl w-full max-h-[85vh] overflow-hidden flex flex-col"
      role="dialog"
      aria-modal="true"
      aria-labelledby="shortcuts-title"
    >
      <!-- Header -->
      <div
        class="flex items-center justify-between p-4 border-b border-gray-700"
      >
        <div class="flex items-center gap-3">
          <div class="p-2 bg-primary/10 rounded-lg">
            <svg
              class="w-5 h-5 text-primary"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M12 6V4m0 2a2 2 0 100 4m0-4a2 2 0 110 4m-6 8a2 2 0 100-4m0 4a2 2 0 110-4m0 4v2m0-6V4m6 6v10m6-2a2 2 0 100-4m0 4a2 2 0 110-4m0 4v2m0-6V4"
              />
            </svg>
          </div>
          <h2 id="shortcuts-title" class="text-lg font-semibold text-white">
            Keyboard Shortcuts
          </h2>
        </div>
        <button
          onclick={onclose}
          class="p-2 rounded-lg text-gray-400 hover:text-white hover:bg-gray-800 transition-colors"
          aria-label="Close"
        >
          <svg
            class="w-5 h-5"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M6 18L18 6M6 6l12 12"
            />
          </svg>
        </button>
      </div>

      <!-- Content -->
      <div class="flex-1 overflow-y-auto p-4">
        <div class="grid gap-6 md:grid-cols-2">
          {#each categoryOrder as category}
            {@const shortcuts = shortcutsByCategory()[category]}
            {#if shortcuts && shortcuts.length > 0}
              <div>
                <h3
                  class="text-sm font-medium text-gray-400 uppercase tracking-wide mb-3"
                >
                  {categoryLabels[category]}
                </h3>
                <div class="space-y-2">
                  {#each shortcuts as shortcut}
                    <div
                      class="flex items-center justify-between py-2 px-3 bg-gray-800/50 rounded-lg"
                    >
                      <span class="text-gray-300 text-sm">
                        {shortcut.description}
                      </span>
                      <div class="flex items-center gap-1">
                        {#each shortcut.keys as key, i}
                          {#if i > 0}
                            <span class="text-gray-600 text-xs mx-1">/</span>
                          {/if}
                          {#if shortcut.keys.length === 2 && shortcut.category === "navigation" && i === 0}
                            <!-- Multi-key sequence like g + h -->
                            <kbd
                              class="px-2 py-1 bg-gray-700 rounded text-xs font-mono text-gray-200 border border-gray-600"
                            >
                              {key}
                            </kbd>
                            <span class="text-gray-500 mx-1">then</span>
                          {:else}
                            <kbd
                              class="px-2 py-1 bg-gray-700 rounded text-xs font-mono text-gray-200 border border-gray-600"
                            >
                              {key}
                            </kbd>
                          {/if}
                        {/each}
                      </div>
                    </div>
                  {/each}
                </div>
              </div>
            {/if}
          {/each}
        </div>
      </div>

      <!-- Footer -->
      <div class="p-4 border-t border-gray-700 bg-gray-800/30">
        <div class="flex items-center justify-between text-sm">
          <span class="text-gray-500">
            Press <kbd
              class="px-1.5 py-0.5 bg-gray-700 rounded text-xs font-mono text-gray-300 border border-gray-600"
              >?</kbd
            > anytime to show this help
          </span>
          <span class="text-gray-500">
            Press <kbd
              class="px-1.5 py-0.5 bg-gray-700 rounded text-xs font-mono text-gray-300 border border-gray-600"
              >Esc</kbd
            > to close
          </span>
        </div>
      </div>
    </div>
  </div>
{/if}
