// Theme store for dark/light mode toggle with persistence
import { get, writable } from "svelte/store";
import { browser } from "$app/env";
import {
  DEFAULT_FINISH,
  isFinish,
  type Finish,
  type NavFlyoutPreference,
} from "@kombiverselabs/design/contract";
import {
  applyNavFlyout,
  readDeviceNavFlyout,
  writeDeviceNavFlyout,
} from "@kombiverselabs/design/runtime";
import { fetchAccountDesign } from "#lib/design/accountDesign.js";

export type Theme = "dark" | "light" | "system";
export type SystemCardShape = "square" | "app";

/**
 * Appearance (owner rule 2026-10-09): the lasting default is the account's,
 * set in kombify Cloud Settings. A switch inside Techstack holds for this
 * browser session only, and an embedding host always decides. The retired
 * `techstack-theme` device choice outranked the host and is removed on init.
 */
const LEGACY_DEVICE_THEME_KEY = "techstack-theme";
const SESSION_THEME_KEY = "techstack-theme-session";
/** The last account default read; only a first-paint hint for app.html. */
const ACCOUNT_THEME_CACHE_KEY = "techstack-account-theme";
const CARD_SHAPE_STORAGE_KEY = "techstack-system-card-shape";
const FINISH_STORAGE_KEY = "techstack-finish";
/** How the rail's hover preview meets the rail (axis `navFlyout`), device-only. */
const NAV_FLYOUT_STORAGE_KEY = "techstack-nav-flyout";

/**
 * When embedded in the kombify Cloud portal, the portal passes its own theme as
 * `?theme=dark|light` on the iframe src. The portal's `theme` postMessage can
 * only arrive after our bridge announces "ready", which is too late for the
 * first paint and reads as a flash.
 *
 * This is inherited context, and it wins over every choice inside the app.
 */
function themeFromEmbedUrl(): Theme | null {
  if (!browser) return null;
  const value = new URLSearchParams(window.location.search).get("theme");
  return value === "dark" || value === "light" ? value : null;
}

function isTheme(value: unknown): value is Theme {
  return value === "dark" || value === "light" || value === "system";
}

function readStoredTheme(storage: () => Storage, key: string): Theme | null {
  if (!browser) return null;
  try {
    const stored = storage().getItem(key);
    return isTheme(stored) ? stored : null;
  } catch {
    return null;
  }
}

/** This session's temporary switch. */
function sessionTheme(): Theme | null {
  return readStoredTheme(() => sessionStorage, SESSION_THEME_KEY);
}

function isEmbedded(): boolean {
  if (!browser) return false;
  try {
    return window.parent !== window;
  } catch {
    return true;
  }
}

// The SSO continuation and client navigation remove the launch query. Keep
// its validated host seed for this document; live host messages still win.
const embeddedAppearance = isEmbedded() ? themeFromEmbedUrl() : null;
let hostAppearance: Theme | null = null;
let accountAppearance: Theme | null = null;

function cachedAccountTheme(): Theme | null {
  return readStoredTheme(() => localStorage, ACCOUNT_THEME_CACHE_KEY);
}

function resolveTheme(): Theme {
  const account = accountAppearance ?? cachedAccountTheme();
  if (isEmbedded()) {
    return (
      hostAppearance ??
      embeddedAppearance ??
      sessionTheme() ??
      account ??
      "system"
    );
  }
  return sessionTheme() ?? account ?? "system";
}

function resolveAppearance(value: Theme): "dark" | "light" {
  return value === "dark" ||
    (value === "system" &&
      browser &&
      window.matchMedia("(prefers-color-scheme: dark)").matches)
    ? "dark"
    : "light";
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
  const themeStore = writable<Theme>(resolveTheme());
  const { subscribe, set } = themeStore;
  const preferenceStore = writable<Theme | null>(sessionTheme());
  const resolvedStore = writable<"dark" | "light">(
    resolveAppearance(resolveTheme()),
  );
  let initialized = false;
  const finishStore = writable<Finish>(
    browser ? resolveFinish() : DEFAULT_FINISH,
  );
  const navFlyoutStore = writable<NavFlyoutPreference>(
    browser
      ? readDeviceNavFlyout(localStorage, NAV_FLYOUT_STORAGE_KEY)
      : "auto",
  );

  function reapplyFinish() {
    const resolved = resolveFinish();
    applyFinishToDom(resolved);
    finishStore.set(resolved);
  }

  function applyTheme(theme: Theme) {
    if (!browser) return;

    const isDark = resolveAppearance(theme) === "dark";

    document.documentElement.classList.toggle("dark", isDark);
    document.documentElement.classList.toggle("light", !isDark);

    // Also set a data attribute for CSS targeting
    document.documentElement.dataset.theme = isDark ? "dark" : "light";

    // The design axis the generated material layer reads
    // (KOMBIFY-DESIGN-SYSTEM-STANDARD §4). It has to stay in lockstep with
    // the `dark` class; the pre-paint script in app.html sets the same pair
    // before first paint, and this keeps it true after every toggle.
    document.documentElement.dataset.appearance = isDark ? "dark" : "light";
    resolvedStore.set(isDark ? "dark" : "light");
  }

  function reapplyTheme() {
    const value = resolveTheme();
    applyTheme(value);
    preferenceStore.set(sessionTheme());
    set(value);
  }

  /** A temporary switch for this browser session. */
  function chooseTheme(value: Theme) {
    if (browser) {
      try {
        sessionStorage.setItem(SESSION_THEME_KEY, value);
      } catch {
        // Session storage is optional.
      }
    }
    reapplyTheme();
  }

  return {
    subscribe,
    preference: { subscribe: preferenceStore.subscribe },
    resolved: { subscribe: resolvedStore.subscribe },
    /** The resolved finish, for pickers and previews. */
    finish: { subscribe: finishStore.subscribe },
    /** The device's rail preview shape; `auto` follows the finish. */
    navFlyout: { subscribe: navFlyoutStore.subscribe },
    setNavFlyout: (navFlyout: NavFlyoutPreference) => {
      if (!browser) return;
      writeDeviceNavFlyout(localStorage, navFlyout, NAV_FLYOUT_STORAGE_KEY);
      const stored = readDeviceNavFlyout(localStorage, NAV_FLYOUT_STORAGE_KEY);
      applyNavFlyout(document.documentElement, stored);
      navFlyoutStore.set(stored);
    },
    set: chooseTheme,
    setHostTheme: (value: Theme) => {
      hostAppearance = value;
      reapplyTheme();
    },
    followDefaultTheme: () => {
      if (browser) {
        try {
          sessionStorage.removeItem(SESSION_THEME_KEY);
        } catch {
          // Session storage is optional.
        }
      }
      reapplyTheme();
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
      chooseTheme(get(resolvedStore) === "dark" ? "light" : "dark");
    },
    cycle: () => {
      const order: Theme[] = ["dark", "light", "system"];
      chooseTheme(order[(order.indexOf(get(themeStore)) + 1) % order.length]);
    },
    init: () => {
      if (browser) {
        try {
          localStorage.removeItem(LEGACY_DEVICE_THEME_KEY);
        } catch {
          // Device storage is optional.
        }
      }
      reapplyTheme();
      applySystemCardShape(getInitialSystemCardShape());
      reapplyFinish();
      if (browser) {
        applyNavFlyout(
          document.documentElement,
          readDeviceNavFlyout(localStorage, NAV_FLYOUT_STORAGE_KEY),
        );
      }

      // Listen for system theme changes when in system mode
      if (browser && !initialized) {
        initialized = true;
        window
          .matchMedia("(prefers-color-scheme: dark)")
          .addEventListener("change", reapplyTheme);

        // Account default, post-paint and fail-soft: it only shows where no
        // host context and no session switch exist, so a late fetch never
        // fights an explicit selection. It is cached for the next first paint.
        void fetchAccountDesign().then((account) => {
          if (!account) return;
          if (isFinish(account.finish)) {
            accountFinish = account.finish;
            reapplyFinish();
          }
          accountAppearance = isTheme(account.appearance)
            ? account.appearance
            : null;
          if (accountAppearance) {
            try {
              localStorage.setItem(ACCOUNT_THEME_CACHE_KEY, accountAppearance);
            } catch {
              // Device storage is optional.
            }
          }
          reapplyTheme();
        });
      }
    },
  };
}

export const theme = createThemeStore();
