/**
 * Keyboard Shortcuts Store
 *
 * Provides reactive state for keyboard navigation across kombify-TechStack.
 * Supports j/k navigation, vim-like shortcuts, and multi-key sequences.
 */
import { writable, derived, get } from "svelte/store";

/** State for keyboard navigation */
export interface ShortcutsState {
  /** Whether shortcuts are globally enabled */
  enabled: boolean;
  /** Currently selected item index in navigable lists (-1 = none) */
  selectedIndex: number;
  /** Total number of items in current navigable list */
  totalItems: number;
  /** ID of the currently focused navigable element */
  activeListId: string | null;
  /** Whether help modal is shown */
  showHelp: boolean;
  /** Pending key for multi-key sequences (e.g., 'g' waiting for 'h') */
  pendingKey: string | null;
  /** Timeout ID for clearing pending key */
  pendingKeyTimeout: ReturnType<typeof setTimeout> | null;
}

/** Shortcut definition for help display */
export interface ShortcutDef {
  keys: string[];
  description: string;
  category: "navigation" | "list" | "actions" | "dialogs";
}

/** All available shortcuts */
export const SHORTCUTS: ShortcutDef[] = [
  // List Navigation
  { keys: ["j", "↓"], description: "Move down in list", category: "list" },
  { keys: ["k", "↑"], description: "Move up in list", category: "list" },
  { keys: ["Enter"], description: "Select/open item", category: "list" },
  { keys: ["Home"], description: "Jump to first item", category: "list" },
  { keys: ["End"], description: "Jump to last item", category: "list" },

  // Page Navigation (g + key sequences)
  {
    keys: ["g", "h"],
    description: "Go to Home/Dashboard",
    category: "navigation",
  },
  { keys: ["g", "s"], description: "Go to Services", category: "navigation" },
  { keys: ["g", "m"], description: "Go to Monitoring", category: "navigation" },
  { keys: ["g", "w"], description: "Go to Wallet", category: "navigation" },
  { keys: ["g", "t"], description: "Go to Settings", category: "navigation" },

  // Actions
  { keys: ["r"], description: "Refresh current view", category: "actions" },
  { keys: ["/"], description: "Focus search input", category: "actions" },

  // Dialogs
  {
    keys: ["?"],
    description: "Show keyboard shortcuts help",
    category: "dialogs",
  },
  {
    keys: ["Escape"],
    description: "Close dialog/deselect",
    category: "dialogs",
  },
];

const initialState: ShortcutsState = {
  enabled: true,
  selectedIndex: -1,
  totalItems: 0,
  activeListId: null,
  showHelp: false,
  pendingKey: null,
  pendingKeyTimeout: null,
};

function createShortcutsStore() {
  const { subscribe, set, update } = writable<ShortcutsState>(initialState);

  return {
    subscribe,

    /** Enable or disable shortcuts globally */
    setEnabled(enabled: boolean) {
      update((s) => ({ ...s, enabled }));
    },

    /** Toggle shortcuts on/off */
    toggleEnabled() {
      update((s) => ({ ...s, enabled: !s.enabled }));
    },

    /** Register a navigable list */
    registerList(listId: string, totalItems: number) {
      update((s) => ({
        ...s,
        activeListId: listId,
        totalItems,
        selectedIndex: totalItems > 0 ? 0 : -1,
      }));
    },

    /** Unregister a navigable list */
    unregisterList(listId: string) {
      update((s) => {
        if (s.activeListId === listId) {
          return {
            ...s,
            activeListId: null,
            totalItems: 0,
            selectedIndex: -1,
          };
        }
        return s;
      });
    },

    /** Update total items count (e.g., after filtering) */
    setTotalItems(totalItems: number) {
      update((s) => ({
        ...s,
        totalItems,
        selectedIndex:
          s.selectedIndex >= totalItems
            ? Math.max(0, totalItems - 1)
            : s.selectedIndex,
      }));
    },

    /** Move selection down */
    moveDown() {
      update((s) => {
        if (s.totalItems === 0) return s;
        const newIndex =
          s.selectedIndex < s.totalItems - 1 ? s.selectedIndex + 1 : 0; // Wrap around
        return { ...s, selectedIndex: newIndex };
      });
    },

    /** Move selection up */
    moveUp() {
      update((s) => {
        if (s.totalItems === 0) return s;
        const newIndex =
          s.selectedIndex > 0 ? s.selectedIndex - 1 : s.totalItems - 1; // Wrap around
        return { ...s, selectedIndex: newIndex };
      });
    },

    /** Jump to first item */
    jumpToFirst() {
      update((s) => ({
        ...s,
        selectedIndex: s.totalItems > 0 ? 0 : -1,
      }));
    },

    /** Jump to last item */
    jumpToLast() {
      update((s) => ({
        ...s,
        selectedIndex: s.totalItems > 0 ? s.totalItems - 1 : -1,
      }));
    },

    /** Set selection to specific index */
    setSelectedIndex(index: number) {
      update((s) => ({
        ...s,
        selectedIndex: Math.max(-1, Math.min(index, s.totalItems - 1)),
      }));
    },

    /** Clear selection */
    clearSelection() {
      update((s) => ({ ...s, selectedIndex: -1 }));
    },

    /** Show help modal */
    showHelp() {
      update((s) => ({ ...s, showHelp: true }));
    },

    /** Hide help modal */
    hideHelp() {
      update((s) => ({ ...s, showHelp: false }));
    },

    /** Toggle help modal */
    toggleHelp() {
      update((s) => ({ ...s, showHelp: !s.showHelp }));
    },

    /** Set pending key for multi-key sequences */
    setPendingKey(key: string | null) {
      update((s) => {
        // Clear existing timeout
        if (s.pendingKeyTimeout) {
          clearTimeout(s.pendingKeyTimeout);
        }

        if (key === null) {
          return { ...s, pendingKey: null, pendingKeyTimeout: null };
        }

        // Set new timeout to clear pending key after 1.5 seconds
        const timeout = setTimeout(() => {
          update((inner) => ({
            ...inner,
            pendingKey: null,
            pendingKeyTimeout: null,
          }));
        }, 1500);

        return { ...s, pendingKey: key, pendingKeyTimeout: timeout };
      });
    },

    /** Get current pending key */
    getPendingKey(): string | null {
      return get({ subscribe }).pendingKey;
    },

    /** Clear pending key */
    clearPendingKey() {
      update((s) => {
        if (s.pendingKeyTimeout) {
          clearTimeout(s.pendingKeyTimeout);
        }
        return { ...s, pendingKey: null, pendingKeyTimeout: null };
      });
    },

    /** Reset to initial state */
    reset() {
      update((s) => {
        if (s.pendingKeyTimeout) {
          clearTimeout(s.pendingKeyTimeout);
        }
        return { ...initialState };
      });
    },
  };
}

export const shortcutsStore = createShortcutsStore();

// Derived stores for convenience
export const selectedIndex = derived(shortcutsStore, ($s) => $s.selectedIndex);
export const totalItems = derived(shortcutsStore, ($s) => $s.totalItems);
export const shortcutsEnabled = derived(shortcutsStore, ($s) => $s.enabled);
export const showShortcutsHelp = derived(shortcutsStore, ($s) => $s.showHelp);
export const pendingKey = derived(shortcutsStore, ($s) => $s.pendingKey);
