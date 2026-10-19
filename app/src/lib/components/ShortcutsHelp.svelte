<script lang="ts">
  import { tr, trParts } from "#lib/i18n.svelte.js";
  /**
   * ShortcutsHelp Modal
   *
   * Displays all available keyboard shortcuts grouped by category, and the
   * switch that turns them off (WCAG 2.1.4). Triggered by pressing the ? key.
   */
  import Modal from "./Modal.svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import { toast } from "#lib/stores/toast.js";
  import {
    SHORTCUTS,
    shortcutsStore,
    type ShortcutDef,
  } from "#lib/stores/shortcuts.js";

  interface Props {
    show: boolean;
    onclose: () => void;
  }

  let { show, onclose }: Props = $props();

  // Group shortcuts by category
  const shortcutsByCategory = $derived.by(() => {
    const grouped: Record<string, ShortcutDef[]> = {
      navigation: [],
      actions: [],
      dialogs: [],
    };

    for (const shortcut of SHORTCUTS) {
      grouped[shortcut.category].push(shortcut);
    }

    return grouped;
  });

  const categoryLabels: Record<string, string> = $derived({
    navigation: tr("ui.shortcutsHelp.pageNavigation"),
    actions: tr("ui.stacksId.actions"),
    dialogs: tr("ui.shortcutsHelp.dialogs"),
  });

  const categoryOrder = ["navigation", "actions", "dialogs"];

  function turnOff() {
    shortcutsStore.setEnabled(false);
    toast.info(tr("shortcuts.turnedOff"), tr("shortcuts.turnedOffHint"));
  }
</script>

{#if show}
  <Modal
    title={tr("ui.shortcuts.title")}
    onClose={onclose}
    maxWidth="2xl"
    testId="keyboard-shortcuts-help"
  >
    <div class="grid gap-6 md:grid-cols-2">
      {#each categoryOrder as category (category)}
        {@const shortcuts = shortcutsByCategory[category]}
        {#if shortcuts && shortcuts.length > 0}
          <section>
            <h3
              class="text-sm font-medium text-muted-foreground uppercase tracking-wide mb-3"
            >
              {categoryLabels[category]}
            </h3>
            <ul class="space-y-2">
              {#each shortcuts as shortcut (shortcut.keys.join("+"))}
                <li
                  class="flex items-center justify-between gap-3 py-2 px-3 bg-muted/50 rounded-lg"
                >
                  <span class="text-foreground text-sm">
                    {shortcut.description}
                  </span>
                  <span class="flex shrink-0 items-center gap-1">
                    {#each shortcut.keys as key, i (i)}
                      {#if i > 0}
                        <span class="text-muted-foreground text-xs mx-1"
                          >{shortcut.category === "navigation"
                            ? tr("ui.shortcutsHelp.then")
                            : "/"}</span
                        >
                      {/if}
                      <kbd
                        class="px-2 py-1 bg-muted rounded text-xs font-mono text-foreground border border-border"
                      >
                        {key === "Escape" ? tr("ui.shortcutsHelp.esc") : key}
                      </kbd>
                    {/each}
                  </span>
                </li>
              {/each}
            </ul>
          </section>
        {/if}
      {/each}
    </div>

    {#snippet footer()}
      <span class="mr-auto text-sm text-muted-foreground">
        {trParts("ui.shortcuts.pressToShow")[0]}<kbd
          class="px-1.5 py-0.5 bg-muted rounded text-xs font-mono text-foreground border border-border"
          >?</kbd
        >{trParts("ui.shortcuts.pressToShow")[1]}
      </span>
      <Button variant="secondary" testId="shortcuts-turn-off" onclick={turnOff}>
        {tr("shortcuts.turnOff")}
      </Button>
    {/snippet}
  </Modal>
{/if}
