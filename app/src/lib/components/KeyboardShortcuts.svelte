<script lang="ts">
  import { onMount } from "svelte";
  import { tr } from "#lib/i18n.svelte.js";
  /**
   * KeyboardShortcuts
   *
   * Global keyboard listener for kombify-TechStack: `g` + key jumps to a
   * page, `/` focuses the search field, `?` opens the shortcut list.
   *
   * Rules this handler keeps (WCAG 2.1.1, 2.1.4):
   * - The whole set can be turned off (settings, and the help dialog); the
   *   choice is device-local.
   * - It never acts while focus is in a form control or an interactive
   *   widget (text fields, selects, radio groups, tabs, sliders, the rail
   *   separator, menus), and never while a dialog is open.
   * - It never intercepts arrow keys, Home, End, Enter or Space: those belong
   *   to scrolling, widgets and the focused element.
   *
   * This component should be included once in the root layout.
   */
  import { goto } from "$app/navigation";
  import {
    shortcutsStore,
    shortcutsEnabled,
    pendingKey,
    showShortcutsHelp,
  } from "#lib/stores/shortcuts.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import ShortcutsHelp from "./ShortcutsHelp.svelte";

  // Navigation routes for g+key sequences
  const NAVIGATION_ROUTES: Record<string, string> = {
    h: "/dashboard", // Home opens the owner's singular Homelab dashboard.
    s: "/services",
    m: "/monitoring",
    w: "/wallet",
    t: "/settings",
  };

  const WIDGET_ROLES = new Set([
    "textbox",
    "searchbox",
    "combobox",
    "listbox",
    "option",
    "radio",
    "radiogroup",
    "checkbox",
    "switch",
    "slider",
    "spinbutton",
    "separator",
    "scrollbar",
    "tab",
    "tablist",
    "menu",
    "menubar",
    "menuitem",
    "menuitemcheckbox",
    "menuitemradio",
    "tree",
    "treeitem",
    "grid",
    "gridcell",
    "application",
  ]);

  /**
   * True when the focused element handles keys itself: a form control, an
   * editable region or an ARIA widget. A plain link or button is not one:
   * `g h` from a focused button is still a page jump.
   */
  function focusHandlesKeys(): boolean {
    const active = document.activeElement;
    if (!active || active === document.body) return false;

    const tagName = active.tagName.toLowerCase();
    if (tagName === "input" || tagName === "textarea" || tagName === "select") {
      return true;
    }
    if (active instanceof HTMLElement && active.isContentEditable) return true;
    if (active.hasAttribute("contenteditable")) return true;

    const role = active.getAttribute("role");
    if (role && WIDGET_ROLES.has(role)) return true;
    // Anything inside a widget (an option inside a listbox, a tab in a tablist).
    return (
      active.closest(
        "[role='listbox'], [role='menu'], [role='menubar'], [role='radiogroup'], [role='tablist'], [role='tree'], [role='grid']",
      ) !== null
    );
  }

  function handleKeydown(e: KeyboardEvent) {
    // Don't handle shortcuts if disabled
    if (!$shortcutsEnabled) return;

    // Don't handle shortcuts if not logged in
    if (!authStore.isAuthenticated) return;

    // Another handler already owns this key press, or the input method does.
    if (e.defaultPrevented || e.isComposing || e.repeat) return;

    // Modifier combinations belong to the browser and the system.
    if (e.ctrlKey || e.metaKey || e.altKey) return;

    // Dialogs (native modal <dialog>) own the keyboard while open. The help
    // list is itself a dialog and may close with `?`.
    if (document.querySelector("dialog[open]") && !$showShortcutsHelp) return;

    // Form controls and widgets own their keys.
    if (focusHandlesKeys()) return;

    const key = e.key.toLowerCase();

    // Help list toggle
    if (e.key === "?") {
      e.preventDefault();
      shortcutsStore.toggleHelp();
      return;
    }

    // While the help list is open the native dialog handles Escape; nothing
    // else is a shortcut there.
    if ($showShortcutsHelp) return;

    // Multi-key sequences (g + ...)
    if ($pendingKey === "g") {
      shortcutsStore.clearPendingKey();

      if (key in NAVIGATION_ROUTES) {
        e.preventDefault();
        goto(NAVIGATION_ROUTES[key]);
      }
      // Unknown second key - ignore
      return;
    }

    // Start multi-key sequence
    if (key === "g") {
      e.preventDefault();
      shortcutsStore.setPendingKey("g");
      return;
    }

    // Search focus (/): the first search field on the page, if there is one.
    if (key === "/") {
      const searchInput = document.querySelector(
        'input[type="search"], [data-search-input]',
      );
      if (searchInput instanceof HTMLElement) {
        e.preventDefault();
        searchInput.focus();
      }
      return;
    }
  }

  onMount(() => {
    // The stored device preference, read once the page is in the browser.
    shortcutsStore.loadPreference();
  });
</script>

<svelte:window onkeydown={handleKeydown} />

<!-- Pending key indicator -->
{#if $pendingKey}
  <div
    data-kx="plate"
    role="status"
    class="fixed bottom-4 right-4 px-3 py-2 shadow-lg z-50"
  >
    <span class="text-muted-foreground text-sm"
      >{tr("ui.shortcuts.waiting")}
    </span>
    <kbd
      class="px-2 py-1 bg-muted rounded text-sm font-mono text-primary border border-border"
    >
      {$pendingKey}
    </kbd>
    <span class="text-muted-foreground text-sm ml-2">+ ?</span>
  </div>
{/if}

<!-- Help modal -->
<ShortcutsHelp
  show={$showShortcutsHelp}
  onclose={() => shortcutsStore.hideHelp()}
/>
