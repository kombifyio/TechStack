// Theme store for dark/light mode toggle with persistence
import { writable } from "svelte/store";
import { browser } from "$app/env";
import {
  DEFAULT_FINISH,
  isFinish,
  type Finish,
} from "@kombiverselabs/design/contract";
import { fetchAccountDesign } from "#lib/design/accountDesign.js";

export type Theme = "dark" | "light" | "system";
export type SystemCardShape = "square" | "app";

const STORAGE_KEY = "techstack-theme";
const CARD_SHAPE_STORAGE_KEY = "techstack-system-card-shape";
const FINISH_STORAGE_KEY = "techstack-finish";

/**
 * When embedded in the kombify Cloud portal, the portal passes its own theme as
 * `?theme=dark|light` on the iframe src. The portal's `theme` postMessage can
 * only arrive after our bridge announces "ready", which is too late for the
 * first paint and reads as a flash.
 *
 * The param is only ever present when a host put it there, so its presence
 * means "embedded", and in that case the host wins: an embed that does not
 * match the page around it is the bug we are fixing. Standalone Techstack is
 * unaffected — no param, so localStorage keeps deciding.
 */
function themeFromEmbedUrl(): Theme | null {
  if (!browser) return null;
  const value = new URLSearchParams(window.location.search).get("theme");
  return value === "dark" || value === "light" ? value : null;
}

function getInitialTheme(): Theme {
  if (!browser) return "dark";

  const fromEmbed = themeFromEmbedUrl();
  if (fromEmbed) return fromEmbed;

  const stored = localStorage.getItem(STORAGE_KEY) as Theme | null;
  if (stored && ["dark", "light", "system"].includes(stored)) {
    return stored;
  }

  return "dark"; // Default to dark (kombify-TechStack is designed dark-first)
}

function cardShapeFromEmbedUrl(): SystemCardShape | null {
  if (!browser) return null;
  const value = new URLSearchParams(window.location.search).get(
    "system_card_shape",
  );
  return value === "square" || value === "app" ? value : null;
}

function getInitialSystemCardShape(): SystemCardShape {
  if (!browser) return "square";

  const fromEmbed = cardShapeFromEmbedUrl();
  if (fromEmbed) return fromEmbed;

  const stored = localStorage.getItem(CARD_SHAPE_STORAGE_KEY);
  return stored === "app" || stored === "square" ? stored : "square";
}

function applySystemCardShape(systemCardShape: SystemCardShape) {
  if (!browser) return;
  document.documentElement.dataset.systemCardShape = systemCardShape;
}

/**
 * The finish axis (KOMBIFY-DESIGN-SYSTEM-STANDARD §4), resolved by the
 * standard's precedence — highest first:
 *
 *   1. host design context: a portal `{type:'theme', finish}` push or the
 *      `?finish=` embed param (that embedding only, never persisted)
 *   2. the device's explicit choice (`techstack-finish` in localStorage)
 *   3. the account default (`companionV2.panelAppearance.finish`, fetched
 *      fail-soft from the Gateway after first paint)
 *   4. the `kombify` baseline
 *
 * The pre-paint script in app.html applies tiers 1/2/4 before first paint;
 * this store re-resolves after mount and whenever a tier changes.
 */
function finishFromEmbedUrl(): Finish | null {
  if (!browser) return null;
  const value = new URLSearchParams(window.location.search).get("finish");
  return isFinish(value) ? value : null;
}

function deviceFinish(): Finish | null {
  if (!browser) return null;
  const stored = localStorage.getItem(FINISH_STORAGE_KEY);
  return isFinish(stored) ? stored : null;
}

function applyFinishToDom(finish: Finish) {
  if (!browser) return;
  document.documentElement.dataset.finish = finish;
}

/** Portal-pushed host finish (tier 1); lives for this document only. */
let hostFinish: Finish | null = null;
/** Account default (tier 3); filled by the post-paint gateway fetch. */
let accountFinish: Finish | null = null;

function resolveFinish(): Finish {
  return (
    hostFinish ??
    finishFromEmbedUrl() ??
    deviceFinish() ??
    accountFinish ??
    DEFAULT_FINISH
  );
}

function createThemeStore() {
  const { subscribe, set, update } = writable<Theme>(getInitialTheme());
  const finishStore = writable<Finish>(
    browser ? resolveFinish() : DEFAULT_FINISH,
  );

  function reapplyFinish() {
    const resolved = resolveFinish();
    applyFinishToDom(resolved);
    finishStore.set(resolved);
  }

  function applyTheme(theme: Theme) {
    if (!browser) return;

    const isDark =
      theme === "dark" ||
      (theme === "system" &&
        window.matchMedia("(prefers-color-scheme: dark)").matches);

    document.documentElement.classList.toggle("dark", isDark);
    document.documentElement.classList.toggle("light", !isDark);

    // Also set a data attribute for CSS targeting
    document.documentElement.dataset.theme = isDark ? "dark" : "light";

    // The design axis the generated material layer reads
    // (KOMBIFY-DESIGN-SYSTEM-STANDARD §4). It has to stay in lockstep with
    // the `dark` class; the pre-paint script in app.html sets the same pair
    // before first paint, and this keeps it true after every toggle.
    document.documentElement.dataset.appearance = isDark ? "dark" : "light";
  }

  return {
    subscribe,
    /** The resolved finish, for pickers and previews. */
    finish: { subscribe: finishStore.subscribe },
    set: (theme: Theme) => {
      if (browser) {
        localStorage.setItem(STORAGE_KEY, theme);
      }
      applyTheme(theme);
      set(theme);
    },
    /** An explicit device choice — outranks the account default (§4). */
    setFinish: (finish: Finish) => {
      if (!isFinish(finish)) return;
      if (browser) {
        localStorage.setItem(FINISH_STORAGE_KEY, finish);
      }
      reapplyFinish();
    },
    /**
     * Drop the device choice and follow the account default again. The
     * account tier stays live from the last sync; re-fetches on next boot.
     */
    followAccountFinish: () => {
      if (browser) {
        localStorage.removeItem(FINISH_STORAGE_KEY);
      }
      reapplyFinish();
    },
    /** True when a device-local finish choice exists (settings UI state). */
    hasDeviceFinish: () => deviceFinish() !== null,
    /**
     * Host design context from the portal's `theme` postMessage — the top
     * tier, for this embedding only (FLOATING-PANEL-V2-CONTRACT §6 analog).
     */
    setHostFinish: (finish: unknown) => {
      if (!isFinish(finish)) return;
      hostFinish = finish;
      reapplyFinish();
    },
    setSystemCardShape: (systemCardShape: SystemCardShape) => {
      if (browser) {
        localStorage.setItem(CARD_SHAPE_STORAGE_KEY, systemCardShape);
      }
      applySystemCardShape(systemCardShape);
    },
    toggle: () => {
      update((current) => {
        const next = current === "dark" ? "light" : "dark";
        if (browser) {
          localStorage.setItem(STORAGE_KEY, next);
        }
        applyTheme(next);
        return next;
      });
    },
    cycle: () => {
      update((current) => {
        const order: Theme[] = ["dark", "light", "system"];
        const nextIndex = (order.indexOf(current) + 1) % order.length;
        const next = order[nextIndex];
        if (browser) {
          localStorage.setItem(STORAGE_KEY, next);
        }
        applyTheme(next);
        return next;
      });
    },
    init: () => {
      const theme = getInitialTheme();
      applyTheme(theme);
      set(theme);
      applySystemCardShape(getInitialSystemCardShape());
      reapplyFinish();

      // Listen for system theme changes when in system mode
      if (browser) {
        window
          .matchMedia("(prefers-color-scheme: dark)")
          .addEventListener("change", (e) => {
            const currentTheme = localStorage.getItem(STORAGE_KEY) as Theme;
            if (currentTheme === "system") {
              applyTheme("system");
            }
          });

        // Account default (tier 3), post-paint and fail-soft: it only shows
        // where no host context and no device choice exist, so a late fetch
        // never fights an explicit selection.
        void fetchAccountDesign().then((account) => {
          if (!account) return;
          if (isFinish(account.finish)) {
            accountFinish = account.finish;
            reapplyFinish();
          }
          const hasUrlTheme = themeFromEmbedUrl() !== null;
          const hasStoredTheme = localStorage.getItem(STORAGE_KEY) !== null;
          if (!hasUrlTheme && !hasStoredTheme) {
            // Session-only: not persisted, so a later account change still
            // propagates to this device on its next boot.
            applyTheme(account.appearance);
            set(account.appearance);
          }
        });
      }
    },
  };
}

export const theme = createThemeStore();
