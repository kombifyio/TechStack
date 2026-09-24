/**
 * Public documentation links. One place for the docs origin - the app already
 * pointed at it from the footer and the dashboard - so a use case's guide is
 * built here and nowhere else.
 */
export const DOCS_ORIGIN = "https://docs.kombify.io";

/** The use-case overview, the honest fallback when a use case has no guide yet. */
export const USE_CASES_OVERVIEW_PATH = "/guides/stackkits/use-cases/overview";

/**
 * The guide for a use case: its own page when the StackKits catalog names one,
 * else the overview. Never an invented per-use-case URL.
 */
export function useCaseGuideUrl(docsPath?: string): string {
  const path = (docsPath ?? "").trim();
  return `${DOCS_ORIGIN}${path.startsWith("/") ? path : USE_CASES_OVERVIEW_PATH}`;
}
