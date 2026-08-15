<script lang="ts">
  /**
   * KeyboardShortcuts
   *
   * Global keyboard event listener for kombify-TechStack.
   * Handles vim-style navigation (j/k), multi-key sequences (g+h), and common shortcuts.
   *
   * This component should be included once in the root layout.
   */
  import { goto } from "$app/navigation";
  import { page } from "$app/stores";
  import {
    shortcutsStore,
    shortcutsEnabled,
    pendingKey,
    showShortcutsHelp,
  } from "$lib/stores/shortcuts";
  import { authStore } from "$lib/stores/auth.svelte";
  import ShortcutsHelp from "./ShortcutsHelp.svelte";

  // Navigation routes for g+key sequences
  const NAVIGATION_ROUTES: Record<string, string> = {
    h: "/stacks", // Home/Dashboard -> Stacks is the main view
    s: "/services",
    m: "/monitoring",
    w: "/wallet",
    t: "/settings",
  };

  // Track if we're in a text input
  function isInputFocused(): boolean {
    const active = document.activeElement;
    if (!active) return false;

    const tagName = active.tagName.toLowerCase();
    if (tagName === "input" || tagName === "textarea" || tagName === "select") {
      return true;
    }

    // Check for contenteditable
    if (active.hasAttribute("contenteditable")) {
      return true;
    }

    // Check for elements with role="textbox"
    if (active.getAttribute("role") === "textbox") {
      return true;
    }

    return false;
  }

  function handleKeydown(e: KeyboardEvent) {
    // Don't handle shortcuts if disabled
    if (!$shortcutsEnabled) return;

    // Don't handle shortcuts if not logged in
    if (!authStore.isAuthenticated) return;

    // Don't handle if in input field (except for Escape)
    if (isInputFocused() && e.key !== "Escape") return;

    // Don't handle if modifier keys are pressed (except for ?)
    if ((e.ctrlKey || e.metaKey || e.altKey) && e.key !== "?") return;

    const key = e.key.toLowerCase();

    // Handle help modal toggle
    if (e.key === "?" || (e.shiftKey && key === "/")) {
      e.preventDefault();
      shortcutsStore.toggleHelp();
      return;
    }

    // If help modal is shown, only handle Escape
    if ($showShortcutsHelp) {
      if (e.key === "Escape") {
        e.preventDefault();
        shortcutsStore.hideHelp();
      }
      return;
    }

    // Handle Escape
    if (e.key === "Escape") {
      e.preventDefault();
      // Clear selection if any
      shortcutsStore.clearSelection();
      // Clear pending key
      shortcutsStore.clearPendingKey();
      // Dispatch custom event for components to handle (e.g., close modals)
      window.dispatchEvent(new CustomEvent("keyboard-escape"));
      return;
    }

    // Handle multi-key sequences (g + ...)
    if ($pendingKey === "g") {
      shortcutsStore.clearPendingKey();

      if (key in NAVIGATION_ROUTES) {
        e.preventDefault();
        goto(NAVIGATION_ROUTES[key]);
        return;
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

    // Handle list navigation
    if (key === "j" || e.key === "ArrowDown") {
      e.preventDefault();
      shortcutsStore.moveDown();
      scrollSelectedIntoView();
      return;
    }

    if (key === "k" || e.key === "ArrowUp") {
      e.preventDefault();
      shortcutsStore.moveUp();
      scrollSelectedIntoView();
      return;
    }

    if (e.key === "Home") {
      e.preventDefault();
      shortcutsStore.jumpToFirst();
      scrollSelectedIntoView();
      return;
    }

    if (e.key === "End") {
      e.preventDefault();
      shortcutsStore.jumpToLast();
      scrollSelectedIntoView();
      return;
    }

    // Handle Enter - trigger click on selected item
    if (e.key === "Enter") {
      const selected = document.querySelector(
        "[data-keyboard-selected='true']",
      );
      if (selected instanceof HTMLElement) {
        e.preventDefault();
        selected.click();
      }
      return;
    }

    // Handle refresh (r)
    if (key === "r") {
      e.preventDefault();
      // Dispatch custom event for components to handle refresh
      window.dispatchEvent(new CustomEvent("keyboard-refresh"));
      return;
    }

    // Handle search focus (/)
    if (key === "/") {
      e.preventDefault();
      // Focus the first search input on the page
      const searchInput = document.querySelector(
        'input[type="search"], input[placeholder*="Search"], input[placeholder*="search"], [data-search-input]',
      );
      if (searchInput instanceof HTMLElement) {
        searchInput.focus();
      }
      return;
    }
  }

  function scrollSelectedIntoView() {
    // Use requestAnimationFrame to ensure DOM has updated
    requestAnimationFrame(() => {
      const selected = document.querySelector(
        "[data-keyboard-selected='true']",
      );
      if (selected instanceof HTMLElement) {
        selected.scrollIntoView({ behavior: "smooth", block: "nearest" });
      }
    });
  }
</script>

<svelte:window onkeydown={handleKeydown} />

<!-- Pending key indicator -->
{#if $pendingKey}
  <div
    class="fixed bottom-4 right-4 px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg shadow-lg z-50"
  >
    <span class="text-gray-400 text-sm">Waiting for key: </span>
    <kbd
      class="px-2 py-1 bg-gray-700 rounded text-sm font-mono text-primary border border-gray-600"
    >
      {$pendingKey}
    </kbd>
    <span class="text-gray-500 text-sm ml-2">+ ?</span>
  </div>
{/if}

<!-- Help modal -->
<ShortcutsHelp
  show={$showShortcutsHelp}
  onclose={() => shortcutsStore.hideHelp()}
/>
