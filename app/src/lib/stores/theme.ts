// Theme store for dark/light mode toggle with persistence
import { writable } from "svelte/store";
import { browser } from "$app/environment";

export type Theme = "dark" | "light" | "system";
export type SystemCardShape = "square" | "app";

const STORAGE_KEY = "techstack-theme";
const CARD_SHAPE_STORAGE_KEY = "techstack-system-card-shape";

/**
 * When embedded in the Kombify Cloud portal, the portal passes its own theme as
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

function createThemeStore() {
  const { subscribe, set, update } = writable<Theme>(getInitialTheme());

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
  }

  return {
    subscribe,
    set: (theme: Theme) => {
      if (browser) {
        localStorage.setItem(STORAGE_KEY, theme);
      }
      applyTheme(theme);
      set(theme);
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
      }
    },
  };
}

export const theme = createThemeStore();
