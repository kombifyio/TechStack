/**
 * kombify-TechStack i18n Reactive Store (Svelte 5 Runes)
 *
 * This file contains the reactive state for i18n using Svelte 5 runes.
 * Import from this file in components, use i18n.ts for non-reactive contexts.
 */

import {
  type Locale,
  defaultLocale,
  getStoredLocale,
  setStoredLocale,
  t,
} from "./i18n";

// Reactive locale state using Svelte 5 runes
let currentLocale = $state<Locale>(defaultLocale);

// Track initialization
let initialized = $state(false);

/**
 * Initialize the i18n system - call this in +layout.svelte onMount
 */
export function initI18n(): void {
  if (typeof window !== "undefined" && !initialized) {
    currentLocale = getStoredLocale();
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
}

/**
 * Translate a key using the current locale (reactive)
 */
export function tr(key: string): string {
  return t(key, currentLocale);
}

// Re-export types and utilities
export { type Locale, getAvailableLocales, t } from "./i18n";
