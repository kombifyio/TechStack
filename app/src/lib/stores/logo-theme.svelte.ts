import type { LogoTheme } from "#lib/brand-logo.js";

/*
 * The resolved UI theme as the logo picker needs it. The store in theme.ts can
 * hold `system`; the root `data-theme` attribute is the resolved value and also
 * follows host-pushed theme changes in embedded Techstack, so one observer on
 * it serves every logo on the page.
 */
function read(): LogoTheme {
  if (typeof document === "undefined") return "dark";
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

let current = $state<LogoTheme>(read());

if (typeof document !== "undefined") {
  new MutationObserver(() => {
    current = read();
  }).observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["data-theme"],
  });
}

/** The resolved theme; reactive in effects and `$derived`. */
export function logoTheme(): LogoTheme {
  return current;
}
