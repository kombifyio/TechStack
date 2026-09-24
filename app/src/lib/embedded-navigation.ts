/**
 * Navigation ownership for a hosted tool embed.
 *
 * Cloud can keep its site navigation authoritative while an embedded
 * Techstack document is open by adding `host_navigation=true` to the iframe
 * URL. The flag is deliberately explicit and scoped to the current browser
 * URL; standalone and self-hosted sessions never infer host ownership.
 */

export const HOST_NAVIGATION_QUERY = "host_navigation";

const LOCAL_APP_ORIGIN = "https://techstack.local";

/** Return true only for the explicit hosted-navigation value. */
export function hostNavigationRequested(
  value: URL | URLSearchParams | string,
): boolean {
  let searchParams: URLSearchParams;

  if (value instanceof URL) {
    searchParams = value.searchParams;
  } else if (value instanceof URLSearchParams) {
    searchParams = value;
  } else {
    try {
      searchParams = new URL(value, LOCAL_APP_ORIGIN).searchParams;
    } catch {
      return false;
    }
  }

  return searchParams.get(HOST_NAVIGATION_QUERY) === "true";
}

/**
 * Carry host navigation across an internal SPA target while preserving its
 * query and fragment. Unsafe or external targets are returned unchanged.
 */
export function withHostNavigation(path: string): string {
  if (!isSafeInternalPath(path)) return path;

  const url = new URL(path, LOCAL_APP_ORIGIN);
  url.searchParams.set(HOST_NAVIGATION_QUERY, "true");
  return `${url.pathname}${url.search}${url.hash}`;
}

/**
 * Accept only same-app paths for SPA navigation. In particular, protocol
 * relative URLs and backslash variants must never reach `goto`.
 */
export function isSafeInternalPath(path: string): boolean {
  return (
    path.startsWith("/") &&
    !path.startsWith("//") &&
    !path.includes("\\") &&
    !path.includes("://")
  );
}
