/**
 * Keyboard Shortcuts Store
 *
 * Reactive state for the single-key shortcuts of kombify-TechStack: the
 * `g` + key page jumps, `/` to search, `?` for help. Single-character
 * shortcuts must be switchable (WCAG 2.1.4 Character Key Shortcuts), so the
 * on/off choice is a device-local preference with a switch in the settings
 * and in the help dialog; this store owns that preference.
 *
 * Arrow keys, Home and End are never shortcuts: they belong to scrolling,
 * radio groups, tabs and the rail separator.
 */
import { writable, derived, get } from "svelte/store";

import { tr } from "#lib/i18n.svelte.js";

/** Device-local preference key: "off" disables, anything else means on. */
export const SHORTCUTS_STORAGE_KEY = "techstack-keyboard-shortcuts";

/** State for keyboard navigation */
export interface ShortcutsState {
  /** Whether shortcuts are globally enabled (device-local preference) */
  enabled: boolean;
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
  category: "navigation" | "actions" | "dialogs";
}

/** All available shortcuts */
export const SHORTCUTS: ShortcutDef[] = [
  // Page Navigation (g + key sequences)
  {
    keys: ["g", "h"],
    get description() {
      return tr("ui.shortcuts.goToHomeDashboard");
    },
    category: "navigation",
  },
  {
    keys: ["g", "s"],
    get description() {
      return tr("ui.shortcuts.goToServices");
    },
    category: "navigation",
  },
  {
    keys: ["g", "m"],
    get description() {
      return tr("ui.shortcuts.goToMonitoring");
    },
    category: "navigation",
  },
  {
    keys: ["g", "w"],
    get description() {
      return tr("ui.shortcuts.goToWallet");
    },
    category: "navigation",
  },
  {
    keys: ["g", "t"],
    get description() {
      return tr("ui.shortcuts.goToSettings");
    },
    category: "navigation",
  },

  // Actions
  {
    keys: ["/"],
    get description() {
      return tr("ui.shortcuts.focusSearchInput");
    },
    category: "actions",
  },

  // Dialogs
  {
    keys: ["?"],
    get description() {
      return tr("ui.shortcuts.showKeyboardShortcutsHelp");
    },
    category: "dialogs",
  },
  {
    keys: ["Escape"],
    get description() {
      return tr("ui.shortcuts.closeDialog");
    },
    category: "dialogs",
  },
];

/** The stored choice; shortcuts are on unless this device turned them off. */
export function readStoredShortcutsEnabled(): boolean {
  if (typeof window === "undefined") return true;
  try {
    return window.localStorage.getItem(SHORTCUTS_STORAGE_KEY) !== "off";
  } catch {
    // Storage can be blocked; the default applies for this session.
    return true;
  }
}

function writeStoredShortcutsEnabled(enabled: boolean): void {
  if (typeof window === "undefined") return;
  try {
    if (enabled) window.localStorage.removeItem(SHORTCUTS_STORAGE_KEY);
    else window.localStorage.setItem(SHORTCUTS_STORAGE_KEY, "off");
  } catch {
    // The choice still applies until the page is reloaded.
  }
}

const initialState: ShortcutsState = {
  enabled: true,
  showHelp: false,
  pendingKey: null,
  pendingKeyTimeout: null,
};

function createShortcutsStore() {
  const { subscribe, update } = writable<ShortcutsState>({
    ...initialState,
    enabled: readStoredShortcutsEnabled(),
  });

  return {
    subscribe,

    /** Re-read the device preference (after hydration). */
    loadPreference() {
      const enabled = readStoredShortcutsEnabled();
      update((s) => ({ ...s, enabled }));
    },

    /** Turn shortcuts on or off for this device. */
    setEnabled(enabled: boolean) {
      writeStoredShortcutsEnabled(enabled);
      update((s) => {
        if (!enabled && s.pendingKeyTimeout) clearTimeout(s.pendingKeyTimeout);
        return {
          ...s,
          enabled,
          showHelp: enabled ? s.showHelp : false,
          pendingKey: enabled ? s.pendingKey : null,
          pendingKeyTimeout: enabled ? s.pendingKeyTimeout : null,
        };
      });
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
  };
}

export const shortcutsStore = createShortcutsStore();

// Derived stores for convenience
export const shortcutsEnabled = derived(shortcutsStore, ($s) => $s.enabled);
export const showShortcutsHelp = derived(shortcutsStore, ($s) => $s.showHelp);
export const pendingKey = derived(shortcutsStore, ($s) => $s.pendingKey);
