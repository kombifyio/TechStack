/**
 * kombify-TechStack i18n Reactive Store (Svelte 5 Runes)
 *
 * This file contains the reactive state for i18n using Svelte 5 runes.
 * Import from this file in components, use i18n.ts for non-reactive contexts.
 */

import {
  type Locale,
  type MessageParams,
  defaultLocale,
  getStoredLocale,
  localeDirection,
  setStoredLocale,
  t,
} from "./i18n";

// Reactive locale state using Svelte 5 runes
// The app is a client-side SPA, so the stored or browser language is resolved
// at module load: components that read `tr()` once during initialisation
// already see the right language.
let currentLocale = $state<Locale>(
  typeof window !== "undefined" ? getStoredLocale() : defaultLocale,
);

// Track initialization
let initialized = $state(false);

/** Keep <html lang> and <html dir> in step with the active locale. */
function applyDocumentLocale(locale: Locale): void {
  if (typeof document === "undefined") return;
  document.documentElement.lang = locale;
  document.documentElement.dir = localeDirection(locale);
}

/**
 * Initialize the i18n system - call this in +layout.svelte onMount
 */
export function initI18n(): void {
  if (typeof window !== "undefined" && !initialized) {
    currentLocale = getStoredLocale();
    applyDocumentLocale(currentLocale);
    initialized = true;
  }
}

/**
 * Get the current locale (reactive)
 */
export function getLocale(): Locale {
  return currentLocale;
}

/**
 * Set the current locale and persist to localStorage
 */
export function setLocale(locale: Locale): void {
  currentLocale = locale;
  setStoredLocale(locale);
  applyDocumentLocale(locale);
}

/**
 * Translate a key using the current locale (reactive)
 */
export function tr(key: string, params?: MessageParams): string {
  return t(key, currentLocale, params);
}

/**
 * Translate a count-dependent message. Looks up `<key>.<plural category>`
 * for the active locale and falls back to `<key>.other`; `{count}` is filled in.
 */
export function trn(
  key: string,
  count: number,
  params: MessageParams = {},
): string {
  const category = new Intl.PluralRules(currentLocale).select(count);
  const specific = `${key}.${category}`;
  const resolved =
    t(specific, currentLocale) !== specific ? specific : `${key}.other`;
  return t(resolved, currentLocale, { count, ...params });
}

/**
 * Translate a message that embeds one inline element (link, code, key cap).
 * The message carries one `{slot}` token per element; the text parts around
 * them are returned so the caller can render the elements in between.
 */
export function trParts(key: string, params?: MessageParams): string[] {
  return t(key, currentLocale, params).split("{slot}");
}

/**
 * Label for a machine state such as `connected` or `not_reported`. Known
 * states come from the `state.<value>` messages; anything else renders as the
 * plain words of the raw value.
 */
export function stateLabel(value: string | null | undefined): string {
  const raw = value || "unknown";
  const key = `state.${raw}`;
  const known = t(key, currentLocale);
  return known !== key ? known : raw.replace(/[-_]/g, " ");
}

/** Format a date/time in the active locale. Invalid input renders as-is. */
export function formatDateTime(
  value: Date | string | number,
  options: Intl.DateTimeFormatOptions = {
    dateStyle: "medium",
    timeStyle: "short",
  },
): string {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return new Intl.DateTimeFormat(currentLocale, options).format(date);
}

/** Format a date in the active locale. */
export function formatDate(value: Date | string | number): string {
  return formatDateTime(value, { dateStyle: "medium" });
}

/** Format a number in the active locale. */
export function formatNumber(
  value: number,
  options?: Intl.NumberFormatOptions,
): string {
  return new Intl.NumberFormat(currentLocale, options).format(value);
}

// Re-export types and utilities
export {
  type Locale,
  getAvailableLocales,
  isLocale,
  localeDirection,
  t,
} from "./i18n";
